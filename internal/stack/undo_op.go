package stack

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
)

// Undo reverts the mutation recorded in entry: branches the undone command
// created are deleted (moving HEAD out of the way first when needed), every
// recorded ref is restored in one transaction, the state is rolled back to the
// snapshot, and the branch that was checked out at capture time is checked out
// again when possible. s is the currently-loaded state (nil when it could not
// be loaded); on success it is replaced in place with the snapshot state and
// persisted via env.save(). The working tree is never modified. The journal
// entry itself is not dropped — that is the caller's job after a successful
// undo.
//
// Undo plans through planUndo (undo_plan.go) — the same read-only computation
// UndoPreview renders — then executes: created-branch deletion → the one-shot
// ref transaction → *s = *prev + env.save() → the recorded-branch checkout.
//
// External-drift policy: when the entry carries PostRefs (every entry a
// successful op records now), undo first verifies the branches still sit
// where the operation left them. A recorded branch whose live tip matches
// neither its recorded pre-op tip (already restored — safe retry) nor its
// post-op tip moved outside st; restoring it would rewind commits undo did
// not record. The same check covers branches the op created (a post-op
// commit on a to-be-deleted branch would otherwise be discarded silently).
// Drift refuses BEFORE any worktree, HEAD, branch, or state mutation; force
// downgrades the refusal to unconditional restore with a note naming the
// diverged refs. Entries without PostRefs — older journals, and entries
// retained for FAILED ops where abort/continue legitimately moves refs
// afterwards — restore unconditionally as they always have.
//
// Failure boundary: the op is not transactional. Earlier cleanup may already
// have deleted created worktrees/branches when a later phase fails; the
// retained journal entry makes a retry safe — a failed ref transaction moved
// nothing, and a failed save reports the already-restored refs so the error
// is never mistaken for "nothing happened".
func Undo(env Env, s *State, entry *UndoEntry, force bool) (*OpResult, error) {
	g := env.Git
	p, err := planUndo(env, s, entry, force, true, 1, nil)
	if err != nil {
		return nil, err
	}
	if p.fatal != nil {
		return nil, p.fatal
	}
	if len(p.refusals) > 0 {
		return nil, p.refusals[0].err
	}

	liveSet := p.liveSet
	skipCheckoutRestore := false
	for _, d := range p.doomed {
		// The plan already discovered the branch's live linked-worktree
		// owner and its verdicts; execute them in gate order: the
		// recorded-vs-live mismatch, then the cwd belt, then the dirty
		// refusal, then the removal.
		if d.mismatch {
			return nil, fmt.Errorf("branch %q is checked out in worktree %q, but undo recorded created worktree %q; not removing an unexpected worktree", d.name, d.owner, entry.CreatedWorktrees[d.name])
		}
		if d.owner != "" {
			// Belt under cmd's teleport pre-flight: never remove the worktree
			// the caller is standing in — doing so deletes the process's own
			// cwd.
			if within, err := CwdWithinWorktree(g, d.owner); err != nil {
				return nil, err
			} else if within {
				return nil, fmt.Errorf("cannot remove worktree %q: you are inside it; run from the main worktree (or another worktree)", d.owner)
			}
			if d.dirty {
				return nil, fmt.Errorf("branch %q has uncommitted changes in its worktree %q; commit/stash there or run `st worktree rm %s` first", d.name, d.owner, d.name)
			}
			if err := g.WorktreeRemove(d.owner, false); err != nil {
				return nil, fmt.Errorf("removing worktree %q for %q: %w", d.owner, d.name, err)
			}
		}
		if cur, err := g.CurrentBranch(); err == nil && cur == d.name {
			// HEAD is on a branch we are about to delete; move it to target (the
			// parent, or trunk) so the branch can be removed. The final landing
			// branch is restored below from entry.CurrentBranch (the branch that
			// was checked out when the command ran) — for a current-branch rename
			// that is the restored old name, so no command-label coupling is
			// needed here.
			target := p.prev.Trunk
			if s != nil {
				if b, ok := s.Get(d.name); ok && liveSet.exists(g, b.Parent) {
					target = b.Parent
				}
			}
			if !liveSet.exists(g, target) {
				sha, ok := entry.Refs[target]
				if !ok {
					return nil, fmt.Errorf("cannot restore checkout target %q before deleting %q", target, d.name)
				}
				if p.cas {
					// The drift preflight already proved target's recorded
					// post-op tip was absent — only an op-deleted branch
					// reaches here — so the resurrect is CAS'd on "still
					// absent" like the batch restore below.
					if err := g.UpdateRefsCas(map[string]git.RefUpdate{
						branchTipRef(target): {New: sha, Old: zeroSHA},
					}); err != nil {
						return nil, fmt.Errorf("restoring branch %q before deleting %q: %w", target, d.name, err)
					}
				} else if err := g.UpdateRef(branchTipRef(target), sha); err != nil {
					return nil, fmt.Errorf("restoring branch %q before deleting %q: %w", target, d.name, err)
				}
				if liveSet != nil {
					liveSet[target] = sha
				}
			}
			if err := g.Checkout(target); err != nil {
				if !checkoutBlockedByLocalChanges(err) && !checkoutBlockedByOtherWorktree(err) {
					return nil, fmt.Errorf("checking out %q before deleting %q: %w", target, d.name, err)
				}
				// Local changes or a target branch checked out in another
				// worktree block the checkout: park HEAD on a detached commit so
				// the branch can still be deleted without touching the working
				// tree.
				head, revErr := g.RevParse("HEAD")
				if revErr != nil {
					return nil, fmt.Errorf("resolving HEAD before deleting %q: %w", d.name, revErr)
				}
				if detachErr := g.CheckoutDetach(head); detachErr != nil {
					return nil, fmt.Errorf("detaching HEAD before deleting %q: %w", d.name, detachErr)
				}
				skipCheckoutRestore = true
			}
		}
		if err := g.DeleteBranch(d.name, true); err != nil {
			return nil, fmt.Errorf("deleting branch %q created by undone command: %w", d.name, err)
		}
		// Keep the snapshot truthful for the next doomed branch's
		// parent/target existence questions — a doomed branch's recorded
		// parent may be a sibling this loop just deleted.
		delete(liveSet, d.name)
	}

	// Restore every recorded ref in ONE update-ref transaction BEFORE saving
	// the snapshot metadata: on failure no ref moves, so the live refs and
	// persisted metadata stay consistent with each other and the retained
	// journal entry can simply be retried. Saving first would instead leave
	// snapshot-era parentSHAs beside the newer un-restored tips.
	//
	// With PostRefs the transaction is a compare-and-swap keyed on the
	// post-op tips the preflight already verified — the expected-old values
	// also close the gap between the preflight read and the batch. A ref
	// already back on its recorded tip is skipped (safe retry of a
	// partially-failed earlier undo); a ref the op deleted may only be
	// resurrected while still absent. Without PostRefs (old journals and
	// failed-op entries) or under force, the batch is unconditional.
	updates := make(map[string]git.RefUpdate, len(entry.Refs))
	for _, name := range p.restores {
		sha := entry.Refs[name]
		if !p.cas {
			updates[branchTipRef(name)] = git.RefUpdate{New: sha}
			continue
		}
		if live, ok := liveSet.tip(g, name); ok && live == sha {
			continue // already restored — nothing to CAS against
		}
		old := zeroSHA
		if post, ok := entry.PostRefs[name]; ok {
			old = post // must still sit at its post-op tip
		}
		updates[branchTipRef(name)] = git.RefUpdate{New: sha, Old: old}
	}
	if err := g.UpdateRefsCas(updates); err != nil {
		return nil, fmt.Errorf("restoring branch refs: %w", err)
	}
	// The transaction may have recreated refs absent from the snapshot —
	// fold them in so the checkout restore sees them as live.
	if liveSet != nil {
		for name, sha := range entry.Refs {
			liveSet[name] = sha
		}
	}

	// Refs are restored; now swap in and persist the snapshot metadata. If
	// the save fails the live refs are already rolled back while persisted
	// metadata is not — do NOT roll the transaction back: the journal entry
	// was not dropped, so a retry restores the same refs and saves then.
	if s != nil {
		*s = *p.prev
		if s.Branches == nil {
			s.Branches = make(map[string]*Branch)
		}
	}
	if err := env.save(); err != nil {
		return nil, fmt.Errorf("saving restored stack state (branch refs were already restored; fix the cause and rerun `st undo`): %w", err)
	}

	if !skipCheckoutRestore && entry.CurrentBranch != "" && liveSet.exists(g, entry.CurrentBranch) {
		if err := g.Checkout(entry.CurrentBranch); err != nil {
			// Local changes blocking the final checkout are tolerated: the refs are
			// already restored and HEAD simply stays where it is. A branch that is
			// already checked out in another worktree is likewise restored; this
			// process just cannot check it out in place.
			if !checkoutBlockedByLocalChanges(err) && !checkoutBlockedByOtherWorktree(err) {
				return nil, fmt.Errorf("checking out restored branch %q: %w", entry.CurrentBranch, err)
			}
		}
	}

	res := &OpResult{
		Summary:   "undid: " + entry.Label,
		Restacked: p.restores,
	}
	if p.noPostRef {
		res.Notes = append(res.Notes, "the journal entry has no post-operation tips on record; branch refs were restored unconditionally")
	} else if force && len(p.diverged) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("--force: %s had moved outside st since %q ran and %s overwritten anyway", joinBranchList(p.diverged), entry.Label, pluralRefs(p.diverged)))
	}
	// An undo of an absorb restores the pre-absorb refs — the amended tips
	// that now carry the caller's staged edits become unreachable. Point at
	// them explicitly or the edits vanish into reflog-only limbo.
	if len(entry.AbsorbedCommits) > 0 {
		res.Notes = append(res.Notes, absorbedCommitsNote(entry.AbsorbedCommits))
	}
	return res, nil
}

// createdBranchCandidates is the candidate set the undo plan scans for
// branches the undone command created: every branch the current state knows
// about (trunk included — a tracked trunk is corrupt state, not a reason to
// skip the check) plus the entry's recorded CreatedBranches.
func createdBranchCandidates(s *State, entry *UndoEntry) map[string]bool {
	candidates := map[string]bool{}
	if s != nil {
		candidates[s.Trunk] = true
		for name := range s.Branches {
			candidates[name] = true
		}
	}
	for _, name := range entry.CreatedBranches {
		candidates[name] = true
	}
	return candidates
}

// undoExternalDrift reports the sorted branch names that no longer sit where
// the entry's post-operation snapshot left them — evidence they moved outside
// st between the op and this undo. The rules, per recorded ref:
//
//	live == recorded pre-op tip   → already restored (safe retry): not drift
//	live == recorded post-op tip  → untouched since the op: not drift
//	absent, post-op absent        → op deleted it, still gone: not drift
//	anything else                 → moved, recreated, or deleted outside st
//
// and per doomed (op-created) branch still live: it must still sit at its
// recorded post-op tip — a post-op commit on a to-be-deleted branch would
// otherwise be discarded silently. Returns nil for entries without PostRefs
// (legacy journals, failed-op entries): they carry no expectation to check.
func undoExternalDrift(l liveBranches, g Git, s *State, entry *UndoEntry) []string {
	if entry.PostRefs == nil {
		return nil
	}
	var bad []string
	for name, pre := range entry.Refs {
		live, exists := l.tip(g, name)
		if exists && live == pre {
			continue // already restored — retry/no-op case
		}
		post, recorded := entry.PostRefs[name]
		if recorded && exists && live == post {
			continue // still where the operation left it
		}
		if !recorded && !exists {
			continue // the op deleted it and it stayed deleted
		}
		bad = append(bad, name)
	}
	for name := range createdBranchCandidates(s, entry) {
		if !entry.CreatesBranch(name) || !l.exists(g, name) {
			continue
		}
		live, _ := l.tip(g, name)
		if post, ok := entry.PostRefs[name]; ok && live == post {
			continue
		}
		bad = append(bad, name)
	}
	sort.Strings(bad)
	return bad
}

func joinBranchList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}

func pluralRefs(names []string) string {
	if len(names) == 1 {
		return "that ref"
	}
	return "those refs"
}

func pluralHave(names []string) string {
	if len(names) == 1 {
		return "has"
	}
	return "have"
}

// undoPausedRebases returns the sorted names of branches this undo entry would
// actually rewrite — every recorded ref whose live tip differs from its
// recorded value (an already-restored or never-moved ref is a no-op write,
// so a pause there blocks nothing), plus every live doomed branch — that have
// a rebase paused in a linked worktree. Probe failures are surfaced: guessing
// "not paused" is how a delete lands mid-cleanup.
func undoPausedRebases(g Git, s *State, liveSet liveBranches, entry *UndoEntry) ([]string, error) {
	wts, err := g.Worktrees()
	if err != nil {
		return nil, fmt.Errorf("listing worktrees for paused-rebase check: %w", err)
	}
	if !IsMultiWorktree(wts) {
		return nil, nil
	}
	relevant := make(map[string]bool, len(entry.Refs))
	for name, recorded := range entry.Refs {
		if live, exists := liveSet.tip(g, name); !exists || live != recorded {
			relevant[name] = true
		}
	}
	for name := range createdBranchCandidates(s, entry) {
		if entry.CreatesBranch(name) && liveSet.exists(g, name) {
			relevant[name] = true
		}
	}
	paused, err := PausedRebaseOwners(g, wts)
	if err != nil {
		return nil, err
	}
	var names []string
	for head := range paused {
		if relevant[head] {
			names = append(names, head)
		}
	}
	sort.Strings(names)
	return names, nil
}

// liveBranches is one Tips() snapshot answering "does this local branch
// exist" and "what is its tip" for every probe a pass needs — one spawn
// instead of one per ref. A nil map means the batch read failed: every
// answer then degrades to the per-branch probes the batch replaced.
type liveBranches map[string]string

// probeLiveBranches reads every local branch tip in one spawn; the error is
// degraded to nil (per-branch fallback) rather than propagated, matching the
// tolerance BranchExists' quiet show-ref had.
func probeLiveBranches(g Git) liveBranches {
	live, err := g.Tips()
	if err != nil {
		return nil
	}
	return live
}

func (l liveBranches) exists(g Git, name string) bool {
	if l == nil {
		return g.BranchExists(name)
	}
	_, ok := l[name]
	return ok
}

func (l liveBranches) tip(g Git, name string) (string, bool) {
	if l == nil {
		t, err := g.RevParse(branchTipRef(name))
		return t, err == nil
	}
	t, ok := l[name]
	return t, ok
}

func sameWorktreePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func checkoutBlockedByLocalChanges(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "local changes") || strings.Contains(msg, "would be overwritten")
}

func checkoutBlockedByOtherWorktree(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already checked out") ||
		strings.Contains(msg, "used by worktree") ||
		strings.Contains(msg, "checked out at")
}

// CreatesBranch reports whether name was created by the command the entry
// snapshots: it is listed in CreatedBranches, or absent from the local-branch
// list captured before the command ran. Note it answers true for ANY branch
// absent from LocalBranches — including one created after the snapshot — so
// callers checking "does undo doom this branch" must also require candidate
// membership; DoomedBranch does both.
func (e *UndoEntry) CreatesBranch(name string) bool {
	for _, created := range e.CreatedBranches {
		if created == name {
			return true
		}
	}
	for _, existed := range e.LocalBranches {
		if existed == name {
			return false
		}
	}
	return true
}

// DoomedBranch reports whether the undo plan will try to delete name: the
// deletion set only ever considers the candidate set (current-state branches
// ∪ the entry's CreatedBranches) and only members the entry created are
// doomed. Branch liveness is a git probe the caller adds; the
// LocalBranches==nil degrade (deletion skipped entirely) is honored here.
func (e *UndoEntry) DoomedBranch(s *State, name string) bool {
	if e.LocalBranches == nil {
		return false
	}
	candidate := false
	if s != nil {
		candidate = name == s.Trunk || s.IsTracked(name)
	}
	for _, c := range e.CreatedBranches {
		candidate = candidate || c == name
	}
	return candidate && e.CreatesBranch(name)
}

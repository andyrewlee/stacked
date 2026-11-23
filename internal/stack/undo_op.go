package stack

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
)

// Undo reverts the single mutation recorded in entry: branches the undone
// command created are deleted (moving HEAD out of the way first when needed),
// every recorded ref is restored in one transaction (CAS-guarded when PostRefs
// are present unless force is true), the state is rolled back to the snapshot,
// and the branch that was checked out at capture time is checked out again when
// possible. The working tree is never modified.
func Undo(env Env, s *State, entry *UndoEntry, force bool) (*OpResult, error) {
	g := env.Git
	if inProgress, err := g.RebaseInProgress(); err != nil {
		return nil, fmt.Errorf("checking rebase state: %w", err)
	} else if inProgress {
		return nil, errors.New(UndoRebaseGateMsg)
	}
	if s != nil && s.Version > stateSchemaVersion {
		return nil, fmt.Errorf("current state: %w (schema v%d; this st understands v%d) — upgrade st or check for a downgrade", ErrStateTooNew, s.Version, stateSchemaVersion)
	}
	prev, err := decodeState(entry.State)
	if err != nil {
		return nil, fmt.Errorf("parsing undo state: %w", err)
	}
	if err := validateUndoEntry(entry); err != nil {
		return nil, err
	}

	liveSet := probeLiveBranches(g)
	restores := sortedRefNames(entry.Refs)
	noPostRef := entry.PostRefs == nil
	cas := entry.PostRefs != nil && !force

	diverged := undoExternalDrift(liveSet, g, s, entry)
	if len(diverged) > 0 && !force {
		return nil, fmt.Errorf("cannot undo %q: %s moved outside st since the command ran — refusing to overwrite %s; run `st undo --force` to restore anyway",
			entry.Label, joinBranchList(diverged), pluralRefs(diverged))
	}
	if !force {
		paused, err := undoPausedRebases(g, s, liveSet, entry)
		if err != nil {
			return nil, err
		}
		if len(paused) > 0 {
			return nil, fmt.Errorf("cannot undo %q: %s %s a rebase in progress in a linked worktree; resolve it there (`st continue` or `st abort`) first — or run `st undo --force`",
				entry.Label, joinBranchList(paused), pluralHave(paused))
		}
	}

	var doomed []string
	if entry.LocalBranches != nil {
		for name := range createdBranchCandidates(s, entry) {
			if entry.CreatesBranch(name) && liveSet.exists(g, name) {
				doomed = append(doomed, name)
			}
		}
		sort.Strings(doomed)
	}

	cur, _ := g.CurrentBranch()
	if len(doomed) > 0 {
		wts, err := g.Worktrees()
		if err != nil {
			return nil, fmt.Errorf("listing worktrees: %w", err)
		}
		for _, name := range doomed {
			if owner, elsewhere := ownerElsewhereFrom(wts, name, cur); elsewhere {
				return nil, fmt.Errorf("branch %q is checked out in another worktree %q; switch away from it there before undoing %q", name, owner.Path, entry.Label)
			}
		}
	}

	skipCheckoutRestore := false
	batchDelete := liveSet != nil
	if batchDelete {
		for _, name := range doomed {
			delete(liveSet, name)
		}
	}
	var doomedNames []string
	for _, name := range doomed {
		if curNow, err := g.CurrentBranch(); err == nil && curNow == name {
			target := prev.Trunk
			if s != nil {
				if b, ok := s.Get(name); ok && liveSet.exists(g, b.Parent) {
					target = b.Parent
				}
			}
			if !liveSet.exists(g, target) {
				sha, ok := entry.Refs[target]
				if !ok {
					return nil, fmt.Errorf("cannot restore checkout target %q before deleting %q", target, name)
				}
				if cas {
					if err := g.UpdateRefsCas(map[string]git.RefUpdate{
						branchTipRef(target): {New: sha, Old: zeroSHA},
					}); err != nil {
						return nil, fmt.Errorf("restoring branch %q before deleting %q: %w", target, name, err)
					}
				} else if err := g.UpdateRef(branchTipRef(target), sha); err != nil {
					return nil, fmt.Errorf("restoring branch %q before deleting %q: %w", target, name, err)
				}
				if liveSet != nil {
					liveSet[target] = sha
				}
			}
			if err := g.Checkout(target); err != nil {
				if !recoverableCheckoutFailure(g, target, err) {
					return nil, fmt.Errorf("checking out %q before deleting %q: %w", target, name, err)
				}
				head, revErr := g.RevParse("HEAD")
				if revErr != nil {
					return nil, fmt.Errorf("resolving HEAD before deleting %q: %w", name, revErr)
				}
				if detachErr := g.CheckoutDetach(head); detachErr != nil {
					return nil, fmt.Errorf("detaching HEAD before deleting %q: %w", name, detachErr)
				}
				skipCheckoutRestore = true
			}
		}
		if batchDelete {
			doomedNames = append(doomedNames, name)
			continue
		}
		if err := g.DeleteBranch(name, true); err != nil {
			return nil, fmt.Errorf("deleting branch %q created by undone command: %w", name, err)
		}
	}
	if len(doomedNames) > 0 {
		if err := g.DeleteBranches(doomedNames, true); err != nil {
			return nil, fmt.Errorf("deleting branches %s created by undone command: %w", joinBranchList(doomedNames), err)
		}
	}

	updates := make(map[string]git.RefUpdate, len(entry.Refs))
	for _, name := range restores {
		sha := entry.Refs[name]
		if !cas {
			updates[branchTipRef(name)] = git.RefUpdate{New: sha}
			continue
		}
		if live, ok := liveSet.tip(g, name); ok && live == sha {
			continue
		}
		old := zeroSHA
		if post, ok := entry.PostRefs[name]; ok {
			old = post
		}
		updates[branchTipRef(name)] = git.RefUpdate{New: sha, Old: old}
	}
	if err := g.UpdateRefsCas(updates); err != nil {
		return nil, fmt.Errorf("restoring branch refs: %w", err)
	}
	if liveSet != nil {
		for name, sha := range entry.Refs {
			liveSet[name] = sha
		}
	}

	if s != nil {
		*s = *prev
		if s.Branches == nil {
			s.Branches = make(map[string]*Branch)
		}
	}
	if err := env.save(); err != nil {
		return nil, fmt.Errorf("saving restored stack state (branch refs were already restored; fix the cause and rerun `st undo`): %w", err)
	}

	if !skipCheckoutRestore && entry.CurrentBranch != "" && liveSet.exists(g, entry.CurrentBranch) {
		if err := g.Checkout(entry.CurrentBranch); err != nil {
			if !recoverableCheckoutFailure(g, entry.CurrentBranch, err) {
				return nil, fmt.Errorf("checking out restored branch %q: %w", entry.CurrentBranch, err)
			}
		}
	}

	res := &OpResult{
		Summary:   "undid: " + entry.Label,
		Restacked: restores,
	}
	if noPostRef {
		res.Notes = append(res.Notes, "the journal entry has no post-operation tips on record; branch refs were restored unconditionally")
	} else if force && len(diverged) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("--force: %s had moved outside st since %q ran and %s overwritten anyway", joinBranchList(diverged), entry.Label, pluralRefs(diverged)))
	}
	return res, nil
}

func sortedRefNames(refs map[string]string) []string {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

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

func undoExternalDrift(l liveBranches, g Git, s *State, entry *UndoEntry) []string {
	if entry.PostRefs == nil {
		return nil
	}
	var bad []string
	for name, pre := range entry.Refs {
		live, exists := l.tip(g, name)
		if exists && live == pre {
			continue
		}
		post, recorded := entry.PostRefs[name]
		if recorded && exists && live == post {
			continue
		}
		if !recorded && !exists {
			continue
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

type liveBranches map[string]string

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

func recoverableCheckoutFailure(g Git, target string, err error) bool {
	if wts, werr := g.Worktrees(); werr == nil {
		if _, ok := LinkedOwnerOf(wts, target); ok {
			return true
		}
		if paused, perr := PausedRebaseOwners(g, wts); perr == nil {
			if _, held := paused[target]; held {
				return true
			}
		}
	}
	return checkoutBlockedByLocalChanges(err) || checkoutBlockedByOtherWorktree(err)
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

// CreatesBranch reports whether name was created by the command the entry snapshots.
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

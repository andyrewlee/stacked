// ops_delete_sync.go — Delete plus the Sync/Prune family: fetching,
// fast-forwarding the trunk, pruning merged branches, and restacking what
// survives.
package stack

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
)

// isMergedOrContained reports whether branch's commits are already in parent —
// either by commit ancestry (ordinary/ff merge) or by exact tree-content
// containment (squash-merge / cherry-pick).
func isMergedOrContained(g Git, parent, name string) (bool, error) {
	merged, err := g.IsAncestor(branchTipRef(name), branchTipRef(parent))
	if err != nil || merged {
		return merged, err
	}
	return g.ChangesContainedIn(branchTipRef(parent), branchTipRef(name))
}

// Delete removes a tracked branch, re-parents its children onto the deleted
// branch's parent, and restacks them so the deleted branch's commits are dropped
// from their history.
func Delete(env Env, s *State, name string, force bool) (*OpResult, error) {
	g := env.Git
	if name == s.Trunk {
		return nil, fmt.Errorf("cannot delete the trunk branch %q", name)
	}
	b, err := s.tracked(name)
	if err != nil {
		return nil, err
	}
	if err := requireClean(g); err != nil {
		return nil, err
	}
	parent := b.Parent

	if !force {
		mergedIntoParent, err := isMergedOrContained(g, parent, name)
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, parent, err)
		}
		if !mergedIntoParent {
			return nil, fmt.Errorf("branch %q is not merged into its stack parent %q (use --force to delete anyway)", name, parent)
		}
	}
	// If name lives in another worktree, git refuses to delete it; tear that
	// (clean) worktree down first. A dirty owner errors out and nothing changes.
	if err := s.releaseOwnedWorktree(env, name); err != nil {
		return nil, err
	}
	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if start == name {
		if owner, elsewhere, err := s.ownerElsewhere(g, parent); err != nil {
			return nil, err
		} else if elsewhere {
			return nil, fmt.Errorf("cannot delete current branch %q because its parent %q is checked out in another worktree %q", name, parent, owner.Path)
		}
		if err := g.Checkout(parent); err != nil {
			return nil, fmt.Errorf("checking out parent %q: %w", parent, err)
		}
		start = parent
	}

	if err := g.DeleteBranch(name, true); err != nil {
		err = fmt.Errorf("deleting branch %q: %w", name, err)
		if restoreErr := restoreHEAD(env, start, s.Trunk); restoreErr != nil {
			return nil, AlsoFailed(err, fmt.Sprintf("restore %q", start), restoreErr)
		}
		return nil, err
	}
	beforeParents := parentSnapshot(s)
	formerChildren := s.RemoveBranch(name)
	reparented := diffReparents(beforeParents, s)
	if err := env.save(); err != nil {
		return nil, err
	}

	// One pass over one shared tips map (the walk order — each child, then its
	// descendants — matches DeletePlan's appendRestackPlans exactly), instead
	// of a fresh full ref scan per re-parented child.
	restacked, err := s.restackForestWithOrig(env, formerChildren, start)
	if err != nil {
		return nil, restoreHEADAfterNonConflict(env, start, s.Trunk, err)
	}
	notes := skippedWorktreeNotes(s)
	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}

	res := &OpResult{Summary: fmt.Sprintf("Deleted %s", name), Deleted: []string{name}, Reparented: reparented, Restacked: restacked, Notes: notes}
	if len(formerChildren) > 0 {
		res.Summary = fmt.Sprintf("Deleted %s; re-parented %d branch(es) onto %s", name, len(formerChildren), parent)
	}
	return res, nil
}

// syncScope resolves which tracked branches sync operates on. When all is
// true, or when HEAD is on trunk (or an untracked branch), every tracked branch
// is in scope (whole forest). Otherwise only the current branch's connected
// stack (BottomOf(cur) and its descendants) is in scope, leaving unrelated
// stacks and their worktrees untouched.
func syncScope(s *State, cur string, all bool) (scoped bool, members map[string]bool) {
	if all || !s.IsTracked(cur) {
		return false, nil
	}
	stackBranches := s.StackBranches(cur)
	members = make(map[string]bool, len(stackBranches))
	for _, name := range stackBranches {
		members[name] = true
	}
	return true, members
}

// survivingRoots returns the restack root branches for a sync pass after
// pruning: for a whole-forest sync every direct child of trunk; for a scoped
// stack sync every surviving member of members whose parent is no longer in
// members (typically trunk), sorted deterministically.
func survivingRoots(s *State, scoped bool, members map[string]bool) []string {
	if !scoped {
		return s.childNames(s.Trunk)
	}
	var roots []string
	for _, name := range s.BranchNames() {
		if !members[name] {
			continue
		}
		if b := s.Branches[name]; b != nil && !members[b.Parent] {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	return roots
}

// singleSurvivingChild returns the sole immediate surviving descendant of
// prunedOrig after pruning (e.g. the new bottom branch when the merged bottom
// branch was just pruned), or "" when there are zero or multiple surviving
// immediate descendants.
func singleSurvivingChild(s *State, origDescendants []string) string {
	if len(origDescendants) == 0 {
		return ""
	}
	descSet := make(map[string]bool, len(origDescendants))
	for _, name := range origDescendants {
		descSet[name] = true
	}
	var direct []string
	for _, name := range origDescendants {
		b, ok := s.Get(name)
		if !ok {
			continue
		}
		// An immediate surviving descendant is one whose post-prune parent is
		// no longer inside orig's old descendant set (e.g. re-parented onto
		// trunk or an ancestor above orig).
		if !descSet[b.Parent] {
			direct = append(direct, name)
		}
	}
	if len(direct) != 1 {
		return ""
	}
	return direct[0]
}

// Sync fetches and fast-forwards the trunk via the remote port, prunes branches
// already merged into the trunk, restacks remaining branches onto the updated
// trunk, and restores the caller's branch (or advances to the single surviving
// child when the current branch was pruned). When all is false and HEAD is on
// a tracked branch, only the current stack is pruned and restacked; from trunk
// (or with all=true) the entire forest is synced. With noDelete, merged
// branches are kept. With noFetch the remote is untouched — no fetch, no
// fast-forward — and the local trunk is the single basis for both pruning and
// restacking: a branch merged only into the cached remote ref survives until
// the local trunk advances. Requires a clean working tree.
func Sync(env Env, r Remote, s *State, remote string, noDelete, noFetch, all bool) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	orig, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	scoped, scopeMembers := syncScope(s, orig, all)
	var origDescendants []string
	if s.IsTracked(orig) {
		origDescendants = s.Descendants(orig)
	}
	beforeParents := parentSnapshot(s)

	// Resolve where the trunk is checked out ONCE: sync's trunk operations run
	// in the trunk's own worktree, so `st sync` works from a linked worktree
	// (where git refuses to check out the trunk a second time).
	trunkHere := orig == s.Trunk
	trunkOwnerDir := ""
	if !trunkHere {
		wts, wtErr := g.Worktrees()
		if wtErr != nil {
			return nil, wtErr
		}
		if owner, ok := OwnerOf(wts, s.Trunk); ok {
			trunkOwnerDir = owner.Path
		}
	}

	// The prune basis is the local trunk — after a fast-forward it is the
	// remote tip. Under --no-fetch nothing moves the local trunk, and it stays
	// the basis for BOTH pruning and restacking: using a cached remote ref for
	// pruning while restacking onto the local tip could drop a landed
	// ancestor's content from a surviving child.
	// Always qualified: a bare "main" resolves through gitrevisions order where
	// a tag named main would shadow the branch.
	ffResult := "skipped (no remote)"
	trunkRef := branchTipRef(s.Trunk)
	switch {
	case noFetch:
		ffResult = "skipped (--no-fetch)"
	case r.Exists(remote):
		if err := r.Fetch(remote); err != nil {
			return nil, fmt.Errorf("fetch %q: %w", remote, err)
		}
		ffResult, err = r.FastForward(s.Trunk, remote, trunkOwnerDir, trunkHere)
		if err != nil {
			if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
			}
			return nil, err
		}
	}

	var deleted []string
	if !noDelete {
		// HEAD must not sit on a prunable branch. Checking out the trunk does
		// that; from a linked worktree (trunk owned elsewhere) git refuses the
		// checkout, so park HEAD detached instead — any branch, including orig,
		// can then be pruned.
		if trunkOwnerDir != "" {
			head, revErr := g.RevParse("HEAD")
			if revErr != nil {
				return nil, fmt.Errorf("resolving HEAD before pruning: %w", revErr)
			}
			if err := g.CheckoutDetach(head); err != nil {
				return nil, fmt.Errorf("detaching HEAD before pruning: %w", err)
			}
		} else if err := g.Checkout(s.Trunk); err != nil {
			return nil, fmt.Errorf("checkout trunk %q before pruning: %w", s.Trunk, err)
		}
		if deleted, err = pruneMergedScoped(env, s, trunkRef, scoped, scopeMembers); err != nil {
			if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
			}
			return nil, err
		}
	}
	reparented := diffReparents(beforeParents, s)
	// Persist the prune before restacking so a conflict cannot leave the metadata
	// referencing already-deleted branches.
	if err := env.save(); err != nil {
		return nil, err
	}

	// If the branch HEAD started on was pruned and had a single surviving child
	// that isn't owned by another worktree, advance HEAD to that new bottom
	// branch instead of stranding the caller on trunk or detached HEAD.
	restoreTarget := orig
	advancedTo := ""
	if !g.BranchExists(orig) {
		if next := singleSurvivingChild(s, origDescendants); next != "" && g.BranchExists(next) {
			if _, elsewhere, err := s.ownerElsewhere(g, next); err == nil && !elsewhere {
				restoreTarget = next
				advancedTo = next
			}
		}
	}

	roots := survivingRoots(s, scoped, scopeMembers)
	rebased, err := s.restackForestWithOrig(env, roots, restoreTarget)
	if err != nil {
		// Leaves a conflict's rebase in progress for `st continue`; restores HEAD
		// otherwise. Same guard the other mutations use.
		return nil, restoreHEADAfterNonConflict(env, restoreTarget, s.Trunk, err)
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	notes := []string{"trunk: " + ffResult}
	if advancedTo != "" {
		if err := restoreHEAD(env, advancedTo, s.Trunk); err != nil {
			return nil, err
		}
		notes = append(notes, "switched to "+advancedTo)
	} else if !g.BranchExists(orig) && trunkOwnerDir != "" {
		// orig was pruned and restoreHEAD's trunk fallback cannot be checked out
		// here (it lives in another worktree): stay where the cascade left HEAD
		// rather than fail or hijack that worktree's branch. That is NOT always
		// detached — an in-place rebase of a surviving branch re-attaches HEAD to
		// that branch — so report where HEAD actually landed.
		if cur, curErr := g.CurrentBranch(); curErr == nil && cur != "" {
			notes = append(notes, "HEAD is on "+cur+"; trunk is checked out in "+trunkOwnerDir)
		} else {
			notes = append(notes, "HEAD left detached; trunk is checked out in "+trunkOwnerDir)
		}
	} else if err := restoreHEAD(env, restoreTarget, s.Trunk); err != nil {
		return nil, err
	}

	return &OpResult{
		Summary:    "sync complete",
		Branch:     advancedTo,
		Deleted:    deleted,
		Reparented: reparented,
		Restacked:  rebased,
		Notes:      append(notes, append(skippedWorktreeNotes(s), unreachableNotes(s)...)...),
	}, nil
}

// SyncPlanAgainst previews what a sync would do against the supplied trunk
// ref — which merged branches it would prune and which branches it would
// restack — without fetching, fast-forwarding, or mutating anything. Used by
// the CLI dry-run path against the already-fetched remote trunk ref (the
// dry run never fetches).
func SyncPlanAgainst(env Env, s *State, noDelete, all bool, trunkRef string) (*OpResult, error) {
	g := env.Git
	// The real Sync requires a clean tree (Sync, above), so the preview must
	// too — otherwise it reports branches it "would restack" that the real
	// command will refuse to touch, returning exit 0 instead of the dirty-tree
	// exit code.
	if err := requireClean(g); err != nil {
		return nil, err
	}
	orig, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	scoped, scopeMembers := syncScope(s, orig, all)
	tipNames := s.TipNames()
	if scoped {
		tipNames = append([]string{s.Trunk}, s.StackBranches(orig)...)
	}
	tips, err := g.TipsFor(tipNames)
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	for _, name := range tipNames {
		if err := requireBranchTip(tips, name); err != nil {
			return nil, err
		}
	}
	if trunkRef != s.Trunk && trunkRef != branchTipRef(s.Trunk) {
		trunkTip, err := g.RevParse(trunkRef)
		if err != nil {
			return nil, fmt.Errorf("resolve trunk ref %q: %w", trunkRef, err)
		}
		tips[s.Trunk] = trunkTip
	}
	planState := cloneState(s)
	beforeParents := parentSnapshot(planState)
	deleted := map[string]bool{}
	var deletedList []string
	if !noDelete {
		candidates, _, err := pruneTargetsScoped(env, s, trunkRef, scoped, scopeMembers, true)
		if err != nil {
			return nil, err
		}
		for _, name := range candidates {
			planState.RemoveBranch(name)
			deleted[name] = true
			deletedList = append(deletedList, name)
		}
	}
	reparented := diffReparents(beforeParents, planState)
	roots := survivingRoots(planState, scoped, scopeMembers)
	preview, err := appendRestackPlans(env, planState, tips, roots...)
	if err != nil {
		return nil, err
	}
	var plan []string
	for _, name := range preview.restacked {
		if !deleted[name] {
			plan = append(plan, name)
		}
	}
	return &OpResult{Summary: "sync (dry run)", Deleted: deletedList, Reparented: reparented, Restacked: plan, Notes: preview.notes(), DryRun: true}, nil
}

// PruneMerged deletes tracked branches whose commits or content are already
// contained in the local trunk. It returns the deleted branch names in sorted
// order. The caller persists.
func PruneMerged(env Env, s *State) ([]string, error) {
	return PruneMergedAgainst(env, s, branchTipRef(s.Trunk))
}

// PruneMergedAgainst is PruneMerged against an arbitrary basis ref — the local
// trunk, or a fetched remote-tracking ref (st prune --remote, sync --dry-run).
func PruneMergedAgainst(env Env, s *State, trunkRef string) ([]string, error) {
	return pruneMergedScoped(env, s, trunkRef, false, nil)
}

func pruneMergedScoped(env Env, s *State, trunkRef string, scoped bool, members map[string]bool) ([]string, error) {
	candidates, releases, err := pruneTargetsScoped(env, s, trunkRef, scoped, members, true)
	if err != nil {
		return nil, err
	}
	return applyPrune(env, s, candidates, releases)
}

// Prune is the standalone `st prune`: delete every tracked branch already
// merged into trunkRef (local trunk or an explicit remote-tracking ref). Unlike
// Sync it never fetches, fast-forwards, moves HEAD, or restacks — so when HEAD
// is on a prunable branch it refuses rather than auto-checkout: sync owns the
// move. No clean-tree requirement: branch deletion touches no worktree content.
func Prune(env Env, s *State, trunkRef string) (*OpResult, error) {
	names, err := pruneMergedNames(env, s, trunkRef, false, nil)
	if err != nil {
		return nil, err
	}
	if err := refusePruneCurrent(env, names); err != nil {
		return nil, err
	}
	candidates, releases, err := gatePruneCandidates(env, s, names, false)
	if err != nil {
		return nil, err
	}
	beforeParents := parentSnapshot(s)
	deleted, err := applyPrune(env, s, candidates, releases)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary:    fmt.Sprintf("Pruned %d merged branch(es)", len(deleted)),
		Deleted:    deleted,
		Reparented: diffReparents(beforeParents, s),
	}, nil
}

// PrunePlan previews Prune against trunkRef — the same merged enumeration,
// current-branch refusal, and worktree-release gate, with nothing deleted.
func PrunePlan(env Env, s *State, trunkRef string) (*OpResult, error) {
	names, err := pruneMergedNames(env, s, trunkRef, false, nil)
	if err != nil {
		return nil, err
	}
	if err := refusePruneCurrent(env, names); err != nil {
		return nil, err
	}
	candidates, _, err := gatePruneCandidates(env, s, names, false)
	if err != nil {
		return nil, err
	}
	planState := cloneState(s)
	beforeParents := parentSnapshot(planState)
	for _, name := range candidates {
		planState.RemoveBranch(name)
	}
	return &OpResult{Summary: "prune (dry run)", Deleted: candidates, Reparented: diffReparents(beforeParents, planState), DryRun: true}, nil
}

// refusePruneCurrent refuses a standalone prune that would delete the branch
// HEAD is on — Prune never moves HEAD, so the user must switch away first (or
// run st sync, which owns the move). A detached HEAD reports "detached HEAD"
// from CurrentBranch — not a branch name — so the check degrades to "no branch
// to protect".
func refusePruneCurrent(env Env, names []string) error {
	cur, _ := env.Git.CurrentBranch()
	for _, name := range names {
		if name == cur {
			return fmt.Errorf("current branch %q would be pruned; check out another branch or run st sync", cur)
		}
	}
	return nil
}

// gatePruneCandidates applies the worktree-release check to each merged name —
// the same check applyPrune performs at release time — so a preview and an
// apply share identical eligibility, and a dirty owner fails BEFORE any
// branch is deleted instead of mid-loop. It also returns the computed release
// plan (name → worktree path, "" when nothing is released) so the apply can
// remove worktrees without re-probing — the gate's verdict is authoritative
// within one locked command. When allowCurrent is true (Sync / SyncPlanAgainst),
// a candidate owned by the current worktree is permitted without scheduling
// worktree removal because Sync parks HEAD elsewhere before pruning.
func gatePruneCandidates(env Env, s *State, names []string, allowCurrent bool) (candidates []string, releases map[string]string, err error) {
	g := env.Git
	wts, err := g.Worktrees()
	if err != nil {
		return nil, nil, err
	}
	if !IsMultiWorktree(wts) {
		return names, nil, nil
	}
	main, _ := MainWorktree(wts)
	// Pass 1 collects the refusals that need no cleanliness answer and the
	// linked owners that do. CurrentBranch/RepoRoot are read lazily once —
	// the serial version paid them per candidate.
	var cur, root string
	var curSet, rootSet bool
	var rootErr error
	early := map[string]error{}
	linked := map[string]git.Worktree{}
	var cleanPaths []string
	for _, name := range names {
		owner, ok := OwnerOf(wts, name)
		if !ok {
			continue
		}
		if owner.Path == main.Path {
			if !curSet {
				cur, _ = g.CurrentBranch()
				curSet = true
			}
			if name != cur {
				early[name] = fmt.Errorf("branch %q is checked out in the main worktree %q; switch away from it there before deleting it", name, owner.Path)
			}
			continue
		}
		if !rootSet {
			root, rootErr = g.RepoRoot()
			rootSet = true
		}
		if rootErr != nil {
			early[name] = fmt.Errorf("locating the current worktree root: %w", rootErr)
			continue
		}
		if sameWorktreePath(root, owner.Path) {
			if allowCurrent {
				continue
			}
			early[name] = fmt.Errorf("cannot remove worktree %q for %q: you are inside it; run from the main worktree (or another worktree)", owner.Path, name)
			continue
		}
		linked[name] = owner
		if !WorktreeStale(g, owner) {
			cleanPaths = append(cleanPaths, owner.Path)
		}
	}
	// One parallel batch answers every linked owner's cleanliness; pass 2 then
	// evaluates each candidate in name order — early refusals first, then the
	// clean verdict — exactly the order the serial per-name checks ran in.
	verdicts := probeWorktrees(g, cleanPaths, false)
	releases = map[string]string{}
	for _, name := range names {
		if e := early[name]; e != nil {
			return nil, nil, e
		}
		owner, ok := linked[name]
		if !ok {
			candidates = append(candidates, name)
			continue
		}
		if WorktreeStale(g, owner) {
			return nil, nil, fmt.Errorf("branch %q's worktree %q is a stale registration (the directory is gone); run `git worktree prune` to release it before deleting %q", name, owner.Path, name)
		}
		v := verdicts[owner.Path]
		if v.cleanErr != nil {
			return nil, nil, fmt.Errorf("checking worktree %q for %q: %w", owner.Path, name, v.cleanErr)
		}
		if !v.clean {
			return nil, nil, fmt.Errorf("branch %q has uncommitted changes in its worktree %q; commit/stash there or run `st worktree rm %s` first", name, owner.Path, name)
		}
		releases[name] = owner.Path
		candidates = append(candidates, name)
	}
	return candidates, releases, nil
}

// pruneTargetsScoped returns the deletable merged set in sorted order for the
// sync path (optionally restricted to members when scoped is true).
func pruneTargetsScoped(env Env, s *State, trunkRef string, scoped bool, members map[string]bool, allowCurrent bool) (candidates []string, releases map[string]string, err error) {
	names, err := pruneMergedNames(env, s, trunkRef, scoped, members)
	if err != nil {
		return nil, nil, err
	}
	return gatePruneCandidates(env, s, names, allowCurrent)
}

// pruneMergedNames returns the tracked branches already merged into trunkRef,
// in sorted order — tips sanity check plus the merged enumeration. When scoped
// is true, only branches in members are checked and returned.
func pruneMergedNames(env Env, s *State, trunkRef string, scoped bool, members map[string]bool) ([]string, error) {
	g := env.Git
	names := s.BranchNames()
	if scoped {
		filtered := make([]string, 0, len(members))
		for _, name := range names {
			if members[name] {
				filtered = append(filtered, name)
			}
		}
		names = filtered
	}
	tips, err := g.TipsFor(names)
	if err != nil {
		return nil, fmt.Errorf("read tracked branch tips: %w", err)
	}
	for _, name := range names {
		if _, ok := tips[name]; !ok {
			return nil, fmt.Errorf("tracked branch %q does not exist", name)
		}
	}
	merged, err := mergedBranches(g, names, trunkRef)
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, name := range names {
		if merged[name] {
			candidates = append(candidates, name)
		}
	}
	return candidates, nil
}

// applyPrune deletes each candidate: releases its owned worktree (the gate's
// release plan says which — re-probing would pay a second round of `git -C`
// status checks for an answer computed moments ago under the same lock), drops
// the git branch, untracks it, and checkpoints. Callers pass the
// gatePruneCandidates result.
func applyPrune(env Env, s *State, candidates []string, releases map[string]string) ([]string, error) {
	g := env.Git
	var deleted []string
	// Release phase: every owned worktree is torn down before any deletion —
	// a dirty owner aborts the whole prune with zero branches gone.
	for _, name := range candidates {
		if path := releases[name]; path != "" {
			if err := g.WorktreeRemove(path, false); err != nil {
				return deleted, fmt.Errorf("removing worktree %q for %q: %w", path, name, err)
			}
		}
	}
	// Delete phase: ONE `git branch -D` for the whole set. git deletes what it
	// can and errors per-arg on the rest, so on failure each candidate is
	// re-probed and the survivors retried one-by-one — the error names the
	// branch that actually failed, not just the batch's first complaint.
	deletedSet := make(map[string]bool, len(candidates))
	var firstErr error
	if err := g.DeleteBranches(candidates, true); err == nil {
		for _, name := range candidates {
			deletedSet[name] = true
		}
	} else {
		for _, name := range candidates {
			if !g.BranchExists(name) {
				deletedSet[name] = true
				continue
			}
			if err := g.DeleteBranch(name, true); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("delete merged branch %q: %w", name, err)
				}
				continue
			}
			deletedSet[name] = true
		}
	}
	// Checkpoint each deleted branch's untracking exactly as the per-branch
	// loop did — the crash-recovery bound is unchanged. One checkpoint costs
	// ~10ms (fsync-bound, measured via atomicWriteFile bench on M1 Max):
	// material, but unbatchable — the per-branch granularity is precisely
	// what a mid-prune crash recovers from, so it stays.
	for _, name := range candidates {
		if !deletedSet[name] {
			continue
		}
		s.RemoveBranch(name)
		deleted = append(deleted, name)
		if err := env.save(); err != nil {
			return deleted, fmt.Errorf("save state after pruning %q: %w", name, err)
		}
	}
	if firstErr != nil {
		return deleted, firstErr
	}
	return deleted, nil
}

// mergedBranches returns the set of tracked branches trunkRef already
// contains — the ones a sync prune may delete without losing anything. Two
// detection layers: ancestry-merged tips, answered for every local branch by
// one batched MergedInto scan; and content-equivalent branches — squash-merged
// or fully cherry-picked, where the branch's whole diff relative to its merge
// base is already in trunkRef's tree even though its tip is no ancestor —
// answered per unmerged branch by ChangesContainedIn's exact tree comparison
// (never a patch-id heuristic, so a branch carrying unique content is never
// flagged). trunkRef may be the local trunk or a fetched remote-tracking ref;
// a bare name is qualified so a tag can never shadow the trunk branch.
func mergedBranches(g Git, names []string, trunkRef string) (map[string]bool, error) {
	if !strings.HasPrefix(trunkRef, "refs/") {
		trunkRef = branchTipRef(trunkRef)
	}
	merged, err := g.MergedInto(trunkRef)
	if err != nil {
		return nil, fmt.Errorf("list branches merged into %q: %w", trunkRef, err)
	}
	// The containment diffs are independent read-only probes — fan them out
	// and fold the results back in sorted-name order, so the merged map and
	// the first error are exactly what a serial pass would produce.
	unmerged := make([]string, 0, len(names))
	for _, name := range names {
		if !merged[name] {
			unmerged = append(unmerged, name)
		}
	}
	contained := make([]bool, len(unmerged))
	if err := git.ParallelProbes(len(unmerged), func(i int) error {
		ok, err := g.ChangesContainedIn(trunkRef, branchTipRef(unmerged[i]))
		if err != nil {
			return fmt.Errorf("check whether %q's changes are contained in %q: %w", unmerged[i], trunkRef, err)
		}
		contained[i] = ok
		return nil
	}); err != nil {
		return nil, err
	}
	for i, name := range unmerged {
		if contained[i] {
			merged[name] = true
		}
	}
	return merged, nil
}

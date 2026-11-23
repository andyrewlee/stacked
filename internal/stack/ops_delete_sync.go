// ops_delete_sync.go — Delete and Sync: fetching, fast-forwarding the trunk,
// pruning merged branches, and restacking what survives.
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
	// Never remove another worktree on disk: refuse when the branch is checked
	// out elsewhere and tell the caller which worktree owns it.
	if owner, elsewhere, err := s.ownerElsewhere(g, name); err != nil {
		return nil, err
	} else if elsewhere {
		if owner.Prunable {
			return nil, fmt.Errorf("branch %q is registered to a missing worktree %q; run `git worktree prune` first", name, owner.Path)
		}
		return nil, fmt.Errorf("branch %q is checked out in another worktree %q; switch away from it there before deleting it", name, owner.Path)
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
// pruning.
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
		if !descSet[b.Parent] {
			direct = append(direct, name)
		}
	}
	if len(direct) != 1 {
		return ""
	}
	return direct[0]
}

// SyncOptions configures SyncWithOptions.
type SyncOptions struct {
	Remote   string
	NoDelete bool
	NoFetch  bool
	All      bool
	// ExtraMerged names branches confirmed merged externally (e.g. via GitHub
	// PR state) even when their local diff was edited during squash-merge.
	ExtraMerged map[string]bool
}

// Sync fetches and fast-forwards the trunk via the remote port, prunes branches
// already merged into the trunk, restacks remaining branches onto the updated
// trunk, and restores the caller's branch (or advances to the single surviving
// child when the current branch was pruned).
func Sync(env Env, r Remote, s *State, remote string, noDelete, noFetch, all bool) (*OpResult, error) {
	return SyncWithOptions(env, r, s, SyncOptions{
		Remote:   remote,
		NoDelete: noDelete,
		NoFetch:  noFetch,
		All:      all,
	})
}

// SyncWithOptions is Sync with full SyncOptions support (including ExtraMerged
// branches confirmed merged on GitHub).
func SyncWithOptions(env Env, r Remote, s *State, opts SyncOptions) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	orig, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	scoped, scopeMembers := syncScope(s, orig, opts.All)
	var origDescendants []string
	if s.IsTracked(orig) {
		origDescendants = s.Descendants(orig)
	}
	beforeParents := parentSnapshot(s)

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

	ffResult := "skipped (no remote)"
	trunkRef := branchTipRef(s.Trunk)
	switch {
	case opts.NoFetch:
		ffResult = "skipped (--no-fetch)"
	case r.Exists(opts.Remote):
		if err := r.Fetch(opts.Remote); err != nil {
			return nil, fmt.Errorf("fetch %q: %w", opts.Remote, err)
		}
		ffResult, err = r.FastForward(s.Trunk, opts.Remote, trunkOwnerDir, trunkHere)
		if err != nil {
			if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
			}
			return nil, err
		}
	}

	var deleted []string
	var pruneNotes []string
	if !opts.NoDelete {
		candidates, skippedNotes, err := pruneTargetsScoped(env, s, trunkRef, scoped, scopeMembers, orig, opts.ExtraMerged)
		if err != nil {
			return nil, err
		}
		pruneNotes = skippedNotes
		if len(candidates) > 0 {
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
			if deleted, err = applyPrune(env, s, candidates); err != nil {
				if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
					return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
				}
				return nil, err
			}
		}
	}
	reparented := diffReparents(beforeParents, s)
	if err := env.save(); err != nil {
		return nil, err
	}

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
		if cur, curErr := g.CurrentBranch(); curErr == nil && cur != "" {
			notes = append(notes, "HEAD is on "+cur+"; trunk is checked out in "+trunkOwnerDir)
		} else {
			notes = append(notes, "HEAD left detached; trunk is checked out in "+trunkOwnerDir)
		}
	} else if err := restoreHEAD(env, restoreTarget, s.Trunk); err != nil {
		return nil, err
	}
	notes = append(notes, pruneNotes...)
	notes = append(notes, skippedWorktreeNotes(s)...)
	notes = append(notes, unreachableNotes(s)...)

	return &OpResult{
		Summary:    "sync complete",
		Branch:     advancedTo,
		Deleted:    deleted,
		Reparented: reparented,
		Restacked:  rebased,
		Notes:      notes,
	}, nil
}

// SyncPlanAgainst previews what a sync would do against the supplied trunk
// ref — which merged branches it would prune and which branches it would
// restack — without fetching, fast-forwarding, or mutating anything.
func SyncPlanAgainst(env Env, s *State, noDelete, all bool, trunkRef string) (*OpResult, error) {
	return SyncPlanAgainstWithMerged(env, s, noDelete, all, trunkRef, nil)
}

// SyncPlanAgainstWithMerged is SyncPlanAgainst with an optional ExtraMerged set.
func SyncPlanAgainstWithMerged(env Env, s *State, noDelete, all bool, trunkRef string, extraMerged map[string]bool) (*OpResult, error) {
	g := env.Git
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
	var pruneNotes []string
	if !noDelete {
		candidates, skippedNotes, err := pruneTargetsScoped(env, s, trunkRef, scoped, scopeMembers, orig, extraMerged)
		if err != nil {
			return nil, err
		}
		pruneNotes = skippedNotes
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
	notes := make([]string, 0, len(pruneNotes)+len(preview.notes()))
	notes = append(notes, pruneNotes...)
	notes = append(notes, preview.notes()...)
	return &OpResult{Summary: "sync (dry run)", Deleted: deletedList, Reparented: reparented, Restacked: plan, Notes: notes, DryRun: true}, nil
}

// PruneMerged deletes tracked branches whose commits or content are already
// contained in the local trunk.
func PruneMerged(env Env, s *State) ([]string, error) {
	cur, _ := env.Git.CurrentBranch()
	candidates, _, err := pruneTargetsScoped(env, s, branchTipRef(s.Trunk), false, nil, cur, nil)
	if err != nil {
		return nil, err
	}
	return applyPrune(env, s, candidates)
}

// pruneTargetsScoped returns the deletable merged set in sorted order plus
// notes for any merged branches skipped because they are checked out in
// another worktree.
func pruneTargetsScoped(env Env, s *State, trunkRef string, scoped bool, members map[string]bool, cur string, extraMerged map[string]bool) (candidates, notes []string, err error) {
	names, err := pruneMergedNames(env, s, trunkRef, scoped, members, extraMerged)
	if err != nil {
		return nil, nil, err
	}
	if len(names) == 0 {
		return nil, nil, nil
	}
	g := env.Git
	wts, err := g.Worktrees()
	if err != nil {
		return nil, nil, err
	}
	if !IsMultiWorktree(wts) {
		return names, nil, nil
	}
	for _, name := range names {
		if owner, elsewhere := ownerElsewhereFrom(wts, name, cur); elsewhere {
			notes = append(notes, fmt.Sprintf("skipped pruning %s: checked out in worktree %s", name, owner.Path))
			continue
		}
		candidates = append(candidates, name)
	}
	return candidates, notes, nil
}

// pruneMergedNames returns the tracked branches already merged into trunkRef
// (or confirmed merged in extraMerged), in sorted order.
func pruneMergedNames(env Env, s *State, trunkRef string, scoped bool, members, extraMerged map[string]bool) ([]string, error) {
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
	for name, ok := range extraMerged {
		if ok {
			merged[name] = true
		}
	}
	var candidates []string
	for _, name := range names {
		if merged[name] {
			candidates = append(candidates, name)
		}
	}
	return candidates, nil
}

// applyPrune deletes each candidate git branch, untracks it, and checkpoints.
func applyPrune(env Env, s *State, candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	g := env.Git
	var deleted []string
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

// mergedBranches returns the set of tracked branches trunkRef already contains
// via commit ancestry (MergedInto) or exact tree-content containment
// (ChangesContainedIn).
func mergedBranches(g Git, names []string, trunkRef string) (map[string]bool, error) {
	if !strings.HasPrefix(trunkRef, "refs/") {
		trunkRef = branchTipRef(trunkRef)
	}
	merged, err := g.MergedInto(trunkRef)
	if err != nil {
		return nil, fmt.Errorf("list branches merged into %q: %w", trunkRef, err)
	}
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

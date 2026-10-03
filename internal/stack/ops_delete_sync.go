// ops_delete_sync.go — Delete plus the Sync/Prune family: fetching,
// fast-forwarding the trunk, pruning merged branches, and restacking what
// survives.
package stack

import (
	"fmt"
	"strings"
)

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
		mergedIntoParent, err := g.IsAncestor(branchTipRef(name), branchTipRef(parent))
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
	formerChildren := s.RemoveBranch(name)
	if err := env.save(); err != nil {
		return nil, err
	}

	// One pass over one shared tips map (the walk order — each child, then its
	// descendants — matches DeletePlan's appendRestackPlans exactly), instead
	// of a fresh full ref scan per re-parented child.
	restacked, err := s.restackForest(env, formerChildren)
	if err != nil {
		return nil, restoreHEADAfterNonConflict(env, start, s.Trunk, err)
	}
	notes := skippedWorktreeNotes(s)
	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}

	res := &OpResult{Summary: fmt.Sprintf("Deleted %s", name), Deleted: []string{name}, Restacked: restacked, Notes: notes}
	if len(formerChildren) > 0 {
		res.Summary = fmt.Sprintf("Deleted %s; re-parented %d branch(es) onto %s", name, len(formerChildren), parent)
	}
	return res, nil
}

// Sync fetches and fast-forwards the trunk via the remote port, prunes branches
// already merged into the trunk, restacks every remaining stack onto the updated
// trunk, and restores the caller's branch. With noDelete, merged branches are
// kept. With noFetch the remote is untouched — no fetch, no fast-forward — and
// the local trunk is the single basis for both pruning and restacking: a branch
// merged only into the cached remote ref survives until the local trunk
// advances. Requires a clean working tree.
func Sync(env Env, r Remote, s *State, remote string, noDelete, noFetch bool) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	orig, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
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
		if deleted, err = PruneMergedAgainst(env, s, trunkRef); err != nil {
			if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
			}
			return nil, err
		}
	}
	// Persist the prune before restacking so a conflict cannot leave the metadata
	// referencing already-deleted branches.
	if err := env.save(); err != nil {
		return nil, err
	}

	rebased, err := restackAll(env, s)
	if err != nil {
		// Leaves a conflict's rebase in progress for `st continue`; restores HEAD
		// otherwise. Same guard the other mutations use.
		return nil, restoreHEADAfterNonConflict(env, orig, s.Trunk, err)
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	notes := []string{"trunk: " + ffResult}
	if !g.BranchExists(orig) && trunkOwnerDir != "" {
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
	} else if err := restoreHEAD(env, orig, s.Trunk); err != nil {
		return nil, err
	}

	return &OpResult{
		Summary:   "sync complete",
		Deleted:   deleted,
		Restacked: rebased,
		Notes:     append(notes, append(skippedWorktreeNotes(s), unreachableNotes(s)...)...),
	}, nil
}

// SyncPlanAgainst previews what a sync would do against the supplied trunk
// ref — which merged branches it would prune and which branches it would
// restack — without fetching, fast-forwarding, or mutating anything. Used by
// the CLI dry-run path against the already-fetched remote trunk ref (the
// dry run never fetches).
func SyncPlanAgainst(env Env, s *State, noDelete bool, trunkRef string) (*OpResult, error) {
	g := env.Git
	// The real Sync requires a clean tree (Sync, above), so the preview must
	// too — otherwise it reports branches it "would restack" that the real
	// command will refuse to touch, returning exit 0 instead of the dirty-tree
	// exit code.
	if err := requireClean(g); err != nil {
		return nil, err
	}
	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	if err := requireStateTips(s, tips); err != nil {
		return nil, err
	}
	if trunkRef != s.Trunk && trunkRef != branchTipRef(s.Trunk) {
		trunkTip, err := g.RevParse(trunkRef)
		if err != nil {
			return nil, fmt.Errorf("resolve trunk ref %q: %w", trunkRef, err)
		}
		tips[s.Trunk] = trunkTip
	}
	planState := cloneState(s)
	deleted := map[string]bool{}
	var deletedList []string
	if !noDelete {
		candidates, err := pruneTargets(env, s, trunkRef)
		if err != nil {
			return nil, err
		}
		for _, name := range candidates {
			planState.RemoveBranch(name)
			deleted[name] = true
			deletedList = append(deletedList, name)
		}
	}
	preview, err := restackPlanAgainstWithWorktrees(env, planState, planState.Trunk, tips)
	if err != nil {
		return nil, err
	}
	var plan []string
	for _, name := range preview.restacked {
		if !deleted[name] {
			plan = append(plan, name)
		}
	}
	return &OpResult{Summary: "sync (dry run)", Deleted: deletedList, Restacked: plan, Notes: preview.notes(), DryRun: true}, nil
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
	candidates, err := pruneTargets(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	return applyPrune(env, s, candidates)
}

// Prune is the standalone `st prune`: delete every tracked branch already
// merged into trunkRef (local trunk or an explicit remote-tracking ref). Unlike
// Sync it never fetches, fast-forwards, moves HEAD, or restacks — so when HEAD
// is on a prunable branch it refuses rather than auto-checkout: sync owns the
// move. No clean-tree requirement: branch deletion touches no worktree content.
func Prune(env Env, s *State, trunkRef string) (*OpResult, error) {
	names, err := pruneMergedNames(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	if err := refusePruneCurrent(env, names); err != nil {
		return nil, err
	}
	candidates, err := gatePruneCandidates(env, s, names)
	if err != nil {
		return nil, err
	}
	deleted, err := applyPrune(env, s, candidates)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary: fmt.Sprintf("Pruned %d merged branch(es)", len(deleted)),
		Deleted: deleted,
	}, nil
}

// PrunePlan previews Prune against trunkRef — the same merged enumeration,
// current-branch refusal, and worktree-release gate, with nothing deleted.
func PrunePlan(env Env, s *State, trunkRef string) (*OpResult, error) {
	names, err := pruneMergedNames(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	if err := refusePruneCurrent(env, names); err != nil {
		return nil, err
	}
	candidates, err := gatePruneCandidates(env, s, names)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: "prune (dry run)", Deleted: candidates, DryRun: true}, nil
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
// the same check applyPrune's releaseOwnedWorktree performs — so a preview and
// an apply share identical eligibility, and a dirty owner fails BEFORE any
// branch is deleted instead of mid-loop.
func gatePruneCandidates(env Env, s *State, names []string) ([]string, error) {
	var candidates []string
	for _, name := range names {
		if _, err := s.ownedWorktreeReleaseTarget(env, name); err != nil {
			return nil, err
		}
		candidates = append(candidates, name)
	}
	return candidates, nil
}

// pruneTargets returns the deletable merged set in sorted order for the sync
// path (HEAD already moved off any prunable branch): pruneMergedNames plus the
// release gate.
func pruneTargets(env Env, s *State, trunkRef string) ([]string, error) {
	names, err := pruneMergedNames(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	return gatePruneCandidates(env, s, names)
}

// pruneMergedNames returns the tracked branches already merged into trunkRef,
// in sorted order — tips sanity check plus the merged enumeration. Shared by
// every prune preview and apply path.
func pruneMergedNames(env Env, s *State, trunkRef string) ([]string, error) {
	g := env.Git
	names := sortedBranchNames(s)
	tips, err := g.TipsFor(names)
	if err != nil {
		return nil, fmt.Errorf("read tracked branch tips: %w", err)
	}
	for _, name := range names {
		if _, ok := tips[name]; !ok {
			return nil, fmt.Errorf("tracked branch %q does not exist", name)
		}
	}
	merged, err := mergedBranches(g, s, trunkRef)
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

// applyPrune deletes each candidate: releases its owned worktree (a dirty one
// errors, leaving everything pruned so far intact), drops the git branch,
// untracks it, and checkpoints. Callers pass the gatePruneCandidates result.
func applyPrune(env Env, s *State, candidates []string) ([]string, error) {
	g := env.Git
	var deleted []string
	// Release phase: every owned worktree is torn down before any deletion —
	// a dirty owner aborts the whole prune with zero branches gone.
	for _, name := range candidates {
		if err := s.releaseOwnedWorktree(env, name); err != nil {
			return deleted, err
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
	// loop did — the crash-recovery bound is unchanged.
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
func mergedBranches(g Git, s *State, trunkRef string) (map[string]bool, error) {
	if !strings.HasPrefix(trunkRef, "refs/") {
		trunkRef = branchTipRef(trunkRef)
	}
	merged, err := g.MergedInto(trunkRef)
	if err != nil {
		return nil, fmt.Errorf("list branches merged into %q: %w", trunkRef, err)
	}
	for _, name := range sortedBranchNames(s) {
		if merged[name] {
			continue
		}
		contained, err := g.ChangesContainedIn(trunkRef, branchTipRef(name))
		if err != nil {
			return nil, fmt.Errorf("check whether %q's changes are contained in %q: %w", name, trunkRef, err)
		}
		if contained {
			merged[name] = true
		}
	}
	return merged, nil
}

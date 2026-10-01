// ops_combine.go — history-combining ops: Fold collapses a branch into its
// parent; Squash rewrites a branch's commits into one.
package stack

import (
	"fmt"
	"strings"
)

// Fold folds the current branch into its parent: the parent advances to include
// the branch's commits, the branch is deleted, and its children are re-parented
// onto the parent.
func Fold(env Env, s *State) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, b, err := currentTracked(g, s)
	if err != nil {
		return nil, err
	}
	parent := b.Parent
	if parent == s.Trunk {
		return nil, fmt.Errorf("cannot fold %q into the trunk %q", cur, parent)
	}
	needs, err := s.NeedsRestack(g, cur)
	if err != nil {
		return nil, err
	}
	if needs {
		return nil, fmt.Errorf("%q needs restack before folding (run: st restack)", cur)
	}
	// Fold deletes cur; if it lives in another worktree git would refuse, so tear
	// that (clean) worktree down first. A dirty owner errors before any git
	// mutation, so nothing changes. (Normally cur is checked out HERE and this is
	// a no-op; the guard covers the case where fold runs against an owned-elsewhere
	// branch.)
	if err := s.releaseOwnedWorktree(env, cur); err != nil {
		return nil, err
	}
	if owner, elsewhere, err := s.ownerElsewhere(g, parent); err != nil {
		return nil, err
	} else if elsewhere {
		return nil, fmt.Errorf("cannot fold into %q because it is checked out in another worktree %q", parent, owner.Path)
	}

	curTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, err
	}
	// Capture the parent's tip first so the git side can be rolled back if a later
	// step fails. Advancing the parent ref and only then failing to check out or
	// delete would leave the parent silently holding cur's commits while the
	// persisted metadata still listed cur as a separate branch — a repo/state
	// disagreement a naive retry would compound. The metadata is therefore moved
	// (re-parent children, untrack cur) and saved only after every git mutation
	// has committed.
	parentTip, err := g.RevParse(branchTipRef(parent))
	if err != nil {
		return nil, err
	}
	if err := g.ForceBranch(parent, curTip); err != nil {
		return nil, fmt.Errorf("advancing %q to %q: %w", parent, cur, err)
	}
	if err := g.Checkout(parent); err != nil {
		err = fmt.Errorf("checking out %q: %w", parent, err)
		if rollbackErr := g.UpdateRef(branchTipRef(parent), parentTip); rollbackErr != nil {
			return nil, AlsoFailed(err, fmt.Sprintf("roll back %q", parent), rollbackErr)
		}
		return nil, err
	}
	if err := g.DeleteBranch(cur, true); err != nil {
		if rollbackErr := g.UpdateRef(branchTipRef(parent), parentTip); rollbackErr != nil {
			return nil, AlsoFailed(fmt.Errorf("deleting %q: %w", cur, err), fmt.Sprintf("roll back %q", parent), rollbackErr)
		}
		if restoreErr := g.Checkout(cur); restoreErr != nil {
			return nil, AlsoFailed(fmt.Errorf("deleting %q: %w", cur, err), fmt.Sprintf("restore %q after rolling back %q", cur, parent), restoreErr)
		}
		return nil, fmt.Errorf("deleting %q: %w", cur, err)
	}
	s.RemoveBranch(cur)
	if err := env.save(); err != nil {
		return nil, err
	}

	upstack, err := finishUpstack(env, s, parent)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Folded %s into %s", cur, parent), Branch: parent, Restacked: upstack.restacked, Notes: upstack.notes}, nil
}

// Squash collapses every commit on the current branch (since its parent) into
// one, then restacks its descendants. With an empty message the squashed
// message is composed from the existing commit subjects.
func Squash(env Env, s *State, message string) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, b, err := currentTracked(g, s)
	if err != nil {
		return nil, err
	}
	needs, err := s.NeedsRestack(g, cur)
	if err != nil {
		return nil, err
	}
	if needs {
		return nil, fmt.Errorf("%q needs restack before squashing (run: st restack)", cur)
	}

	base := b.ParentSHA
	subjects, err := g.CommitSubjects(base, cur)
	if err != nil {
		return nil, err
	}
	if len(subjects) <= 1 {
		return &OpResult{Summary: fmt.Sprintf("%s already has a single commit; nothing to squash", cur), Branch: cur}, nil
	}
	origTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, fmt.Errorf("resolving %q before squash: %w", cur, err)
	}
	if message == "" {
		message = subjects[len(subjects)-1]
		var body []string
		for i := len(subjects) - 2; i >= 0; i-- {
			body = append(body, "- "+subjects[i])
		}
		if len(body) > 0 {
			message += "\n\n" + strings.Join(body, "\n")
		}
	}

	if err := g.ResetSoft(base); err != nil {
		return nil, fmt.Errorf("resetting %q to base: %w", cur, err)
	}
	if err := g.Commit(message, false); err != nil {
		err = fmt.Errorf("creating squashed commit on %q: %w", cur, err)
		if restoreErr := g.ResetSoft(origTip); restoreErr != nil {
			return nil, AlsoFailed(err, fmt.Sprintf("restore %q to %s", cur, origTip), restoreErr)
		}
		return nil, err
	}

	upstack, err := finishUpstack(env, s, cur)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Squashed %d commits on %s into one", len(subjects), cur), Branch: cur, Restacked: upstack.restacked, Notes: upstack.notes}, nil
}

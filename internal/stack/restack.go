package stack

import "fmt"

func branchTipRef(name string) string {
	return "refs/heads/" + name
}

// NeedsRestack reports whether the named branch is out of date relative to its
// parent, i.e. the parent's current tip differs from the SHA this branch was
// last based onto.
func (s *State) NeedsRestack(g Git, name string) (bool, error) {
	return s.needsRestackAgainst(g, name, branchTipRef(s.Trunk))
}

func (s *State) needsRestackAgainst(g Git, name, trunkRef string) (bool, error) {
	b, err := s.tracked(name)
	if err != nil {
		return false, err
	}
	parentRef := branchTipRef(b.Parent)
	if b.Parent == s.Trunk {
		parentRef = trunkRef
	}
	parentTip, err := g.RevParse(parentRef)
	if err != nil {
		return false, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
	}
	return parentTip != b.ParentSHA, nil
}

func (s *State) needsRestackAgainstTips(name string, tips map[string]string) (bool, error) {
	b, err := s.tracked(name)
	if err != nil {
		return false, err
	}
	parentTip, ok := tips[b.Parent]
	return ok && parentTip != b.ParentSHA, nil
}

// rebaseFailure classifies a failed RebaseOnto. paused is true when the rebase
// stopped mid-way and left a rebase in progress — a conflict the caller turns
// into a ConflictError (and may record bookkeeping such as a PendingReparent
// for). When paused is false, nonConflictErr is the error to return: git's
// rebase-state probe failing is reported distinctly from the rebase failing
// outright. action is the verb used in the messages ("rebasing"/"moving"). This
// is the single definition of "did the rebase pause on a conflict?", shared by
// RestackBranch and Onto so the two cannot drift.
func rebaseFailure(g Git, rebaseErr error, action, branch, onto string) (paused bool, nonConflictErr error) {
	inProgress, progressErr := g.RebaseInProgress()
	if progressErr == nil && inProgress {
		return true, nil
	}
	if progressErr != nil {
		return false, fmt.Errorf("checking rebase state after %s %q onto %q failed: %v (original error: %w)", action, branch, onto, progressErr, rebaseErr)
	}
	return false, fmt.Errorf("%s %q onto %q: %w", action, branch, onto, rebaseErr)
}

// RestackBranch rebases the named branch onto the current tip of its parent if
// it is out of date; otherwise it is a no-op. It reports whether it actually
// rebased, so callers need no NeedsRestack pre-check. On a successful rebase
// the branch's ParentSHA is updated to the parent tip and the env is asked to
// persist. If the rebase fails, a wrapped error explaining how to recover is
// returned.
func (s *State) RestackBranch(env Env, name string) (bool, error) {
	b, err := s.tracked(name)
	if err != nil {
		return false, err
	}
	parentTip, err := env.Git.RevParse(branchTipRef(b.Parent))
	if err != nil {
		return false, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
	}
	if parentTip == b.ParentSHA {
		return false, nil
	}

	// Owner-driven cross-worktree restack: if name is checked out in ANOTHER
	// worktree, git forbids rebasing it here, so the rebase must run in that
	// worktree. This path activates only in a multi-worktree repo where name is
	// owned elsewhere; single-tree behavior (and the model invariant test, whose
	// fake reports a single worktree) is unchanged.
	if owner, elsewhere, err := s.ownerElsewhere(env.Git, name); err != nil {
		return false, err
	} else if elsewhere {
		return s.restackInWorktree(env, name, b, parentTip, owner)
	}

	start, startErr := env.Git.CurrentBranch()
	if rebaseErr := env.Git.RebaseOnto(parentTip, b.ParentSHA, name); rebaseErr != nil {
		paused, outErr := rebaseFailure(env.Git, rebaseErr, "rebasing", name, b.Parent)
		if paused {
			return false, &ConflictError{Action: "rebasing", Branch: name, Onto: b.Parent}
		}
		// A non-conflict failure left HEAD wherever the rebase aborted to; put the
		// caller back on the branch they started on before surfacing outErr.
		if startErr == nil {
			if restoreErr := restoreHEAD(env, start, s.Trunk); restoreErr != nil {
				return false, AlsoFailed(outErr, fmt.Sprintf("restore %q", start), restoreErr)
			}
		}
		return false, outErr
	}
	b.ParentSHA = parentTip
	if err := env.save(); err != nil {
		return false, fmt.Errorf("save state after restacking %q: %w", name, err)
	}
	return true, nil
}

// DriftAgainst computes which tracked branches need a restack purely from a
// branch-name → tip map (one Tips() read), with no further git access. A
// parent missing from the map means its git branch is missing; drift is
// reported false for that branch (the missing branch itself is a problem the
// consumers report separately). Mutation paths keep reading live tips per
// step — correctness during restacks depends on it — this is for the
// read-only consumers (log, validate).
func (s *State) DriftAgainst(tips map[string]string) map[string]bool {
	drift := make(map[string]bool, len(s.Branches))
	for name, b := range s.Branches {
		parentTip, ok := tips[b.Parent]
		drift[name] = ok && parentTip != b.ParentSHA
	}
	return drift
}

// RestackPlan previews the branches a restack from the current branch would
// rebase, without mutating anything.
func RestackPlan(env Env, s *State) (*OpResult, error) {
	// The real Restack requires a clean tree, so the preview must too — otherwise
	// it lists branches it "would restack" that the real command refuses to
	// touch, exiting 0 instead of the dirty-tree exit code.
	if err := requireClean(env.Git); err != nil {
		return nil, err
	}
	tips, err := env.Git.Tips()
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	start, err := env.Git.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if start != s.Trunk && !s.IsTracked(start) {
		return nil, fmt.Errorf("branch %q is not tracked", start)
	}
	preview, err := restackPlanAgainstWithWorktrees(env, s, start, tips)
	if err != nil {
		return nil, err
	}
	summary := "nothing to restack"
	if len(preview.restacked) > 0 {
		summary = fmt.Sprintf("would restack %d branch(es)", len(preview.restacked))
	}
	return &OpResult{Summary: summary, Restacked: preview.restacked, Notes: preview.notes(), DryRun: true}, nil
}

// RestackUpstack restacks the descendants of name in topological order
// (parents before children). The branch name itself is not restacked. It
// returns the names of the branches that were actually rebased.
func (s *State) RestackUpstack(env Env, name string) ([]string, error) {
	var rebased []string
	for _, child := range s.Descendants(name) {
		did, err := s.RestackBranch(env, child)
		if err != nil {
			return rebased, err
		}
		if did {
			rebased = append(rebased, child)
		}
	}
	return rebased, nil
}

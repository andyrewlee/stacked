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
	b, ok := s.Get(name)
	if !ok {
		return false, fmt.Errorf("branch %q is not tracked", name)
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

// RestackBranch rebases the named branch onto the current tip of its parent if
// it is out of date; otherwise it is a no-op. It reports whether it actually
// rebased, so callers need no NeedsRestack pre-check. On a successful rebase
// the branch's ParentSHA is updated to the parent tip and the env is asked to
// persist. If the rebase fails, a wrapped error explaining how to recover is
// returned.
func (s *State) RestackBranch(env Env, name string) (bool, error) {
	b, ok := s.Get(name)
	if !ok {
		return false, fmt.Errorf("branch %q is not tracked", name)
	}
	parentTip, err := env.Git.RevParse(branchTipRef(b.Parent))
	if err != nil {
		return false, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
	}
	if parentTip == b.ParentSHA {
		return false, nil
	}
	start, startErr := env.Git.CurrentBranch()
	if err := env.Git.RebaseOnto(parentTip, b.ParentSHA, name); err != nil {
		inProgress, progressErr := env.Git.RebaseInProgress()
		if progressErr == nil && inProgress {
			return false, fmt.Errorf("rebasing %q onto %q: %w", name, b.Parent, ErrConflict)
		}
		if startErr == nil {
			if restoreErr := restoreHEAD(env, start, s.Trunk); restoreErr != nil {
				return false, AlsoFailed(fmt.Errorf("rebasing %q onto %q: %w", name, b.Parent, err), fmt.Sprintf("restore %q", start), restoreErr)
			}
		}
		if progressErr != nil {
			return false, fmt.Errorf("checking rebase state after rebasing %q onto %q failed: %v (original error: %w)", name, b.Parent, progressErr, err)
		}
		return false, fmt.Errorf("rebasing %q onto %q: %w", name, b.Parent, err)
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

// restackPlan returns, in order, the branches a restack starting at `start`
// would rebase — without changing anything. A branch is rebased if it is out of
// date, or its parent will be rebased (which moves the parent tip and forces the
// child). When start is the trunk, the whole forest is considered.
func (s *State) restackPlan(g Git, start string) ([]string, error) {
	return s.restackPlanAgainst(g, start, branchTipRef(s.Trunk))
}

func (s *State) restackPlanAgainst(g Git, start, trunkRef string) ([]string, error) {
	var order []string
	if start != s.Trunk {
		order = append(order, start)
		order = append(order, s.Descendants(start)...)
	} else {
		order = s.Descendants(s.Trunk)
	}
	inPlan := map[string]bool{}
	var plan []string
	for _, name := range order {
		b, ok := s.Get(name)
		if !ok {
			continue
		}
		needs, err := s.needsRestackAgainst(g, name, trunkRef)
		if err != nil {
			return nil, err
		}
		if needs || inPlan[b.Parent] {
			plan = append(plan, name)
			inPlan[name] = true
		}
	}
	return plan, nil
}

// RestackPlan previews the branches a restack from the current branch would
// rebase, without mutating anything.
func RestackPlan(env Env, s *State) (*OpResult, error) {
	start, err := env.Git.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if start != s.Trunk && !s.IsTracked(start) {
		return nil, fmt.Errorf("branch %q is not tracked", start)
	}
	plan, err := s.restackPlan(env.Git, start)
	if err != nil {
		return nil, err
	}
	summary := "nothing to restack"
	if len(plan) > 0 {
		summary = fmt.Sprintf("would restack %d branch(es)", len(plan))
	}
	return &OpResult{Summary: summary, Restacked: plan, DryRun: true}, nil
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

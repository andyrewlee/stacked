package stack

import "fmt"

// NeedsRestack reports whether the named branch is out of date relative to its
// parent, i.e. the parent's current tip differs from the SHA this branch was
// last based onto.
func (s *State) NeedsRestack(g Git, name string) (bool, error) {
	b, ok := s.Get(name)
	if !ok {
		return false, fmt.Errorf("branch %q is not tracked", name)
	}
	parentTip, err := g.RevParse(b.Parent)
	if err != nil {
		return false, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
	}
	return parentTip != b.ParentSHA, nil
}

// RestackBranch rebases the named branch onto the current tip of its parent if
// it is out of date; otherwise it is a no-op. On a successful rebase the
// branch's ParentSHA is updated to the parent tip and the env is asked to
// persist. If the rebase fails, a wrapped error explaining how to recover is
// returned.
func (s *State) RestackBranch(env Env, name string) error {
	b, ok := s.Get(name)
	if !ok {
		return fmt.Errorf("branch %q is not tracked", name)
	}
	parentTip, err := env.Git.RevParse(b.Parent)
	if err != nil {
		return fmt.Errorf("resolve parent %q: %w", b.Parent, err)
	}
	if parentTip == b.ParentSHA {
		return nil
	}
	if err := env.Git.RebaseOnto(parentTip, b.ParentSHA, name); err != nil {
		return fmt.Errorf("rebasing %q onto %q: %w", name, b.Parent, ErrConflict)
	}
	b.ParentSHA = parentTip
	if err := env.save(); err != nil {
		return fmt.Errorf("save state after restacking %q: %w", name, err)
	}
	return nil
}

// restackPlan returns, in order, the branches a restack starting at `start`
// would rebase — without changing anything. A branch is rebased if it is out of
// date, or its parent will be rebased (which moves the parent tip and forces the
// child). When start is the trunk, the whole forest is considered.
func (s *State) restackPlan(g Git, start string) ([]string, error) {
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
		needs, err := s.NeedsRestack(g, name)
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
		needs, err := s.NeedsRestack(env.Git, child)
		if err != nil {
			return rebased, err
		}
		if !needs {
			continue
		}
		if err := s.RestackBranch(env, child); err != nil {
			return rebased, err
		}
		rebased = append(rebased, child)
	}
	return rebased, nil
}

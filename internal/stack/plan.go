package stack

import "fmt"

// FoldPlan previews folding the current branch into its parent.
func FoldPlan(env Env, s *State) (*OpResult, error) {
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
	if _, err := s.ownedWorktreeReleaseTarget(env, cur); err != nil {
		return nil, err
	}

	curTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, err
	}
	tips, err := g.Tips()
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	planState := cloneState(s)
	planState.RemoveBranch(cur)
	tips[parent] = curTip
	restacked, err := planState.restackPlanAgainst(parent, tips)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary:   fmt.Sprintf("would fold %s into %s", cur, parent),
		Branch:    parent,
		Restacked: restacked,
		Deleted:   []string{cur},
		DryRun:    true,
	}, nil
}

// SquashPlan previews squashing all commits on the current branch into one.
func SquashPlan(env Env, s *State, message string) (*OpResult, error) {
	_ = message
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
		return &OpResult{Summary: fmt.Sprintf("%s already has a single commit; nothing to squash", cur), Branch: cur, DryRun: true}, nil
	}
	tips, err := g.Tips()
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	restacked, err := planAfterTipChange(s, cur, tips, false)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary:   fmt.Sprintf("would squash %d commits on %s into one", len(subjects), cur),
		Branch:    cur,
		Restacked: restacked,
		DryRun:    true,
	}, nil
}

// OntoPlan previews moving the current branch onto target.
func OntoPlan(env Env, s *State, target string) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, b, err := currentTracked(g, s)
	if err != nil {
		return nil, err
	}
	if target == cur {
		return nil, fmt.Errorf("cannot move %q onto itself", cur)
	}
	if target != s.Trunk && !s.IsTracked(target) {
		return nil, fmt.Errorf("target %q is not the trunk or a tracked branch", target)
	}
	for _, d := range s.Descendants(cur) {
		if d == target {
			return nil, fmt.Errorf("cannot move %q onto its own descendant %q", cur, target)
		}
	}
	if b.Parent == target {
		return &OpResult{Summary: fmt.Sprintf("%s is already stacked on %s", cur, target), Branch: cur, DryRun: true}, nil
	}

	newParentTip, err := g.RevParse(branchTipRef(target))
	if err != nil {
		return nil, err
	}
	tips, err := g.Tips()
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	planState := cloneState(s)
	planBranch, _ := planState.Get(cur)
	planBranch.Parent = target
	planBranch.ParentSHA = newParentTip
	restacked, err := planAfterTipChange(planState, cur, tips, false)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary:   fmt.Sprintf("would move %s onto %s", cur, target),
		Branch:    cur,
		Restacked: restacked,
		DryRun:    true,
	}, nil
}

// DeletePlan previews deleting a tracked branch.
func DeletePlan(env Env, s *State, name string, force bool) (*OpResult, error) {
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
	if _, err := s.ownedWorktreeReleaseTarget(env, name); err != nil {
		return nil, err
	}
	parent := b.Parent

	if _, err := g.CurrentBranch(); err != nil {
		return nil, err
	}
	if !force {
		mergedIntoParent, err := g.IsAncestor(name, parent)
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, parent, err)
		}
		if !mergedIntoParent {
			return nil, fmt.Errorf("branch %q is not merged into its stack parent %q (use --force to delete anyway)", name, parent)
		}
	}
	tips, err := g.Tips()
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	planState := cloneState(s)
	formerChildren := planState.RemoveBranch(name)
	restacked, err := appendRestackPlans(planState, tips, formerChildren...)
	if err != nil {
		return nil, err
	}

	res := &OpResult{Summary: fmt.Sprintf("would delete %s", name), Deleted: []string{name}, Restacked: restacked, DryRun: true}
	if len(formerChildren) > 0 {
		res.Summary = fmt.Sprintf("would delete %s; re-parented %d branch(es) onto %s", name, len(formerChildren), parent)
	}
	return res, nil
}

// planAfterTipChange previews the upstack impact of an operation that rewrites
// changed's tip. The rewritten branch is treated as moved even though previews
// do not know its future SHA, so descendants whose parent moves are included.
func planAfterTipChange(s *State, changed string, tips map[string]string, includeChanged bool) ([]string, error) {
	if _, err := s.tracked(changed); err != nil {
		return nil, err
	}
	order := append([]string{changed}, s.Descendants(changed)...)
	inPlan := map[string]bool{changed: true}
	var plan []string
	if includeChanged {
		plan = append(plan, changed)
	}
	for _, name := range order[1:] {
		b, ok := s.Get(name)
		if !ok {
			continue
		}
		needs, err := s.needsRestackAgainstTips(name, tips)
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

// appendRestackPlans previews the same child-by-child restack order Delete uses,
// de-duplicating descendants reached through multiple former children.
func appendRestackPlans(s *State, tips map[string]string, starts ...string) ([]string, error) {
	seen := map[string]bool{}
	var plan []string
	for _, start := range starts {
		next, err := s.restackPlanAgainst(start, tips)
		if err != nil {
			return nil, err
		}
		for _, name := range next {
			if seen[name] {
				continue
			}
			seen[name] = true
			plan = append(plan, name)
		}
	}
	return plan, nil
}

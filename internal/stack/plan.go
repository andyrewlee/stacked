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
	if owner, elsewhere, err := s.ownerElsewhere(g, parent); err != nil {
		return nil, err
	} else if elsewhere {
		return nil, fmt.Errorf("cannot fold into %q because it is checked out in another worktree %q", parent, owner.Path)
	}

	curTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, err
	}
	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	planState := cloneState(s)
	planState.RemoveBranch(cur)
	tips[parent] = curTip
	preview, err := finishUpstackPlan(env, planState, parent, tips)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary:   fmt.Sprintf("would fold %s into %s", cur, parent),
		Branch:    parent,
		Restacked: preview.restacked,
		Deleted:   []string{cur},
		Notes:     preview.notes(),
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
	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	preview, err := planAfterTipChange(env, s, cur, tips, false)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary:   fmt.Sprintf("would squash %d commits on %s into one", len(subjects), cur),
		Branch:    cur,
		Restacked: preview.restacked,
		Notes:     preview.notes(),
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
	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	planState := cloneState(s)
	planBranch, _ := planState.Get(cur)
	planBranch.Parent = target
	planBranch.ParentSHA = newParentTip
	var preview restackPreview
	if newParentTip != b.ParentSHA {
		preview, err = planAfterTipChange(env, planState, cur, tips, false)
	} else {
		preview, err = finishUpstackPlan(env, planState, cur, tips)
	}
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary:   fmt.Sprintf("would move %s onto %s", cur, target),
		Branch:    cur,
		Restacked: preview.restacked,
		Notes:     preview.notes(),
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
	parent := b.Parent

	if !force {
		mergedIntoParent, err := g.IsAncestor(name, parent)
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, parent, err)
		}
		if !mergedIntoParent {
			return nil, fmt.Errorf("branch %q is not merged into its stack parent %q (use --force to delete anyway)", name, parent)
		}
	}
	if _, err := s.ownedWorktreeReleaseTarget(env, name); err != nil {
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
	}
	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	if err := requireBranchTip(tips, name); err != nil {
		return nil, err
	}
	planState := cloneState(s)
	formerChildren := planState.RemoveBranch(name)
	preview, err := appendRestackPlans(env, planState, tips, formerChildren...)
	if err != nil {
		return nil, err
	}

	res := &OpResult{Summary: fmt.Sprintf("would delete %s", name), Deleted: []string{name}, Restacked: preview.restacked, Notes: preview.notes(), DryRun: true}
	if len(formerChildren) > 0 {
		res.Summary = fmt.Sprintf("would delete %s; re-parented %d branch(es) onto %s", name, len(formerChildren), parent)
	}
	return res, nil
}

type restackPreview struct {
	restacked []string
	skipped   []string
}

func (p restackPreview) notes() []string {
	return skippedWorktreeNotesFrom(p.skipped)
}

type restackPreviewAccumulator struct {
	restacked   []string
	skipped     []string
	moved       map[string]bool
	seen        map[string]bool
	seenSkipped map[string]bool
}

func newRestackPreviewAccumulator() *restackPreviewAccumulator {
	return &restackPreviewAccumulator{
		moved:       map[string]bool{},
		seen:        map[string]bool{},
		seenSkipped: map[string]bool{},
	}
}

func (a *restackPreviewAccumulator) preview() restackPreview {
	return restackPreview{restacked: a.restacked, skipped: a.skipped}
}

func (a *restackPreviewAccumulator) consider(env Env, s *State, tips map[string]string, name string) error {
	b, ok := s.Get(name)
	if !ok {
		return nil
	}
	needs, err := s.needsRestackAgainstTips(name, tips)
	if err != nil {
		return err
	}
	if !needs && !a.moved[b.Parent] {
		return nil
	}
	skipped, err := wouldSkipDirtyWorktreeRestack(env, s, name)
	if err != nil {
		return err
	}
	if skipped {
		if !a.seenSkipped[name] {
			a.seenSkipped[name] = true
			a.skipped = append(a.skipped, name)
		}
		return nil
	}
	if !a.seen[name] {
		a.seen[name] = true
		a.restacked = append(a.restacked, name)
	}
	a.moved[name] = true
	return nil
}

func wouldSkipDirtyWorktreeRestack(env Env, s *State, branch string) (bool, error) {
	owner, elsewhere, err := s.ownerElsewhere(env.Git, branch)
	if err != nil {
		return false, err
	}
	if !elsewhere {
		return false, nil
	}
	clean, err := env.Git.IsCleanIn(owner.Path)
	if err != nil {
		return false, fmt.Errorf("checking worktree %q for %q: %w", owner.Path, branch, err)
	}
	return !clean, nil
}

func restackPlanAgainstWithWorktrees(env Env, s *State, start string, tips map[string]string) (restackPreview, error) {
	var order []string
	if start != s.Trunk {
		order = append(order, start)
		order = append(order, s.Descendants(start)...)
	} else {
		order = s.Descendants(s.Trunk)
	}
	acc := newRestackPreviewAccumulator()
	for _, name := range order {
		if err := acc.consider(env, s, tips, name); err != nil {
			return restackPreview{}, err
		}
	}
	return acc.preview(), nil
}

func finishUpstackPlan(env Env, s *State, anchor string, tips map[string]string) (restackPreview, error) {
	acc := newRestackPreviewAccumulator()
	for _, name := range s.Descendants(anchor) {
		if err := acc.consider(env, s, tips, name); err != nil {
			return restackPreview{}, err
		}
	}
	return acc.preview(), nil
}

// planAfterTipChange previews the upstack impact of an operation that rewrites
// changed's tip. The rewritten branch is treated as moved even though previews
// do not know its future SHA, so descendants whose parent moves are included.
func planAfterTipChange(env Env, s *State, changed string, tips map[string]string, includeChanged bool) (restackPreview, error) {
	if _, err := s.tracked(changed); err != nil {
		return restackPreview{}, err
	}
	acc := newRestackPreviewAccumulator()
	acc.moved[changed] = true
	if includeChanged {
		acc.seen[changed] = true
		acc.restacked = append(acc.restacked, changed)
	}
	for _, name := range s.Descendants(changed) {
		if err := acc.consider(env, s, tips, name); err != nil {
			return restackPreview{}, err
		}
	}
	return acc.preview(), nil
}

// appendRestackPlans previews the same child-by-child restack order Delete uses,
// de-duplicating descendants reached through multiple former children.
func appendRestackPlans(env Env, s *State, tips map[string]string, starts ...string) (restackPreview, error) {
	acc := newRestackPreviewAccumulator()
	for _, start := range starts {
		order := append([]string{start}, s.Descendants(start)...)
		for _, name := range order {
			if err := acc.consider(env, s, tips, name); err != nil {
				return restackPreview{}, err
			}
		}
	}
	return acc.preview(), nil
}

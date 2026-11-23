package stack

import (
	"errors"
	"fmt"
)

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
	tips, err := g.TipsFor(s.TipNames())
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
	tips, err := g.TipsFor(s.TipNames())
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
		mergedIntoParent, err := isMergedOrContained(g, parent, name)
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, parent, err)
		}
		if !mergedIntoParent {
			return nil, fmt.Errorf("branch %q is not merged into its stack parent %q (use --force to delete anyway)", name, parent)
		}
	}
	if owner, elsewhere, err := s.ownerElsewhere(g, name); err != nil {
		return nil, err
	} else if elsewhere {
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
	}
	tips, err := g.TipsFor(s.TipNames())
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	if err := requireBranchTip(tips, name); err != nil {
		return nil, err
	}
	planState := cloneState(s)
	beforeParents := parentSnapshot(planState)
	formerChildren := planState.RemoveBranch(name)
	reparented := diffReparents(beforeParents, planState)
	preview, err := appendRestackPlans(env, planState, tips, formerChildren...)
	if err != nil {
		return nil, err
	}

	res := &OpResult{Summary: fmt.Sprintf("would delete %s", name), Deleted: []string{name}, Reparented: reparented, Restacked: preview.restacked, Notes: preview.notes(), DryRun: true}
	if len(formerChildren) > 0 {
		res.Summary = fmt.Sprintf("would delete %s; re-parented %d branch(es) onto %s", name, len(formerChildren), parent)
	}
	return res, nil
}

type restackPreview struct {
	restacked   []string
	skipped     []string
	skipReasons map[string]wtSkipReason
}

func (p restackPreview) notes() []string {
	return skippedWorktreeNotesFrom(p.skipped, p.skipReasons)
}

type restackPreviewAccumulator struct {
	restacked   []string
	skipped     []string
	skipReasons map[string]wtSkipReason
	moved       map[string]bool
	seen        map[string]bool
	seenSkipped map[string]bool
	cw          *cascadeWorktrees
	cur         string
	cwErr       error
	cwLoaded    bool
}

func newRestackPreviewAccumulator() *restackPreviewAccumulator {
	return &restackPreviewAccumulator{
		moved:       map[string]bool{},
		seen:        map[string]bool{},
		seenSkipped: map[string]bool{},
	}
}

func (a *restackPreviewAccumulator) preview() restackPreview {
	return restackPreview{restacked: a.restacked, skipped: a.skipped, skipReasons: a.skipReasons}
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
	reason, err := a.wouldSkipWorktreeRestack(env, s, name)
	if err != nil {
		return err
	}
	if reason != wtSkipNone {
		if !a.seenSkipped[name] {
			a.seenSkipped[name] = true
			a.skipped = append(a.skipped, name)
			if a.skipReasons == nil {
				a.skipReasons = map[string]wtSkipReason{}
			}
			a.skipReasons[name] = reason
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

func (a *restackPreviewAccumulator) worktreeCtx(env Env) (*cascadeWorktrees, error) {
	if a.cwLoaded {
		return a.cw, a.cwErr
	}
	a.cwLoaded = true
	wts, err := env.Git.Worktrees()
	if err != nil {
		a.cwErr = err
		return nil, err
	}
	a.cur, _ = env.Git.CurrentBranch()
	cw := &cascadeWorktrees{wts: wts}
	if IsMultiWorktree(wts) {
		main, _ := MainWorktree(wts)
		var paths []string
		for _, wt := range wts {
			if wt.Path != main.Path && wt.Branch != "" && !WorktreeStale(env.Git, wt) {
				paths = append(paths, wt.Path)
			}
		}
		cw.verdicts = probeWorktrees(env.Git, paths, true)
	}
	a.cw = cw
	return cw, nil
}

func (a *restackPreviewAccumulator) wouldSkipWorktreeRestack(env Env, s *State, branch string) (wtSkipReason, error) {
	cw, err := a.worktreeCtx(env)
	if err != nil {
		return wtSkipNone, err
	}
	owner, elsewhere := ownerElsewhereFrom(cw.wts, branch, a.cur)
	if !elsewhere {
		return wtSkipNone, nil
	}
	return worktreeRestackDisposition(env, owner, branch, cw.verdicts)
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

// ModifyPlan previews Modify — the amend/commit gates and the upstack cascade
// the real operation would run — without staging, committing, or moving refs.
func ModifyPlan(env Env, s *State, message string, all, commit bool) (*OpResult, error) {
	g := env.Git
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if cur == s.Trunk {
		return nil, fmt.Errorf("cannot modify the trunk branch %q", cur)
	}
	if !s.IsTracked(cur) {
		return nil, ErrNotTracked(cur)
	}
	if commit && message == "" {
		return nil, errors.New("--commit requires a commit message (-m <msg>)")
	}
	if !all {
		staged, err := g.HasStagedChanges()
		if err != nil {
			return nil, fmt.Errorf("checking staged changes: %w", err)
		}
		if commit && !staged {
			return nil, errors.New("no staged changes to commit; stage changes or pass -a")
		}
		if !commit && !staged && message == "" {
			unstaged, err := g.HasUnstagedChanges()
			if err != nil {
				return nil, fmt.Errorf("checking unstaged changes: %w", err)
			}
			if unstaged {
				return nil, errors.New("no staged changes to amend; stage changes with git add or pass -a")
			}
		}
		if len(s.Descendants(cur)) > 0 {
			unstaged, err := g.HasUnstagedChanges()
			if err != nil {
				return nil, fmt.Errorf("checking unstaged changes: %w", err)
			}
			if unstaged {
				return nil, ErrDirty
			}
		}
	}
	var action string
	switch {
	case commit:
		action = "would commit on " + cur
	case message != "":
		action = "would amend " + cur + " with new message"
	default:
		action = "would amend " + cur
	}
	tips, err := g.TipsFor(s.TipNames())
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	preview, err := planAfterTipChange(env, s, cur, tips, false)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: action, Branch: cur, Restacked: preview.restacked, Notes: preview.notes(), DryRun: true}, nil
}

// UntrackPlan previews UntrackBranch without touching state or refs.
func UntrackPlan(env Env, s *State, name string) (*OpResult, error) {
	g := env.Git
	if name == "" {
		cur, err := g.CurrentBranch()
		if err != nil {
			return nil, err
		}
		name = cur
	}
	if name == s.Trunk {
		return nil, fmt.Errorf("cannot untrack the trunk %q", name)
	}
	b, err := s.tracked(name)
	if err != nil {
		return nil, err
	}
	children := s.Children(name)
	var notes []string
	for _, child := range children {
		notes = append(notes, fmt.Sprintf("child %s would re-parent onto %s", child.Name, b.Parent))
	}
	return &OpResult{
		Summary: fmt.Sprintf("would untrack %s (re-parenting children onto %s)", name, b.Parent),
		Branch:  name,
		Notes:   notes,
		DryRun:  true,
	}, nil
}

// RenamePlan previews Rename without renaming anything.
func RenamePlan(env Env, s *State, oldName, newName string) (*OpResult, error) {
	g := env.Git
	if oldName == "" {
		cur, err := g.CurrentBranch()
		if err != nil {
			return nil, err
		}
		oldName = cur
	}
	if oldName == newName {
		return nil, errors.New("new name is the same as the old name")
	}
	if g.BranchExists(newName) {
		return nil, fmt.Errorf("branch %q already exists", newName)
	}
	isTrunk := oldName == s.Trunk
	if !isTrunk && !s.IsTracked(oldName) {
		return nil, fmt.Errorf("%q is not the trunk or a tracked branch", oldName)
	}
	var notes []string
	if isTrunk {
		notes = append(notes, fmt.Sprintf("trunk becomes %s", newName))
	}
	for _, child := range s.Children(oldName) {
		notes = append(notes, fmt.Sprintf("child %s would re-parent onto %s", child.Name, newName))
	}
	return &OpResult{
		Summary: fmt.Sprintf("would rename %s -> %s", oldName, newName),
		Branch:  newName,
		Notes:   notes,
		DryRun:  true,
	}, nil
}

// CreatePlan previews Create without creating the branch, staging, or committing.
func CreatePlan(env Env, s *State, name, message string, all bool) (*OpResult, error) {
	g := env.Git
	if g.BranchExists(name) {
		return nil, fmt.Errorf("branch %q already exists", name)
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if cur != s.Trunk && !s.IsTracked(cur) {
		return nil, fmt.Errorf("current branch %q is not the trunk or a tracked branch", cur)
	}
	if all && message == "" {
		return nil, errors.New("-a requires a commit message (-m <msg>)")
	}
	if !all {
		staged, err := g.HasStagedChanges()
		if err != nil {
			return nil, fmt.Errorf("checking staged changes: %w", err)
		}
		if message == "" && staged {
			return nil, errors.New("staged changes present; provide a commit message with -m")
		}
		if message != "" && !staged {
			return nil, errors.New("no staged changes to commit; stage changes or pass -a")
		}
	}
	return &OpResult{Summary: fmt.Sprintf("would create %s on top of %s", name, cur), Branch: name, DryRun: true}, nil
}

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

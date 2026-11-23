package stack

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/git"
)

// OwnerOf returns the worktree that has branch checked out, and whether one
// exists. git forbids rebasing a branch checked out in another worktree, so the
// engine consults this to know which worktree must drive a branch's restack.
func OwnerOf(worktrees []git.Worktree, branch string) (git.Worktree, bool) {
	for _, wt := range worktrees {
		if wt.Branch == branch {
			return wt, true
		}
	}
	return git.Worktree{}, false
}

// MainWorktree returns the repository's main (non-linked) worktree.
func MainWorktree(worktrees []git.Worktree) (git.Worktree, bool) {
	if len(worktrees) == 0 {
		return git.Worktree{}, false
	}
	return worktrees[0], true
}

// LinkedOwnerOf returns the linked worktree that has branch checked out,
// ignoring the main worktree.
func LinkedOwnerOf(worktrees []git.Worktree, branch string) (git.Worktree, bool) {
	main, _ := MainWorktree(worktrees)
	for _, wt := range worktrees {
		if wt.Branch == branch && wt.Path != main.Path {
			return wt, true
		}
	}
	return git.Worktree{}, false
}

// PausedRebaseOwners maps each branch that has a rebase paused in a detached
// worktree to that worktree, in one pass over a caller-fetched worktree list.
func PausedRebaseOwners(g Git, wts []git.Worktree) (map[string]git.Worktree, error) {
	var paused map[string]git.Worktree
	for _, wt := range wts {
		if wt.Branch != "" {
			continue
		}
		if WorktreeStale(g, wt) {
			continue
		}
		inRebase, err := g.RebaseInProgressIn(wt.Path)
		if err != nil {
			return nil, fmt.Errorf("checking rebase state in worktree %q: %w", wt.Path, err)
		}
		if !inRebase {
			continue
		}
		head, err := g.RebaseHeadNameIn(wt.Path)
		if err != nil {
			return nil, fmt.Errorf("reading paused rebase head-name in worktree %q: %w", wt.Path, err)
		}
		if head == "" {
			continue
		}
		if paused == nil {
			paused = map[string]git.Worktree{}
		}
		paused[head] = wt
	}
	return paused, nil
}

// WorktreeStale reports whether a worktree registration is dead: git already
// marked it prunable, or its directory is simply gone.
func WorktreeStale(g Git, wt git.Worktree) bool {
	return wt.Prunable || !g.DirExists(wt.Path)
}

func (s *State) ownerElsewhere(g Git, branch string) (git.Worktree, bool, error) {
	return s.ownerElsewhereWith(g, branch, "")
}

func (s *State) ownerElsewhereWith(g Git, branch, curHint string) (git.Worktree, bool, error) {
	wts, err := g.Worktrees()
	if err != nil {
		return git.Worktree{}, false, err
	}
	if !IsMultiWorktree(wts) {
		return git.Worktree{}, false, nil
	}
	owner, ok := OwnerOf(wts, branch)
	if !ok {
		return git.Worktree{}, false, nil
	}
	cur := curHint
	if cur == "" {
		cur, _ = g.CurrentBranch()
	}
	if branch == cur {
		return git.Worktree{}, false, nil
	}
	return owner, true, nil
}

func ownerElsewhereFrom(wts []git.Worktree, branch, cur string) (git.Worktree, bool) {
	if !IsMultiWorktree(wts) {
		return git.Worktree{}, false
	}
	owner, ok := OwnerOf(wts, branch)
	if !ok || branch == cur {
		return git.Worktree{}, false
	}
	return owner, true
}

// restackInWorktree rebases name onto parentTip inside its owning worktree
// (git -C <path>), gated on that worktree having no lock, no paused rebase
// and no dirty tree.
func (s *State) restackInWorktree(env Env, name string, b *Branch, parentTip string, owner git.Worktree, verdicts map[string]wtVerdict) (bool, error) {
	reason, err := worktreeRestackDisposition(env, owner, name, verdicts)
	if err != nil {
		return false, err
	}
	if reason != wtSkipNone {
		s.recordSkippedWorktree(name, reason)
		return false, nil
	}
	if err := env.Git.RebaseOntoIn(owner.Path, parentTip, b.ParentSHA, name); err != nil {
		rebaseErr := fmt.Errorf("rebasing %q in its worktree %q: %w (resolve it there, then re-run)", name, owner.Path, err)
		inRebase, probeErr := env.Git.RebaseInProgressIn(owner.Path)
		if probeErr != nil {
			return false, AlsoFailed(rebaseErr, fmt.Sprintf("verify the paused rebase in %q belongs to this command; resolve it there", owner.Path), probeErr)
		}
		if !inRebase {
			return false, rebaseErr
		}
		if headName, hnErr := env.Git.RebaseHeadNameIn(owner.Path); hnErr != nil || headName != name {
			return false, fmt.Errorf("%w (a rebase is in progress in %q that this command did not start; it was left untouched)", rebaseErr, owner.Path)
		}
		if abortErr := env.Git.RebaseAbortIn(owner.Path); abortErr != nil {
			inRebase, probeErr := env.Git.RebaseInProgressIn(owner.Path)
			if probeErr == nil && !inRebase {
				return false, rebaseErr
			}
			return false, AlsoFailed(rebaseErr, fmt.Sprintf("abort the paused rebase in %q (it is still in progress there)", owner.Path), abortErr)
		}
		return false, rebaseErr
	}
	b.ParentSHA = parentTip
	if err := env.save(); err != nil {
		return false, fmt.Errorf("save state after restacking %q in worktree: %w", name, err)
	}
	return true, nil
}

type wtVerdict struct {
	paused    bool
	clean     bool
	rebaseErr error
	cleanErr  error
}

func probeWorktrees(g Git, paths []string, checkRebase bool) map[string]wtVerdict {
	var uniq []string
	seen := map[string]bool{}
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	slots := make([]wtVerdict, len(uniq))
	_ = git.ParallelProbes(len(uniq), func(i int) error {
		path := uniq[i]
		var v wtVerdict
		if checkRebase {
			paused, err := g.RebaseInProgressIn(path)
			if err != nil {
				v.rebaseErr = err
				slots[i] = v
				return nil
			}
			v.paused = paused
			if paused {
				slots[i] = v
				return nil
			}
		}
		clean, err := g.IsCleanIn(path)
		if err != nil {
			v.cleanErr = err
		} else {
			v.clean = clean
		}
		slots[i] = v
		return nil
	})
	verdicts := make(map[string]wtVerdict, len(uniq))
	for i, p := range uniq {
		verdicts[p] = slots[i]
	}
	return verdicts
}

type wtSkipReason int

const (
	wtSkipNone wtSkipReason = iota
	wtSkipDirty
	wtSkipPausedRebase
	wtSkipLocked
	wtSkipStale
)

func worktreeRestackDisposition(env Env, owner git.Worktree, branch string, verdicts map[string]wtVerdict) (wtSkipReason, error) {
	if owner.Locked {
		return wtSkipLocked, nil
	}
	if WorktreeStale(env.Git, owner) {
		return wtSkipStale, nil
	}
	if v, ok := verdicts[owner.Path]; ok {
		if v.rebaseErr != nil {
			return wtSkipNone, fmt.Errorf("checking rebase state in worktree %q for %q: %w", owner.Path, branch, v.rebaseErr)
		}
		if v.paused {
			return wtSkipPausedRebase, nil
		}
		if v.cleanErr != nil {
			return wtSkipNone, fmt.Errorf("checking worktree %q for %q: %w", owner.Path, branch, v.cleanErr)
		}
		if !v.clean {
			return wtSkipDirty, nil
		}
		return wtSkipNone, nil
	}
	inRebase, err := env.Git.RebaseInProgressIn(owner.Path)
	if err != nil {
		return wtSkipNone, fmt.Errorf("checking rebase state in worktree %q for %q: %w", owner.Path, branch, err)
	}
	if inRebase {
		return wtSkipPausedRebase, nil
	}
	clean, err := env.Git.IsCleanIn(owner.Path)
	if err != nil {
		return wtSkipNone, fmt.Errorf("checking worktree %q for %q: %w", owner.Path, branch, err)
	}
	if !clean {
		return wtSkipDirty, nil
	}
	return wtSkipNone, nil
}

type cascadeWorktrees struct {
	wts      []git.Worktree
	verdicts map[string]wtVerdict
}

func newCascadeWorktrees(env Env, s *State, starts []string, idx map[string][]string, cur string) (*cascadeWorktrees, error) {
	wts, err := env.Git.Worktrees()
	if err != nil {
		return nil, err
	}
	cw := &cascadeWorktrees{wts: wts}
	if !IsMultiWorktree(wts) {
		return cw, nil
	}
	var paths []string
	for _, start := range starts {
		for _, name := range append([]string{start}, descendantsOf(idx, start)...) {
			if owner, elsewhere := ownerElsewhereFrom(wts, name, cur); elsewhere && !WorktreeStale(env.Git, owner) {
				paths = append(paths, owner.Path)
			}
		}
	}
	cw.verdicts = probeWorktrees(env.Git, paths, true)
	return cw, nil
}

func (cw *cascadeWorktrees) headMoved(oldHEAD, newHEAD string) {
	if oldHEAD == "" || oldHEAD == newHEAD {
		return
	}
	for i := range cw.wts {
		if cw.wts[i].Branch == oldHEAD {
			cw.wts[i].Branch = newHEAD
		}
	}
}

// IsMultiWorktree reports whether the repository has more than one worktree.
func IsMultiWorktree(worktrees []git.Worktree) bool {
	return len(worktrees) > 1
}

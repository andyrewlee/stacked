package stack

// ops_read.go — read-only planners: answers built from State plus git probes
// that move nothing (no lock, no journal, no mutation). `st commits` is the
// first surface here.

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/git"
)

// CommitsResult answers "which commits does this branch carry" — every commit
// between the branch's recorded base (ParentSHA, the stack model's claim about
// where the branch sits) and its live tip, newest first. The base is the
// RECORDED parentSHA, not a live merge-base: a drifted stack reports its stale
// base honestly and surfaces the drift through needsRestack/validate rather
// than by silently recomputing a different answer.
type CommitsResult struct {
	Branch    string           `json:"branch"`
	ParentSHA string           `json:"parentSHA"`
	Commits   []git.CommitInfo `json:"commits"`
}

// CommitsPlan lists the commits in branch's recorded range parentSHA..tip.
// An empty branch resolves to the current branch. The trunk is refused — it
// has no stack base (it IS the base); an untracked branch gets the canonical
// ErrNotTracked.
func CommitsPlan(env Env, s *State, branch string) (*CommitsResult, error) {
	g := env.Git
	if branch == "" {
		cur, err := g.CurrentBranch()
		if err != nil {
			return nil, err
		}
		branch = cur
	}
	if branch == s.Trunk {
		return nil, fmt.Errorf("branch %q is the trunk — it has no stack base", branch)
	}
	b, ok := s.Get(branch)
	if !ok {
		return nil, ErrNotTracked(branch)
	}
	if b.ParentSHA == "" {
		return nil, fmt.Errorf("branch %q has no recorded base", branch)
	}
	commits, err := g.CommitList(b.ParentSHA, branch)
	if err != nil {
		return nil, err
	}
	if commits == nil {
		commits = []git.CommitInfo{}
	}
	return &CommitsResult{Branch: branch, ParentSHA: b.ParentSHA, Commits: commits}, nil
}

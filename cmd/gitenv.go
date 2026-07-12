package cmd

// The git-port and state-loading seam of the adapter layer: how a command
// obtains the persisted stack state and the (cached, invalidating) git port
// the engine runs against.

import (
	"errors"
	"fmt"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// loadState loads the persisted stack state. If stacked has not been initialized
// in this repo, the underlying stack.ErrNotInitialized is returned unchanged so
// that callers can print it directly.
func loadState() (*stack.State, error) {
	s, err := stack.Load()
	if err != nil {
		if errors.Is(err, stack.ErrNotInitialized) {
			return nil, err
		}
		return nil, fmt.Errorf("loading stack state: %w", err)
	}
	return s, nil
}

// loadStateAndCurrent loads the stack state and the current branch together, the
// common preamble for commands that position relative to where HEAD currently is.
func loadStateAndCurrent() (*stack.State, string, error) {
	s, err := loadState()
	if err != nil {
		return nil, "", err
	}
	cur, err := currentBranch()
	if err != nil {
		return nil, "", err
	}
	return s, cur, nil
}

// gitShell is the production git port used by the stack engine. It is the real
// git.Shell with one override: Worktrees() routes through the per-process cache
// (worktrees()) so the cross-worktree restack cascade, which resolves branch
// ownership once per branch, spawns `git worktree list` at most once — the same
// memoization the read/navigation commands get.
var gitShell stack.Git = cachedShell{}

// cachedShell is git.Shell with a cached Worktrees(); every other method is
// inherited unchanged, except the worktree MUTATIONS, which must invalidate
// that cache so any later worktrees() read reflects the new topology.
type cachedShell struct{ git.Shell }

func (cachedShell) Worktrees() ([]git.Worktree, error) { return worktrees() }

// WorktreeRemove routes the engine-driven worktree removal through the cache
// invalidation. The cache is reset even when the removal fails: a failed git
// worktree command can still have changed registration state, and a spare
// re-list is cheap.
func (c cachedShell) WorktreeRemove(dir string, force bool) error {
	err := c.Shell.WorktreeRemove(dir, force)
	resetWorktreeCache()
	return err
}

// cachedQuietShell is git.QuietShell (quiet rebase output for JSON mode) with the
// same cached Worktrees() and invalidating WorktreeRemove overrides.
type cachedQuietShell struct{ git.QuietShell }

func (cachedQuietShell) Worktrees() ([]git.Worktree, error) { return worktrees() }

func (c cachedQuietShell) WorktreeRemove(dir string, force bool) error {
	err := c.QuietShell.WorktreeRemove(dir, force)
	resetWorktreeCache()
	return err
}

// stackEnv builds the engine environment for s, persisting via s.Save. In JSON
// mode the quiet git port is used so rebase output cannot corrupt the payload.
func stackEnv(s *stack.State, asJSON bool) stack.Env {
	g := gitShell
	if asJSON {
		if _, ok := g.(cachedShell); ok {
			g = cachedQuietShell{}
		}
	}
	return stack.Env{Git: g, Save: s.Save}
}

// currentBranch returns the name of the currently checked-out branch.
func currentBranch() (string, error) {
	return git.CurrentBranch()
}

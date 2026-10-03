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

// gitShell is the production git port used by the stack engine: the real
// git.Shell wrapped in cachedPort, which routes Worktrees() through the
// per-process cache (worktrees()) so the cross-worktree restack cascade,
// resolving branch ownership once per branch, spawns `git worktree list` at
// most once — the same memoization the read/navigation commands get.
var gitShell stack.Git = cachedPort{Git: git.Shell{}}

// cachedPort decorates any stack.Git with a cached Worktrees(); every other
// method is inherited unchanged, except the worktree/HEAD MUTATIONS, which must
// invalidate that cache so any later worktrees() read reflects the new
// topology.
type cachedPort struct{ stack.Git }

func (cachedPort) Worktrees() ([]git.Worktree, error) { return worktrees() }

// WorktreeRemove routes the engine-driven worktree removal through the cache
// invalidation. The cache is reset even when the removal fails: a failed git
// worktree command can still have changed registration state, and a spare
// re-list is cheap.
func (c cachedPort) WorktreeRemove(dir string, force bool) error {
	err := c.Git.WorktreeRemove(dir, force)
	resetWorktreeCache()
	return err
}

// Checkout and CheckoutDetach move HEAD, which changes which branch the
// current worktree owns — so they must invalidate the worktree cache too, or a
// later worktrees() read (e.g. sync's prune step after detaching HEAD) sees the
// stale pre-checkout ownership and can remove the wrong worktree. Reset even on
// error: a partial checkout can still have moved HEAD, and a re-list is cheap.
func (c cachedPort) Checkout(name string) error {
	err := c.Git.Checkout(name)
	resetWorktreeCache()
	return err
}

func (c cachedPort) CheckoutDetach(ref string) error {
	err := c.Git.CheckoutDetach(ref)
	resetWorktreeCache()
	return err
}

// RenameBranch retargets any worktree HEAD that had the old name checked out
// (git branch -m), so cached ownership is stale after it — invalidate like
// Checkout. Dormant today (no in-process reader follows a rename), but the
// cache comment promises every ownership-changing op invalidates.
func (c cachedPort) RenameBranch(oldName, newName string) error {
	err := c.Git.RenameBranch(oldName, newName)
	resetWorktreeCache()
	return err
}

// The rebase family moves HEAD inside a worktree — start, continue, and abort
// all can leave a different branch (or a detached/paused HEAD) checked out
// than the cached list recorded. A rebase that fails mid-run can also have
// already switched HEAD, so every wrapper invalidates on error as well as
// success, exactly like Checkout.
func (c cachedPort) RebaseOnto(newBase, oldBase, branch string) error {
	err := c.Git.RebaseOnto(newBase, oldBase, branch)
	resetWorktreeCache()
	return err
}

func (c cachedPort) RebaseOntoIn(dir, newBase, oldBase, branch string) error {
	err := c.Git.RebaseOntoIn(dir, newBase, oldBase, branch)
	resetWorktreeCache()
	return err
}

func (c cachedPort) RebaseContinue() error {
	err := c.Git.RebaseContinue()
	resetWorktreeCache()
	return err
}

func (c cachedPort) RebaseAbort() error {
	err := c.Git.RebaseAbort()
	resetWorktreeCache()
	return err
}

func (c cachedPort) RebaseAbortIn(dir string) error {
	err := c.Git.RebaseAbortIn(dir)
	resetWorktreeCache()
	return err
}

// stackEnv builds the engine environment for s, persisting via s.Save. In JSON
// mode the port is rebuilt over git.QuietShell — quiet rebase output cannot
// corrupt the payload — so the swap never depends on the plain-mode port's
// concrete type.
func stackEnv(s *stack.State, asJSON bool) stack.Env {
	g := gitShell
	if asJSON {
		g = cachedPort{Git: git.QuietShell{}}
	}
	return stack.Env{Git: g, Save: s.Save}
}

// currentBranch returns the name of the currently checked-out branch.
func currentBranch() (string, error) {
	return git.CurrentBranch()
}

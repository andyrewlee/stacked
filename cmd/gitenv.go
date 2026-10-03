package cmd

// The git-port and state-loading seam of the adapter layer: how a command
// obtains the persisted stack state and the (cached, invalidating) git port
// the engine runs against.

import (
	"errors"
	"fmt"
	"sync"

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

// newGitPort builds the production git port used by the stack engine: the
// real git.Shell wrapped in cachedPort, which routes Worktrees() through the
// per-process cache (worktrees()) so the cross-worktree restack cascade,
// resolving branch ownership once per branch, spawns `git worktree list` at
// most once — the same memoization the read/navigation commands get.
//
// Each call returns a fresh port: the per-instance CurrentBranch memo dies
// with it, so a memoized answer can never outlive the command (Env, probe
// bundle, or one-off read) it was built for. Sharing one port across commands
// would let a stale branch name survive an out-of-port HEAD move.
func newGitPort() *cachedPort { return &cachedPort{Git: git.Shell{}} }

// cachedPort decorates any stack.Git with a cached Worktrees() plus per-
// instance CurrentBranch and RepoRoot memos; every other method is inherited
// unchanged, except the worktree/HEAD MUTATIONS, which must invalidate the
// memos so a later read reflects the new topology. The memos are per
// instance (see newGitPort): the engine's snapshot/restore and worktree-
// removal paths ask both questions several times per command, and the memo
// keeps that to one spawn per port.
type cachedPort struct {
	stack.Git
	mu      sync.Mutex
	curSet  bool
	cur     string
	curErr  error
	rootSet bool
	root    string
	rootErr error
	remotes map[string]remoteURLEntry
}

type remoteURLEntry struct {
	url string
	err error
}

func (*cachedPort) Worktrees() ([]git.Worktree, error) { return worktrees() }

// CurrentBranch memoizes the first answer for this port's lifetime. The
// error is memoized too (a detached HEAD keeps reporting
// git.ErrDetachedHEAD) — both are cleared by resetMemos, which every
// HEAD-moving wrapper below runs after its git call.
func (c *cachedPort) CurrentBranch() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.curSet {
		c.cur, c.curErr = c.Git.CurrentBranch()
		c.curSet = true
	}
	return c.cur, c.curErr
}

// RepoRoot memoizes the current worktree's root for this port's lifetime:
// the answer cannot change while a port lives (cwd only moves off-port, in
// prepareUndoCreatedWorktrees' chdir — and the undo loop builds its ports
// after that move). Worktree-removal loops call it once per candidate via
// CwdWithinWorktree; the memo turns that into a single probe.
func (c *cachedPort) RepoRoot() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.rootSet {
		c.root, c.rootErr = c.Git.RepoRoot()
		c.rootSet = true
	}
	return c.root, c.rootErr
}

// resetMemos drops this port's memoized answers. The mutating wrappers call
// it alongside resetProcCaches: those ops can leave a different branch (or
// a detached/paused HEAD) checked out — or a different worktree set — than
// the memos recorded.
func (c *cachedPort) resetMemos() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.curSet = false
	c.cur = ""
	c.curErr = nil
	c.rootSet = false
	c.root = ""
	c.rootErr = nil
}

// remoteURL returns the named remote's fetch URL, probing `git remote
// get-url` at most once per remote per port. The two questions commands ask
// of a remote — does it exist, and what is its URL — run the same
// subprocess (git.RemoteExists is get-url's exit code, git.RemoteURL its
// output), so one port asking both folds them into a single spawn. The memo
// lives on the port: it cannot leak a stale "does not exist" into the next
// command the way a process-scoped map could when remote configuration
// changes outside the port.
func (c *cachedPort) remoteURL(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.remotes[name]; ok {
		return e.url, e.err
	}
	url, err := git.RemoteURL(name)
	if c.remotes == nil {
		c.remotes = map[string]remoteURLEntry{}
	}
	c.remotes[name] = remoteURLEntry{url: url, err: err}
	return url, err
}

// remoteExists reports whether the named remote exists — the success of the
// same get-url probe remoteURL memoizes, so an exists+URL pair spawns once.
func (c *cachedPort) remoteExists(name string) bool {
	_, err := c.remoteURL(name)
	return err == nil
}

// WorktreeRemove routes the engine-driven worktree removal through the cache
// invalidation. The cache is reset even when the removal fails: a failed git
// worktree command can still have changed registration state, and a spare
// re-list is cheap.
func (c *cachedPort) WorktreeRemove(dir string, force bool) error {
	err := c.Git.WorktreeRemove(dir, force)
	c.resetMemos()
	resetProcCaches()
	return err
}

// Checkout and CheckoutDetach move HEAD, which changes which branch the
// current worktree owns — so they must invalidate the worktree cache too, or a
// later worktrees() read (e.g. sync's prune step after detaching HEAD) sees the
// stale pre-checkout ownership and can remove the wrong worktree. Reset even on
// error: a partial checkout can still have moved HEAD, and a re-list is cheap.
func (c *cachedPort) Checkout(name string) error {
	err := c.Git.Checkout(name)
	c.resetMemos()
	resetProcCaches()
	return err
}

func (c *cachedPort) CheckoutDetach(ref string) error {
	err := c.Git.CheckoutDetach(ref)
	c.resetMemos()
	resetProcCaches()
	return err
}

// RenameBranch retargets any worktree HEAD that had the old name checked out
// (git branch -m), so cached ownership — and, if HEAD named the renamed
// branch, the memoized current-branch answer — is stale after it: invalidate
// like Checkout.
func (c *cachedPort) RenameBranch(oldName, newName string) error {
	err := c.Git.RenameBranch(oldName, newName)
	c.resetMemos()
	resetProcCaches()
	return err
}

// The rebase family moves HEAD inside a worktree — start, continue, and abort
// all can leave a different branch (or a detached/paused HEAD) checked out
// than the cached list recorded. A rebase that fails mid-run can also have
// already switched HEAD, so every wrapper invalidates on error as well as
// success, exactly like Checkout.
func (c *cachedPort) RebaseOnto(newBase, oldBase, branch string) error {
	err := c.Git.RebaseOnto(newBase, oldBase, branch)
	c.resetMemos()
	resetProcCaches()
	return err
}

func (c *cachedPort) RebaseOntoIn(dir, newBase, oldBase, branch string) error {
	err := c.Git.RebaseOntoIn(dir, newBase, oldBase, branch)
	c.resetMemos()
	resetProcCaches()
	return err
}

func (c *cachedPort) RebaseContinue() error {
	err := c.Git.RebaseContinue()
	c.resetMemos()
	resetProcCaches()
	return err
}

func (c *cachedPort) RebaseAbort() error {
	err := c.Git.RebaseAbort()
	c.resetMemos()
	resetProcCaches()
	return err
}

func (c *cachedPort) RebaseAbortIn(dir string) error {
	err := c.Git.RebaseAbortIn(dir)
	c.resetMemos()
	resetProcCaches()
	return err
}

// stackEnv builds the engine environment for s, persisting via s.Save. In JSON
// mode the port is rebuilt over git.QuietShell — quiet rebase output cannot
// corrupt the payload — so the swap never depends on the plain-mode port's
// concrete type.
func stackEnv(s *stack.State, asJSON bool) stack.Env {
	var g stack.Git = newGitPort()
	if asJSON {
		g = &cachedPort{Git: git.QuietShell{}}
	}
	return stack.Env{Git: g, Save: s.Save}
}

// currentBranch returns the name of the currently checked-out branch.
// Detached HEAD is reported via git.ErrDetachedHEAD like the underlying
// probe. Each command asks this at most once, so unlike the engine's
// repeated port reads it needs no memo.
func currentBranch() (string, error) { return git.CurrentBranch() }

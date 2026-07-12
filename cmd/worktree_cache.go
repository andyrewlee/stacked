package cmd

import (
	"sync"

	"github.com/andyrewlee/stacked/internal/git"
)

// cachedWorktrees memoizes `git worktree list --porcelain` for the lifetime of
// one st invocation. Worktrees() is consulted by nearly every read/navigation
// command — st log and st status annotate branches with where they live, the
// teleport path looks up a branch's owner, and the cross-worktree restack cascade
// resolves ownership per branch — so an unmemoized probe spawns the same git
// subprocess many times in a single command. The cache stays correct because
// every worktree-mutating site invalidates it via resetWorktreeCache(): the
// cached port's WorktreeRemove override (cmd/util.go) covers engine-driven
// removals, and the cmd layer's direct git.WorktreeAdd/git.WorktreeRemove calls
// (materializeWorktree and worktreeRemove in cmd/worktree.go, including the
// copy-failure rollback) reset explicitly. worktrees() may therefore be called
// at ANY point in a command and reflects the live worktree topology; new
// worktree-mutating code must either go through the cached port or call
// resetWorktreeCache() itself. (A branch checkout in the SAME process can
// still stale the main-worktree entry's Branch field — no current command
// reads it after switching; invalidating checkouts too is plan 056 territory.)
//
// The OnceValues closure lives behind a swappable package var so the test binary
// — which runs many commands against different temp repos in one process — can
// reset it between repos (resetWorktreeCache, called by the test harness).
var cachedWorktrees = newWorktreeCache()

func newWorktreeCache() func() ([]git.Worktree, error) {
	return sync.OnceValues(git.Worktrees)
}

// worktrees returns the process-cached worktree list, probing git at most once.
func worktrees() ([]git.Worktree, error) {
	return cachedWorktrees()
}

// resetWorktreeCache discards the memoized worktree list so the next worktrees()
// call re-probes git. Every worktree-mutating call site invalidates through it;
// the test harness also calls it when it chdirs into a fresh repo.
func resetWorktreeCache() {
	cachedWorktrees = newWorktreeCache()
}

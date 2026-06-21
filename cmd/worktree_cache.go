package cmd

import (
	"sync"

	"stacked/internal/git"
)

// cachedWorktrees memoizes `git worktree list --porcelain` for the lifetime of
// one st invocation. Worktrees() is consulted by nearly every read/navigation
// command — st log and st status annotate branches with where they live, the
// teleport path looks up a branch's owner, and the cross-worktree restack cascade
// resolves ownership per branch — so an unmemoized probe spawns the same git
// subprocess many times in a single command. A process runs exactly one st
// command, and the worktree topology cannot change underneath it except via that
// command's own `worktree add`/`rm` (which read before they mutate and never
// re-read), so caching once per process is behavior-preserving: it only removes
// redundant subprocess spawns.
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
// call re-probes git. Production never calls it (one command per process); the
// test harness calls it when it chdirs into a fresh repo.
func resetWorktreeCache() {
	cachedWorktrees = newWorktreeCache()
}

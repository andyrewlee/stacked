package cmd

import (
	"path/filepath"
	"sync"

	"github.com/andyrewlee/stacked/internal/git"
)

// worktreeCacheState memoizes `git worktree list --porcelain` for the
// lifetime of one st invocation. Worktrees() is consulted by nearly every
// read/navigation command — st log and st status annotate branches with where
// they live, the teleport path looks up a branch's owner, and the
// cross-worktree restack cascade resolves ownership per branch — so an
// unmemoized probe spawns the same git subprocess many times in a single
// command. The cache stays correct because every worktree-mutating AND
// HEAD-moving site invalidates it via resetWorktreeCache(): the cached port's
// WorktreeRemove/Checkout/CheckoutDetach/RenameBranch overrides
// (cmd/gitenv.go) cover engine-driven removals and checkouts, and its
// RebaseOnto/RebaseOntoIn/RebaseContinue/RebaseAbort/RebaseAbortIn overrides
// cover rebase-driven HEAD moves (a rebase attempt — even one that errors —
// can leave a different branch checked out in a worktree). The cmd layer's
// direct git.WorktreeRemove and git.Checkout calls reset explicitly. The one surgical
// exception is noteWorktreeAdded: a worktree the process just created is fully
// described by the {Path, Branch} it asked git for, so `st worktree --all` —
// which lists once per add otherwise — appends the known entry instead of
// re-listing. worktrees() may therefore be called at ANY point in a command
// and reflects the live worktree topology. New worktree-mutating or
// HEAD-moving code must either go through the cached port, call
// resetWorktreeCache(), or append what it knows via noteWorktreeAdded.
//
// The state lives behind mutexed package vars so the test binary — which runs
// many commands against different temp repos in one process — can reset it
// between repos (resetWorktreeCache, called by the test harness).
var worktreeCacheState = struct {
	sync.Mutex
	probed bool
	wts    []git.Worktree
}{}

// worktrees returns the process-cached worktree list, probing git at most
// once. The returned slice is shared — callers that sort or trim must copy
// first (worktreeList does).
func worktrees() ([]git.Worktree, error) {
	worktreeCacheState.Lock()
	defer worktreeCacheState.Unlock()
	if !worktreeCacheState.probed {
		wts, err := git.Worktrees()
		if err != nil {
			return nil, err
		}
		worktreeCacheState.wts = wts
		worktreeCacheState.probed = true
	}
	return worktreeCacheState.wts, nil
}

// noteWorktreeAdded records a worktree this process just created. The entry
// carries only the fields the add asked git for — Path and Branch, the only
// fields the ownership lookups consult; Head stays empty, which a hypothetical
// in-process `ls` after an add would render blank (no such flow exists — each
// command is its own process). When the cache has not probed yet the entry is
// not appended: the next worktrees() will list it from git anyway.
func noteWorktreeAdded(path, branch string) {
	worktreeCacheState.Lock()
	defer worktreeCacheState.Unlock()
	if !worktreeCacheState.probed {
		return
	}
	// `git worktree list` reports canonicalized paths (it resolves symlinks —
	// e.g. macOS /var -> /private/var) while the caller's path was derived
	// from $HOME or the repo root unexpanded. Match git's spelling or
	// consumers comparing paths (teleport hints, sameWorktreePath) would see
	// two spellings of one worktree.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	// Copy-on-write: earlier callers may hold the previous slice.
	wts := make([]git.Worktree, 0, len(worktreeCacheState.wts)+1)
	wts = append(wts, worktreeCacheState.wts...)
	wts = append(wts, git.Worktree{Path: path, Branch: branch})
	worktreeCacheState.wts = wts
}

// resetWorktreeCache discards the memoized worktree list so the next
// worktrees() call re-probes git. Every worktree-mutating call site
// invalidates through it (except the appendable add); the test harness also
// calls it when it chdirs into a fresh repo.
func resetWorktreeCache() {
	worktreeCacheState.Lock()
	defer worktreeCacheState.Unlock()
	worktreeCacheState.probed = false
	worktreeCacheState.wts = nil
}

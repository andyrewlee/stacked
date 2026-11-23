package cmd

import (
	"sync"

	"github.com/andyrewlee/stacked/internal/git"
)

// Process-scoped memo for `git worktree list --porcelain` so read/nav commands
// and cross-worktree restack cascades spawn it at most once per invocation.
// Every HEAD-moving or rebase-mutating site resets it via cachedPort or
// checkoutAndReset; tests call resetProcCaches between temp repos.
var worktreeCacheState = struct {
	sync.Mutex
	probed bool
	wts    []git.Worktree
}{}

// worktrees returns the process-cached worktree list, probing git at most once.
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

// resetProcCaches discards process-scoped git memos.
func resetProcCaches() {
	worktreeCacheState.Lock()
	defer worktreeCacheState.Unlock()
	worktreeCacheState.probed = false
	worktreeCacheState.wts = nil
	git.ForgetDirMemos()
}

// checkoutAndReset runs git.Checkout then invalidates process memos.
func checkoutAndReset(branch string) error {
	err := git.Checkout(branch)
	resetProcCaches()
	return err
}

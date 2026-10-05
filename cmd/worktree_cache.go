package cmd

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/andyrewlee/stacked/internal/git"
)

// The process-scoped git memo: `git worktree list --porcelain`, cached for
// the lifetime of one st invocation. Worktrees() is consulted by nearly
// every read/navigation command — st log and st status annotate branches
// with where they live, the teleport path looks up a branch's owner, and the
// cross-worktree restack cascade resolves ownership per branch — so
// unmemoized probes spawn the same git subprocesses many times in a single
// command. The cache stays correct because every worktree-mutating AND
// HEAD-moving site invalidates it structurally, not by remembering: the
// cached port's WorktreeRemove/Checkout/CheckoutDetach/RenameBranch
// overrides (cmd/gitenv.go) cover engine-driven removals and checkouts, its
// RebaseOnto/RebaseOntoIn/RebaseContinue/RebaseAbort/RebaseAbortIn overrides
// cover rebase-driven HEAD moves (a rebase attempt — even one that errors —
// can leave a different branch checked out in a worktree), and the cmd
// layer's direct calls go through helpers that own the reset —
// addWorktreePath/removeWorktreePath for `git worktree add`/`remove`,
// checkoutAndReset for the one direct checkout, moveCwdAndReset for the one
// off-port HEAD move (prepareUndoCreatedWorktrees' os.Chdir into the main
// worktree). The one surgical exception is noteWorktreeAdded, reached only
// through addWorktreePath: a worktree the process just created is fully
// described by the {Path, Branch} it asked git for, so `st worktree --all` —
// which lists once per add otherwise — appends the known entry instead of
// re-listing. worktrees() may therefore be called at ANY point in a command
// and reflects the live worktree topology. New worktree-mutating or
// HEAD-moving code must go through the cached port or these helpers —
// reaching for a bare git.Worktree*/git.Checkout/os.Chdir in cmd is a
// design smell.
//
// CurrentBranch is NOT memoized here: its memo lives per cachedPort instance
// (cmd/gitenv.go) so a stale branch name cannot outlive the port a command
// was built on, no matter what moves HEAD outside the port.
//
// The state lives behind mutexed package vars so the test binary — which runs
// many commands against different temp repos in one process — can reset it
// between repos (resetProcCaches, called by the test harness).
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

// resetProcCaches discards the process-scoped git memo — the worktree
// list — so the next worktrees() call re-lists. Worktree-mutating and
// HEAD-moving call sites invalidate through the helpers below (or the
// cached port's overrides), never through a bare resetProcCaches(); the
// test harness calls it directly when it chdirs into a fresh repo.
func resetProcCaches() {
	worktreeCacheState.Lock()
	defer worktreeCacheState.Unlock()
	worktreeCacheState.probed = false
	worktreeCacheState.wts = nil
	git.ForgetDirMemos()
}

// The mutation helpers own the invalidation so a caller cannot forget it —
// the structural form of the rule above. Each resets even on error: a failed
// mutation can still have moved state.

// addWorktreePath runs git.WorktreeAdd, then either records the fully-known
// entry (noteWorktreeAdded — the deliberate append exception) on success or
// resets the cache on error, when the failed add may have changed
// registration state.
func addWorktreePath(path, branch string) error {
	if err := git.WorktreeAdd(path, branch); err != nil {
		resetProcCaches()
		return err
	}
	noteWorktreeAdded(path, branch)
	return nil
}

// removeWorktreePath runs git.WorktreeRemove then invalidates.
func removeWorktreePath(path string, force bool) error {
	err := git.WorktreeRemove(path, force)
	resetProcCaches()
	return err
}

// checkoutAndReset runs git.Checkout then invalidates — a checkout attempt
// can move HEAD even when it ultimately fails.
func checkoutAndReset(branch string) error {
	err := git.Checkout(branch)
	resetProcCaches()
	return err
}

// moveCwdAndReset runs os.Chdir then invalidates. The process's cwd moving
// worktrees does not change the worktree list itself, but worktree paths and
// HEAD now read relative to the destination — drop the memo so any later
// read re-lists.
func moveCwdAndReset(dir string) error {
	err := os.Chdir(dir)
	resetProcCaches()
	return err
}

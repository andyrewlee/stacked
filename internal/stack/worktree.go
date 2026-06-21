package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"stacked/internal/git"
)

// WorktreesRoot is the central per-repo directory under the user's home where
// lazily-materialized worktrees live: ~/.stacked/worktrees. This mirrors the
// existing .git/stacked/ naming and keeps linked worktrees OUT of the repo tree
// so test runners, linters, and watchers never walk into them.
func WorktreesRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".stacked", "worktrees"), nil
}

// WorktreePath returns the canonical on-disk path a worktree for branch would
// live at: ~/.stacked/worktrees/<repo>/<branch>. It is a PURE path computation
// — the worktree need not exist. repo and branch are sanitized so nested
// branch names (feat/foo) and odd repo identifiers stay within a single
// directory level (slashes and separators collapse to dashes).
func WorktreePath(repo, branch string) (string, error) {
	root, err := WorktreesRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sanitizeSegment(repo), sanitizeSegment(branch)), nil
}

// sanitizeSegment turns an arbitrary identifier into a single safe path
// segment: path separators and other risky characters become dashes, and any
// leading dots are stripped so the segment can never escape its parent or be a
// hidden/relative entry.
func sanitizeSegment(s string) string {
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', ' ':
			return '-'
		default:
			return r
		}
	}
	out := strings.Map(repl, s)
	out = strings.Trim(out, "-")
	out = strings.TrimLeft(out, ".")
	if out == "" {
		return "_"
	}
	return out
}

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

// MainWorktree returns the repository's main (non-linked) worktree. `git worktree
// list` always reports the main worktree first, so it is the slice's first
// element; the slice preserves that order end to end. Returns false for an empty
// slice.
func MainWorktree(worktrees []git.Worktree) (git.Worktree, bool) {
	if len(worktrees) == 0 {
		return git.Worktree{}, false
	}
	return worktrees[0], true
}

// LinkedOwnerOf returns the LINKED worktree that has branch checked out, and
// whether one exists. Unlike OwnerOf it ignores the main worktree, so it answers
// the question `st worktree add`/`rm` actually care about — "does branch have its
// OWN separate worktree?" — rather than treating the main tree (where the current
// branch already lives) as a removable/duplicate worktree.
func LinkedOwnerOf(worktrees []git.Worktree, branch string) (git.Worktree, bool) {
	main, _ := MainWorktree(worktrees)
	for _, wt := range worktrees {
		if wt.Branch == branch && wt.Path != main.Path {
			return wt, true
		}
	}
	return git.Worktree{}, false
}

// ownerElsewhere reports whether branch is checked out in a worktree OTHER than
// the one this process runs in. In a single-tree repo (or when branch is the
// current branch here) it returns false, so the in-place rebase path is taken
// and existing behavior is preserved. The returned Worktree is branch's owner.
func (s *State) ownerElsewhere(g Git, branch string) (git.Worktree, bool, error) {
	wts, err := g.Worktrees()
	if err != nil {
		return git.Worktree{}, false, err
	}
	if !IsMultiWorktree(wts) {
		return git.Worktree{}, false, nil
	}
	owner, ok := OwnerOf(wts, branch)
	if !ok {
		return git.Worktree{}, false, nil // not checked out anywhere: rebase here
	}
	cur, _ := g.CurrentBranch()
	if branch == cur {
		return git.Worktree{}, false, nil // checked out HERE: rebase in place
	}
	return owner, true, nil
}

// restackInWorktree rebases name onto parentTip inside its owning worktree
// (git -C <path>), gated on that worktree being clean. A dirty owner is SKIPPED
// (recorded in s.skippedWorktrees, never clobbered). A conflict during the
// cross-worktree rebase is rolled back in that worktree and surfaced as an
// error, rather than left paused where the main process cannot drive it.
func (s *State) restackInWorktree(env Env, name string, b *Branch, parentTip string, owner git.Worktree) (bool, error) {
	clean, err := env.Git.IsCleanIn(owner.Path)
	if err != nil {
		return false, fmt.Errorf("checking worktree %q for %q: %w", owner.Path, name, err)
	}
	if !clean {
		s.skippedWorktrees = append(s.skippedWorktrees, name)
		return false, nil
	}
	if err := env.Git.RebaseOntoIn(owner.Path, parentTip, b.ParentSHA, name); err != nil {
		// Roll back the owner worktree's rebase so it is never left paused; the
		// main process's continue/abort operate on the cwd and could not finish it.
		_ = env.Git.RebaseAbortIn(owner.Path)
		return false, fmt.Errorf("rebasing %q in its worktree %q: %w (resolve it there, then re-run)", name, owner.Path, err)
	}
	b.ParentSHA = parentTip
	if err := env.save(); err != nil {
		return false, fmt.Errorf("save state after restacking %q in worktree: %w", name, err)
	}
	return true, nil
}

// releaseOwnedWorktree tears down the linked worktree that owns branch, if any,
// so a follow-up branch deletion is not refused by git ("checked out in another
// worktree"). It is a no-op in a single-tree repo, when branch is checked out
// here (the current worktree, which git lets us delete from after a checkout
// elsewhere), or when branch owns no worktree.
//
// DESIGN: only a CLEAN owning worktree is auto-removed. A dirty one is left in
// place and an error is returned so the caller deletes nothing — in-progress
// work in another worktree is never silently discarded; the user is told to
// commit/stash or `st worktree rm` first.
func (s *State) releaseOwnedWorktree(env Env, branch string) error {
	owner, elsewhere, err := s.ownerElsewhere(env.Git, branch)
	if err != nil {
		return err
	}
	if !elsewhere {
		return nil
	}
	clean, err := env.Git.IsCleanIn(owner.Path)
	if err != nil {
		return fmt.Errorf("checking worktree %q for %q: %w", owner.Path, branch, err)
	}
	if !clean {
		return fmt.Errorf("branch %q has uncommitted changes in its worktree %q; commit/stash there or run `st worktree rm %s` first", branch, owner.Path, branch)
	}
	if err := env.Git.WorktreeRemove(owner.Path, false); err != nil {
		return fmt.Errorf("removing worktree %q for %q: %w", owner.Path, branch, err)
	}
	return nil
}

// IsMultiWorktree reports whether the repository has more than one worktree.
// The whole worktree feature is gated on this: when only the main worktree
// exists it returns false and every caller short-circuits to today's
// single-tree behavior, which must stay byte-for-byte unchanged.
func IsMultiWorktree(worktrees []git.Worktree) bool {
	return len(worktrees) > 1
}

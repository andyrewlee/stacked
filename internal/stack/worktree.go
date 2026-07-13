package stack

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
)

const repoKeyHashBytes = 6

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

// StableRepoKey returns the repo path segment for generated worktrees. The
// readable prefix comes from the repository basename, while the hash input is a
// stable repository identity such as GitCommonDir.
func StableRepoKey(repoBase, identityPath string) (string, error) {
	absIdentity, err := filepath.Abs(identityPath)
	if err != nil {
		return "", err
	}
	absIdentity = filepath.Clean(absIdentity)
	sum := sha256.Sum256([]byte(absIdentity))
	return fmt.Sprintf("%s-%x", sanitizeSegment(repoBase), sum[:repoKeyHashBytes]), nil
}

// StableRepoBase returns the human-readable basename to pair with a common git
// dir identity. For normal repositories, GitCommonDir points at the main
// worktree's .git directory even when called from a linked worktree, so its
// parent is the stable basename. Other layouts fall back to the current repo
// root's basename.
func StableRepoBase(repoRoot, commonDir string) string {
	cleanCommon := filepath.Clean(commonDir)
	if filepath.Base(cleanCommon) == ".git" {
		parent := filepath.Dir(cleanCommon)
		if parent != "." && parent != string(os.PathSeparator) {
			return filepath.Base(parent)
		}
	}
	return filepath.Base(repoRoot)
}

// WorktreePath returns the canonical on-disk path a worktree for branch would
// live under: ~/.stacked/worktrees/<repo-key>/<encoded-branch>. It is a PURE
// path computation — the worktree need not exist. repo is sanitized into one
// path segment, while branch is losslessly encoded into one path segment.
func WorktreePath(repo, branch string) (string, error) {
	root, err := WorktreesRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sanitizeSegment(repo), encodeBranchSegment(branch)), nil
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

// encodeBranchSegment encodes an arbitrary branch name into a single
// filesystem-safe path segment without collapsing distinct branch names. Safe
// ASCII branch bytes stay readable; unsafe bytes are percent-encoded.
func encodeBranchSegment(s string) string {
	if s == "" {
		return "~"
	}
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isSafeBranchSegmentByte(c) && (i != 0 || c != '.') {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(upperHex[c>>4])
		out.WriteByte(upperHex[c&0x0f])
	}
	return out.String()
}

const upperHex = "0123456789ABCDEF"

func isSafeBranchSegmentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '-' ||
		c == '_' ||
		c == '.'
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
	return s.ownerElsewhereWith(g, branch, "")
}

// ownerElsewhereWith is ownerElsewhere with the caller's known current branch
// as a hint: the restack cascade already threads HEAD's position
// (expectedHEAD), so passing it avoids one CurrentBranch spawn per
// worktree-owned branch. curHint == "" means unknown (detached HEAD, or a
// caller without the threading discipline) and falls back to the live read.
// The fallback deliberately ignores the CurrentBranch error, exactly like the
// original: on a detached HEAD cur is "" and the branch counts as owned
// elsewhere.
func (s *State) ownerElsewhereWith(g Git, branch, curHint string) (git.Worktree, bool, error) {
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
	cur := curHint
	if cur == "" {
		cur, _ = g.CurrentBranch()
	}
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
		rebaseErr := fmt.Errorf("rebasing %q in its worktree %q: %w (resolve it there, then re-run)", name, owner.Path, err)
		if abortErr := env.Git.RebaseAbortIn(owner.Path); abortErr != nil {
			if noRebaseInProgress(abortErr) {
				return false, rebaseErr
			}
			return false, AlsoFailed(rebaseErr, fmt.Sprintf("abort the paused rebase in %q (it is still in progress there)", owner.Path), abortErr)
		}
		return false, rebaseErr
	}
	b.ParentSHA = parentTip
	if err := env.save(); err != nil {
		return false, fmt.Errorf("save state after restacking %q in worktree: %w", name, err)
	}
	return true, nil
}

func noRebaseInProgress(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no rebase in progress")
}

// releaseOwnedWorktree tears down the linked worktree that owns branch, if any,
// so a follow-up branch deletion is not refused by git ("checked out in another
// worktree"). It is a no-op in a single-tree repo, when branch is checked out
// here (the current worktree, which git lets us delete from after a checkout
// elsewhere), or when branch owns no worktree. It rejects a branch owned by the
// main worktree while the command runs elsewhere: the main worktree is not a
// removable linked worktree.
//
// DESIGN: only a CLEAN owning worktree is auto-removed. A dirty one is left in
// place and an error is returned so the caller deletes nothing — in-progress
// work in another worktree is never silently discarded; the user is told to
// commit/stash or `st worktree rm` first.
func (s *State) releaseOwnedWorktree(env Env, branch string) error {
	path, err := s.ownedWorktreeReleaseTarget(env, branch)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	if err := env.Git.WorktreeRemove(path, false); err != nil {
		return fmt.Errorf("removing worktree %q for %q: %w", path, branch, err)
	}
	return nil
}

func (s *State) ownedWorktreeReleaseTarget(env Env, branch string) (string, error) {
	wts, err := env.Git.Worktrees()
	if err != nil {
		return "", err
	}
	if !IsMultiWorktree(wts) {
		return "", nil
	}
	owner, ok := OwnerOf(wts, branch)
	if !ok {
		return "", nil
	}
	cur, _ := env.Git.CurrentBranch()
	if branch == cur {
		return "", nil
	}
	main, _ := MainWorktree(wts)
	if owner.Path == main.Path {
		return "", fmt.Errorf("branch %q is checked out in the main worktree %q; switch away from it there before deleting it", branch, owner.Path)
	}
	clean, err := env.Git.IsCleanIn(owner.Path)
	if err != nil {
		return "", fmt.Errorf("checking worktree %q for %q: %w", owner.Path, branch, err)
	}
	if !clean {
		return "", fmt.Errorf("branch %q has uncommitted changes in its worktree %q; commit/stash there or run `st worktree rm %s` first", branch, owner.Path, branch)
	}
	return owner.Path, nil
}

// IsMultiWorktree reports whether the repository has more than one worktree.
// The whole worktree feature is gated on this: when only the main worktree
// exists it returns false and every caller short-circuits to today's
// single-tree behavior, which must stay byte-for-byte unchanged.
func IsMultiWorktree(worktrees []git.Worktree) bool {
	return len(worktrees) > 1
}

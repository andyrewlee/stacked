package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// worktreeIncludeFile is the manifest, in the repo root, listing paths to copy
// into a freshly-created worktree. Each non-comment line is a repo-root-relative
// path or shell-glob pattern (`*`/`?` per segment; a segment of exactly `**`
// matches zero or more directories — NOT gitignore syntax, no negation), and
// only matches that are gitignored are copied, so tracked files (already
// materialized by `git worktree add`) are never duplicated.
const worktreeIncludeFile = ".worktreeinclude"

// copyWorktreeIncludes copies, from srcRoot into dstRoot, every literal path
// listed in srcRoot/.worktreeinclude that is also gitignored. It returns the
// relative paths copied. Missing manifest => nothing to do (nil, nil). The copy
// prefers a copy-on-write reflink (instant for big dirs like node_modules) and
// falls back to a plain recursive copy.
//
// This function is the adapter edge of the include pipeline: it reads the
// manifest and runs the two git probes (check-ignore, ls-files), while every
// decision rule — expansion, validation, ignore-gating, nested-drop, collision
// preflight, destination safety — lives in stack's worktree_include.go.
func copyWorktreeIncludes(srcRoot, dstRoot string) ([]string, error) {
	manifest := filepath.Join(srcRoot, worktreeIncludeFile)
	data, err := os.ReadFile(manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	entries := stack.ParseIncludePatterns(string(data))
	if len(entries) == 0 {
		return nil, nil
	}
	// Expansion is untrusted input to the SAME pipeline literals use: every
	// expanded match goes through validation and the containment guards below,
	// so globbing cannot widen the path-escape surface.
	entries, err = stack.ExpandIncludePatterns(srcRoot, entries)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	entries, err = stack.ValidateWorktreeIncludePaths(entries)
	if err != nil {
		return nil, err
	}
	// One `git check-ignore --stdin` spawn answers every entry; the per-entry
	// decision order below (absent -> not ignored -> containment) is unchanged.
	// Computed BEFORE the nested drop: only a directory the loop will actually
	// copy (a gitignored one) may suppress its descendants.
	ignored, err := gitIgnoredSet(srcRoot, entries)
	if err != nil {
		return nil, err
	}
	// A selected IGNORED directory already delivers everything beneath it
	// wholesale. A nested selection copied AGAIN would duplicate: `cp -R src
	// dst` copies INTO an existing destination directory (nesting a spurious
	// extra level), unlike plainCopy which merges — so drop descendants of
	// selected to-be-copied directories. A TRACKED selected directory is
	// skipped by the loop and must NOT suppress its gitignored descendants.
	entries = stack.DropNestedIncludeDirs(srcRoot, entries, ignored)

	// Phase 1 — select the entries that would actually copy. The gates are the
	// same as before (exists in source, gitignored, contained in the repo);
	// nothing is written to the destination in this phase.
	candidates, err := stack.SelectWorktreeIncludes(srcRoot, entries, ignored)
	if err != nil {
		return nil, err
	}

	// Phase 2 — destination preflight, all-or-nothing BEFORE the first copy:
	// a later colliding entry must not leave earlier copies behind. Read-only:
	// no destination directories are created here.
	tracked, trackedSorted, err := gitTrackedPaths(dstRoot)
	if err != nil {
		return nil, err
	}
	if err := stack.RefuseWorktreeIncludeCollisions(dstRoot, candidates, tracked, trackedSorted); err != nil {
		return nil, err
	}

	var copied []string
	for _, rel := range candidates {
		dst, err := stack.PrepareIncludeDestination(dstRoot, rel)
		if err != nil {
			return copied, fmt.Errorf(".worktreeinclude path %q: %w", rel, err)
		}
		if err := reflinkCopy(filepath.Join(srcRoot, rel), dst); err != nil {
			return copied, err
		}
		copied = append(copied, rel)
	}
	return copied, nil
}

// gitIgnoredSet returns which of rels (relative to root) are gitignored, in
// one `git check-ignore -z --stdin` spawn. Entries travel NUL-separated both
// ways (-z sidesteps core.quotePath quoting). Exit status 1 means "none
// ignored" and is not an error. A fatal exit (128 — e.g. one entry reaches
// beyond a symlinked directory) poisons the whole batch, so it falls back to
// per-entry probes, preserving the old per-path semantics: a path git cannot
// classify counts as not ignored (the copy loop then skips it). The batch +
// fallback semantics live in the port (git.CheckIgnored).
func gitIgnoredSet(root string, rels []string) (map[string]bool, error) {
	return git.CheckIgnored(root, rels)
}

// gitTrackedPaths returns the destination worktree's tracked paths as a
// lookup set plus a sorted list (for descendant prefix probes), from one
// `git -C dstRoot ls-files -z` — NUL-separated so paths with spaces or quotes
// survive byte-exact. A destination that is not a git worktree (plain dir, as
// in unit tests) yields an empty set; a real ls-files failure is an error.
func gitTrackedPaths(dstRoot string) (tracked map[string]bool, trackedSorted []string, err error) {
	tracked = map[string]bool{}
	if _, err := os.Lstat(filepath.Join(dstRoot, ".git")); err != nil {
		return tracked, nil, nil // not a git worktree: nothing can be tracked
	}
	paths, err := git.LsFilesZ(dstRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("git -C %s ls-files: %w", dstRoot, err)
	}
	for _, p := range paths {
		tracked[p] = true
		trackedSorted = append(trackedSorted, p)
	}
	sort.Strings(trackedSorted)
	return tracked, trackedSorted, nil
}

// reflinkCopy copies src to dst using a copy-on-write reflink when the platform
// supports it (instant, space-shared), falling back to a plain recursive copy.
// macOS uses `cp -c`; Linux uses `cp --reflink=auto` (which itself falls back to
// a full copy when the filesystem lacks reflink support).
func reflinkCopy(src, dst string) error {
	if err := stack.RejectDestinationSymlink(dst); err != nil {
		return err
	}
	var args []string
	switch runtime.GOOS {
	case "darwin":
		args = []string{"-cR", src, dst}
	case "linux":
		args = []string{"--reflink=auto", "-R", src, dst}
	default:
		return plainCopy(src, dst)
	}
	if err := exec.Command("cp", args...).Run(); err != nil {
		// cp may be absent or reject -c on an unsupported FS; fall back so the
		// copy still happens, just without the reflink speedup. But only when cp
		// left nothing behind: if it created partial destination content, merging
		// a fresh plainCopy into that tree mixes strategies on half-written data —
		// return the cp error so the caller's fresh-worktree rollback discards it.
		if _, statErr := os.Lstat(dst); statErr == nil {
			return err
		}
		return plainCopy(src, dst)
	}
	return nil
}

// plainCopy recursively copies src to dst with the standard library, preserving
// file modes and symlinks. It is the portable fallback when no reflink-capable cp
// is usable. Symlinks are recreated verbatim (os.Readlink + os.Symlink) rather
// than dereferenced, matching the reflink `cp` path: this preserves the link and,
// crucially, lets a BROKEN symlink (e.g. a node_modules/.bin entry whose target
// is absent) copy without failing — os.ReadFile would follow it and abort the
// whole recursion, leaving a half-populated worktree.
func plainCopy(src, dst string) error {
	if err := stack.RejectDestinationSymlink(dst); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := plainCopy(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, info.Mode().Perm())
}

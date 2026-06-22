package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// worktreeIncludeFile is the manifest, in the repo root, listing paths to copy
// into a freshly-created worktree. It uses .gitignore syntax and is adopted
// verbatim from Claude Code's mechanism: only entries that BOTH match a pattern
// AND are gitignored are copied, so tracked files (already materialized by `git
// worktree add`) are never duplicated.
const worktreeIncludeFile = ".worktreeinclude"

// copyWorktreeIncludes copies, from srcRoot into dstRoot, every path listed in
// srcRoot/.worktreeinclude that is also gitignored. It returns the relative
// paths copied. Missing manifest => nothing to do (nil, nil). The copy prefers a
// copy-on-write reflink (instant for big dirs like node_modules) and falls back
// to a plain recursive copy.
func copyWorktreeIncludes(srcRoot, dstRoot string) ([]string, error) {
	manifest := filepath.Join(srcRoot, worktreeIncludeFile)
	data, err := os.ReadFile(manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	patterns := parseIncludePatterns(string(data))
	if len(patterns) == 0 {
		return nil, nil
	}

	var copied []string
	for _, rel := range patterns {
		src := filepath.Join(srcRoot, rel)
		if _, err := os.Lstat(src); err != nil {
			continue // listed but absent: skip rather than fail the whole create
		}
		if !isGitIgnored(srcRoot, rel) {
			continue // tracked (or not ignored): git worktree add already has it
		}
		dst := filepath.Join(dstRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return copied, err
		}
		if err := reflinkCopy(src, dst); err != nil {
			return copied, err
		}
		copied = append(copied, rel)
	}
	return copied, nil
}

// parseIncludePatterns extracts the path entries from a .worktreeinclude file,
// skipping blank lines and # comments. Each remaining line is treated as a
// repo-root-relative path (the common case; full glob expansion is intentionally
// out of scope for the foundation).
func parseIncludePatterns(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, filepath.Clean(line))
	}
	return out
}

// isGitIgnored reports whether rel (relative to root) is ignored by git, via
// `git -C <root> check-ignore`. check-ignore exits 0 when the path is ignored,
// 1 when it is not, so a nil error means ignored.
func isGitIgnored(root, rel string) bool {
	cmd := exec.Command("git", "-C", root, "check-ignore", "-q", "--", rel)
	return cmd.Run() == nil
}

// reflinkCopy copies src to dst using a copy-on-write reflink when the platform
// supports it (instant, space-shared), falling back to a plain recursive copy.
// macOS uses `cp -c`; Linux uses `cp --reflink=auto` (which itself falls back to
// a full copy when the filesystem lacks reflink support).
func reflinkCopy(src, dst string) error {
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
		// cp may be absent or reject -c on an unsupported FS; fall back so the copy
		// still happens, just without the reflink speedup.
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

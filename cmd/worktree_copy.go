package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
func copyWorktreeIncludes(srcRoot, dstRoot string) ([]string, error) {
	manifest := filepath.Join(srcRoot, worktreeIncludeFile)
	data, err := os.ReadFile(manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	entries := parseIncludePatterns(string(data))
	if len(entries) == 0 {
		return nil, nil
	}
	// Expansion is untrusted input to the SAME pipeline literals use: every
	// expanded match goes through validation and the containment guards below,
	// so globbing cannot widen the path-escape surface.
	entries, err = expandIncludePatterns(srcRoot, entries)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	entries, err = validateWorktreeIncludePaths(entries)
	if err != nil {
		return nil, err
	}

	realRoot, err := filepath.EvalSymlinks(srcRoot)
	if err != nil {
		return nil, err
	}
	// One `git check-ignore --stdin` spawn answers every entry; the per-entry
	// decision order below (absent -> not ignored -> containment) is unchanged.
	ignored, err := gitIgnoredSet(srcRoot, entries)
	if err != nil {
		return nil, err
	}

	var copied []string
	for _, rel := range entries {
		src := filepath.Join(srcRoot, rel)
		if _, err := os.Lstat(src); err != nil {
			continue // listed but absent: skip rather than fail the whole create
		}
		if !ignored[rel] {
			continue // tracked (or not ignored): git worktree add already has it
		}
		realParent, err := filepath.EvalSymlinks(filepath.Dir(src))
		if err != nil {
			continue // unresolvable: skip like an absent entry
		}
		if realParent != realRoot && !strings.HasPrefix(realParent, realRoot+string(filepath.Separator)) {
			continue // resolves outside the repo, for example through a symlinked dir
		}
		dst, err := prepareSafeDestination(dstRoot, rel)
		if err != nil {
			return copied, fmt.Errorf(".worktreeinclude path %q: %w", rel, err)
		}
		if err := reflinkCopy(src, dst); err != nil {
			return copied, err
		}
		copied = append(copied, rel)
	}
	return copied, nil
}

func validateWorktreeIncludePaths(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, rel := range entries {
		cleaned, err := validateWorktreeIncludePath(rel)
		if err != nil {
			return nil, err
		}
		out = append(out, cleaned)
	}
	return out, nil
}

func validateWorktreeIncludePath(rel string) (string, error) {
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("unsafe .worktreeinclude path %q: absolute paths are not allowed", rel)
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe .worktreeinclude path %q: path must stay within the repository", rel)
	}
	return cleaned, nil
}

// parseIncludePatterns extracts the path entries from a .worktreeinclude file,
// skipping blank lines and # comments. Validation later cleans or rejects each
// entry so unsafe paths cannot be silently discarded after earlier entries have
// been copied.
func parseIncludePatterns(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// expandIncludePatterns expands every parsed manifest line into concrete
// repo-relative paths, deduplicated in first-seen order (per-line results are
// sorted for determinism). Lines without glob metacharacters pass through
// unchanged whether or not they exist — the copy loop's Lstat skip handles
// absence, exactly as before.
func expandIncludePatterns(srcRoot string, entries []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, entry := range entries {
		matches, err := expandIncludePattern(srcRoot, entry)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// expandIncludePattern expands one manifest line. A non-matching glob expands
// to nothing (silent skip, like a listed-but-absent literal); a syntactically
// invalid pattern is a hard error naming the line. Expansion NEVER validates —
// every result goes through the same validation pipeline as a literal line.
func expandIncludePattern(srcRoot, pattern string) ([]string, error) {
	if !strings.ContainsAny(pattern, "*?[") {
		return []string{pattern}, nil
	}
	segs := strings.Split(filepath.ToSlash(pattern), "/")
	hasDoubleStar := false
	for _, seg := range segs {
		if seg == "**" {
			hasDoubleStar = true
			continue
		}
		// Surface ErrBadPattern up front so a bad pattern errors even when
		// nothing would match (filepath.Match fully parses since Go 1.15).
		if _, err := filepath.Match(seg, ""); err != nil {
			return nil, fmt.Errorf(".worktreeinclude pattern %q: %w", pattern, err)
		}
	}
	if !hasDoubleStar {
		abs, err := filepath.Glob(filepath.Join(srcRoot, pattern))
		if err != nil {
			return nil, fmt.Errorf(".worktreeinclude pattern %q: %w", pattern, err)
		}
		rels := make([]string, 0, len(abs))
		for _, m := range abs {
			rel, relErr := filepath.Rel(srcRoot, m)
			if relErr != nil {
				continue
			}
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		return rels, nil
	}
	// '**' walk. fs.WalkDir does not descend symlinked directories (matches
	// that ARE symlinks still pass through; the copier recreates them
	// verbatim), and .git is never walked.
	var rels []string
	walkErr := fs.WalkDir(os.DirFS(srcRoot), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, like an absent literal
		}
		if p == "." {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		ok, mErr := matchGlobSegments(segs, strings.Split(p, "/"))
		if mErr != nil {
			return mErr
		}
		if ok {
			rels = append(rels, filepath.FromSlash(p))
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf(".worktreeinclude pattern %q: %w", pattern, walkErr)
	}
	sort.Strings(rels)
	return rels, nil
}

// matchGlobSegments matches slash-split path segments against pattern
// segments, where a pattern segment of exactly "**" consumes zero or more
// path segments and every other segment matches per filepath.Match.
func matchGlobSegments(pat, name []string) (bool, error) {
	if len(pat) == 0 {
		return len(name) == 0, nil
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			ok, err := matchGlobSegments(pat[1:], name[i:])
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	if len(name) == 0 {
		return false, nil
	}
	ok, err := filepath.Match(pat[0], name[0])
	if err != nil || !ok {
		return ok, err
	}
	return matchGlobSegments(pat[1:], name[1:])
}

// gitIgnoredSet returns which of rels (relative to root) are gitignored, in
// one `git check-ignore -z --stdin` spawn. Entries travel NUL-separated both
// ways (-z sidesteps core.quotePath quoting). Exit status 1 means "none
// ignored" and is not an error. A fatal exit (128 — e.g. one entry reaches
// beyond a symlinked directory) poisons the whole batch, so it falls back to
// per-entry probes, preserving the old per-path semantics: a path git cannot
// classify counts as not ignored (the copy loop then skips it).
func gitIgnoredSet(root string, rels []string) (map[string]bool, error) {
	ignored := make(map[string]bool, len(rels))
	if len(rels) == 0 {
		return ignored, nil
	}
	cmd := exec.Command("git", "-C", root, "check-ignore", "-z", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(rels, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git check-ignore: %w", err)
		}
		switch exitErr.ExitCode() {
		case 1:
			return ignored, nil // none of the paths are ignored
		default:
			for _, rel := range rels {
				if isGitIgnored(root, rel) {
					ignored[rel] = true
				}
			}
			return ignored, nil
		}
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored, nil
}

// isGitIgnored reports whether rel (relative to root) is ignored by git, via
// `git -C <root> check-ignore`. check-ignore exits 0 when the path is ignored,
// 1 when it is not, so a nil error means ignored. It is the per-entry fallback
// when the batched probe hits a fatal entry.
func isGitIgnored(root, rel string) bool {
	cmd := exec.Command("git", "-C", root, "check-ignore", "-q", "--", rel)
	return cmd.Run() == nil
}

func prepareSafeDestination(dstRoot, rel string) (string, error) {
	dst := filepath.Join(dstRoot, rel)
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(dstRoot)
	if err != nil {
		return "", err
	}
	parentRel := filepath.Dir(rel)
	if parentRel != "." {
		cur := dstRoot
		for _, part := range strings.Split(parentRel, string(filepath.Separator)) {
			if part == "" || part == "." {
				continue
			}
			cur = filepath.Join(cur, part)
			if err := ensureSafeDestinationDir(realRoot, cur); err != nil {
				return "", err
			}
		}
	}
	if err := rejectDestinationSymlink(dst); err != nil {
		return "", err
	}
	return dst, nil
}

func ensureSafeDestinationDir(realRoot, dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.Mkdir(dir, 0o755)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if !pathWithin(realRoot, realDir) {
			return fmt.Errorf("unsafe destination parent %q resolves outside worktree", dir)
		}
		stat, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if !stat.IsDir() {
			return fmt.Errorf("destination parent %q is not a directory", dir)
		}
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("destination parent %q is not a directory", dir)
	}
	return nil
}

func pathWithin(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func rejectDestinationSymlink(dst string) error {
	info, err := os.Lstat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe destination symlink %q", dst)
	}
	return nil
}

// reflinkCopy copies src to dst using a copy-on-write reflink when the platform
// supports it (instant, space-shared), falling back to a plain recursive copy.
// macOS uses `cp -c`; Linux uses `cp --reflink=auto` (which itself falls back to
// a full copy when the filesystem lacks reflink support).
func reflinkCopy(src, dst string) error {
	if err := rejectDestinationSymlink(dst); err != nil {
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
	if err := rejectDestinationSymlink(dst); err != nil {
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

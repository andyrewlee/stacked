package stack

// The policy half of the .worktreeinclude copy subsystem: manifest parsing,
// path-containment validation, segment-glob matching, glob expansion, the
// candidate-selection and collision-preflight rules, and destination-safety
// checks. Everything here is pure or plain-filesystem work — git probes
// (check-ignore, ls-files) arrive as injected sets — so the whole pipeline
// tests on temp dirs. The byte-copy executor (reflink/plain cp) stays in
// cmd/worktree_copy.go.

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ParseIncludePatterns extracts the path entries from a .worktreeinclude file,
// skipping blank lines and # comments. Validation later cleans or rejects each
// entry so unsafe paths cannot be silently discarded after earlier entries have
// been copied.
func ParseIncludePatterns(content string) []string {
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

// ValidateWorktreeIncludePaths cleans every entry via
// ValidateWorktreeIncludePath, failing on the first unsafe one.
func ValidateWorktreeIncludePaths(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, rel := range entries {
		cleaned, err := ValidateWorktreeIncludePath(rel)
		if err != nil {
			return nil, err
		}
		out = append(out, cleaned)
	}
	return out, nil
}

// ValidateWorktreeIncludePath is the pure containment guard on manifest
// entries: absolute paths and paths that escape the repository (".", "..",
// "../…") are rejected; safe entries are returned cleaned.
func ValidateWorktreeIncludePath(rel string) (string, error) {
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("unsafe .worktreeinclude path %q: absolute paths are not allowed", rel)
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe .worktreeinclude path %q: path must stay within the repository", rel)
	}
	return cleaned, nil
}

// MatchGlobSegments matches slash-split path segments against pattern
// segments, where a pattern segment of exactly "**" consumes zero or more
// path segments and every other segment matches per filepath.Match.
func MatchGlobSegments(pat, name []string) (bool, error) {
	if len(pat) == 0 {
		return len(name) == 0, nil
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			ok, err := MatchGlobSegments(pat[1:], name[i:])
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
	return MatchGlobSegments(pat[1:], name[1:])
}

// PathWithin reports whether p equals root or lives beneath it, compared on
// path-segment boundaries (never a bare string prefix, so "/a/bc" is not
// within "/a/b").
func PathWithin(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// --- fs-bound policy: expansion, selection, collision preflight ------------
// These functions read the filesystem but never spawn git — the caller injects
// the probe results (the ignored set from `git check-ignore`, the destination
// tracked set from `git ls-files`), so the whole pipeline tests on temp dirs.

// ExpandIncludePatterns expands every parsed manifest line into concrete
// repo-relative paths, deduplicated in first-seen order (per-line results are
// sorted for determinism). Lines without glob metacharacters pass through
// unchanged whether or not they exist — the copy loop's Lstat skip handles
// absence.
func ExpandIncludePatterns(srcRoot string, entries []string) ([]string, error) {
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
		ok, mErr := MatchGlobSegments(segs, strings.Split(p, "/"))
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

// DropNestedIncludeDirs removes every entry that lives inside another selected
// entry that is a directory on disk AND will itself be copied (is
// gitignored): copying that ancestor already delivers the descendant. A
// tracked selected directory is skipped by the copy loop, so it must not
// suppress — its gitignored descendants still need their own copy.
// Comparison is on path-segment boundaries ("nm" never suppresses a sibling
// like "nmx"); only real directories suppress (a symlink-to-dir does not,
// keeping the conservative pre-existing behavior for links). Survivor order
// is preserved.
func DropNestedIncludeDirs(srcRoot string, entries []string, ignored map[string]bool) []string {
	dirs := make([]string, 0, len(entries))
	for _, rel := range entries {
		if !ignored[rel] {
			continue
		}
		if info, err := os.Lstat(filepath.Join(srcRoot, rel)); err == nil && info.IsDir() {
			dirs = append(dirs, rel)
		}
	}
	out := make([]string, 0, len(entries))
	for _, rel := range entries {
		nested := false
		for _, dir := range dirs {
			if rel != dir && strings.HasPrefix(rel, dir+string(filepath.Separator)) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, rel)
		}
	}
	return out
}

// SelectWorktreeIncludes is the copy's phase-1 selection: it keeps only
// entries that exist in the source, are gitignored, and resolve inside the
// repository. Nothing is written to the destination here.
func SelectWorktreeIncludes(srcRoot string, entries []string, ignored map[string]bool) ([]string, error) {
	realRoot, err := filepath.EvalSymlinks(srcRoot)
	if err != nil {
		return nil, err
	}
	var candidates []string
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
		candidates = append(candidates, rel)
	}
	return candidates, nil
}

// RefuseWorktreeIncludeCollisions rejects every selected include whose
// destination is not empty, BEFORE any file is copied. A collision is: rel is
// tracked in the destination (ls-files — including a tracked-but-missing
// worktree file), a tracked path lives beneath a selected dir, a tracked path
// is an ancestor of rel (destination tracks "dir" as a file/symlink/gitlink
// while rel is "dir/sub"), or ANY filesystem entry already sits at the
// destination root (untracked file, symlink, or directory — including an
// empty one: `cp -R src dst` would merge into an existing dst dir, while
// plainCopy would overwrite files, so the strategies must both start from an
// absent root). Existing ANCESTOR directories are still fine — the copy's own
// containment checks govern those. Pure validation: no directory creation.
// The destination's tracked set is supplied by the caller (one ls-files
// probe).
func RefuseWorktreeIncludeCollisions(dstRoot string, candidates []string, tracked map[string]bool, trackedSorted []string) error {
	for _, rel := range candidates {
		slashed := filepath.ToSlash(rel)
		dst := filepath.Join(dstRoot, rel)
		info, lerr := os.Lstat(dst)
		// A symlink is refused FIRST even when it is also tracked: it names
		// the more specific hazard — a copy could follow it outside the
		// worktree.
		if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(".worktreeinclude path %q: unsafe destination symlink %q", rel, dst)
		}
		if tracked[slashed] {
			return fmt.Errorf(".worktreeinclude path %q is tracked in the destination worktree", rel)
		}
		// A tracked DESCENDANT under a selected directory entry: copying the
		// dir would merge into tracked content.
		i := sort.SearchStrings(trackedSorted, slashed+"/")
		if i < len(trackedSorted) && strings.HasPrefix(trackedSorted[i], slashed+"/") {
			return fmt.Errorf(".worktreeinclude path %q contains the destination-tracked path %q", rel, trackedSorted[i])
		}
		// A tracked ANCESTOR of rel: the destination tracks "dir" itself (file,
		// symlink or gitlink), so "dir/sub" cannot be created beneath it.
		for parent := path.Dir(slashed); parent != "." && parent != "/"; parent = path.Dir(parent) {
			if tracked[parent] {
				return fmt.Errorf(".worktreeinclude path %q sits under the destination-tracked path %q", rel, parent)
			}
		}
		if lerr != nil && !os.IsNotExist(lerr) {
			return fmt.Errorf(".worktreeinclude path %q: %w", rel, lerr)
		}
		if lerr == nil {
			// Non-symlink entry (file or directory — even an empty dir, which
			// `cp -R` would merge into) already occupies the destination root.
			return fmt.Errorf(".worktreeinclude path %q already exists in the destination worktree", rel)
		}
	}
	return nil
}

// PrepareIncludeDestination resolves the destination path for one include,
// creating and vetting its parent chain: dstRoot is created if missing, every
// ancestor directory is created or verified to resolve inside the worktree,
// and a symlinked destination is refused. It returns the path to copy into.
func PrepareIncludeDestination(dstRoot, rel string) (string, error) {
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
	if err := RejectDestinationSymlink(dst); err != nil {
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
		if !PathWithin(realRoot, realDir) {
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

// RejectDestinationSymlink refuses to copy onto an existing symlink — the copy
// would follow it outside the worktree. An absent destination is fine.
func RejectDestinationSymlink(dst string) error {
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

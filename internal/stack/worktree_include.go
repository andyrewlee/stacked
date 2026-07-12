package stack

// The pure (no filesystem, no git) helpers of the .worktreeinclude copy
// subsystem: manifest parsing, path-containment validation, and segment-glob
// matching. The fs-bound containment guards and the byte-copy executor stay in
// cmd/worktree_copy.go — only string logic lives here, so it tests without a
// real tree.

import (
	"fmt"
	"os"
	"path/filepath"
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

// PathWithin reports whether path equals root or lives beneath it, compared on
// path-segment boundaries (never a bare string prefix, so "/a/bc" is not
// within "/a/b").
func PathWithin(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

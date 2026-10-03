package stack

// Tempdir coverage of the fs-bound .worktreeinclude policy moved out of cmd
// (plan 025): glob expansion, nested-drop, candidate selection, collision
// preflight, and destination safety. Git probe results arrive as injected
// sets, so nothing here spawns git — the byte-copy executor stays in cmd.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExpandIncludePatternsGlobsAndDedupes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "dist/app.js")
	writeFile(t, root, "dist/lib/util.js")
	writeFile(t, root, "node_modules/pkg/index.js")
	writeFile(t, root, "plain.txt")

	got, err := ExpandIncludePatterns(root, []string{"dist/*", "plain.txt", "dist/*", "missing*"})
	if err != nil {
		t.Fatalf("ExpandIncludePatterns: %v", err)
	}
	// dist/* matches the app.js file and the lib dir (per-segment glob);
	// plain.txt passes through literally; the repeat dedupes; missing* expands
	// to nothing.
	want := []string{"dist/app.js", "dist/lib", "plain.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ExpandIncludePatterns = %v, want %v", got, want)
	}
}

func TestExpandIncludePatternsDoubleStar(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "a/deep/hit.txt")
	writeFile(t, root, "a/b/deeper/hit.txt")
	writeFile(t, root, "other/hit.txt")

	got, err := ExpandIncludePatterns(root, []string{"a/**/hit.txt"})
	if err != nil {
		t.Fatalf("ExpandIncludePatterns: %v", err)
	}
	want := []string{filepath.FromSlash("a/b/deeper/hit.txt"), filepath.FromSlash("a/deep/hit.txt")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ExpandIncludePatterns(**) = %v, want %v", got, want)
	}
}

func TestExpandIncludePatternsRejectsBadPattern(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := ExpandIncludePatterns(root, []string{"dist/["}); err == nil {
		t.Fatal("ExpandIncludePatterns accepted a malformed pattern")
	}
}

func TestDropNestedIncludeDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "ignored-dir/inner.txt")
	writeFile(t, root, "tracked-dir/inner.txt")

	// ignored-dir is an ignored directory: it suppresses its descendant.
	// tracked-dir is not ignored: it must NOT suppress, and a sibling that
	// merely shares a prefix (ignored-dirx) is never nested.
	entries := []string{"ignored-dir", "ignored-dir/inner.txt", "tracked-dir", "tracked-dir/inner.txt"}
	ignored := map[string]bool{"ignored-dir": true, "ignored-dir/inner.txt": true, "tracked-dir/inner.txt": true}
	got := DropNestedIncludeDirs(root, entries, ignored)
	want := []string{"ignored-dir", "tracked-dir", "tracked-dir/inner.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("DropNestedIncludeDirs = %v, want %v", got, want)
	}
}

func TestSelectWorktreeIncludesGatesOnExistenceAndIgnore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "ignored.txt")
	writeFile(t, root, "tracked.txt")
	writeFile(t, root, "ignored-dir/f.txt")

	ignored := map[string]bool{"ignored.txt": true, "ignored-dir": true}
	got, err := SelectWorktreeIncludes(root, []string{"ignored.txt", "tracked.txt", "absent.txt", "ignored-dir"}, ignored)
	if err != nil {
		t.Fatalf("SelectWorktreeIncludes: %v", err)
	}
	want := []string{"ignored.txt", "ignored-dir"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SelectWorktreeIncludes = %v, want %v", got, want)
	}
}

func TestSelectWorktreeIncludesSkipsSymlinkEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt")
	// A symlinked directory inside the repo pointing outside it: children
	// resolve beyond the root and must not be selected.
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ignored := map[string]bool{"link/secret.txt": true}
	got, err := SelectWorktreeIncludes(root, []string{"link/secret.txt"}, ignored)
	if err != nil {
		t.Fatalf("SelectWorktreeIncludes: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("SelectWorktreeIncludes selected an escapee: %v", got)
	}
}

func TestRefuseWorktreeIncludeCollisions(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()

	// Baseline: a clean destination accepts the candidate.
	if err := RefuseWorktreeIncludeCollisions(dst, []string{"nm"}, nil, nil); err != nil {
		t.Fatalf("clean destination refused: %v", err)
	}

	cases := []struct {
		name      string
		candidate string
		setup     func(t *testing.T)
		tracked   map[string]bool
		sorted    []string
	}{
		{"tracked-exact", "nm", nil, map[string]bool{"nm": true}, []string{"nm"}},
		{"tracked-descendant", "nm", nil, map[string]bool{"nm/sub": true}, []string{"nm/sub"}},
		{"tracked-ancestor", "dir/sub", nil, map[string]bool{"dir": true}, []string{"dir"}},
		{"existing-file", "nm", func(t *testing.T) { writeFile(t, dst, "nm") }, nil, nil},
	}
	for _, tc := range cases {
		if tc.setup != nil {
			tc.setup(t)
		}
		err := RefuseWorktreeIncludeCollisions(dst, []string{tc.candidate}, tc.tracked, tc.sorted)
		if err == nil {
			t.Errorf("%s: RefuseWorktreeIncludeCollisions accepted %q", tc.name, tc.candidate)
		}
		if tc.setup != nil {
			os.RemoveAll(filepath.Join(dst, tc.candidate))
		}
	}
}

func TestRefuseWorktreeIncludeCollisionsSymlink(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()
	if err := os.Symlink("/tmp", filepath.Join(dst, "nm")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := RefuseWorktreeIncludeCollisions(dst, []string{"nm"}, map[string]bool{"nm": true}, []string{"nm"})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink collision error = %v, want an unsafe-symlink refusal", err)
	}
}

func TestPrepareIncludeDestination(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()

	got, err := PrepareIncludeDestination(dst, "a/b/f.txt")
	if err != nil {
		t.Fatalf("PrepareIncludeDestination: %v", err)
	}
	if want := filepath.Join(dst, "a", "b", "f.txt"); got != want {
		t.Fatalf("PrepareIncludeDestination = %q, want %q", got, want)
	}
	if info, err := os.Stat(filepath.Join(dst, "a", "b")); err != nil || !info.IsDir() {
		t.Fatalf("parent chain not created: %v", err)
	}
}

func TestPrepareIncludeDestinationRejectsSymlinkedParent(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dst, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := PrepareIncludeDestination(dst, "link/f.txt"); err == nil {
		t.Fatal("PrepareIncludeDestination accepted a parent resolving outside the worktree")
	}
}

// A dangling symlinked parent fails the EvalSymlinks probe — the error
// surfaces rather than being treated as a safe destination.
func TestPrepareIncludeDestinationDanglingSymlinkParent(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()
	if err := os.Symlink(filepath.Join(dst, "gone"), filepath.Join(dst, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := PrepareIncludeDestination(dst, "link/f.txt"); err == nil {
		t.Fatal("PrepareIncludeDestination accepted a dangling-symlink parent")
	}
}

// A symlinked parent that resolves INSIDE the worktree but to a regular file
// is not a usable destination directory — os.Stat follows the link, so the
// not-a-directory refusal must fire on the resolved target.
func TestPrepareIncludeDestinationSymlinkToRegularFile(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()
	writeFile(t, dst, "real.txt")
	if err := os.Symlink(filepath.Join(dst, "real.txt"), filepath.Join(dst, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := PrepareIncludeDestination(dst, "link/f.txt"); err == nil {
		t.Fatal("PrepareIncludeDestination accepted a symlink resolving to a regular file")
	}
}

// A regular file sitting where a parent directory is needed is refused by the
// plain not-a-directory arm (no symlink involved).
func TestPrepareIncludeDestinationRegularFileParent(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()
	writeFile(t, dst, "plain.txt")
	if _, err := PrepareIncludeDestination(dst, "plain.txt/f.txt"); err == nil {
		t.Fatal("PrepareIncludeDestination accepted a regular file as a parent directory")
	}
}

func TestRejectDestinationSymlink(t *testing.T) {
	t.Parallel()
	dst := t.TempDir()
	// Absent is fine.
	if err := RejectDestinationSymlink(filepath.Join(dst, "absent")); err != nil {
		t.Fatalf("RejectDestinationSymlink(absent) = %v", err)
	}
	// A regular file is fine (the collision preflight owns that verdict).
	writeFile(t, dst, "plain.txt")
	if err := RejectDestinationSymlink(filepath.Join(dst, "plain.txt")); err != nil {
		t.Fatalf("RejectDestinationSymlink(file) = %v", err)
	}
	// A symlink is refused.
	if err := os.Symlink("/tmp", filepath.Join(dst, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := RejectDestinationSymlink(filepath.Join(dst, "link")); err == nil {
		t.Fatal("RejectDestinationSymlink accepted a symlink")
	}
}

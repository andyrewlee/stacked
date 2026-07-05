package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseIncludePatterns(t *testing.T) {
	in := "# a comment\n\nnode_modules\n  target  \n# trailing\n./build\n"
	got := parseIncludePatterns(in)
	want := []string{"node_modules", "target", "build"}
	if len(got) != len(want) {
		t.Fatalf("parseIncludePatterns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPlainCopyRecursive(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "deep.txt"), []byte("deep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plainCopy(src, dst); err != nil {
		t.Fatalf("plainCopy: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "top.txt")); err != nil || string(b) != "top\n" {
		t.Errorf("top.txt = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sub", "deep.txt")); err != nil || string(b) != "deep\n" {
		t.Errorf("sub/deep.txt = %q, %v", b, err)
	}
}

// TestPlainCopyPreservesSymlinks copies a tree containing both a valid and a
// broken symlink and asserts the copy completes (does not abort mid-tree on the
// broken link) and that links are recreated, not dereferenced. A broken symlink
// is the realistic failure mode (e.g. node_modules/.bin entries); os.ReadFile
// would follow it, fail, and — because os.ReadDir is sorted — leave a
// half-populated worktree.
func TestPlainCopyPreservesSymlinks(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")

	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A valid symlink to the real file.
	if err := os.Symlink("real.txt", filepath.Join(src, "good.link")); err != nil {
		t.Fatal(err)
	}
	// A broken symlink whose target does not exist. Its name sorts before a later
	// regular file so, if the broken link aborted the recursion, that file would
	// be missing from the copy.
	if err := os.Symlink("nonexistent-target", filepath.Join(src, "broken.link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "z-after.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plainCopy(src, dst); err != nil {
		t.Fatalf("plainCopy with a broken symlink should not fail: %v", err)
	}

	// The valid symlink is preserved as a symlink (not dereferenced into a copy).
	gi, err := os.Lstat(filepath.Join(dst, "good.link"))
	if err != nil {
		t.Fatalf("good.link missing: %v", err)
	}
	if gi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("good.link was dereferenced; want a preserved symlink")
	}
	if target, err := os.Readlink(filepath.Join(dst, "good.link")); err != nil || target != "real.txt" {
		t.Errorf("good.link target = %q, %v; want real.txt", target, err)
	}

	// The broken symlink is preserved too (not skipped, not dereferenced).
	bi, err := os.Lstat(filepath.Join(dst, "broken.link"))
	if err != nil {
		t.Fatalf("broken.link missing: %v", err)
	}
	if bi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("broken.link is not a symlink")
	}
	if target, err := os.Readlink(filepath.Join(dst, "broken.link")); err != nil || target != "nonexistent-target" {
		t.Errorf("broken.link target = %q, %v; want nonexistent-target", target, err)
	}

	// The copy did not abort mid-tree: the file after the broken link is present.
	if b, err := os.ReadFile(filepath.Join(dst, "z-after.txt")); err != nil || string(b) != "after\n" {
		t.Errorf("z-after.txt = %q, %v; recursion aborted on the broken symlink", b, err)
	}
}

func TestReflinkCopyFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "f.txt")
	dst := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reflinkCopy(src, dst); err != nil {
		t.Fatalf("reflinkCopy: %v", err)
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "hello\n" {
		t.Errorf("copied file = %q, %v", b, err)
	}
}

// TestCopyWorktreeIncludes drives the manifest end to end against a real repo:
// a gitignored listed file is copied; a tracked listed file is skipped; a listed
// but absent file is silently ignored.
func TestCopyWorktreeIncludes(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "ignored.txt\n")
	write(t, "ignored.txt", "secret\n")
	write(t, "tracked.txt", "public\n")
	mustRun(t, "git", "add", "tracked.txt", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "files")
	write(t, ".worktreeinclude", "ignored.txt\ntracked.txt\nmissing.txt\n")

	dst := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(copied) != 1 || copied[0] != "ignored.txt" {
		t.Fatalf("copied = %v, want only ignored.txt", copied)
	}
	if _, err := os.Stat(filepath.Join(dst, "ignored.txt")); err != nil {
		t.Errorf("ignored.txt not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "tracked.txt")); !os.IsNotExist(err) {
		t.Errorf("tracked.txt should not be copied")
	}
}

func TestCopyWorktreeIncludesTreatsWildcardsAsLiteralPaths(t *testing.T) {
	newRepo(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "*.env\n")
	write(t, "secret.env", "TOKEN=1\n")
	write(t, ".worktreeinclude", "*.env\n")

	dst := filepath.Join(t.TempDir(), "wt")
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(copied) != 0 {
		t.Fatalf("copied = %v, want none for wildcard-looking literal entry", copied)
	}
	if _, err := os.Stat(filepath.Join(dst, "secret.env")); !os.IsNotExist(err) {
		t.Errorf("secret.env should not be copied from wildcard-looking entry")
	}
}

func TestCopyWorktreeIncludesRefusesDestinationSymlink(t *testing.T) {
	newRepo(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "ignored.txt\n")
	write(t, "ignored.txt", "secret\n")
	write(t, ".worktreeinclude", "ignored.txt\n")

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "ignored.txt")); err != nil {
		t.Fatal(err)
	}

	copied, err := copyWorktreeIncludes(root, dst)

	if b, readErr := os.ReadFile(outside); readErr != nil || string(b) != "outside\n" {
		t.Fatalf("outside file = %q, %v; destination symlink was followed", b, readErr)
	}
	if err == nil {
		t.Fatalf("copyWorktreeIncludes error = nil, copied = %v; want unsafe destination symlink error", copied)
	}
	if !strings.Contains(err.Error(), "destination symlink") {
		t.Fatalf("copyWorktreeIncludes error = %v, want destination symlink context", err)
	}
}

func TestCopyWorktreeIncludesRejectsUnsafePaths(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "outside.txt")
	unsafe := []string{
		absolute,
		".",
		"..",
		"../outside.txt",
		"dir/../../outside.txt",
	}
	for _, rel := range unsafe {
		if _, err := validateWorktreeIncludePath(rel); err == nil {
			t.Errorf("validateWorktreeIncludePath(%q) error = nil, want unsafe path error", rel)
		}
	}

	if got, err := validateWorktreeIncludePath("./safe/../ignored.txt"); err != nil || got != "ignored.txt" {
		t.Fatalf("validateWorktreeIncludePath safe path = %q, %v; want ignored.txt, nil", got, err)
	}
}

func TestCopyWorktreeIncludesRejectsUnsafePathBeforeCopy(t *testing.T) {
	newRepo(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "ignored.txt\n")
	write(t, "ignored.txt", "secret\n")
	write(t, ".worktreeinclude", "ignored.txt\n../outside.txt\n")

	dst := filepath.Join(t.TempDir(), "wt")
	copied, err := copyWorktreeIncludes(root, dst)
	if err == nil {
		t.Fatalf("copyWorktreeIncludes error = nil, copied = %v; want unsafe path error", copied)
	}
	if !strings.Contains(err.Error(), "unsafe .worktreeinclude path") {
		t.Fatalf("copyWorktreeIncludes error = %v, want unsafe path context", err)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "ignored.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("ignored.txt was copied before unsafe manifest rejection: %v", statErr)
	}
}

func TestCopyWorktreeIncludesNoManifest(t *testing.T) {
	src := t.TempDir()
	copied, err := copyWorktreeIncludes(src, t.TempDir())
	if err != nil {
		t.Fatalf("copyWorktreeIncludes without manifest: %v", err)
	}
	if copied != nil {
		t.Errorf("copied = %v, want nil with no manifest", copied)
	}
}

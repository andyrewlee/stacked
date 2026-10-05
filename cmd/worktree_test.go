package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

// TestParseIncludePatterns and the direct ValidateWorktreeIncludePath test
// moved with the pure helpers to internal/stack/worktree_include_test.go
// (plan 068 slice 1). The fs-bound containment suite below stays here.

// seedWorktreeCache injects a worktree list without a git probe, the way a
// warm cache holds one. Cleanup resets so other tests re-probe.
func seedWorktreeCache(t *testing.T, wts []git.Worktree) {
	t.Helper()
	worktreeCacheState.Lock()
	worktreeCacheState.wts = wts
	worktreeCacheState.probed = true
	worktreeCacheState.Unlock()
	t.Cleanup(resetProcCaches)
}

func TestWorktreeListDoesNotMutateCachedOrder(t *testing.T) {
	seedWorktreeCache(t, []git.Worktree{
		{Path: "/repo-z", Branch: "main", Head: "111"},
		{Path: "/repo-a", Branch: "feat-a", Head: "222"},
	})

	out := captureStdout(t, func() {
		if err := worktreeList(false); err != nil {
			t.Fatalf("worktreeList: %v", err)
		}
	})
	if !strings.HasPrefix(out, "feat-a\t/repo-a\nmain\t/repo-z\n") {
		t.Fatalf("worktreeList text output = %q, want sorted by path", out)
	}

	wts, err := worktrees()
	if err != nil {
		t.Fatalf("worktrees after list: %v", err)
	}
	if wts[0].Path != "/repo-z" || wts[0].Branch != "main" {
		t.Fatalf("worktreeList mutated cached order: %+v", wts)
	}
}

// TestWorktreeMutationsInvalidateCache pins the cache invariant: worktrees()
// may be warmed at any point before a topology mutation and a later call must
// still reflect reality. Without the resetProcCaches() calls at the
// mutation sites, both post-mutation reads below see the stale warmed list.
func TestWorktreeMutationsInvalidateCache(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")
	// The checkout above warmed the cache mid-command (owner lookup) while
	// HEAD was still feat-a; drop it so this test starts from an accurate
	// warmed list, as a fresh production process would.
	resetProcCaches()

	// findReported returns the list entry matching path, comparing resolved
	// paths: on macOS the temp HOME sits behind the /var -> /private/var
	// symlink and git reports the resolved form.
	findReported := func(wts []git.Worktree, path string) (string, bool) {
		want, _ := filepath.EvalSymlinks(path)
		for _, wt := range wts {
			got, _ := filepath.EvalSymlinks(wt.Path)
			if wt.Path == path || (want != "" && got == want) {
				return wt.Path, true
			}
		}
		return "", false
	}

	// Warm the cache before mutating topology.
	before, err := worktrees()
	if err != nil {
		t.Fatalf("worktrees (warm): %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("warmed list = %+v, want just the main worktree", before)
	}
	created, err := materializeWorktree("feat-a")
	if err != nil {
		t.Fatalf("materializeWorktree: %v", err)
	}
	after, err := worktrees()
	if err != nil {
		t.Fatalf("worktrees (after add): %v", err)
	}
	reported, ok := findReported(after, created.Path)
	if !ok {
		t.Fatalf("worktrees() stale after add: %q missing from %+v", created.Path, after)
	}

	if err := worktreeRemove("feat-a", false); err != nil {
		t.Fatalf("worktreeRemove: %v", err)
	}
	afterRm, err := worktrees()
	if err != nil {
		t.Fatalf("worktrees (after rm): %v", err)
	}
	for _, wt := range afterRm {
		if wt.Path == reported {
			t.Fatalf("worktrees() stale after remove: %q still in %+v", reported, afterRm)
		}
	}
}

// TestWorktreeRemoveRefusesFromInsideTarget pins the cwd guard on the
// single-branch removal path: run from inside the worktree being removed,
// `st worktree rm` refuses instead of deleting the process's own cwd.
func TestWorktreeRemoveRefusesFromInsideTarget(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")
	// mustCheckout warmed the worktree list mid-command while HEAD was still
	// feat-a; reset so materializeWorktree sees the post-checkout owners.
	resetProcCaches()

	created, err := materializeWorktree("feat-a")
	if err != nil {
		t.Fatalf("materializeWorktree: %v", err)
	}
	t.Chdir(created.Path)
	resetProcCaches()

	err = worktreeRemove("feat-a", false)
	if err == nil || !strings.Contains(err.Error(), "you are inside it") {
		t.Fatalf("worktreeRemove from inside target = %v, want the cwd refusal", err)
	}
	if _, statErr := os.Stat(created.Path); statErr != nil {
		t.Fatalf("refused removal still deleted %q: %v", created.Path, statErr)
	}
}

// TestWorktreeRemoveAllSkipsCwdWorktree: rm --all must not stop on the caller's
// own worktree — it is skipped like a dirty one while the rest are removed.
func TestWorktreeRemoveAllSkipsCwdWorktree(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "main")
	resetProcCaches() // see above: checkout warmed the list pre-move

	stay, err := materializeWorktree("feat-a")
	if err != nil {
		t.Fatalf("materializeWorktree feat-a: %v", err)
	}
	gone, err := materializeWorktree("feat-b")
	if err != nil {
		t.Fatalf("materializeWorktree feat-b: %v", err)
	}
	t.Chdir(stay.Path)
	resetProcCaches()

	out := captureStdout(t, func() {
		if err := worktreeRemoveAll(false); err != nil {
			t.Fatalf("worktreeRemoveAll: %v", err)
		}
	})
	if !strings.Contains(out, "skipped feat-a") || !strings.Contains(out, "inside") {
		t.Fatalf("rm --all output = %q, want the inside-it skip named", out)
	}
	if _, statErr := os.Stat(stay.Path); statErr != nil {
		t.Fatalf("rm --all removed the caller's worktree %q: %v", stay.Path, statErr)
	}
	if _, statErr := os.Stat(gone.Path); !os.IsNotExist(statErr) {
		t.Fatalf("rm --all left sibling worktree %q: %v", gone.Path, statErr)
	}
}

// TestGitIgnoredSet pins the batched check-ignore probe: one spawn classifies
// every manifest entry, exit status 1 (nothing ignored) is an empty set, and
// -z round-trips paths with spaces verbatim.
func TestGitIgnoredSet(t *testing.T) {
	newRepo(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "node_modules\ndist\nwith space.txt\n")
	write(t, "tracked.txt", "t\n")
	mustRun(t, "git", "add", ".gitignore", "tracked.txt")
	mustRun(t, "git", "commit", "-q", "-m", "ignore rules")

	got, err := git.CheckIgnored(root, []string{"node_modules", "dist", "with space.txt", "tracked.txt", "nonexistent"})
	if err != nil {
		t.Fatalf("git.CheckIgnored: %v", err)
	}
	want := map[string]bool{"node_modules": true, "dist": true, "with space.txt": true}
	if len(got) != len(want) {
		t.Fatalf("ignored = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("ignored[%q] = false, want true", k)
		}
	}

	// Nothing ignored: exit status 1 must be an empty set, not an error.
	empty, err := git.CheckIgnored(root, []string{"tracked.txt", "nonexistent"})
	if err != nil {
		t.Fatalf("git.CheckIgnored (none ignored): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ignored = %v, want empty", empty)
	}

	// No entries: no spawn, empty set.
	none, err := git.CheckIgnored(root, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("git.CheckIgnored(nil) = %v, %v; want empty, nil", none, err)
	}
}

// TestGitIgnoredSetPoisonedBatchClassifiesSiblings pins the fallback arm of
// the batched check-ignore probe: one entry git cannot classify (a path
// through a symlinked directory — check-ignore dies fatally on it) makes the
// WHOLE batch exit non-cleanly, so the probe drops to per-entry checks. The
// sibling entries must still classify correctly — a genuinely ignored path
// stays ignored, the poison path counts as not ignored. Existing traversal
// tests only ever feed the fallback entries whose answer is "not ignored"
// either way; an empty-result regression would be invisible to them.
func TestGitIgnoredSetPoisonedBatchClassifiesSiblings(t *testing.T) {
	newRepo(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "ignored.txt\nescape\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore rules")

	// The poison: a path whose parent is a symlink out of the worktree —
	// check-ignore cannot classify it and the batch dies with a fatal (non-1)
	// exit, forcing the per-entry fallback.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "inner.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	got, err := git.CheckIgnored(root, []string{"ignored.txt", "escape/inner.txt"})
	if err != nil {
		t.Fatalf("git.CheckIgnored: %v", err)
	}
	if !got["ignored.txt"] {
		t.Fatalf("ignored.txt classified %v — the poisoned batch must still classify its ignored siblings", got)
	}
	if got["escape/inner.txt"] {
		t.Fatalf("escape/inner.txt classified %v — the unclassifiable path must count as not ignored", got)
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

func TestCopyWorktreeIncludesRejectsEscapingPath(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(root)
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "outside.txt\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore")
	write(t, ".worktreeinclude", "../outside.txt\n")

	dst := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	copied, err := copyWorktreeIncludes(root, dst)
	if err == nil {
		t.Fatalf("copyWorktreeIncludes error = nil, copied = %v; want unsafe path error", copied)
	}
	if !strings.Contains(err.Error(), "unsafe .worktreeinclude path") {
		t.Fatalf("copyWorktreeIncludes error = %v, want unsafe path context", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "..", "outside.txt")); !os.IsNotExist(err) {
		t.Errorf("outside file should not be copied")
	}
}

// TestCopyWorktreeIncludesExpandsGlobs drives the shell-glob matrix: `*`
// within a segment, `**` across directories (including zero directories),
// literals unchanged, a non-matching glob as a silent skip, and a bad pattern
// as a hard error naming the line.
func TestCopyWorktreeIncludesExpandsGlobs(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "dist\nnode_modules\n.env\n")
	if err := os.MkdirAll("dist", 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join("dist", "a.js"), "a\n")
	write(t, filepath.Join("dist", "b.js"), "b\n")
	for _, dir := range []string{
		filepath.Join("packages", "one", "node_modules"),
		filepath.Join("packages", "two", "deep", "node_modules"),
		"node_modules",
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "mod.txt"), "m\n")
	}
	write(t, ".env", "TOKEN=1\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore rules")
	write(t, ".worktreeinclude", "dist/*\npackages/**/node_modules\n.env\nno-such-*\n")

	dst := filepath.Join(t.TempDir(), "wt")
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	want := []string{
		filepath.Join("dist", "a.js"),
		filepath.Join("dist", "b.js"),
		filepath.Join("packages", "one", "node_modules"),
		filepath.Join("packages", "two", "deep", "node_modules"),
		".env",
	}
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("%s not copied: %v (copied=%v)", rel, err, copied)
		}
	}
	if len(copied) != len(want) {
		t.Fatalf("copied = %v, want exactly %v", copied, want)
	}
	// The zero-directories case: packages/**/node_modules must NOT have
	// matched the top-level node_modules (it is not under packages/), and a
	// separate `**/node_modules` line would. Prove the boundary:
	if _, err := os.Stat(filepath.Join(dst, "node_modules")); !os.IsNotExist(err) {
		t.Errorf("top-level node_modules copied by packages/**/node_modules")
	}

	// A syntactically invalid pattern is a hard error naming the line.
	write(t, ".worktreeinclude", "dist/[\n")
	if _, err := copyWorktreeIncludes(root, filepath.Join(t.TempDir(), "wt2")); err == nil || !strings.Contains(err.Error(), "dist/[") {
		t.Fatalf("bad pattern error = %v, want it to name the line", err)
	}
}

// TestCopyWorktreeIncludesDoubleStarMatchesZeroDirs pins "** matches zero or
// more directories": a/**/f matches a/f as well as a/b/f.
func TestCopyWorktreeIncludesDoubleStarMatchesZeroDirs(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "a\n")
	if err := os.MkdirAll(filepath.Join("a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join("a", "f.txt"), "0\n")
	write(t, filepath.Join("a", "b", "f.txt"), "1\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore")
	write(t, ".worktreeinclude", "a/**/f.txt\n")

	dst := filepath.Join(t.TempDir(), "wt")
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("copied = %v, want both f.txt levels", copied)
	}
}

// TestCopyWorktreeIncludesGlobCannotEscape proves expansion output feeds the
// SAME validation pipeline as literals: ..-escaping globs are hard errors,
// never copies.
func TestCopyWorktreeIncludesGlobCannotEscape(t *testing.T) {
	newRepo(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(root)
	if err := os.WriteFile(filepath.Join(parent, "outside-glob.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"../*", "dist/../../*"} {
		write(t, ".worktreeinclude", pattern+"\n")
		dst := filepath.Join(t.TempDir(), "wt")
		copied, err := copyWorktreeIncludes(root, dst)
		if err == nil {
			t.Fatalf("pattern %q: error = nil, copied = %v; want unsafe path error", pattern, copied)
		}
		if !strings.Contains(err.Error(), "unsafe .worktreeinclude path") {
			t.Fatalf("pattern %q: error = %v, want unsafe path rejection", pattern, err)
		}
		if _, statErr := os.Stat(filepath.Join(dst, "..", "outside-glob.txt")); statErr == nil {
			t.Fatalf("pattern %q escaped the destination", pattern)
		}
	}
}

// TestCopyWorktreeIncludesGlobSkipsSymlinkTraversal mirrors the literal
// symlink-traversal test with a glob line: a glob that expands to a path
// through a symlinked directory is skipped by the real-parent containment
// check, not copied and not an error.
func TestCopyWorktreeIncludesGlobSkipsSymlinkTraversal(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "escape\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	write(t, ".worktreeinclude", "escape/*.txt\n")

	dst := filepath.Join(t.TempDir(), "wt")
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(copied) != 0 {
		t.Fatalf("copied = %v, want none through a symlinked dir", copied)
	}
	if _, err := os.Stat(filepath.Join(dst, "escape", "secret.txt")); !os.IsNotExist(err) {
		t.Errorf("escape/secret.txt should not be copied through a symlinked dir")
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

func TestCopyWorktreeIncludesRejectsDestinationParentSymlink(t *testing.T) {
	newRepo(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "cache/sub/token\n")
	if err := os.MkdirAll(filepath.Join("cache", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join("cache", "sub", "token"), "secret\n")
	write(t, ".worktreeinclude", "cache/sub/token\n")

	outside := t.TempDir()
	outsideSubdir := filepath.Join(outside, "sub")
	dst := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "cache")); err != nil {
		t.Fatal(err)
	}

	copied, err := copyWorktreeIncludes(root, dst)
	if err == nil {
		t.Fatalf("copyWorktreeIncludes error = nil, copied = %v; want unsafe destination parent error", copied)
	}
	if !strings.Contains(err.Error(), "destination parent") {
		t.Fatalf("copyWorktreeIncludes error = %v, want destination parent context", err)
	}
	if _, statErr := os.Stat(outsideSubdir); !os.IsNotExist(statErr) {
		t.Fatalf("outside subdir was created before destination parent rejection: %v", statErr)
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

func TestCopyWorktreeIncludesSkipsSymlinkTraversal(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "escape\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	write(t, ".worktreeinclude", "escape/secret.txt\n")

	dst := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(copied) != 0 {
		t.Fatalf("copied = %v, want none", copied)
	}
	if _, err := os.Stat(filepath.Join(dst, "escape", "secret.txt")); !os.IsNotExist(err) {
		t.Errorf("escape/secret.txt should not be copied through a symlinked dir")
	}
}

func TestCopyWorktreeIncludesCopiesManifestSymlinkVerbatim(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "secret.link\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore")
	if err := os.Symlink(outside, filepath.Join(root, "secret.link")); err != nil {
		t.Fatal(err)
	}
	write(t, ".worktreeinclude", "secret.link\n")

	dst := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(copied) != 1 || copied[0] != "secret.link" {
		t.Fatalf("copied = %v, want only secret.link", copied)
	}
	info, err := os.Lstat(filepath.Join(dst, "secret.link"))
	if err != nil {
		t.Fatalf("secret.link not copied: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("secret.link mode = %v, want symlink", info.Mode())
	}
	target, err := os.Readlink(filepath.Join(dst, "secret.link"))
	if err != nil {
		t.Fatal(err)
	}
	if target != outside {
		t.Fatalf("secret.link target = %q, want %q", target, outside)
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

// TestReflinkCopyFallsBackWhenCpFails proves the fallback wiring: a failing
// (or absent) `cp` must route through plainCopy and still produce a correct
// copy. PATH is overridden only after all fixtures exist; reflinkCopy is the
// sole subprocess spawned afterward.
func TestReflinkCopyFallsBackWhenCpFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("reflink cp path is unix-only; windows uses plainCopy directly")
	}
	buildSrc := func(t *testing.T) string {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("top.txt", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		return src
	}
	assertCopied := func(t *testing.T, dst string) {
		if b, err := os.ReadFile(filepath.Join(dst, "top.txt")); err != nil || string(b) != "top\n" {
			t.Fatalf("top.txt = %q, %v", b, err)
		}
		info, err := os.Lstat(filepath.Join(dst, "link"))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link not preserved as symlink: %v, %v", info, err)
		}
	}

	t.Run("cp exits nonzero", func(t *testing.T) {
		src := buildSrc(t)
		dst := filepath.Join(t.TempDir(), "out")
		shimDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(shimDir, "cp"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", shimDir)
		if err := reflinkCopy(src, dst); err != nil {
			t.Fatalf("reflinkCopy with failing cp: %v", err)
		}
		assertCopied(t, dst)
	})

	t.Run("cp absent from PATH", func(t *testing.T) {
		src := buildSrc(t)
		dst := filepath.Join(t.TempDir(), "out")
		t.Setenv("PATH", t.TempDir())
		if err := reflinkCopy(src, dst); err != nil {
			t.Fatalf("reflinkCopy with no cp on PATH: %v", err)
		}
		assertCopied(t, dst)
	})

	// A cp that dies mid-copy has already created destination content — the
	// strategies must NOT mix: reflinkCopy returns cp's error (the caller's
	// rollback owns the partial tree) instead of merging a plainCopy into it.
	t.Run("cp writes partial content then fails", func(t *testing.T) {
		src := buildSrc(t)
		dst := filepath.Join(t.TempDir(), "out")
		shimDir := t.TempDir()
		shim := "#!/bin/sh\nfor last; do :; done\n/bin/mkdir -p \"$last\"\necho partial > \"$last/partial.txt\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(shimDir, "cp"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", shimDir)
		if err := reflinkCopy(src, dst); err == nil {
			t.Fatal("reflinkCopy succeeded after cp failed on a partial destination")
		}
		if b, err := os.ReadFile(filepath.Join(dst, "partial.txt")); err != nil || string(b) != "partial\n" {
			t.Fatalf("partial.txt = %q, %v; cp's partial content must be left for rollback", b, err)
		}
		if _, err := os.Lstat(filepath.Join(dst, "top.txt")); !os.IsNotExist(err) {
			t.Fatal("top.txt exists — a fallback copy merged into cp's partial output")
		}
	})
}

// TestEmitWorktreeTextOutput covers emitWorktree's text branch: the summary
// line always, the copied: line only when entries were copied.
func TestEmitWorktreeTextOutput(t *testing.T) {
	out := captureStdout(t, func() {
		if err := emitWorktree(false, "feat-a", "/wt/feat-a", []string{"node_modules", ".env"}, "created worktree", false); err != nil {
			t.Fatalf("emitWorktree: %v", err)
		}
	})
	if !strings.Contains(out, "created worktree: feat-a -> /wt/feat-a") {
		t.Fatalf("output missing summary line: %q", out)
	}
	if !strings.Contains(out, "copied: node_modules, .env") {
		t.Fatalf("output missing copied line: %q", out)
	}

	out = captureStdout(t, func() {
		if err := emitWorktree(false, "feat-a", "/wt/feat-a", nil, "worktree already exists", false); err != nil {
			t.Fatalf("emitWorktree: %v", err)
		}
	})
	if !strings.Contains(out, "worktree already exists: feat-a -> /wt/feat-a") {
		t.Fatalf("output missing summary line: %q", out)
	}
	if strings.Contains(out, "copied:") {
		t.Fatalf("copied line present with no copied entries: %q", out)
	}
}

// TestDropNestedEntries pins the pure suppression rule: descendants of a
// selected on-disk directory that WILL BE COPIED (gitignored) are dropped,
// path-boundary compared ("a" must not suppress "ax"), survivor order
// preserved. A tracked (non-ignored) directory never suppresses — see
// TestCopyWorktreeIncludesKeepsIgnoredFileUnderTrackedDir for the pipeline
// half of that rule.
func TestDropNestedEntries(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "c", "ax"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c", "d"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := []string{"a", filepath.Join("a", "b"), "ax", filepath.Join("c", "d"), "c"}
	allIgnored := map[string]bool{}
	for _, e := range entries {
		allIgnored[e] = true
	}
	got := stack.DropNestedIncludeDirs(root, entries, allIgnored)
	want := []string{"a", "ax", "c"}
	if len(got) != len(want) {
		t.Fatalf("dropNestedEntries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dropNestedEntries[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestCopyWorktreeIncludesRefusesDestinationCollision: an ignored source file
// may be TRACKED in the destination branch (or otherwise occupied). The copier
// must refuse before writing anything — a destination file is never
// overwritten or merged, and a later colliding entry prevents even earlier
// candidates from copying.
func TestCopyWorktreeIncludesRefusesDestinationCollision(t *testing.T) {
	// dstWorktree builds a branch that TRACKS the colliding paths inside a new
	// real git worktree, so ls-files reports them there. Writing the tracked
	// content INSIDE the worktree keeps the source worktree untouched: the
	// source's ignored files stay in place (switching the source worktree to a
	// branch that tracks them and back would delete them), and the new
	// worktree holds exactly the branch's tracked files.
	dstWorktree := func(t *testing.T, files map[string]string, deleteFromWorktree ...string) string {
		t.Helper()
		dst := filepath.Join(t.TempDir(), "wt")
		mustRun(t, "git", "worktree", "add", "-q", "-b", "wt-dest", dst, "main")
		for name, content := range files {
			path := filepath.Join(dst, name)
			if dir := filepath.Dir(path); dir != dst {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		// -f: the destination branch deliberately tracks files the source
		// branch ignores — that is the collision under test.
		mustRun(t, "git", append([]string{"-C", dst, "add", "-f"}, names...)...)
		mustRun(t, "git", "-C", dst, "commit", "-q", "-m", "destination branch files")
		for _, name := range deleteFromWorktree {
			// Remove the file from the worktree but keep it tracked — the
			// index entry still protects the path.
			if err := os.Remove(filepath.Join(dst, name)); err != nil {
				t.Fatal(err)
			}
		}
		return dst
	}
	// srcFixture builds the source side: every candidate is gitignored and
	// present on disk, so the destination checks are the only gates left.
	srcFixture := func(t *testing.T) string {
		newRepo(t)
		mustInit(t)
		write(t, ".gitignore", "config.local\nvendor/\nnotes.txt\nbundle/\ngone.txt\n")
		write(t, "config.local", "SECRET=src\n")
		write(t, "notes.txt", "src notes\n")
		if err := os.MkdirAll(filepath.Join("vendor", "lib"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join("vendor", "lib", "dep.go"), "package lib\n")
		if err := os.MkdirAll("bundle", 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join("bundle", "x"), "x\n")
		write(t, "gone.txt", "gone in dst\n")
		mustRun(t, "git", "add", ".gitignore")
		mustRun(t, "git", "commit", "-q", "-m", "ignore rules")
		root, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		return root
	}
	wantRefusal := func(t *testing.T, err error, rel string) {
		t.Helper()
		if err == nil {
			t.Fatalf("copyWorktreeIncludes error = nil, want a collision refusal for %q", rel)
		}
		// The refusal formats rel with %q, which escapes the separator as \\
		// on Windows — needle on the quoted form so the check is literal.
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", rel)) {
			t.Fatalf("error = %v, want it to name %q", err, rel)
		}
	}

	t.Run("ignored source file is tracked in the destination", func(t *testing.T) {
		root := srcFixture(t)
		write(t, ".worktreeinclude", "config.local\n")
		dst := dstWorktree(t, map[string]string{"config.local": "SECRET=dst\n"})
		_, err := copyWorktreeIncludes(root, dst)
		wantRefusal(t, err, "config.local")
		if b, rerr := os.ReadFile(filepath.Join(dst, "config.local")); rerr != nil || string(b) != "SECRET=dst\n" {
			t.Fatalf("destination config.local = %q, %v; must remain untouched", b, rerr)
		}
	})

	t.Run("later collision prevents earlier copies", func(t *testing.T) {
		root := srcFixture(t)
		write(t, ".worktreeinclude", "notes.txt\nconfig.local\n")
		dst := dstWorktree(t, map[string]string{"config.local": "SECRET=dst\n"})
		_, err := copyWorktreeIncludes(root, dst)
		wantRefusal(t, err, "config.local")
		if _, serr := os.Lstat(filepath.Join(dst, "notes.txt")); !os.IsNotExist(serr) {
			t.Fatalf("earlier candidate notes.txt was copied before the later refusal")
		}
	})

	t.Run("selected dir contains a tracked descendant", func(t *testing.T) {
		root := srcFixture(t)
		write(t, ".worktreeinclude", "vendor\n")
		dst := dstWorktree(t, map[string]string{filepath.Join("vendor", "lib", "owned.go"): "package lib\n"})
		_, err := copyWorktreeIncludes(root, dst)
		wantRefusal(t, err, "vendor")
		if _, serr := os.Lstat(filepath.Join(dst, "vendor", "lib", "dep.go")); !os.IsNotExist(serr) {
			t.Fatal("vendor/lib/dep.go merged into a tree with tracked descendants")
		}
	})

	t.Run("tracked file blocks a descendant include", func(t *testing.T) {
		root := srcFixture(t)
		write(t, ".worktreeinclude", filepath.Join("bundle", "x")+"\n")
		dst := dstWorktree(t, map[string]string{"bundle": "dst file\n"})
		_, err := copyWorktreeIncludes(root, dst)
		wantRefusal(t, err, filepath.Join("bundle", "x"))
	})

	t.Run("tracked-but-absent file still refuses", func(t *testing.T) {
		root := srcFixture(t)
		write(t, ".worktreeinclude", "gone.txt\n")
		dst := dstWorktree(t, map[string]string{"gone.txt": "tracked\n"}, "gone.txt")
		_, err := copyWorktreeIncludes(root, dst)
		wantRefusal(t, err, "gone.txt")
		if _, serr := os.Lstat(filepath.Join(dst, "gone.txt")); !os.IsNotExist(serr) {
			t.Fatal("gone.txt was recreated despite its tracked index entry")
		}
	})

	t.Run("existing untracked destination file refuses", func(t *testing.T) {
		root := srcFixture(t)
		write(t, ".worktreeinclude", "notes.txt\n")
		dst := filepath.Join(t.TempDir(), "wt")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "notes.txt"), []byte("dst\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := copyWorktreeIncludes(root, dst)
		wantRefusal(t, err, "notes.txt")
		if b, rerr := os.ReadFile(filepath.Join(dst, "notes.txt")); rerr != nil || string(b) != "dst\n" {
			t.Fatalf("untracked destination file = %q, %v; must remain untouched", b, rerr)
		}
	})

	t.Run("existing untracked destination dir refuses", func(t *testing.T) {
		root := srcFixture(t)
		write(t, ".worktreeinclude", "vendor\n")
		dst := filepath.Join(t.TempDir(), "wt")
		// Even an EMPTY directory must refuse — cp -R would merge into it.
		if err := os.MkdirAll(filepath.Join(dst, "vendor"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := copyWorktreeIncludes(root, dst)
		wantRefusal(t, err, "vendor")
	})
}

// TestCopyWorktreeIncludesKeepsIgnoredFileUnderTrackedDir pins the other half
// of the nesting rule (the #137 regression): a selected TRACKED directory is
// skipped by the copy loop, so it must not suppress its selected gitignored
// descendants — the file the user asked for has to arrive in the worktree.
func TestCopyWorktreeIncludesKeepsIgnoredFileUnderTrackedDir(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{name: "glob", manifest: "config/**\n"},
		{name: "literals", manifest: "config\nconfig/secret.env\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newRepo(t)
			mustInit(t)
			root, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			write(t, ".gitignore", "config/secret.env\n")
			if err := os.MkdirAll("config", 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join("config", "app.conf"), "app\n")
			mustRun(t, "git", "add", ".gitignore", filepath.Join("config", "app.conf"))
			mustRun(t, "git", "commit", "-q", "-m", "tracked config")
			write(t, filepath.Join("config", "secret.env"), "SECRET=1\n")
			write(t, ".worktreeinclude", tc.manifest)

			dst := filepath.Join(t.TempDir(), "wt")
			copied, err := copyWorktreeIncludes(root, dst)
			if err != nil {
				t.Fatalf("copyWorktreeIncludes: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dst, "config", "secret.env"))
			if err != nil {
				t.Fatalf("secret.env missing from the worktree copy (copied=%v): %v", copied, err)
			}
			if string(data) != "SECRET=1\n" {
				t.Fatalf("secret.env content = %q, want the original", data)
			}
			// The tracked file is the worktree-add's job, not the copier's.
			if _, err := os.Stat(filepath.Join(dst, "config", "app.conf")); !os.IsNotExist(err) {
				t.Fatalf("tracked app.conf was copied by the include copier: %v", err)
			}
		})
	}
}

// TestCopyWorktreeIncludesDropsNestedLiteralEntry reproduces the duplicate-
// nesting bug: a directory entry plus a nested subpath of it must copy the
// tree ONCE, not re-copy the inner dir into its already-populated destination
// (which produced nm/pkg/nm/nm/inner.txt via cp's copy-into-existing-dir).
func TestCopyWorktreeIncludesDropsNestedLiteralEntry(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "nm\n")
	if err := os.MkdirAll(filepath.Join("nm", "pkg", "nm"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join("nm", "pkg", "nm", "inner.txt"), "inner\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore")
	write(t, ".worktreeinclude", "nm\nnm/pkg/nm\n")

	dst := filepath.Join(t.TempDir(), "wt")
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(copied) != 1 || copied[0] != "nm" {
		t.Fatalf("copied = %v, want only the ancestor nm", copied)
	}
	if _, err := os.Stat(filepath.Join(dst, "nm", "pkg", "nm", "inner.txt")); err != nil {
		t.Fatalf("inner.txt missing from the single copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "nm", "pkg", "nm", "nm")); !os.IsNotExist(err) {
		t.Fatalf("spurious nested duplicate nm/pkg/nm/nm exists (stat err = %v)", err)
	}
}

// TestCopyWorktreeIncludesDropsGlobNestedDescendant covers the glob trigger:
// packages/**/node_modules matches both an outer node_modules and a nested
// one inside it; only the outer copy must happen.
func TestCopyWorktreeIncludesDropsGlobNestedDescendant(t *testing.T) {
	newRepo(t)
	mustInit(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	write(t, ".gitignore", "node_modules\n")
	inner := filepath.Join("packages", "one", "node_modules", "dep", "node_modules")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(inner, "leaf.txt"), "leaf\n")
	mustRun(t, "git", "add", ".gitignore")
	mustRun(t, "git", "commit", "-q", "-m", "ignore")
	write(t, ".worktreeinclude", "packages/**/node_modules\n")

	dst := filepath.Join(t.TempDir(), "wt")
	copied, err := copyWorktreeIncludes(root, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	outer := filepath.Join("packages", "one", "node_modules")
	if len(copied) != 1 || copied[0] != outer {
		t.Fatalf("copied = %v, want only the outer %s", copied, outer)
	}
	if _, err := os.Stat(filepath.Join(dst, inner, "leaf.txt")); err != nil {
		t.Fatalf("leaf.txt missing from the single copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, inner, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("spurious nested duplicate under %s exists (stat err = %v)", inner, err)
	}
}

// noteWorktreeAdded must leave the cache coherent: a worktree appended after
// a successful `git worktree add` is visible to the very next worktrees() in
// this process — the `st worktree --all` loop depends on it (each add's
// LinkedOwnerOf check consults the list the previous add appended to).
func TestNoteWorktreeAddedKeepsCacheCoherent(t *testing.T) {
	newRepo(t)
	resetProcCaches()

	wts, err := worktrees()
	if err != nil {
		t.Fatalf("worktrees: %v", err)
	}
	before := len(wts)

	noteWorktreeAdded("/wt/feat-x", "feat-x")

	again, err := worktrees()
	if err != nil {
		t.Fatalf("worktrees after append: %v", err)
	}
	if len(again) != before+1 {
		t.Fatalf("worktrees() = %d entries, want %d (the appended one)", len(again), before+1)
	}
	owner, ok := stack.LinkedOwnerOf(again, "feat-x")
	if !ok || owner.Path != "/wt/feat-x" {
		t.Fatalf("LinkedOwnerOf(feat-x) = %+v, %v — appended entry not visible", owner, ok)
	}

	// And a still-unprobed cache defers to git instead of lying about the
	// appended path: reset → the phantom entry is gone.
	resetProcCaches()
	fresh, err := worktrees()
	if err != nil {
		t.Fatalf("worktrees after reset: %v", err)
	}
	if _, ok := stack.LinkedOwnerOf(fresh, "feat-x"); ok {
		t.Fatal("reset cache still reports the appended phantom worktree")
	}
}

// TestWorktreePausedRebaseGates pins the plan-002 contract against real Git:
// a tracked branch paused mid-rebase in a LINKED worktree reports `detached`
// to `git worktree list`, so the rebase head-name — not the branch owner —
// carries the truth. The mutation gate, `worktree rm`, `rm --all`, and undo
// must all refuse before git would fail mid-operation.
func TestWorktreePausedRebaseGates(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Diverge main FIRST so the journal records the diverged tip — an
	// out-of-band main move after snapshotting is exactly the plan-001 drift
	// check, which would fire ahead of the pause refusal under test.
	mustCheckout(t, "main")
	write(t, "a.txt", "diverged\n")
	mustRun(t, "git", "add", "a.txt")
	mustRun(t, "git", "commit", "-q", "-m", "diverge main")
	mustCheckout(t, "feat-a")

	// A modify entry that rewrites feat-a on undo (touches a2.txt only so
	// the main-side a.txt edit conflicts on rebase).
	write(t, "a2.txt", "B\n")
	if err := runModify([]string{"-a"}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mustCheckout(t, "main")
	resetProcCaches()

	wt, err := materializeWorktree("feat-a")
	if err != nil {
		t.Fatalf("materializeWorktree: %v", err)
	}
	if err := exec.Command("git", "-C", wt.Path, "rebase", "main").Run(); err == nil {
		t.Fatal("git -C wt rebase main succeeded — fixture did not pause mid-rebase")
	}
	// Real Git reports the paused worktree as detached; drop the warmed list
	// so lookups see the same porcelain truth a fresh process would.
	resetProcCaches()

	// Mutation gate: a tracked-branch mutation refuses upfront, naming the
	// branch and its worktree, before git could fail mid-operation.
	delErr := runDelete([]string{"feat-a"})
	if delErr == nil || !strings.Contains(delErr.Error(), "rebase in progress") ||
		!strings.Contains(delErr.Error(), "feat-a") || !strings.Contains(delErr.Error(), wt.Path) {
		t.Fatalf("runDelete on paused branch = %v, want the worktree pause refusal", delErr)
	}
	if !strings.Contains(delErr.Error(), "st continue") || !strings.Contains(delErr.Error(), "st abort") {
		t.Fatalf("pause refusal lost the continue/abort pointer: %v", delErr)
	}
	// Nothing half-deleted: branch, tracking, and worktree all survive.
	if err := exec.Command("git", "rev-parse", "--verify", "-q", "refs/heads/feat-a").Run(); err != nil {
		t.Fatal("refused delete removed the paused branch anyway")
	}
	if _, statErr := os.Stat(wt.Path); statErr != nil {
		t.Fatalf("refused delete removed %q: %v", wt.Path, statErr)
	}
	s, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if !s.IsTracked("feat-a") {
		t.Fatal("refused delete untracked the paused branch")
	}

	// worktree rm: the branch's worktree is found through the paused
	// head-name (porcelain reports it detached) and refused.
	rmErr := worktreeRemove("feat-a", false)
	if rmErr == nil || !strings.Contains(rmErr.Error(), "rebase is in progress there") {
		t.Fatalf("worktreeRemove on paused branch = %v, want the pause refusal", rmErr)
	}

	// rm --all: the paused worktree is skipped with a reason, not removed.
	out := captureStdout(t, func() {
		if err := worktreeRemoveAll(false); err != nil {
			t.Fatalf("worktreeRemoveAll: %v", err)
		}
	})
	if !strings.Contains(out, "skipped feat-a") || !strings.Contains(out, "rebase is in progress there") {
		t.Fatalf("rm --all output = %q, want a paused-rebase skip for feat-a", out)
	}
	if _, statErr := os.Stat(wt.Path); statErr != nil {
		t.Fatalf("rm --all removed the paused worktree %q: %v", wt.Path, statErr)
	}

	// Undo: the modify journal entry rewrites feat-a — a pause there blocks.
	undoErr := runUndoApply(false, 1, false)
	if undoErr == nil || !strings.Contains(undoErr.Error(), "cannot undo") ||
		!strings.Contains(undoErr.Error(), "rebase in progress") || !strings.Contains(undoErr.Error(), "feat-a") {
		t.Fatalf("runUndoApply with paused journal branch = %v, want the pause refusal", undoErr)
	}
	// --force bypasses the pause (the caller accepts the risk).
	if err := runUndoApply(false, 1, true); err != nil {
		t.Fatalf("runUndoApply --force blocked by a pause: %v", err)
	}
}

// TestWorktreeAddAllSkipsPausedRebase pins the plan-038 fix: a branch paused
// mid-rebase in a detached worktree is skipped by `worktree --all` — the same
// reason rm --all emits — instead of dying on git's own refusal mid-loop.
func TestWorktreeAddAllSkipsPausedRebase(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	// Pause feat-a in its own linked worktree: diverge main so the rebase
	// conflicts, then start it inside the worktree — porcelain reports the
	// worktree detached and head-name carries the branch.
	mustCheckout(t, "main")
	write(t, "a.txt", "diverged\n")
	mustRun(t, "git", "add", "a.txt")
	mustRun(t, "git", "commit", "-q", "-m", "diverge main")
	resetProcCaches()

	wt, err := materializeWorktree("feat-a")
	if err != nil {
		t.Fatalf("materializeWorktree: %v", err)
	}
	if err := exec.Command("git", "-C", wt.Path, "rebase", "main").Run(); err == nil {
		t.Fatal("git -C wt rebase main succeeded — fixture did not pause mid-rebase")
	}
	resetProcCaches()

	out := captureStdout(t, func() {
		if err := runWorktree([]string{"--all", "--json"}); err != nil {
			t.Fatalf("worktree --all: %v", err)
		}
	})
	var got struct {
		Created []struct {
			Branch  string `json:"branch"`
			Path    string `json:"path"`
			Summary string `json:"summary"`
		} `json:"created"`
		Skipped []struct {
			Branch string `json:"branch"`
			Reason string `json:"reason"`
		} `json:"skipped"`
		Failed *struct {
			Branch string `json:"branch"`
			Error  string `json:"error"`
		} `json:"failed"`
	}
	decodeStrictJSON(t, "worktree --all paused", out, &got)
	if got.Failed != nil {
		t.Fatalf("paused branch landed in failed: %+v", got.Failed)
	}
	if len(got.Created) != 1 || got.Created[0].Branch != "feat-b" {
		t.Fatalf("created = %+v, want only feat-b", got.Created)
	}
	var pausedSkip bool
	for _, sk := range got.Skipped {
		if sk.Branch == "feat-a" && sk.Reason == "a rebase is in progress there" {
			pausedSkip = true
		}
	}
	if !pausedSkip {
		t.Fatalf("skipped = %+v, want feat-a with the rebase-in-progress reason", got.Skipped)
	}

	// The single-branch form refuses the same branch up front, naming its
	// paused worktree, instead of leaking git's refusal.
	err = runWorktree([]string{"feat-a", "--json"})
	if err == nil || !strings.Contains(err.Error(), "rebase is in progress") || !strings.Contains(err.Error(), wt.Path) {
		t.Fatalf("worktree feat-a on paused branch = %v, want the pause refusal", err)
	}
}

// TestWorktreeAddDryRun pins the preview contract: --dry-run reports the path
// and the .worktreeinclude set a real add would produce, marks the payload
// dryRun, and creates nothing on disk.
func TestWorktreeAddDryRun(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")

	// A gitignored, manifest-listed file the copy would carry: .env exists in
	// the source, is ignored, and is not tracked in feat-a's tree.
	write(t, ".gitignore", ".env\n")
	write(t, ".env", "SECRET=1\n")
	write(t, ".worktreeinclude", ".env\n")
	mustRun(t, "git", "add", ".gitignore", ".worktreeinclude")
	mustRun(t, "git", "commit", "-q", "-m", "manifest")
	resetProcCaches()

	// Single-branch preview.
	out := captureStdout(t, func() {
		if err := runWorktree([]string{"feat-a", "--dry-run", "--json"}); err != nil {
			t.Fatalf("worktree feat-a --dry-run: %v", err)
		}
	})
	var one struct {
		Branch  string   `json:"branch"`
		Path    string   `json:"path"`
		Copied  []string `json:"copied"`
		Summary string   `json:"summary"`
		DryRun  bool     `json:"dryRun"`
	}
	decodeStrictJSON(t, "worktree feat-a --dry-run", out, &one)
	if !one.DryRun || one.Branch != "feat-a" || one.Path == "" ||
		one.Summary != "would create worktree" {
		t.Fatalf("dry-run payload = %+v, want the predicted create", one)
	}
	if len(one.Copied) != 1 || one.Copied[0] != ".env" {
		t.Fatalf("predicted copied = %v, want [.env]", one.Copied)
	}
	if _, statErr := os.Stat(one.Path); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run created %q anyway (stat: %v)", one.Path, statErr)
	}

	// Bulk preview: same predictions, main-worktree skip, dryRun on the
	// aggregate.
	out = captureStdout(t, func() {
		if err := runWorktree([]string{"--all", "--dry-run", "--json"}); err != nil {
			t.Fatalf("worktree --all --dry-run: %v", err)
		}
	})
	var all struct {
		DryRun  bool `json:"dryRun"`
		Created []struct {
			Branch  string   `json:"branch"`
			Path    string   `json:"path"`
			Copied  []string `json:"copied"`
			Summary string   `json:"summary"`
		} `json:"created"`
		Skipped []struct {
			Branch string `json:"branch"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	decodeStrictJSON(t, "worktree --all --dry-run", out, &all)
	if !all.DryRun {
		t.Fatalf("bulk dry-run payload missing dryRun: %s", out)
	}
	if len(all.Created) != 1 || all.Created[0].Branch != "feat-a" ||
		all.Created[0].Summary != "would create worktree" {
		t.Fatalf("dry-run created = %+v, want the feat-a prediction", all.Created)
	}
	// main is the trunk — untracked, so absent from the row set entirely.
	if len(all.Skipped) != 0 {
		t.Fatalf("dry-run skipped = %+v, want none (trunk is untracked)", all.Skipped)
	}
	if _, statErr := os.Stat(all.Created[0].Path); !os.IsNotExist(statErr) {
		t.Fatalf("bulk dry-run created %q anyway (stat: %v)", all.Created[0].Path, statErr)
	}

	// A branch that already owns a worktree previews the idempotent outcome.
	if err := runWorktree([]string{"feat-a"}); err != nil {
		t.Fatalf("real worktree feat-a: %v", err)
	}
	out = captureStdout(t, func() {
		if err := runWorktree([]string{"feat-a", "--dry-run", "--json"}); err != nil {
			t.Fatalf("worktree feat-a --dry-run (existing): %v", err)
		}
	})
	var again struct {
		Branch  string `json:"branch"`
		Path    string `json:"path"`
		Summary string `json:"summary"`
		DryRun  bool   `json:"dryRun"`
	}
	decodeStrictJSON(t, "worktree feat-a --dry-run again", out, &again)
	if again.Summary != "worktree already exists" || !again.DryRun {
		t.Fatalf("existing-branch dry-run = %+v, want already-exists", again)
	}
}

// TestWorktreeDryRunRejectsNonAddForms pins the flag's scope: --dry-run
// previews an add; the read-only and remove forms reject it rather than
// silently implying a preview they do not implement.
func TestWorktreeDryRunRejectsNonAddForms(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	for _, args := range [][]string{
		{"ls", "--dry-run"},
		{"rm", "feat-a", "--dry-run"},
		{"rm", "--all", "--dry-run"},
	} {
		if err := runWorktree(args); err == nil || !strings.Contains(err.Error(), "--dry-run") {
			t.Fatalf("runWorktree(%v) = %v, want a --dry-run rejection", args, err)
		}
	}
}

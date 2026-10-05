package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestFastForward drives the three fast-forward outcomes against a real remote
// and asserts each by the trunk's SHA, never by parsing git's merge output
// (which is localized).
func TestFastForward(t *testing.T) {
	setup := func(t *testing.T) (base string) {
		t.Helper()
		newRepo(t)
		base = mustGit(t, "rev-parse", "HEAD")
		bare := t.TempDir()
		mustGit(t, "init", "-q", "--bare", bare)
		mustGit(t, "remote", "add", "origin", bare)
		if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
			t.Fatalf("initial Push: %v", err)
		}
		return base
	}
	advanceMain := func(t *testing.T, name string) string {
		t.Helper()
		writeFile(t, name+".txt", name+"\n")
		mustGit(t, "add", "-A")
		mustGit(t, "commit", "-q", "-m", name)
		return mustGit(t, "rev-parse", "HEAD")
	}

	t.Run("already up to date", func(t *testing.T) {
		setup(t)
		// The local trunk is ahead of the remote: nothing to advance.
		local := advanceMain(t, "local")
		desc, err := (RemoteShell{}).FastForward("main", "origin", "", true)
		if err != nil {
			t.Fatalf("FastForward: %v", err)
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != local {
			t.Fatalf("FastForward moved main to %q, want unchanged %q", got, local)
		}
		if desc != "main already up to date" {
			t.Fatalf("FastForward description = %q, want already up to date", desc)
		}
	})

	t.Run("fast-forwards to the upstream tip", func(t *testing.T) {
		base := setup(t)
		remoteTip := advanceMain(t, "remote")
		if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
			t.Fatalf("Push: %v", err)
		}
		mustGit(t, "reset", "--hard", base)
		if err := Fetch("origin"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if _, err := (RemoteShell{}).FastForward("main", "origin", "", true); err != nil {
			t.Fatalf("FastForward: %v", err)
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != remoteTip {
			t.Fatalf("FastForward moved main to %q, want upstream tip %q", got, remoteTip)
		}
	})

	t.Run("diverged trunk returns an error", func(t *testing.T) {
		base := setup(t)
		advanceMain(t, "remote")
		if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
			t.Fatalf("Push: %v", err)
		}
		mustGit(t, "reset", "--hard", base)
		local := advanceMain(t, "local-divergence")
		if err := Fetch("origin"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if _, err := (RemoteShell{}).FastForward("main", "origin", "", true); err == nil {
			t.Fatal("FastForward on a diverged trunk should error")
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != local {
			t.Fatalf("failed FastForward moved main to %q, want unchanged %q", got, local)
		}
	})

	// prepareBehindRemote advances the remote, resets local main to base, and
	// fetches, leaving refs/heads/main strictly behind origin/main.
	prepareBehindRemote := func(t *testing.T, base string) (remoteTip string) {
		t.Helper()
		remoteTip = advanceMain(t, "remote")
		if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
			t.Fatalf("Push: %v", err)
		}
		mustGit(t, "reset", "--hard", base)
		if err := Fetch("origin"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		return remoteTip
	}

	t.Run("advances the trunk inside its owning worktree", func(t *testing.T) {
		base := setup(t)
		remoteTip := prepareBehindRemote(t, base)
		// Move the trunk into a linked worktree; the cwd stays on a feature branch.
		mustGit(t, "checkout", "-q", "-b", "feature")
		wt := filepath.Join(t.TempDir(), "trunk-wt")
		mustGit(t, "worktree", "add", "-q", wt, "main")
		desc, err := (RemoteShell{}).FastForward("main", "origin", wt, false)
		if err != nil {
			t.Fatalf("FastForward in owner worktree: %v", err)
		}
		if desc != "main fast-forwarded to refs/remotes/origin/main" {
			t.Fatalf("description = %q", desc)
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != remoteTip {
			t.Fatalf("main = %q, want upstream tip %q", got, remoteTip)
		}
		// The owner's working tree advanced too (the remote commit's file exists).
		if _, err := os.Stat(filepath.Join(wt, "remote.txt")); err != nil {
			t.Fatalf("owner worktree file not materialized: %v", err)
		}
	})

	t.Run("refuses a dirty owning worktree", func(t *testing.T) {
		base := setup(t)
		prepareBehindRemote(t, base)
		local := mustGit(t, "rev-parse", "refs/heads/main")
		mustGit(t, "checkout", "-q", "-b", "feature")
		wt := filepath.Join(t.TempDir(), "trunk-wt")
		mustGit(t, "worktree", "add", "-q", wt, "main")
		writeFile(t, filepath.Join(wt, "dirty.txt"), "dirty\n")
		_, err := (RemoteShell{}).FastForward("main", "origin", wt, false)
		if err == nil || !strings.Contains(err.Error(), wt) {
			t.Fatalf("FastForward with dirty owner = %v, want error naming %q", err, wt)
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != local {
			t.Fatalf("dirty-owner FastForward moved main to %q, want unchanged %q", got, local)
		}
	})

	t.Run("moves the ref only when the trunk is checked out nowhere", func(t *testing.T) {
		base := setup(t)
		remoteTip := prepareBehindRemote(t, base)
		mustGit(t, "checkout", "-q", "-b", "feature")
		head := mustGit(t, "rev-parse", "HEAD")
		desc, err := (RemoteShell{}).FastForward("main", "origin", "", false)
		if err != nil {
			t.Fatalf("ref-only FastForward: %v", err)
		}
		if desc != "main fast-forwarded to refs/remotes/origin/main" {
			t.Fatalf("description = %q", desc)
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != remoteTip {
			t.Fatalf("main = %q, want upstream tip %q", got, remoteTip)
		}
		if got := mustGit(t, "rev-parse", "HEAD"); got != head {
			t.Fatalf("ref-only FastForward moved HEAD to %q", got)
		}
	})

	t.Run("never force-moves a diverged unchecked-out trunk", func(t *testing.T) {
		base := setup(t)
		advanceMain(t, "remote")
		if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
			t.Fatalf("Push: %v", err)
		}
		mustGit(t, "reset", "--hard", base)
		local := advanceMain(t, "local-divergence")
		if err := Fetch("origin"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		mustGit(t, "checkout", "-q", "-b", "feature")
		if _, err := (RemoteShell{}).FastForward("main", "origin", "", false); err == nil {
			t.Fatal("ref-only FastForward on a diverged trunk should error")
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != local {
			t.Fatalf("diverged ref-only FastForward moved main to %q, want unchanged %q", got, local)
		}
	})
}

// TestMergeFFOnlyIn proves the -C variant fast-forwards the branch checked out
// in another worktree, including its working tree.
func TestMergeFFOnlyIn(t *testing.T) {
	newRepo(t)
	writeFile(t, "extra.txt", "extra\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "extra")
	tip := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "branch", "-q", "behind", "HEAD~1")

	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, "worktree", "add", "-q", wt, "behind")
	if err := MergeFFOnlyIn(wt, "main"); err != nil {
		t.Fatalf("MergeFFOnlyIn: %v", err)
	}
	if got := mustGit(t, "rev-parse", "refs/heads/behind"); got != tip {
		t.Fatalf("behind = %q, want fast-forwarded to %q", got, tip)
	}
	if _, err := os.Stat(filepath.Join(wt, "extra.txt")); err != nil {
		t.Fatalf("worktree file not materialized: %v", err)
	}
	if err := MergeFFOnlyIn("", "main"); err == nil {
		t.Fatal("MergeFFOnlyIn with empty dir should error")
	}
}

// newRepo creates a temp git repo with one commit on main and chdirs into it.
func newRepo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	mustGit(t, "init", "-q")
	mustGit(t, "symbolic-ref", "HEAD", "refs/heads/main")
	mustGit(t, "config", "user.email", "test@example.com")
	mustGit(t, "config", "user.name", "test")
	writeFile(t, "base.txt", "base\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "init")
}

func mustGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resolveSymlinks canonicalizes a path so comparisons are stable on platforms
// (macOS) where temp dirs live under a symlinked prefix (/var -> /private/var).
func resolveSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return resolved
}

func TestCurrentBranchAndExists(t *testing.T) {
	newRepo(t)
	got, err := CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if got != "main" {
		t.Fatalf("CurrentBranch = %q, want main", got)
	}
	if !BranchExists("main") {
		t.Fatalf("BranchExists(main) = false")
	}
	if BranchExists("nope") {
		t.Fatalf("BranchExists(nope) = true")
	}
}

func TestDetachedHEAD(t *testing.T) {
	newRepo(t)
	sha := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "checkout", "-q", sha) // detach
	if _, err := CurrentBranch(); err != ErrDetachedHEAD {
		t.Fatalf("CurrentBranch on detached HEAD = %v, want ErrDetachedHEAD", err)
	}
}

func TestBranchLifecycle(t *testing.T) {
	newRepo(t)
	if err := CreateBranch("feat"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if !BranchExists("feat") {
		t.Fatalf("feat should exist after CreateBranch")
	}
	if err := RenameBranch("feat", "feat2"); err != nil {
		t.Fatalf("RenameBranch: %v", err)
	}
	if BranchExists("feat") || !BranchExists("feat2") {
		t.Fatalf("rename did not take effect")
	}
	// Force a second branch at a different ref, then delete it.
	writeFile(t, "x.txt", "x\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "x")
	sha := mustGit(t, "rev-parse", "HEAD")
	if err := ForceBranch("marker", sha); err != nil {
		t.Fatalf("ForceBranch: %v", err)
	}
	if got, _ := RevParse("marker"); got != sha {
		t.Fatalf("ForceBranch left marker at %q, want %s", got, sha)
	}
	if err := DeleteBranch("marker", true); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if BranchExists("marker") {
		t.Fatalf("marker still exists after delete")
	}
}

func TestLocalBranchRef(t *testing.T) {
	newRepo(t)
	if err := CreateBranch("feat"); err != nil {
		t.Fatalf("CreateBranch feat: %v", err)
	}
	if err := CreateBranch("feat2"); err != nil {
		t.Fatalf("CreateBranch feat2: %v", err)
	}

	// Existing branches are qualified so rev-list/merge-base/… resolve them
	// unambiguously — a tag or SHA of the same name can't shadow the branch.
	if got := localBranchRef("feat"); got != "refs/heads/feat" {
		t.Fatalf("localBranchRef(feat) = %q, want refs/heads/feat", got)
	}
	if got := localBranchRef("feat2"); got != "refs/heads/feat2" {
		t.Fatalf("localBranchRef(feat2) = %q, want refs/heads/feat2", got)
	}
	// Missing names — including prefix-siblings of real branches — pass
	// through unqualified: the lookup is an exact `show-ref --verify`, never
	// a listing scan. (refs/heads/feat/sub is the nearest over-match shape,
	// but git's D/F constraint forbids it while refs/heads/feat exists, so a
	// sibling is the closest constructible case.)
	if got := localBranchRef("fe"); got != "fe" {
		t.Fatalf("localBranchRef(fe) = %q, want fe unchanged", got)
	}
	if got := localBranchRef("feat/missing"); got != "feat/missing" {
		t.Fatalf("localBranchRef(feat/missing) = %q, want unchanged", got)
	}
	// HEAD, already-qualified refs, and raw SHAs pass through as-is.
	if got := localBranchRef("HEAD"); got != "HEAD" {
		t.Fatalf("localBranchRef(HEAD) = %q, want HEAD", got)
	}
	if got := localBranchRef("refs/heads/feat"); got != "refs/heads/feat" {
		t.Fatalf("localBranchRef(refs/heads/feat) = %q, want unchanged", got)
	}
	sha := mustGit(t, "rev-parse", "HEAD")
	if got := localBranchRef(sha); got != sha {
		t.Fatalf("localBranchRef(%s) = %q, want the SHA unchanged", sha, got)
	}
}

func TestFlagLikeRefNamesRejected(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "checkout",
			run:  func() error { return Checkout("-x") },
		},
		{
			name: "delete branch",
			run:  func() error { return DeleteBranch("--exec=true", true) },
		},
		{
			name: "rebase onto",
			run:  func() error { return RebaseOnto("HEAD", "HEAD", "--exec=true") },
		},
		{
			name: "rebase new base",
			run:  func() error { return RebaseOnto("--root", "HEAD", "main") },
		},
		{
			name: "rebase old base",
			run:  func() error { return RebaseOnto("HEAD", "--root", "main") },
		},
		{
			name: "quiet rebase old base",
			run:  func() error { return RebaseOntoQuiet("HEAD", "--root", "main") },
		},
		{
			name: "fetch",
			run:  func() error { return Fetch("--upload-pack=true") },
		},
		{
			name: "push branches remote",
			run:  func() error { _, err := PushBranches("--receive-pack=true", []string{"main"}, false); return err },
		},
		{
			name: "push branches branch",
			run:  func() error { _, err := PushBranches("origin", []string{"--force"}, false); return err },
		},
		{
			name: "force branch",
			run:  func() error { return ForceBranch("-b", "HEAD") },
		},
		{
			name: "force branch ref",
			run:  func() error { return ForceBranch("topic", "--force") },
		},
		{
			name: "reset soft ref",
			run:  func() error { return ResetSoft("--hard") },
		},
		{
			name: "remote url",
			run:  func() error { _, err := RemoteURL("--upload-pack=true"); return err },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "not a valid git ref name") {
				t.Fatalf("error = %q, want invalid ref name", err)
			}
		})
	}

	// RemoteExists is a predicate rather than an erroring call, so a flag-like
	// remote name is simply "not configured" — and never reaches exec where git
	// could parse it as an option.
	if RemoteExists("--upload-pack=true") {
		t.Error("RemoteExists(--upload-pack=true) = true, want false")
	}
}

func TestCleanStagedAdd(t *testing.T) {
	newRepo(t)
	clean, err := IsClean()
	if err != nil || !clean {
		t.Fatalf("fresh repo should be clean: clean=%v err=%v", clean, err)
	}
	writeFile(t, "n.txt", "n\n")
	if clean, _ := IsClean(); clean {
		t.Fatalf("untracked file should make the tree not clean")
	}
	if err := Add("n.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	staged, err := HasStagedChanges()
	if err != nil || !staged {
		t.Fatalf("expected staged changes: staged=%v err=%v", staged, err)
	}
	if err := Commit("n", false); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if staged, _ := HasStagedChanges(); staged {
		t.Fatalf("no staged changes expected after commit")
	}
}

func TestAddAllAndAmend(t *testing.T) {
	newRepo(t)
	writeFile(t, "a.txt", "a\n")
	writeFile(t, "b.txt", "b\n")
	if err := Add(); err != nil { // add -A
		t.Fatalf("Add all: %v", err)
	}
	if err := Commit("two files", false); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	writeFile(t, "a.txt", "a2\n")
	if err := AmendNoEdit(true); err != nil {
		t.Fatalf("AmendNoEdit: %v", err)
	}
}

func TestMergeBaseAndAncestor(t *testing.T) {
	newRepo(t)
	base := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "f.txt", "f\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "f")
	mustGit(t, "tag", "main", "HEAD")

	mb, err := MergeBase("main", "feat")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if mb != base {
		t.Fatalf("MergeBase = %q, want %s", mb, base)
	}
	if mb, err := MergeBase(base, "refs/heads/feat"); err != nil || mb != base {
		t.Fatalf("MergeBase with generic refs = %q, %v; want %s, nil", mb, err, base)
	}
	ok, err := IsAncestor("main", "feat")
	if err != nil || !ok {
		t.Fatalf("IsAncestor(main, feat) = %v, %v; want true, nil", ok, err)
	}
	ok, err = IsAncestor(base, "refs/heads/feat")
	if err != nil || !ok {
		t.Fatalf("IsAncestor(base, refs/heads/feat) = %v, %v; want true, nil", ok, err)
	}
	ok, err = IsAncestor("feat", "main")
	if err != nil || ok {
		t.Fatalf("IsAncestor(feat, main) = %v, %v; want false, nil", ok, err)
	}
	if _, err := IsAncestor("definitely-not-a-ref", "main"); err == nil {
		t.Fatalf("IsAncestor with an invalid ref returned nil error")
	}
}

func TestResetSoftAndUpdateRef(t *testing.T) {
	newRepo(t)
	first := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "c.txt", "c\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "c")

	if err := ResetSoft(first); err != nil {
		t.Fatalf("ResetSoft: %v", err)
	}
	if got, _ := RevParse("HEAD"); got != first {
		t.Fatalf("ResetSoft left HEAD at %q, want %s", got, first)
	}

	if err := UpdateRef("refs/heads/tagref", first); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	if got, _ := RevParse("tagref"); got != first {
		t.Fatalf("UpdateRef set tagref to %q, want %s", got, first)
	}
}

// UpdateRefsCas is the undo restore's compare-and-swap batch: each update
// carries the tip the ref is expected to sit at, and one mismatch fails the
// whole transaction so no ref moves.
func TestUpdateRefsCas(t *testing.T) {
	newRepo(t)
	first := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "c.txt", "c\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "c")
	second := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "branch", "other", first)
	mustGit(t, "branch", "side", second)

	const zero = "0000000000000000000000000000000000000000"

	t.Run("matching old applies the batch", func(t *testing.T) {
		err := UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/main":  {New: first, Old: second},
			"refs/heads/other": {New: second, Old: first},
		})
		if err != nil {
			t.Fatalf("UpdateRefsCas: %v", err)
		}
		if got, _ := RevParse("main"); got != first {
			t.Fatalf("main = %s, want %s", got, first)
		}
		if got, _ := RevParse("other"); got != second {
			t.Fatalf("other = %s, want %s", got, second)
		}
	})

	t.Run("wrong old fails atomically", func(t *testing.T) {
		err := UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/main": {New: second, Old: second}, // main sits at first
			"refs/heads/side": {New: first, Old: second},  // would match
		})
		if err == nil {
			t.Fatal("UpdateRefsCas with a mismatched old succeeded")
		}
		if got, _ := RevParse("side"); got != second {
			t.Fatalf("side = %s, want unchanged %s (batch must be atomic)", got, second)
		}
		if got, _ := RevParse("main"); got != first {
			t.Fatalf("main = %s, want unchanged %s", got, first)
		}
	})

	t.Run("zero old requires the ref absent", func(t *testing.T) {
		err := UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/main": {New: second, Old: zero},
		})
		if err == nil {
			t.Fatal("zero-old resurrect on an existing ref succeeded")
		}
		err = UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/gone": {New: first, Old: zero},
		})
		if err != nil {
			t.Fatalf("zero-old create on an absent ref: %v", err)
		}
		if got, _ := RevParse("gone"); got != first {
			t.Fatalf("gone = %s, want %s", got, first)
		}
	})

	t.Run("empty old is unverified", func(t *testing.T) {
		err := UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/main": {New: second, Old: ""},
		})
		if err != nil {
			t.Fatalf("empty-old update: %v", err)
		}
		if got, _ := RevParse("main"); got != second {
			t.Fatalf("main = %s, want %s", got, second)
		}
	})

	t.Run("non-oid values never reach git", func(t *testing.T) {
		if err := UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/main": {New: "HEAD~1", Old: second},
		}); err == nil {
			t.Fatal("revision expression as new value accepted")
		}
		if err := UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/main": {New: first, Old: "HEAD~1"},
		}); err == nil {
			t.Fatal("revision expression as old value accepted")
		}
		if err := UpdateRefsCas(map[string]RefUpdate{
			"refs/heads/main": {New: zero, Old: second},
		}); err == nil {
			t.Fatal("all-zeros delete value accepted")
		}
	})
}

// TestShellRefUpdateWrappers exercises the Shell passthroughs for the
// ref-update helpers — the undo path moved to UpdateRefsCas, so the
// unconditional single/batch wrappers need their own coverage anchor.
func TestShellRefUpdateWrappers(t *testing.T) {
	newRepo(t)
	first := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "c.txt", "c\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "c")
	second := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "branch", "other", first)

	sh := Shell{}
	if err := sh.UpdateRef("refs/heads/main", first); err != nil {
		t.Fatalf("Shell.UpdateRef: %v", err)
	}
	if got, _ := RevParse("main"); got != first {
		t.Fatalf("main = %s, want %s", got, first)
	}
	if err := sh.UpdateRefs(map[string]string{
		"refs/heads/main":  second,
		"refs/heads/other": second,
	}); err != nil {
		t.Fatalf("Shell.UpdateRefs: %v", err)
	}
	if got, _ := RevParse("main"); got != second {
		t.Fatalf("main = %s, want %s", got, second)
	}
	if got, _ := RevParse("other"); got != second {
		t.Fatalf("other = %s, want %s", got, second)
	}
}

// A ref beginning with "-" (e.g. a corrupt or hostile state.json branch name)
// must be rejected at the boundary so git never parses it as an option.
func TestRevParseRejectsFlagLikeRef(t *testing.T) {
	newRepo(t)
	if _, err := RevParse("--git-dir"); err == nil {
		t.Fatal("RevParse accepted a flag-like ref")
	}
}

// TestRevParsePrefersBranchOverTag pins the port-level qualification: a tag
// named like the trunk shadows the bare name in gitrevisions order
// (refs/tags/ precedes refs/heads/), so RevParse must resolve the BRANCH.
func TestRevParsePrefersBranchOverTag(t *testing.T) {
	newRepo(t)
	mustGit(t, "commit", "--allow-empty", "-m", "move-main")
	mustGit(t, "tag", "main", "HEAD~1") // tag main at the OLDER commit
	branchTip := mustGit(t, "rev-parse", "refs/heads/main")
	tagTip := mustGit(t, "rev-parse", "refs/tags/main")
	if branchTip == tagTip {
		t.Fatal("test setup: tag and branch should differ")
	}
	got, err := RevParse("main")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	if got != branchTip {
		t.Fatalf("RevParse(main) = %s, want branch tip %s (not tag %s)", got, branchTip, tagTip)
	}
}

func TestUpdateRefRejectsFlagLikeRef(t *testing.T) {
	newRepo(t)
	first := mustGit(t, "rev-parse", "HEAD")
	if err := UpdateRef("--foo", first); err == nil {
		t.Fatal("UpdateRef accepted a flag-like ref")
	}
}

func TestCommitSubjectsRejectsFlagLikeRefs(t *testing.T) {
	newRepo(t)
	if _, err := CommitSubjects("-x", "main"); err == nil {
		t.Fatal("CommitSubjects accepted a flag-like base ref")
	}
	if _, err := CommitSubjects("abc123", "-x"); err == nil {
		t.Fatal("CommitSubjects accepted a flag-like branch")
	}
}

func TestTipSubjectsForScopesToRequestedBranches(t *testing.T) {
	newRepo(t)
	mainSHA := mustGit(t, "rev-parse", "refs/heads/main")
	mustGit(t, "checkout", "-q", "-b", "unrelated")
	writeFile(t, "u.txt", "u\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "unrelated subject")
	unrelatedSHA := mustGit(t, "rev-parse", "refs/heads/unrelated")
	mustGit(t, "tag", "main", unrelatedSHA) // decoy tag sharing the branch name

	subjects, err := TipSubjectsFor([]string{"main", "main", "missing"})
	if err != nil {
		t.Fatalf("TipSubjectsFor: %v", err)
	}
	if len(subjects) != 1 {
		t.Fatalf("TipSubjectsFor = %v, want only main", subjects)
	}
	if subjects["main"] != "init" {
		t.Fatalf("TipSubjectsFor[main] = %q, want init from branch %s", subjects["main"], mainSHA)
	}
	if _, ok := subjects["unrelated"]; ok {
		t.Fatalf("TipSubjectsFor included unrelated branch: %v", subjects)
	}
}

func TestTipSubjectsForDoesNotTreatPrefixAsBranch(t *testing.T) {
	newRepo(t)
	mustGit(t, "branch", "foo/bar")

	subjects, err := TipSubjectsFor([]string{"foo"})
	if err != nil {
		t.Fatalf("TipSubjectsFor: %v", err)
	}
	if len(subjects) != 0 {
		t.Fatalf("TipSubjectsFor(foo) = %v, want missing despite foo/bar", subjects)
	}
}

func TestCommitSubjects(t *testing.T) {
	newRepo(t)
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "f.txt", "f\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "first subject")
	writeFile(t, "g.txt", "g\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "second subject")
	mustGit(t, "tag", "feat", "main")

	subs, err := CommitSubjects("main", "feat")
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	if len(subs) != 2 || subs[0] != "second subject" || subs[1] != "first subject" {
		t.Fatalf("CommitSubjects = %v, want [second, first]", subs)
	}
	if subs, err := CommitSubjects(mustGit(t, "rev-parse", "main"), "feat"); err != nil || len(subs) != 2 {
		t.Fatalf("CommitSubjects with SHA base = %v err=%v, want two subjects", subs, err)
	}
	// Empty range returns no subjects, no error.
	empty, err := CommitSubjects("main", "main")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty CommitSubjects = %v err=%v", empty, err)
	}
	mustGit(t, "tag", "missing-branch", "main")
	if _, err := CommitSubjects("main", "missing-branch"); err == nil {
		t.Fatalf("CommitSubjects accepted a tag in place of a missing local branch")
	}
}

func TestDirsAndRemote(t *testing.T) {
	newRepo(t)
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	gitDir, err := GitDir()
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	if !strings.HasPrefix(gitDir, root) {
		t.Fatalf("GitDir %q not under RepoRoot %q", gitDir, root)
	}
	commonDir, err := GitCommonDir()
	if err != nil {
		t.Fatalf("GitCommonDir: %v", err)
	}
	if filepath.Clean(commonDir) != filepath.Clean(gitDir) {
		t.Fatalf("GitCommonDir %q != GitDir %q in a non-worktree repo", commonDir, gitDir)
	}

	if RemoteExists("origin") {
		t.Fatalf("fresh repo should have no origin")
	}
	bare := t.TempDir()
	mustGit(t, "init", "-q", "--bare", bare)
	mustGit(t, "remote", "add", "origin", bare)
	if !RemoteExists("origin") {
		t.Fatalf("origin should exist after adding it")
	}
	url, err := RemoteURL("origin")
	if err != nil {
		t.Fatalf("RemoteURL: %v", err)
	}
	if filepath.Clean(url) != filepath.Clean(bare) {
		t.Fatalf("RemoteURL = %q, want %s", url, bare)
	}
}

func TestAbsPathFromGitOutputResolvesFromCurrentDirectory(t *testing.T) {
	newRepo(t)
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	subdir := filepath.Join(root, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.Chdir(subdir); err != nil {
		t.Fatalf("chdir subdir: %v", err)
	}

	got, err := absPathFromGitOutput("../.git")
	if err != nil {
		t.Fatalf("absPathFromGitOutput: %v", err)
	}
	want := filepath.Join(root, ".git")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("absPathFromGitOutput = %q, want %q", got, want)
	}
}

func TestIsSingleAbsolutePathRejectsEchoedUnknownRevParseOption(t *testing.T) {
	if isSingleAbsolutePath("--path-format=absolute\n.git") {
		t.Fatalf("isSingleAbsolutePath accepted echoed rev-parse option output")
	}
	if isSingleAbsolutePath(".git") {
		t.Fatalf("isSingleAbsolutePath accepted a relative path")
	}
	if !isSingleAbsolutePath(t.TempDir()) {
		t.Fatalf("isSingleAbsolutePath rejected a single absolute path")
	}
}

func TestFetchAndPush(t *testing.T) {
	newRepo(t)
	bare := t.TempDir()
	mustGit(t, "init", "-q", "--bare", bare)
	mustGit(t, "remote", "add", "origin", bare)

	if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := Fetch("origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// A force push (force-with-lease) of an unchanged ref is a no-op success.
	if _, err := PushBranches("origin", []string{"main"}, true); err != nil {
		t.Fatalf("Push --force-with-lease: %v", err)
	}

	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "feat.txt", "feat\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "feat")
	if _, err := PushBranches("origin", []string{"main", "feat"}, false); err != nil {
		t.Fatalf("PushBranches: %v", err)
	}
	for _, branch := range []string{"main", "feat"} {
		if got := mustGit(t, "--git-dir", bare, "rev-parse", "--verify", "refs/heads/"+branch); got == "" {
			t.Fatalf("remote ref for %s is empty", branch)
		}
	}
}

// TestParsePushPorcelain covers the record classes a `git push --porcelain`
// stream can carry: updated (space/*/+/−), up-to-date, rejected, refs we did
// not request, duplicate records (last wins) and incomplete or malformed
// lines — anything unusable must leave the branch PushUnconfirmed rather than
// guessing an outcome.
func TestParsePushPorcelain(t *testing.T) {
	branches := []string{"feat-a", "feat-b", "feat-c"}
	fresh := func() *PushResult {
		res := &PushResult{Status: map[string]PushStatus{}}
		for _, b := range branches {
			res.Status[b] = PushUnconfirmed
		}
		return res
	}

	for _, tc := range []struct {
		name string
		out  string
		want map[string]PushStatus
	}{
		{
			name: "all new branches",
			out: "To /remote\n" +
				"*\trefs/heads/feat-a:refs/heads/feat-a\t[new branch]\n" +
				"*\trefs/heads/feat-b:refs/heads/feat-b\t[new branch]\n" +
				"*\trefs/heads/feat-c:refs/heads/feat-c\t[new branch]\n" +
				"Done\n",
			want: map[string]PushStatus{"feat-a": PushUpdated, "feat-b": PushUpdated, "feat-c": PushUpdated},
		},
		{
			name: "non-prefix rejection",
			out: "To /remote\n" +
				"*\trefs/heads/feat-a:refs/heads/feat-a\t[new branch]\n" +
				"!\trefs/heads/feat-b:refs/heads/feat-b\t[remote rejected] (hook declined)\n" +
				"*\trefs/heads/feat-c:refs/heads/feat-c\t[new branch]\n" +
				"Done\n",
			want: map[string]PushStatus{"feat-a": PushUpdated, "feat-b": PushRejected, "feat-c": PushUpdated},
		},
		{
			name: "fast-forward flag is a literal space",
			out:  " \trefs/heads/feat-a:refs/heads/feat-a\tabc123..def456\n",
			want: map[string]PushStatus{"feat-a": PushUpdated, "feat-b": PushUnconfirmed, "feat-c": PushUnconfirmed},
		},
		{
			name: "up to date",
			out:  "=\trefs/heads/feat-a:refs/heads/feat-a\t[up to date]\n",
			want: map[string]PushStatus{"feat-a": PushUpToDate, "feat-b": PushUnconfirmed, "feat-c": PushUnconfirmed},
		},
		{
			name: "forced update",
			out:  "+\trefs/heads/feat-a:refs/heads/feat-a\tabc123...def456 (forced update)\n",
			want: map[string]PushStatus{"feat-a": PushUpdated, "feat-b": PushUnconfirmed, "feat-c": PushUnconfirmed},
		},
		{
			name: "unrelated refs and chatter are ignored",
			out: "To /remote\n" +
				"*\trefs/heads/other:refs/heads/other\t[new branch]\n" +
				"branch 'feat-a' set up to track 'origin/feat-a'.\n" +
				"Done\n",
			want: map[string]PushStatus{"feat-a": PushUnconfirmed, "feat-b": PushUnconfirmed, "feat-c": PushUnconfirmed},
		},
		{
			name: "duplicate records keep the remote's final word",
			out: "!\trefs/heads/feat-a:refs/heads/feat-a\t[rejected] (stale info)\n" +
				" \trefs/heads/feat-a:refs/heads/feat-a\tabc123..def456\n",
			want: map[string]PushStatus{"feat-a": PushUpdated, "feat-b": PushUnconfirmed, "feat-c": PushUnconfirmed},
		},
		{
			name: "malformed lines are unconfirmed",
			out: "no tabs at all\n" +
				"=\trefs/heads/feat-b\t[up to date]\n" + // no src:dst pair
				"?\trefs/heads/feat-c:refs/heads/feat-c\t[?]\n", // unknown flag
			want: map[string]PushStatus{"feat-a": PushUnconfirmed, "feat-b": PushUnconfirmed, "feat-c": PushUnconfirmed},
		},
		{
			name: "empty output confirms nothing",
			out:  "",
			want: map[string]PushStatus{"feat-a": PushUnconfirmed, "feat-b": PushUnconfirmed, "feat-c": PushUnconfirmed},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := fresh()
			parsePushPorcelain(tc.out, branches, res)
			for _, b := range branches {
				if res.Status[b] != tc.want[b] {
					t.Errorf("Status[%s] = %v, want %v (out %q)", b, res.Status[b], tc.want[b], tc.out)
				}
			}
		})
	}
}

// TestPushBranchesReportsPerRefStatus pushes three branches to a bare remote
// whose update hook rejects the middle one: the result must report feat-a and
// feat-c confirmed updated and feat-b confirmed rejected from that single
// invocation's own status records.
func TestPushBranchesReportsPerRefStatus(t *testing.T) {
	newRepo(t)
	bare := t.TempDir()
	mustGit(t, "init", "-q", "--bare", bare)
	mustGit(t, "remote", "add", "origin", bare)

	mustGit(t, "checkout", "-q", "-b", "feat-a")
	writeFile(t, "a.txt", "a\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "a")
	for _, b := range []string{"feat-b", "feat-c"} {
		mustGit(t, "checkout", "-q", "-b", b)
		writeFile(t, b+".txt", b+"\n")
		mustGit(t, "add", "-A")
		mustGit(t, "commit", "-q", "-m", b)
	}

	hook := filepath.Join(bare, "hooks", "update")
	script := "#!/bin/sh\n[ \"$1\" = refs/heads/feat-b ] && exit 1\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := PushBranches("origin", []string{"feat-a", "feat-b", "feat-c"}, true)
	if err == nil {
		t.Fatal("PushBranches succeeded while feat-b was rejected")
	}
	want := map[string]PushStatus{
		"feat-a": PushUpdated,
		"feat-b": PushRejected,
		"feat-c": PushUpdated,
	}
	for b, wantStatus := range want {
		if res.Status[b] != wantStatus {
			t.Errorf("Status[%s] = %v, want %v", b, res.Status[b], wantStatus)
		}
	}
	// The remote agrees: a and c landed, b did not.
	for _, b := range []string{"feat-a", "feat-c"} {
		if got := mustGit(t, "--git-dir", bare, "rev-parse", "--verify", "refs/heads/"+b); got == "" {
			t.Fatalf("remote ref for %s is empty — confirmed update did not land", b)
		}
	}
	if err := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", "-q", "refs/heads/feat-b").Run(); err == nil {
		t.Fatal("remote refs/heads/feat-b exists — the hook-rejected ref was pushed")
	}
}

// TestWorktreesSingle asserts that a repo with only the main worktree reports
// exactly one worktree, on the trunk branch with the correct head SHA.
func TestWorktreesSingle(t *testing.T) {
	newRepo(t)
	mainSHA := mustGit(t, "rev-parse", "HEAD")

	wts, err := Worktrees()
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(wts) != 1 {
		t.Fatalf("Worktrees = %v, want exactly the main worktree", wts)
	}
	if wts[0].Branch != "main" {
		t.Errorf("main worktree branch = %q, want main", wts[0].Branch)
	}
	if wts[0].Head != mainSHA {
		t.Errorf("main worktree head = %q, want %s", wts[0].Head, mainSHA)
	}
	if wts[0].Detached || wts[0].Bare {
		t.Errorf("main worktree wrongly flagged detached/bare: %+v", wts[0])
	}
}

// TestShellWorktreesDelegate pins the Shell port method's delegation to the
// package-level Worktrees — same result, same repo.
func TestShellWorktreesDelegate(t *testing.T) {
	newRepo(t)
	mainSHA := mustGit(t, "rev-parse", "HEAD")

	wts, err := (Shell{}).Worktrees()
	if err != nil {
		t.Fatalf("Shell{}.Worktrees: %v", err)
	}
	if len(wts) != 1 || wts[0].Branch != "main" || wts[0].Head != mainSHA {
		t.Fatalf("Shell{}.Worktrees = %+v, want the single main worktree at %s", wts, mainSHA)
	}
}

// TestWorktreesLinked asserts a linked worktree is enumerated alongside the main
// one with its branch and path, and a detached worktree is flagged.
func TestWorktreesLinked(t *testing.T) {
	newRepo(t)
	mustGit(t, "branch", "feat")
	linked := filepath.Join(t.TempDir(), "feat-wt")
	mustGit(t, "worktree", "add", "-q", linked, "feat")
	detachedSHA := mustGit(t, "rev-parse", "HEAD")
	detached := filepath.Join(t.TempDir(), "det-wt")
	mustGit(t, "worktree", "add", "-q", "--detach", detached, detachedSHA)

	wts, err := Worktrees()
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	byBranch := map[string]Worktree{}
	var detachedSeen bool
	for _, wt := range wts {
		if wt.Detached {
			detachedSeen = true
		}
		if wt.Branch != "" {
			byBranch[wt.Branch] = wt
		}
	}
	if _, ok := byBranch["main"]; !ok {
		t.Errorf("missing main worktree in %v", wts)
	}
	feat, ok := byBranch["feat"]
	if !ok {
		t.Fatalf("missing feat worktree in %v", wts)
	}
	if resolveSymlinks(t, feat.Path) != resolveSymlinks(t, linked) {
		t.Errorf("feat worktree path = %q, want %q", feat.Path, linked)
	}
	if !detachedSeen {
		t.Errorf("detached worktree not flagged in %v", wts)
	}
}

// TestParseWorktreesBareAndLocked exercises the porcelain arms a normal local
// repo rarely emits — `bare` (the main entry of a bare repo) and `locked` (a
// worktree pinned with `git worktree lock`) — against a canned fixture, so the
// parser is covered without standing up a bare repo or locking a worktree.
func TestParseWorktreesBareAndLocked(t *testing.T) {
	// A realistic `git worktree list --porcelain` dump: a bare main entry (no
	// HEAD/branch), a normal linked worktree on a branch, and a locked detached
	// worktree. Records are blank-line separated.
	fixture := "worktree /repo.git\n" +
		"bare\n" +
		"\n" +
		"worktree /wt/feat\n" +
		"HEAD 1111111111111111111111111111111111111111\n" +
		"branch refs/heads/feat\n" +
		"\n" +
		"worktree /wt/pinned\n" +
		"HEAD 2222222222222222222222222222222222222222\n" +
		"detached\n" +
		"locked reason for the lock\n"

	wts, err := parseWorktreesLegacy(fixture)
	if err != nil {
		t.Fatalf("parseWorktreesLegacy: %v", err)
	}
	if len(wts) != 3 {
		t.Fatalf("parsed %d worktrees, want 3: %+v", len(wts), wts)
	}

	bare := wts[0]
	if bare.Path != "/repo.git" || !bare.Bare {
		t.Errorf("bare entry = %+v, want path /repo.git and Bare=true", bare)
	}
	if bare.Branch != "" || bare.Head != "" {
		t.Errorf("bare entry should have no branch/head: %+v", bare)
	}

	feat := wts[1]
	if feat.Branch != "feat" || feat.Bare || feat.Locked || feat.Detached {
		t.Errorf("feat entry = %+v, want branch feat and no bare/locked/detached flags", feat)
	}

	pinned := wts[2]
	if !pinned.Locked {
		t.Errorf("locked worktree not flagged: %+v", pinned)
	}
	if !pinned.Detached {
		t.Errorf("locked worktree should also be detached: %+v", pinned)
	}
	if pinned.Branch != "" {
		t.Errorf("detached worktree should have no branch: %+v", pinned)
	}
}

// TestParseWorktreesZ exercises the NUL-terminated variant of
// `git worktree list --porcelain`: attributes are NUL-separated, records are
// separated by an empty attribute, and a path may carry bytes that would be
// structure (newlines, "worktree ", "HEAD ") in the line-based grammar.
func TestParseWorktreesZ(t *testing.T) {
	head1 := "1111111111111111111111111111111111111111"
	head2 := "2222222222222222222222222222222222222222"
	// The second path contains a full fake worktree record — under the NUL
	// grammar those bytes are just path content.
	evilPath := "/wt/evil\nworktree /fake\nHEAD deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\nbranch refs/heads/x"
	fixture := "worktree /repo\x00" +
		"HEAD " + head1 + "\x00" +
		"branch refs/heads/main\x00" +
		"\x00" +
		"worktree " + evilPath + "\x00" +
		"HEAD " + head2 + "\x00" +
		"detached\x00" +
		"locked reason for the lock\x00" +
		"\x00" +
		"worktree /repo-bare\x00" +
		"bare\x00" +
		"\x00" +
		"worktree /gone\x00" +
		"HEAD " + head2 + "\x00" +
		"detached\x00" +
		"prunable gitdir file points to non-existent location\x00" +
		"\x00"

	wts, err := parseWorktreesZ(fixture)
	if err != nil {
		t.Fatalf("parseWorktreesZ: %v", err)
	}
	if len(wts) != 4 {
		t.Fatalf("parsed %d worktrees, want 4: %+v", len(wts), wts)
	}
	main := wts[0]
	if main.Path != "/repo" || main.Head != head1 || main.Branch != "main" {
		t.Errorf("main entry = %+v", main)
	}
	evil := wts[1]
	if evil.Path != evilPath {
		t.Errorf("path bytes mangled: got %q, want %q", evil.Path, evilPath)
	}
	if !evil.Detached || !evil.Locked || evil.Head != head2 {
		t.Errorf("evil entry = %+v, want detached+locked head %s", evil, head2)
	}
	bare := wts[2]
	if bare.Path != "/repo-bare" || !bare.Bare || bare.Branch != "" || bare.Head != "" {
		t.Errorf("bare entry = %+v", bare)
	}
	gone := wts[3]
	if gone.Path != "/gone" || !gone.Detached {
		t.Errorf("prunable entry = %+v, want detached record for /gone", gone)
	}
	for _, wt := range wts {
		if wt.Path == "/fake" {
			t.Fatalf("embedded fake record escaped into a real worktree: %+v", wts)
		}
	}
}

// TestParseWorktreesLegacyMalformed asserts the strict legacy parser refuses
// structurally invalid or ambiguous listings — including path fragments that
// superficially resemble valid records — instead of fabricating worktrees.
func TestParseWorktreesLegacyMalformed(t *testing.T) {
	for name, fixture := range map[string]string{
		// A newline-containing path splits into a field-looking fragment; the
		// unknown continuation key must be refused.
		"split path fragment": "worktree /repo/a\nb c\n" +
			"HEAD 1111111111111111111111111111111111111111\n",
		// A worktree line inside an open record (no blank separator) is a path
		// fragment or corruption, never a new record.
		"worktree inside record": "worktree /a\nworktree /b\n" +
			"HEAD 1111111111111111111111111111111111111111\n",
		"unknown line":        "worktree /x\nbogus v\n",
		"attr before record":  "HEAD 1111111111111111111111111111111111111111\nworktree /x\n",
		"worktree empty path": "worktree \nHEAD 1111111111111111111111111111111111111111\n",
		"missing head":        "worktree /x\ndetached\n",
	} {
		t.Run(name, func(t *testing.T) {
			if wts, err := parseWorktreesLegacy(fixture); err == nil {
				t.Errorf("parseWorktreesLegacy(%q) = %+v, want error", fixture, wts)
			}
		})
	}
}

// TestParseWorktreesZMalformed asserts the NUL grammar refuses structurally
// invalid input instead of silently fabricating or dropping records.
func TestParseWorktreesZMalformed(t *testing.T) {
	for name, fixture := range map[string]string{
		"attribute before record": "HEAD 1111111111111111111111111111111111111111\x00worktree /x\x00",
		"worktree without path":   "worktree\x00HEAD 1111111111111111111111111111111111111111\x00",
		"worktree empty path":     "worktree \x00HEAD 1111111111111111111111111111111111111111\x00",
		"unknown attribute":       "worktree /x\x00bogusfield v\x00",
		"HEAD without value":      "worktree /x\x00HEAD\x00",
		"second worktree inline":  "worktree /x\x00HEAD 1111111111111111111111111111111111111111\x00worktree /y\x00",
		"no HEAD and not bare":    "worktree /x\x00detached\x00",
	} {
		t.Run(name, func(t *testing.T) {
			if wts, err := parseWorktreesZ(fixture); err == nil {
				t.Errorf("parseWorktreesZ(%q) = %+v, want error", fixture, wts)
			}
		})
	}
}

// installGitShim prepends a `git` shim directory to PATH. The shim logs every
// invocation (one line per call) to the file GIT_SHIM_LOG points at, prints
// zFailMsg to stderr and exits zFailCode when the arguments are exactly
// `worktree list --porcelain -z`, and delegates everything else to the real
// git binary. It returns the call-log path.
func installGitShim(t *testing.T, zFailMsg string, zFailCode int) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	t.Setenv("GIT_SHIM_LOG", log)
	var name, script string
	if runtime.GOOS == "windows" {
		// Windows cannot exec an extensionless sh script, and a "git" file
		// would win the LookPath race over every PATHEXT suffix. git.cmd is
		// resolved and run via cmd by os/exec instead.
		name = "git.cmd"
		script = "@echo off\r\n" +
			"echo %*>> \"%GIT_SHIM_LOG%\"\r\n" +
			"if \"%~1\"==\"worktree\" if \"%~2\"==\"list\" if \"%~3\"==\"--porcelain\" if \"%~4\"==\"-z\" (\r\n" +
			"  echo " + zFailMsg + " 1>&2\r\n" +
			"  exit /b " + strconv.Itoa(zFailCode) + "\r\n" +
			")\r\n" +
			"\"" + realGit + "\" %*\r\n" +
			"exit /b %errorlevel%\r\n"
	} else {
		name = "git"
		script = "#!/bin/sh\n" +
			"printf '%s\\n' \"$*\" >> \"$GIT_SHIM_LOG\"\n" +
			"if [ \"$1\" = worktree ] && [ \"$2\" = list ] && [ \"$3\" = --porcelain ] && [ \"$4\" = -z ]; then\n" +
			"echo " + strconv.Quote(zFailMsg) + " >&2\n" +
			"exit " + strconv.Itoa(zFailCode) + "\n" +
			"fi\n" +
			"exec '" + realGit + "' \"$@\"\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// shimCalls reads the call log as trimmed lines (the Windows cmd shim writes
// CRLF, and echo %* can trail a space).
func shimCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read shim log: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestWorktreesLegacyFallback pins the old-git path: when
// `worktree list --porcelain -z` fails with the C-locale unsupported-option
// diagnostic (exit 129), Worktrees retries with the plain line-based listing
// and returns the real records.
func TestWorktreesLegacyFallback(t *testing.T) {
	newRepo(t)
	mustGit(t, "branch", "feat")
	linked := filepath.Join(t.TempDir(), "feat-wt")
	mustGit(t, "worktree", "add", "-q", linked, "feat")

	log := installGitShim(t, "error: unknown option 'z'", 129)
	wts, err := Worktrees()
	if err != nil {
		t.Fatalf("Worktrees via legacy fallback: %v", err)
	}
	byBranch := map[string]Worktree{}
	for _, wt := range wts {
		if wt.Branch != "" {
			byBranch[wt.Branch] = wt
		}
	}
	feat, ok := byBranch["feat"]
	if !ok {
		t.Fatalf("missing feat worktree in %+v", wts)
	}
	if resolveSymlinks(t, feat.Path) != resolveSymlinks(t, linked) {
		t.Errorf("feat worktree path = %q, want %q", feat.Path, linked)
	}
	calls := shimCalls(t, log)
	if !slices.Contains(calls, "worktree list --porcelain -z") {
		t.Errorf("expected the -z probe first; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !slices.Contains(calls, "worktree list --porcelain") {
		t.Errorf("expected a legacy `worktree list --porcelain` retry; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestWorktreesDoesNotFallbackOnError asserts a failure unrelated to -z support
// is propagated untouched — the legacy retry is reserved for the recognized
// unsupported-option diagnostic only.
func TestWorktreesDoesNotFallbackOnError(t *testing.T) {
	newRepo(t)
	log := installGitShim(t, "disk exploded", 1)
	_, err := Worktrees()
	if err == nil || !strings.Contains(err.Error(), "disk exploded") {
		t.Fatalf("Worktrees error = %v, want propagated 'disk exploded'", err)
	}
	calls := shimCalls(t, log)
	for _, line := range calls {
		if line == "worktree list --porcelain" {
			t.Fatalf("legacy retry ran after an unrelated -z failure; calls:\n%s", strings.Join(calls, "\n"))
		}
	}
}

// TestWorktreesLegacyGuardRejectsUnsafeMetadata pins Step 3's precondition: on
// the legacy path, raw worktrees/*/gitdir registration files must prove no
// registered path carries CR/LF bytes — otherwise the line grammar cannot be
// trusted and Worktrees must refuse with an actionable error.
func TestWorktreesLegacyGuardRejectsUnsafeMetadata(t *testing.T) {
	newRepo(t)
	mustGit(t, "branch", "feat")
	linked := filepath.Join(t.TempDir(), "feat-wt")
	mustGit(t, "worktree", "add", "-q", linked, "feat")

	installGitShim(t, "error: unknown option 'z'", 129)
	common := mustGit(t, "rev-parse", "--git-common-dir")
	regs, err := filepath.Glob(filepath.Join(common, "worktrees", "*", "gitdir"))
	if err != nil || len(regs) != 1 {
		t.Fatalf("gitdir registrations = %v (err %v), want exactly 1", regs, err)
	}

	// A newline inside the registered path would split the legacy `worktree`
	// record in two — the guard must refuse rather than parse ambiguity.
	if err := os.WriteFile(regs[0], []byte("/tmp/one\n/tmp/two/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Worktrees()
	if err == nil || !strings.Contains(err.Error(), "gitdir") {
		t.Fatalf("Worktrees error = %v, want an actionable gitdir metadata refusal", err)
	}
}

// TestWorktreesPreservesPathBytes adds a real linked worktree at a path whose
// bytes would corrupt the line-based porcelain grammar — newline, tab,
// trailing space, quote, non-ASCII, and a fully fake embedded record — and
// asserts Worktrees returns the exact path bytes.
func TestWorktreesPreservesPathBytes(t *testing.T) {
	newRepo(t)
	if err := exec.Command("git", "worktree", "list", "--porcelain", "-z").Run(); err != nil {
		t.Skipf("git worktree list -z unsupported (git <2.36): %v", err)
	}
	sha := mustGit(t, "rev-parse", "HEAD")
	leaves := []string{
		"wt\nnewline",
		"wt\ttab",
		"wt trail ",
		"wt'quote",
		"wt-☃-unicode",
		"wt\nworktree /fake\nHEAD deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\nbranch refs/heads/x",
	}
	for i, leaf := range leaves {
		p := filepath.Join(t.TempDir(), leaf)
		err := exec.Command("git", "worktree", "add", "-q", "--detach", p, sha).Run()
		if err != nil {
			t.Logf("leaf %d (%q): filesystem/git cannot create it, skipping", i, leaf)
			continue
		}
		wts, err := Worktrees()
		if err != nil {
			t.Fatalf("Worktrees with path %q: %v", leaf, err)
		}
		var got *Worktree
		for j := range wts {
			if strings.HasSuffix(wts[j].Path, leaf) {
				g := wts[j]
				got = &g
			}
		}
		if got == nil {
			t.Fatalf("leaf %d (%q): no worktree record ending in those bytes: %+v", i, leaf, wts)
		}
		if resolveSymlinks(t, got.Path) != resolveSymlinks(t, p) {
			t.Errorf("leaf %d: worktree path = %q, want canonical %q", i, got.Path, p)
		}
		for _, wt := range wts {
			if wt.Path == "/fake" {
				t.Fatalf("embedded fake record escaped into a real worktree: %+v", wts)
			}
		}
	}
}

// TestWorktreeAddAndRemove drives the add/remove plumbing against real git and
// asserts the worktree shows up in the listing and is gone after removal.
func TestWorktreeAddAndRemove(t *testing.T) {
	newRepo(t)
	mustGit(t, "branch", "feat")
	path := filepath.Join(t.TempDir(), "feat-wt")

	if err := WorktreeAdd(path, "feat"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	wts, err := Worktrees()
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	found := false
	for _, wt := range wts {
		if wt.Branch == "feat" {
			found = true
		}
	}
	if !found {
		t.Fatalf("added worktree not listed: %v", wts)
	}

	if err := WorktreeRemove(path, false); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after remove: %v", err)
	}
}

func TestWorktreeAddRejectsBadArgs(t *testing.T) {
	newRepo(t)
	if err := WorktreeAdd("/tmp/x", "-evil"); err == nil {
		t.Error("WorktreeAdd accepted a flag-like branch name")
	}
	if err := WorktreeAdd("", "feat"); err == nil {
		t.Error("WorktreeAdd accepted an empty path")
	}
	if err := WorktreeRemove("", false); err == nil {
		t.Error("WorktreeRemove accepted an empty path")
	}
}

// TestIsCleanAt asserts the per-directory clean check reflects a linked
// worktree's own working-tree state, independent of the main worktree.
func TestIsCleanAt(t *testing.T) {
	newRepo(t)
	mustGit(t, "branch", "feat")
	path := filepath.Join(t.TempDir(), "feat-wt")
	if err := WorktreeAdd(path, "feat"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	clean, err := IsCleanAt(path)
	if err != nil {
		t.Fatalf("IsCleanAt: %v", err)
	}
	if !clean {
		t.Error("fresh worktree should be clean")
	}
	if err := os.WriteFile(filepath.Join(path, "base.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, err = IsCleanAt(path)
	if err != nil {
		t.Fatalf("IsCleanAt after edit: %v", err)
	}
	if clean {
		t.Error("edited worktree should be dirty")
	}
}

// TestTips asserts the batched ref read returns every local branch tip in one
// spawn, keyed by name, unconfused by a tag sharing a branch's name.
func TestTips(t *testing.T) {
	newRepo(t)
	mainSHA := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "f.txt", "f\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "f")
	featSHA := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "tag", "feat", mainSHA) // decoy tag with a branch's name

	tips, err := Tips()
	if err != nil {
		t.Fatalf("Tips: %v", err)
	}
	if len(tips) != 2 {
		t.Fatalf("Tips = %v, want exactly main and feat", tips)
	}
	if tips["main"] != mainSHA {
		t.Fatalf("tips[main] = %q, want %s", tips["main"], mainSHA)
	}
	if tips["feat"] != featSHA {
		t.Fatalf("tips[feat] = %q, want branch tip %s (not the tag)", tips["feat"], featSHA)
	}
}

func TestTipsForScopesToRequestedBranches(t *testing.T) {
	newRepo(t)
	mainSHA := mustGit(t, "rev-parse", "refs/heads/main")
	mustGit(t, "checkout", "-q", "-b", "unrelated")
	writeFile(t, "u.txt", "u\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "unrelated subject")
	unrelatedSHA := mustGit(t, "rev-parse", "refs/heads/unrelated")
	mustGit(t, "tag", "main", unrelatedSHA) // decoy tag sharing the branch name

	tips, err := TipsFor([]string{"main", "main", "missing"})
	if err != nil {
		t.Fatalf("TipsFor: %v", err)
	}
	if len(tips) != 1 {
		t.Fatalf("TipsFor = %v, want only main", tips)
	}
	if tips["main"] != mainSHA {
		t.Fatalf("TipsFor[main] = %q, want branch tip %s", tips["main"], mainSHA)
	}
	if tips["main"] == unrelatedSHA {
		t.Fatalf("TipsFor[main] used same-named tag target %s", unrelatedSHA)
	}
	if _, ok := tips["unrelated"]; ok {
		t.Fatalf("TipsFor included unrelated branch: %v", tips)
	}
}

func TestTipsForDoesNotTreatPrefixAsBranch(t *testing.T) {
	newRepo(t)
	mustGit(t, "branch", "foo/bar")

	tips, err := TipsFor([]string{"foo"})
	if err != nil {
		t.Fatalf("TipsFor: %v", err)
	}
	if len(tips) != 0 {
		t.Fatalf("TipsFor(foo) = %v, want missing despite foo/bar", tips)
	}
}

func TestTipsForDoesNotResolveRevisionSyntax(t *testing.T) {
	newRepo(t)
	mainSHA := mustGit(t, "rev-parse", "refs/heads/main")

	tips, err := TipsFor([]string{"main", "main^{commit}", "main~0"})
	if err != nil {
		t.Fatalf("TipsFor: %v", err)
	}
	if len(tips) != 1 || tips["main"] != mainSHA {
		t.Fatalf("TipsFor revision syntax = %v, want only exact main tip %s", tips, mainSHA)
	}
}

func TestMergedInto(t *testing.T) {
	newRepo(t)
	base := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "checkout", "-q", "-b", "merged")
	writeFile(t, "merged.txt", "merged\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "merged")
	mustGit(t, "checkout", "-q", "main")
	mustGit(t, "merge", "-q", "--ff-only", "merged")
	mustGit(t, "checkout", "-q", "-b", "unmerged", base)
	writeFile(t, "unmerged.txt", "unmerged\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "unmerged")
	mustGit(t, "tag", "merged", base) // decoy tag with a branch's name
	mustGit(t, "checkout", "-q", "main")

	merged, err := MergedInto("main")
	if err != nil {
		t.Fatalf("MergedInto: %v", err)
	}
	if len(merged) != 2 || !merged["main"] || !merged["merged"] {
		t.Fatalf("MergedInto(main) = %v, want exactly main and merged", merged)
	}
	if merged["unmerged"] {
		t.Fatalf("MergedInto(main) included unmerged branch: %v", merged)
	}
}

// TestChangesContainedIn pins the tree-content containment check: a
// squash-merged branch's tip is no ancestor of the trunk (MergedInto/`git
// cherry` miss it — verified inside the test), yet its whole diff is in the
// trunk's tree. Unique content, and content the trunk has since moved past,
// are both NOT contained.
func TestChangesContainedIn(t *testing.T) {
	newRepo(t)

	// feat: two commits, squash-merged into main as ONE commit (the host
	// squash-merge shape — no ancestry link).
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "a1.txt", "a1\n")
	writeFile(t, "a2.txt", "a2\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "feat 1")
	writeFile(t, "a3.txt", "a3\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "feat 2")
	mustGit(t, "checkout", "-q", "main")
	mustGit(t, "merge", "-q", "--squash", "feat")
	mustGit(t, "commit", "-q", "-m", "squash feat")
	featSHA := mustGit(t, "rev-parse", "feat")
	mainSHA := mustGit(t, "rev-parse", "main")

	// git cherry cannot see it (per-commit patch-id mapping only) — that's the
	// gap this function exists to close.
	if out := mustGit(t, "cherry", "main", "feat"); !strings.Contains(out, "+") {
		t.Fatalf("test premise broken: git cherry should mark squash-merged commits '+', got %q", out)
	}
	if merged, err := MergedInto("main"); err != nil || merged["feat"] {
		t.Fatalf("test premise broken: squash-merged feat must not be ancestry-merged: %v %v", merged, err)
	}

	contained, err := ChangesContainedIn("main", "feat")
	if err != nil {
		t.Fatalf("ChangesContainedIn: %v", err)
	}
	if !contained {
		t.Fatal("squash-merged feat should be content-contained in main")
	}
	// A raw SHA upstream works too — SyncPlanAgainst probes remote tips.
	contained, err = ChangesContainedIn(mainSHA, "feat")
	if err != nil {
		t.Fatalf("ChangesContainedIn by SHA: %v", err)
	}
	if !contained {
		t.Fatal("squash-merged feat should be contained in the main tip SHA")
	}

	// A branch with unique content is not contained.
	mustGit(t, "checkout", "-q", "-b", "unique", featSHA)
	writeFile(t, "unique.txt", "unique\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "unique")
	mustGit(t, "checkout", "-q", "main")
	contained, err = ChangesContainedIn("main", "unique")
	if err != nil {
		t.Fatalf("ChangesContainedIn unique: %v", err)
	}
	if contained {
		t.Fatal("unique must NOT be contained: it has content main lacks")
	}

	// A branch squash-merged and then given new work is not contained.
	mustGit(t, "checkout", "-q", "-b", "continued", featSHA)
	mustGit(t, "checkout", "-q", "main")
	writeFile(t, "later.txt", "later\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "later")
	contained, err = ChangesContainedIn("main", "continued")
	if err != nil {
		t.Fatalf("ChangesContainedIn continued: %v", err)
	}
	if !contained {
		t.Fatal("continued has no content of its own yet; it should still be contained")
	}
	mustGit(t, "checkout", "-q", "continued")
	writeFile(t, "wip.txt", "wip\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "wip")
	mustGit(t, "checkout", "-q", "main")
	contained, err = ChangesContainedIn("main", "continued")
	if err != nil {
		t.Fatalf("ChangesContainedIn continued+wip: %v", err)
	}
	if contained {
		t.Fatal("continued gained content main lacks; must NOT be contained")
	}

	// The trunk moving the same files PAST the squash breaks containment: main
	// no longer agrees with the branch's version at a path the branch changed.
	writeFile(t, "a1.txt", "a1 evolved\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "evolve a1")
	contained, err = ChangesContainedIn("main", "feat")
	if err != nil {
		t.Fatalf("ChangesContainedIn evolved: %v", err)
	}
	if contained {
		t.Fatal("main moved past feat's content; feat must NOT be contained")
	}

	// Rename-vs-copy: renamer renames r.txt to r-moved.txt, while main only
	// copies r.txt to r-moved.txt and keeps r.txt. The branch's deletion of
	// r.txt has not landed upstream, so the branch is NOT contained — a
	// rename-collapsed path set that overlooks the source would wrongly prune
	// it. Assert under diff.renames both enabled and disabled.
	writeFile(t, "r.txt", "r\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "add r")
	mustGit(t, "checkout", "-q", "-b", "renamer")
	mustGit(t, "mv", "r.txt", "r-moved.txt")
	mustGit(t, "commit", "-q", "-m", "rename r")
	mustGit(t, "checkout", "-q", "main")
	writeFile(t, "r-moved.txt", "r\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "copy r")
	for _, renames := range []string{"true", "false"} {
		mustGit(t, "config", "diff.renames", renames)
		contained, err = ChangesContainedIn("main", "renamer")
		if err != nil {
			t.Fatalf("ChangesContainedIn rename-vs-copy (diff.renames=%s): %v", renames, err)
		}
		if contained {
			t.Fatalf("renamer must NOT be contained (diff.renames=%s): upstream kept r.txt, the rename's source deletion never landed", renames)
		}
	}
	mustGit(t, "config", "--unset", "diff.renames")

	// A genuinely contained rename: upstream did the same rename — the source
	// is gone there too and the destination is identical.
	writeFile(t, "s.txt", "s\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "add s")
	mustGit(t, "checkout", "-q", "-b", "same-rename")
	mustGit(t, "mv", "s.txt", "s-moved.txt")
	mustGit(t, "commit", "-q", "-m", "rename s")
	mustGit(t, "checkout", "-q", "main")
	mustGit(t, "mv", "s.txt", "s-moved.txt")
	mustGit(t, "commit", "-q", "-m", "rename s on main")
	contained, err = ChangesContainedIn("main", "same-rename")
	if err != nil {
		t.Fatalf("ChangesContainedIn same-rename: %v", err)
	}
	if !contained {
		t.Fatal("upstream performed the identical rename; same-rename should be contained")
	}

	// Changed source: the branch renames t.txt away, but upstream modified
	// t.txt instead of deleting it — not contained.
	writeFile(t, "t.txt", "t\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "add t")
	mustGit(t, "checkout", "-q", "-b", "moved-src")
	mustGit(t, "mv", "t.txt", "t-moved.txt")
	mustGit(t, "commit", "-q", "-m", "rename t")
	mustGit(t, "checkout", "-q", "main")
	writeFile(t, "t.txt", "t evolved\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "evolve t")
	contained, err = ChangesContainedIn("main", "moved-src")
	if err != nil {
		t.Fatalf("ChangesContainedIn moved-src: %v", err)
	}
	if contained {
		t.Fatal("upstream kept a modified t.txt; moved-src must NOT be contained")
	}

	// Changed destination: the branch renames u.txt to u-moved.txt; upstream
	// deleted u.txt but carries different content at u-moved.txt — not
	// contained.
	writeFile(t, "u.txt", "u\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "add u")
	mustGit(t, "checkout", "-q", "-b", "moved-dst")
	mustGit(t, "mv", "u.txt", "u-moved.txt")
	mustGit(t, "commit", "-q", "-m", "rename u")
	mustGit(t, "checkout", "-q", "main")
	mustGit(t, "rm", "-q", "u.txt")
	writeFile(t, "u-moved.txt", "u different\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "different u-moved")
	contained, err = ChangesContainedIn("main", "moved-dst")
	if err != nil {
		t.Fatalf("ChangesContainedIn moved-dst: %v", err)
	}
	if contained {
		t.Fatal("upstream's u-moved.txt differs; moved-dst must NOT be contained")
	}
}

func TestPushUsesBranchRefspecWhenTagHasSameName(t *testing.T) {
	newRepo(t)
	bare := t.TempDir()
	mustGit(t, "init", "-q", "--bare", bare)
	mustGit(t, "remote", "add", "origin", bare)
	mustGit(t, "tag", "main", "HEAD")

	if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
		t.Fatalf("Push with same-named tag: %v", err)
	}
	want := mustGit(t, "rev-parse", "refs/heads/main")
	gotCmd := exec.Command("git", "--git-dir", bare, "rev-parse", "refs/heads/main")
	gotOut, err := gotCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve pushed branch: %v\n%s", err, gotOut)
	}
	if got := strings.TrimSpace(string(gotOut)); got != want {
		t.Fatalf("pushed branch = %q, want %q", got, want)
	}
}

func TestFastForwardUsesRemoteTrackingRef(t *testing.T) {
	newRepo(t)
	base := mustGit(t, "rev-parse", "HEAD")
	bare := t.TempDir()
	mustGit(t, "init", "-q", "--bare", bare)
	mustGit(t, "remote", "add", "origin", bare)
	if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
		t.Fatalf("initial Push: %v", err)
	}

	mustGit(t, "checkout", "-q", "-b", "decoy", base)
	writeFile(t, "decoy.txt", "decoy\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "decoy")
	decoy := mustGit(t, "rev-parse", "HEAD")

	mustGit(t, "checkout", "-q", "main")
	writeFile(t, "remote.txt", "remote\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "remote")
	remoteTip := mustGit(t, "rev-parse", "HEAD")
	if _, err := PushBranches("origin", []string{"main"}, false); err != nil {
		t.Fatalf("remote Push: %v", err)
	}
	if err := Fetch("origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mustGit(t, "reset", "--hard", base)
	mustGit(t, "update-ref", "refs/heads/origin/main", decoy)
	mustGit(t, "branch", remoteTip, base)
	if _, err := (RemoteShell{}).FastForward("main", "origin", "", true); err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	if got := mustGit(t, "rev-parse", "refs/heads/main"); got != remoteTip {
		t.Fatalf("FastForward moved main to %q, want remote tracking tip %q", got, remoteTip)
	}
}

func TestRebaseInProgressFalse(t *testing.T) {
	newRepo(t)
	inProgress, err := RebaseInProgress()
	if err != nil {
		t.Fatalf("RebaseInProgress: %v", err)
	}
	if inProgress {
		t.Fatalf("no rebase should be in progress in a fresh repo")
	}
	name, err := RebaseHeadName()
	if err != nil {
		t.Fatalf("RebaseHeadName: %v", err)
	}
	if name != "" {
		t.Fatalf("RebaseHeadName = %q, want empty", name)
	}
}

// TestRebaseInProgressIn pins the per-worktree probe: rebase metadata lives
// under the worktree's own git dir, so a paused rebase in a linked worktree is
// visible to RebaseInProgressIn(wtPath) and invisible to the caller's
// RebaseInProgress() — and vice versa.
func TestRebaseInProgressIn(t *testing.T) {
	newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, "branch", "feat")
	mustGit(t, "worktree", "add", wt, "feat")

	inProgress, err := RebaseInProgressIn(wt)
	if err != nil {
		t.Fatalf("RebaseInProgressIn: %v", err)
	}
	if inProgress {
		t.Fatal("no rebase should be in progress in the fresh worktree")
	}

	// Pause a rebase in the LINKED worktree — its git dir lives under
	// .git/worktrees/<name>/, not the main one.
	wtGitDir := mustGit(t, "-C", wt, "rev-parse", "--absolute-git-dir")
	if err := os.MkdirAll(filepath.Join(wtGitDir, "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	inProgress, err = RebaseInProgressIn(wt)
	if err != nil {
		t.Fatalf("RebaseInProgressIn: %v", err)
	}
	if !inProgress {
		t.Fatal("a paused rebase in the linked worktree must be detected")
	}
	if mainInProgress, _ := RebaseInProgress(); mainInProgress {
		t.Fatal("the linked worktree's rebase must be invisible to the main worktree probe")
	}
}

// TestRebaseHeadNameIn pins the plan-002 characterization against real Git: a
// linked worktree paused mid-rebase reports `detached` in `git worktree
// list`, so the head-name file in its own git dir is the only probe that
// names the branch its --continue/--abort will rewrite.
func TestRebaseHeadNameIn(t *testing.T) {
	newRepo(t)
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "f.txt", "feat\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "feat")
	mustGit(t, "checkout", "-q", "main")
	writeFile(t, "f.txt", "main\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "main-advance")

	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, "worktree", "add", "-q", wt, "feat")
	out, err := exec.Command("git", "-C", wt, "rebase", "main").CombinedOutput()
	if err == nil {
		t.Fatalf("git -C wt rebase main unexpectedly succeeded:\n%s", out)
	}

	name, err := RebaseHeadNameIn(wt)
	if err != nil {
		t.Fatalf("RebaseHeadNameIn: %v", err)
	}
	if name != "feat" {
		t.Fatalf("RebaseHeadNameIn = %q, want feat", name)
	}
	// The main worktree's head-name probe stays blind to the linked pause.
	if main, err := RebaseHeadName(); err != nil || main != "" {
		t.Fatalf("RebaseHeadName = %q err=%v, want empty", main, err)
	}
	// After --abort the same worktree reports empty, not an error.
	mustGit(t, "-C", wt, "rebase", "--abort")
	if empty, err := RebaseHeadNameIn(wt); err != nil || empty != "" {
		t.Fatalf("RebaseHeadNameIn on clean worktree = %q err=%v", empty, err)
	}
}

// TestRebaseOntoSHA covers the worktree-local rebase-target reader: both
// metadata backends resolve their recorded commit, and missing or corrupt
// metadata is a distinct actionable error — never a guess.
func TestRebaseOntoSHA(t *testing.T) {
	t.Run("rebase-merge fixture", func(t *testing.T) {
		newRepo(t)
		tip := mustGit(t, "rev-parse", "HEAD")
		gitDir := mustGit(t, "rev-parse", "--absolute-git-dir")
		dir := filepath.Join(gitDir, "rebase-merge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "onto"), []byte(tip+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sha, err := RebaseOntoSHA()
		if err != nil || sha != tip {
			t.Fatalf("RebaseOntoSHA = %q err=%v, want %s", sha, err, tip)
		}
	})

	t.Run("rebase-apply fixture", func(t *testing.T) {
		newRepo(t)
		tip := mustGit(t, "rev-parse", "HEAD")
		gitDir := mustGit(t, "rev-parse", "--absolute-git-dir")
		dir := filepath.Join(gitDir, "rebase-apply")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "onto"), []byte(tip+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sha, err := RebaseOntoSHA()
		if err != nil || sha != tip {
			t.Fatalf("RebaseOntoSHA = %q err=%v, want %s", sha, err, tip)
		}
	})

	t.Run("no rebase metadata", func(t *testing.T) {
		newRepo(t)
		if sha, err := RebaseOntoSHA(); err == nil {
			t.Fatalf("RebaseOntoSHA = %q, want an error with no rebase in progress", sha)
		}
	})

	t.Run("active backend without onto file", func(t *testing.T) {
		newRepo(t)
		gitDir := mustGit(t, "rev-parse", "--absolute-git-dir")
		if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if sha, err := RebaseOntoSHA(); err == nil {
			t.Fatalf("RebaseOntoSHA = %q, want an error for a missing onto file", sha)
		}
	})

	t.Run("corrupt onto value", func(t *testing.T) {
		newRepo(t)
		gitDir := mustGit(t, "rev-parse", "--absolute-git-dir")
		dir := filepath.Join(gitDir, "rebase-merge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "onto"), []byte("not-a-commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if sha, err := RebaseOntoSHA(); err == nil {
			t.Fatalf("RebaseOntoSHA = %q, want an error for an unresolvable target", sha)
		}
	})
}

// pauseRebase starts a real `git rebase --onto` that stops on a conflict,
// returning the SHA the rebase is replaying onto. f.txt differs between the
// onto target and the rebased commit so the replay must pause. The target is
// advanced on a DETACHED head so the helper also works inside a linked
// worktree, where main's checkout is owned by the main worktree.
func pauseRebase(t *testing.T) string {
	t.Helper()
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "f.txt", "feat\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "feat")
	mustGit(t, "checkout", "-q", "--detach", "main")
	writeFile(t, "f.txt", "main\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "main-advance")
	onto := mustGit(t, "rev-parse", "HEAD")
	base := mustGit(t, "rev-parse", "main")
	out, err := exec.Command("git", "rebase", "--onto", onto, base, "feat").CombinedOutput()
	if err == nil {
		t.Fatalf("rebase unexpectedly succeeded:\n%s", out)
	}
	if inProgress, _ := RebaseInProgress(); !inProgress {
		t.Fatalf("expected a paused rebase:\n%s", out)
	}
	return onto
}

// TestRebaseOntoSHARealRebase proves the accessor against an actual paused
// default-backend rebase, not just a fixture file.
func TestRebaseOntoSHARealRebase(t *testing.T) {
	newRepo(t)
	onto := pauseRebase(t)
	sha, err := RebaseOntoSHA()
	if err != nil || sha != onto {
		t.Fatalf("RebaseOntoSHA = %q err=%v, want %s", sha, err, onto)
	}
	mustGit(t, "rebase", "--abort")
	if _, err := RebaseOntoSHA(); err == nil {
		t.Fatal("RebaseOntoSHA succeeded after the rebase was aborted")
	}
}

// TestRebaseOntoSHALinkedWorktree: rebase state is worktree-local — a rebase
// paused inside a linked worktree must be read from THAT worktree's git dir,
// not the common dir.
func TestRebaseOntoSHALinkedWorktree(t *testing.T) {
	newRepo(t)
	linked := filepath.Join(t.TempDir(), "wt")
	mustGit(t, "worktree", "add", "-q", "--detach", linked)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(linked)
	defer t.Chdir(cwd)

	onto := pauseRebase(t)
	sha, err := RebaseOntoSHA()
	if err != nil || sha != onto {
		t.Fatalf("RebaseOntoSHA in linked worktree = %q err=%v, want %s", sha, err, onto)
	}
	mustGit(t, "rebase", "--abort")
}

func TestRebaseContinueQuietUsesValidContinueForm(t *testing.T) {
	newRepo(t)
	err := RebaseContinueQuiet()
	if err == nil {
		t.Fatal("RebaseContinueQuiet unexpectedly succeeded without a rebase")
	}
	if strings.Contains(err.Error(), "usage:") || strings.Contains(err.Error(), "options") {
		t.Fatalf("RebaseContinueQuiet used an invalid option form: %v", err)
	}
}

func TestRunAndRunErr(t *testing.T) {
	newRepo(t)
	out, err := Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || out != "main" {
		t.Fatalf("Run = %q err=%v, want main", out, err)
	}
	// A failing git command returns an error carrying the stderr.
	if _, err := Run("rev-parse", "definitely-not-a-ref"); err == nil {
		t.Fatalf("expected error for a bad ref")
	}
}

// TestCheckBranchName covers the friendly branch-name validator that create and
// rename call before letting an invalid name reach `git branch`/`git checkout
// -b`, where it would otherwise leak git's multi-line "fatal: ... is not a valid
// branch name" + advice hints.
func TestCheckBranchName(t *testing.T) {
	newRepo(t)
	valid := []string{"feat", "feat/foo", "feat-bar", "release/1.2.x"}
	for _, name := range valid {
		if err := CheckBranchName(name); err != nil {
			t.Errorf("CheckBranchName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "bad name", "with~tilde", "a..b", "trailing.lock", "has:colon"}
	for _, name := range invalid {
		if err := CheckBranchName(name); err == nil {
			t.Errorf("CheckBranchName(%q) = nil, want an error", name)
		}
	}
}

// TestUpdateRefs pins the batch wrapper: one invocation moves every ref, a
// missing ref is created (undo's branch resurrection), a bad batch moves
// NOTHING (git applies --stdin as one transaction), flag-like names are
// refused before exec, and empty input is a no-op.
func TestUpdateRefs(t *testing.T) {
	newRepo(t)
	base := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "next.txt", "next\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "next")
	tip := mustGit(t, "rev-parse", "HEAD")
	mustGit(t, "branch", "-q", "one", base)
	mustGit(t, "branch", "-q", "two", base)

	if err := UpdateRefs(map[string]string{
		"refs/heads/one":         tip,
		"refs/heads/two":         tip,
		"refs/heads/resurrected": base, // does not exist yet: update creates it
	}); err != nil {
		t.Fatalf("UpdateRefs: %v", err)
	}
	for _, ref := range []string{"one", "two"} {
		if got := mustGit(t, "rev-parse", "refs/heads/"+ref); got != tip {
			t.Fatalf("%s = %q, want %q", ref, got, tip)
		}
	}
	if got := mustGit(t, "rev-parse", "refs/heads/resurrected"); got != base {
		t.Fatalf("resurrected = %q, want created at %q", got, base)
	}

	// Transactionality: a batch with one bogus SHA moves zero refs.
	err := UpdateRefs(map[string]string{
		"refs/heads/one": base,
		"refs/heads/two": "0123456789012345678901234567890123456789",
	})
	if err == nil {
		t.Fatal("UpdateRefs with a bogus SHA succeeded")
	}
	if got := mustGit(t, "rev-parse", "refs/heads/one"); got != tip {
		t.Fatalf("one = %q after failed batch, want untouched %q", got, tip)
	}

	// A flag-like ref name is refused before any exec.
	if err := UpdateRefs(map[string]string{"--evil": base}); err == nil {
		t.Fatal("UpdateRefs accepted a flag-like ref name")
	}
	// Empty input is a no-op.
	if err := UpdateRefs(nil); err != nil {
		t.Fatalf("UpdateRefs(nil): %v", err)
	}

	// Directive injection: a value carrying a newline + a forged second
	// directive must be refused before exec — and, crucially, the forged
	// delete must NOT fire. (Pre--z framing, this deleted refs/heads/two.)
	err = UpdateRefs(map[string]string{
		"refs/heads/one": base + "\ndelete refs/heads/two",
	})
	if err == nil {
		t.Fatal("UpdateRefs accepted a newline-injected value")
	}
	if got := mustGit(t, "rev-parse", "refs/heads/two"); got != tip {
		t.Fatalf("two = %q after injected batch, want untouched %q (forged delete fired?)", got, tip)
	}
	if got := mustGit(t, "rev-parse", "refs/heads/one"); got != tip {
		t.Fatalf("one = %q after injected batch, want untouched %q", got, tip)
	}

	// A ref key with embedded whitespace is refused before exec.
	if err := UpdateRefs(map[string]string{"refs/heads/a b": base}); err == nil {
		t.Fatal("UpdateRefs accepted a whitespace-carrying ref name")
	}
}

// TestDiffCachedHunks pins the -U0 section parser: the classify-or-refuse
// contract means every staged change is either a text Hunk or an
// UnsupportedRecord, across edits, deletions, binary, mode, rename,
// non-ASCII names, and content lines that look like diff headers.
func TestDiffCachedHunks(t *testing.T) {
	// mustHunks stages nothing itself; it just runs the parser and fails on error.
	mustHunks := func(t *testing.T) ([]Hunk, []UnsupportedRecord) {
		t.Helper()
		hunks, unsupported, err := DiffCachedHunks()
		if err != nil {
			t.Fatalf("DiffCachedHunks: %v", err)
		}
		return hunks, unsupported
	}

	t.Run("single edit, pure addition, two hunks", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "f.txt", "l1\nl2\nl3\nl4\nl5\n")
		mustGit(t, "add", "f.txt")
		mustGit(t, "commit", "-q", "-m", "base")

		writeFile(t, "f.txt", "l1\nEDIT\nl3\nl4\nl5\n")
		mustGit(t, "add", "f.txt")
		hunks, unsupported := mustHunks(t)
		want := Hunk{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}
		if len(hunks) != 1 || hunks[0] != want || len(unsupported) != 0 {
			t.Fatalf("hunks = %+v unsupported = %+v, want exactly %+v", hunks, unsupported, want)
		}

		writeFile(t, "f.txt", "l1\nl2\nl3\nNEW\nl4\nl5\n")
		mustGit(t, "add", "f.txt")
		hunks, _ = mustHunks(t)
		if len(hunks) != 1 || hunks[0].OldN != 0 || hunks[0].NewN != 1 {
			t.Fatalf("addition hunk = %+v, want OldN==0 NewN==1", hunks)
		}

		writeFile(t, "f.txt", "E1\nl2\nl3\nl4\nE5\n")
		mustGit(t, "add", "f.txt")
		hunks, _ = mustHunks(t)
		if len(hunks) != 2 || hunks[0].OldStart != 1 || hunks[1].OldStart != 5 {
			t.Fatalf("hunks = %+v, want two in f.txt at 1 and 5", hunks)
		}
	})

	t.Run("whole-file deletion keeps the pre-image name", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "gone.txt", "a\nb\n")
		mustGit(t, "add", "gone.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		mustGit(t, "rm", "-q", "gone.txt")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 1 || hunks[0].File != "gone.txt" || hunks[0].OldN != 2 || len(unsupported) != 0 {
			t.Fatalf("hunks = %+v unsupported = %+v, want one deletion hunk on gone.txt OldN=2", hunks, unsupported)
		}
	})

	t.Run("binary file is an unsupported record", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "bin.dat", "\x00\x01\x02")
		mustGit(t, "add", "bin.dat")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 0 || len(unsupported) != 1 || unsupported[0].Reason != "binary file" || unsupported[0].File != "bin.dat" {
			t.Fatalf("hunks = %+v unsupported = %+v, want one binary record for bin.dat", hunks, unsupported)
		}
	})

	t.Run("mode-only change is an unsupported record", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "script.sh", "echo hi\n")
		mustGit(t, "add", "script.sh")
		mustGit(t, "commit", "-q", "-m", "base")
		mustGit(t, "update-index", "--chmod=+x", "script.sh")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 0 || len(unsupported) != 1 || unsupported[0].Reason != "mode change" || unsupported[0].File != "script.sh" {
			t.Fatalf("hunks = %+v unsupported = %+v, want one mode record for script.sh", hunks, unsupported)
		}
	})

	t.Run("mode change plus text edit keeps the hunk AND records the mode", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "script.sh", "one\ntwo\n")
		mustGit(t, "add", "script.sh")
		mustGit(t, "commit", "-q", "-m", "base")
		writeFile(t, "script.sh", "one\nTWO\n")
		mustGit(t, "add", "script.sh")
		mustGit(t, "update-index", "--chmod=+x", "script.sh")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 1 || hunks[0].File != "script.sh" {
			t.Fatalf("hunks = %+v, want the text hunk kept", hunks)
		}
		if len(unsupported) != 1 || unsupported[0].Reason != "mode change" {
			t.Fatalf("unsupported = %+v, want the mode record alongside the hunk", unsupported)
		}
	})

	t.Run("rename with edit is an unsupported record, hunks dropped", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "old.txt", "a\nb\nc\nd\ne\nf\ng\nh\n")
		mustGit(t, "add", "old.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		mustGit(t, "mv", "old.txt", "new.txt")
		writeFile(t, "new.txt", "a\nB\nc\nd\ne\nf\ng\nh\n")
		mustGit(t, "add", "new.txt")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 0 || len(unsupported) != 1 || unsupported[0].Reason != "rename" {
			t.Fatalf("hunks = %+v unsupported = %+v, want one rename record with no hunks", hunks, unsupported)
		}
	})

	t.Run("non-ASCII filename arrives raw and unquoted", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "fö.txt", "x\ny\n")
		mustGit(t, "add", "fö.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		writeFile(t, "fö.txt", "X\ny\n")
		mustGit(t, "add", "fö.txt")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 1 || hunks[0].File != "fö.txt" || len(unsupported) != 0 {
			t.Fatalf("hunks = %+v unsupported = %+v, want one hunk on the raw name", hunks, unsupported)
		}
	})

	t.Run("content line that looks like a diff header does not desync", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "real.txt", "keep\n-- a/decoy\nkeep2\n")
		mustGit(t, "add", "real.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		// Deleting the decoy line makes the -U0 diff carry the content line
		// "--- a/decoy", which must NOT be taken as a file header.
		writeFile(t, "real.txt", "keep\nkeep2\n")
		mustGit(t, "add", "real.txt")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 1 || hunks[0].File != "real.txt" || len(unsupported) != 0 {
			t.Fatalf("hunks = %+v unsupported = %+v, want one hunk still attributed to real.txt", hunks, unsupported)
		}
	})

	t.Run("empty added file is refused, not omitted", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "f.txt", "l1\nl2\n")
		mustGit(t, "add", "f.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		// An empty added file produces a `new file mode` section with NO
		// hunks — it must still be accounted for.
		writeFile(t, "empty.txt", "")
		mustGit(t, "add", "empty.txt")
		writeFile(t, "f.txt", "l1\nEDIT\n")
		mustGit(t, "add", "f.txt")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 1 || hunks[0].File != "f.txt" {
			t.Fatalf("hunks = %+v, want the f.txt edit only", hunks)
		}
		if len(unsupported) != 1 || unsupported[0].File != "empty.txt" {
			t.Fatalf("unsupported = %+v, want a refusal naming empty.txt", unsupported)
		}
	})

	t.Run("empty deleted file is refused, not omitted", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "gone.txt", "")
		mustGit(t, "add", "gone.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		mustGit(t, "rm", "-q", "gone.txt")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 0 || len(unsupported) != 1 || unsupported[0].File != "gone.txt" {
			t.Fatalf("hunks = %+v unsupported = %+v, want a refusal naming gone.txt", hunks, unsupported)
		}
	})

	t.Run("noprefix and mnemonicPrefix configs cannot reshape the stream", func(t *testing.T) {
		for _, cfg := range [][2]string{{"diff.noprefix", "true"}, {"diff.mnemonicPrefix", "true"}} {
			newRepo(t)
			writeFile(t, "f.txt", "l1\nl2\n")
			mustGit(t, "add", "f.txt")
			mustGit(t, "commit", "-q", "-m", "base")
			writeFile(t, "f.txt", "l1\nEDIT\n")
			mustGit(t, "add", "f.txt")
			mustGit(t, "config", cfg[0], cfg[1])
			hunks, unsupported := mustHunks(t)
			want := Hunk{File: "f.txt", OldStart: 2, OldN: 1, NewStart: 2, NewN: 1}
			if len(hunks) != 1 || hunks[0] != want || len(unsupported) != 0 {
				t.Fatalf("%s: hunks = %+v unsupported = %+v, want exactly %+v", cfg[0], hunks, unsupported, want)
			}
			mustGit(t, "config", "--unset", cfg[0])
		}
	})

	t.Run("color.ui=always cannot inject escapes into the stream", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "f.txt", "l1\nl2\n")
		mustGit(t, "add", "f.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		writeFile(t, "f.txt", "l1\nEDIT\n")
		mustGit(t, "add", "f.txt")
		mustGit(t, "config", "color.ui", "always")
		hunks, unsupported := mustHunks(t)
		if len(hunks) != 1 || hunks[0].File != "f.txt" || len(unsupported) != 0 {
			t.Fatalf("hunks = %+v unsupported = %+v, want one clean hunk", hunks, unsupported)
		}
	})

	t.Run("configured external diff driver is never invoked", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "f.txt", "l1\nl2\n")
		mustGit(t, "add", "f.txt")
		mustGit(t, "commit", "-q", "-m", "base")
		writeFile(t, "f.txt", "l1\nEDIT\n")
		mustGit(t, "add", "f.txt")
		sentinel := filepath.Join(t.TempDir(), "sentinel")
		stub := writeSentinelStub(t, filepath.Dir(sentinel), "extdiff", sentinel)
		mustGit(t, "config", "diff.external", stub)
		hunks, unsupported := mustHunks(t)
		if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
			t.Fatal("external diff driver was invoked")
		}
		if len(hunks) != 1 || hunks[0].File != "f.txt" || len(unsupported) != 0 {
			t.Fatalf("hunks = %+v unsupported = %+v, want the real diff's hunk", hunks, unsupported)
		}
	})

	t.Run("configured textconv driver is never invoked", func(t *testing.T) {
		newRepo(t)
		writeFile(t, "bin.dat", "\x00\x01\x02")
		mustGit(t, "add", "bin.dat")
		mustGit(t, "commit", "-q", "-m", "base")
		writeFile(t, "bin.dat", "\x00\x03\x04")
		mustGit(t, "add", "bin.dat")
		sentinel := filepath.Join(t.TempDir(), "sentinel")
		stub := writeSentinelStub(t, filepath.Dir(sentinel), "textconv", sentinel)
		mustGit(t, "config", "diff.driver.textconv", stub)
		hunks, unsupported := mustHunks(t)
		if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
			t.Fatal("textconv driver was invoked")
		}
		if len(hunks) != 0 || len(unsupported) != 1 || unsupported[0].Reason != "binary file" {
			t.Fatalf("hunks = %+v unsupported = %+v, want one binary record", hunks, unsupported)
		}
	})
}

// TestAmendTipWithPatch pins the temp-index amend: a staged patch captured on
// one branch lands in ANOTHER branch's tip without any checkout, preserving
// the tip's parent, author, and message; a patch that does not apply leaves
// the repository (and the caller's index) untouched.
func TestAmendTipWithPatch(t *testing.T) {
	newRepo(t)
	// target owns shared.txt line 2; top adds only its own file, so the staged
	// patch's context lines are identical in top's and target's trees.
	writeFile(t, "shared.txt", "one\ntwo\nthree\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "seed shared")
	mustGit(t, "checkout", "-q", "-b", "target")
	writeFile(t, "shared.txt", "one\ntwo-owned\nthree\n")
	mustGit(t, "commit", "-q", "-am", "target owns line 2")
	oldTip := mustGit(t, "rev-parse", "HEAD")
	oldParent := mustGit(t, "rev-parse", "HEAD^")
	oldAuthor := mustGit(t, "log", "-1", "--format=%an <%ae> %aI", oldTip)
	mustGit(t, "checkout", "-q", "-b", "top")
	writeFile(t, "top.txt", "top\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "top work")

	writeFile(t, "shared.txt", "one\ntwo-owned-fixed\nthree\n")
	mustGit(t, "add", "shared.txt")
	hunks, _, err := DiffCachedHunks()
	if err != nil {
		t.Fatalf("DiffCachedHunks: %v", err)
	}
	patch, err := DiffCachedPatchFor(hunks)
	if err != nil || len(patch) == 0 {
		t.Fatalf("DiffCachedPatchFor(all) = %d bytes, %v; want a non-empty patch", len(patch), err)
	}

	newTip, err := AmendTipWithPatch("target", patch)
	if err != nil {
		t.Fatalf("AmendTipWithPatch: %v", err)
	}
	if newTip == oldTip {
		t.Fatal("AmendTipWithPatch returned the old tip; nothing was amended")
	}
	if got := mustGit(t, "rev-parse", "refs/heads/target"); got != newTip {
		t.Fatalf("target = %s, want the returned tip %s", got, newTip)
	}
	if got := mustGit(t, "rev-parse", newTip+"^"); got != oldParent {
		t.Fatalf("amended parent = %s, want %s (amend in place, not a new commit on top)", got, oldParent)
	}
	if got := mustGit(t, "log", "-1", "--format=%s", newTip); got != "target owns line 2" {
		t.Fatalf("amended subject = %q, want the original preserved", got)
	}
	if got := mustGit(t, "log", "-1", "--format=%an <%ae> %aI", newTip); got != oldAuthor {
		t.Fatalf("amended author = %q, want the original %q", got, oldAuthor)
	}
	if got := mustGit(t, "show", "target:shared.txt"); !strings.Contains(got, "two-owned-fixed") {
		t.Fatalf("target:shared.txt = %q, want the absorbed edit", got)
	}
	// The caller's worktree and index are untouched: still on top, edit staged.
	if got := mustGit(t, "rev-parse", "--abbrev-ref", "HEAD"); got != "top" {
		t.Fatalf("HEAD = %q, want top (no checkout)", got)
	}
	if got := mustGit(t, "diff", "--cached", "--name-only"); got != "shared.txt" {
		t.Fatalf("staged files = %q, want the edit still staged here", got)
	}

	// A patch that does not apply to target's tree fails cleanly: ref untouched.
	garbage := []byte("diff --git a/shared.txt b/shared.txt\n--- a/shared.txt\n+++ b/shared.txt\n@@ -1,3 +1,3 @@\n-nope\n-lines\n-mismatch\n+x\n+y\n+z\n")
	if _, err := AmendTipWithPatch("target", garbage); err == nil {
		t.Fatal("AmendTipWithPatch with a non-applying patch succeeded")
	}
	if got := mustGit(t, "rev-parse", "refs/heads/target"); got != newTip {
		t.Fatalf("target = %s after failed apply, want untouched %s", got, newTip)
	}
}

// TestAmendTipWithPatchMergeTip pins the multi-parent path: amending a
// branch whose tip is a merge commit preserves BOTH parents (the -p loop),
// the subject, and the merged content.
func TestAmendTipWithPatchMergeTip(t *testing.T) {
	newRepo(t)
	writeFile(t, "shared.txt", "one\ntwo\nthree\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "seed shared")
	mustGit(t, "checkout", "-q", "-b", "side")
	writeFile(t, "side.txt", "side\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "side work")
	mustGit(t, "checkout", "-q", "main")
	mustGit(t, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	oldTip := mustGit(t, "rev-parse", "HEAD")
	oldParents := strings.Fields(mustGit(t, "rev-list", "--parents", "-n1", oldTip))[1:]
	if len(oldParents) != 2 {
		t.Fatalf("fixture tip has %d parents, want a merge commit", len(oldParents))
	}

	// Capture a patch against the merged tree from a throwaway branch.
	mustGit(t, "checkout", "-q", "-b", "scratch")
	writeFile(t, "shared.txt", "one\ntwo-fixed\nthree\n")
	mustGit(t, "add", "shared.txt")
	mergeHunks, _, err := DiffCachedHunks()
	if err != nil {
		t.Fatalf("DiffCachedHunks: %v", err)
	}
	patch, err := DiffCachedPatchFor(mergeHunks)
	if err != nil || len(patch) == 0 {
		t.Fatalf("DiffCachedPatchFor(all) = %d bytes, %v", len(patch), err)
	}
	mustGit(t, "reset", "-q", "--hard", "HEAD")

	newTip, err := AmendTipWithPatch("main", patch)
	if err != nil {
		t.Fatalf("AmendTipWithPatch on a merge tip: %v", err)
	}
	newParents := strings.Fields(mustGit(t, "rev-list", "--parents", "-n1", newTip))[1:]
	if len(newParents) != 2 || newParents[0] != oldParents[0] || newParents[1] != oldParents[1] {
		t.Fatalf("amended parents = %v, want the original merge parents %v in order", newParents, oldParents)
	}
	if got := mustGit(t, "log", "-1", "--format=%s", newTip); got != "merge side" {
		t.Fatalf("amended subject = %q, want the merge subject preserved", got)
	}
	if got := mustGit(t, "show", "main:shared.txt"); !strings.Contains(got, "two-fixed") {
		t.Fatalf("main:shared.txt = %q, want the absorbed edit", got)
	}
}

// TestAmendTipWithPatchRootTip pins the root-commit refusal: a branch whose
// tip has no parent errors out with the ref untouched.
func TestAmendTipWithPatchRootTip(t *testing.T) {
	newRepo(t)
	mustGit(t, "checkout", "-q", "--orphan", "lone")
	writeFile(t, "lone.txt", "alone\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "root")
	tip := mustGit(t, "rev-parse", "refs/heads/lone")

	patch := []byte("diff --git a/lone.txt b/lone.txt\n--- a/lone.txt\n+++ b/lone.txt\n@@ -1 +1 @@\n-alone\n+company\n")
	if _, err := AmendTipWithPatch("lone", patch); err == nil || !strings.Contains(err.Error(), "root commit") {
		t.Fatalf("AmendTipWithPatch on a root tip = %v, want the root-commit refusal", err)
	}
	if got := mustGit(t, "rev-parse", "refs/heads/lone"); got != tip {
		t.Fatalf("lone = %s after refused amend, want untouched %s", got, tip)
	}
}

// TestUpdateRefCompareAndSwap pins the update-ref old-value semantics that
// AmendTipWithPatch's final `update-ref ref newTip tip` relies on: a swap
// whose expected old value is stale FAILS and moves nothing. (The mid-call
// race itself cannot be staged deterministically; this is the invariant the
// CAS depends on.)
func TestUpdateRefCompareAndSwap(t *testing.T) {
	newRepo(t)
	staleTip := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "next.txt", "next\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "advance")
	newTip := mustGit(t, "rev-parse", "HEAD")

	if _, err := Run("update-ref", "refs/heads/main", staleTip, staleTip); err == nil {
		t.Fatal("update-ref with a stale expected old value succeeded; the CAS guarantee is gone")
	}
	if got := mustGit(t, "rev-parse", "refs/heads/main"); got != newTip {
		t.Fatalf("main = %s after failed CAS, want untouched %s", got, newTip)
	}
}

// TestResetHardIn pins the dir handling: "" resets the current worktree.
func TestResetHardIn(t *testing.T) {
	newRepo(t)
	writeFile(t, "base.txt", "edited\n")
	mustGit(t, "add", "base.txt")
	if err := ResetHardIn("", "HEAD"); err != nil {
		t.Fatalf("ResetHardIn: %v", err)
	}
	if got := mustGit(t, "status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want clean after reset --hard", got)
	}
}

// TestDiffCachedPatchFor pins the per-target patch reassembler: only the
// wanted hunks are emitted (headers verbatim, bodies intact), and when two
// targets own different hunks of the SAME file, each assembled patch's
// post-image numbers are corrected for the omitted hunks — proven by
// round-tripping every assembled patch through `git apply --cached
// --unidiff-zero --check` against a temp index of the pre-image tree.
func TestDiffCachedPatchFor(t *testing.T) {
	newRepo(t)
	// Pre-image: 8 lines; stage an UNEQUAL-size edit early in the file (1
	// line -> 3 lines, net +2) plus a single-line edit later — the later
	// hunk's NewStart must shift by −2 when the earlier hunk is omitted.
	writeFile(t, "f.txt", "a1\np\nq\nr\ns\nt\nu\nz1\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "seed")
	tip := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "f.txt", "A1\nA2\nA3\np\nq\nr\ns\nt\nu\nZ1\n")
	mustGit(t, "add", "f.txt")

	hunks, unsupported, err := DiffCachedHunks()
	if err != nil || len(hunks) != 2 || len(unsupported) != 0 {
		t.Fatalf("fixture hunks = %+v (%v), unsupported=%v; want exactly two text hunks", hunks, err, unsupported)
	}

	applyCheck := func(t *testing.T, patch []byte) {
		t.Helper()
		tmp := filepath.Join(t.TempDir(), "idx")
		env := append(os.Environ(), "LC_ALL=C", "GIT_INDEX_FILE="+tmp)
		read := exec.Command("git", "read-tree", tip)
		read.Env = env
		if out, err := read.CombinedOutput(); err != nil {
			t.Fatalf("read-tree: %v\n%s", err, out)
		}
		check := exec.Command("git", "apply", "--cached", "--unidiff-zero", "--check", "-")
		check.Env = env
		check.Stdin = bytes.NewReader(patch)
		if out, err := check.CombinedOutput(); err != nil {
			t.Fatalf("assembled patch does not apply to the pre-image tree: %v\n%s\npatch:\n%s", err, out, patch)
		}
	}

	// Each single-hunk selection round-trips against the pre-image tree.
	first, err := DiffCachedPatchFor(hunks[:1])
	if err != nil {
		t.Fatalf("DiffCachedPatchFor(first): %v", err)
	}
	if !strings.Contains(string(first), "A2") || strings.Contains(string(first), "Z1") {
		t.Fatalf("first patch carries the wrong hunks:\n%s", first)
	}
	applyCheck(t, first)

	second, err := DiffCachedPatchFor(hunks[1:])
	if err != nil {
		t.Fatalf("DiffCachedPatchFor(second): %v", err)
	}
	if strings.Contains(string(second), "A2") || !strings.Contains(string(second), "Z1") {
		t.Fatalf("second patch carries the wrong hunks:\n%s", second)
	}
	// The delta-correction: the omitted first hunk is net +2 lines, so the
	// second hunk's NewStart must read 8 (its position with only ITS change),
	// not the full-staged 10.
	if !strings.Contains(string(second), "@@ -8 +8 @@") {
		t.Fatalf("second patch header not delta-corrected:\n%s", second)
	}
	applyCheck(t, second)

	// Selecting both reproduces the full patch semantics.
	both, err := DiffCachedPatchFor(hunks)
	if err != nil {
		t.Fatalf("DiffCachedPatchFor(both): %v", err)
	}
	applyCheck(t, both)
}

// TestDiffCachedPatchesForEquivalence is the batch API's safety net: one
// shared capture must assemble byte-identical per-target patches to N
// independent DiffCachedPatchFor calls — the delta correction is computed
// per target against the same stream's omitted hunks.
func TestDiffCachedPatchesForEquivalence(t *testing.T) {
	newRepo(t)
	writeFile(t, "f.txt", "a1\np\nq\nr\ns\nt\nu\nz1\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "seed")
	writeFile(t, "f.txt", "A1\nA2\nA3\np\nq\nr\ns\nt\nu\nZ1\n")
	writeFile(t, "g.txt", "new-file\n")
	mustGit(t, "add", "-A")

	hunks, _, err := DiffCachedHunks()
	if err != nil {
		t.Fatalf("DiffCachedHunks: %v", err)
	}
	// Split the staged hunks across two targets, then compare the batched
	// assembly with independent single calls.
	first, err := DiffCachedPatchFor(hunks[:1])
	if err != nil {
		t.Fatalf("DiffCachedPatchFor(first): %v", err)
	}
	rest, err := DiffCachedPatchFor(hunks[1:])
	if err != nil {
		t.Fatalf("DiffCachedPatchFor(rest): %v", err)
	}
	batch, err := DiffCachedPatchesFor(map[string][]Hunk{
		"early": hunks[:1],
		"rest":  hunks[1:],
	})
	if err != nil {
		t.Fatalf("DiffCachedPatchesFor: %v", err)
	}
	if string(batch["early"]) != string(first) {
		t.Fatalf("batch[early] differs from single call:\nbatch:\n%s\nsingle:\n%s", batch["early"], first)
	}
	if string(batch["rest"]) != string(rest) {
		t.Fatalf("batch[rest] differs from single call:\nbatch:\n%s\nsingle:\n%s", batch["rest"], rest)
	}
}

// TestCommitRange pins the bounded range walk: exclude..include semantics
// (shared history AND trunk-only commits excluded), the empty range, and the
// injection guards.
func TestCommitRange(t *testing.T) {
	newRepo(t)
	commit := func(name, content string) string {
		writeFile(t, name, content)
		mustGit(t, "add", "-A")
		mustGit(t, "commit", "-q", "-m", name)
		return mustGit(t, "rev-parse", "HEAD")
	}
	mustGit(t, "checkout", "-q", "-b", "feat")
	b := commit("b.txt", "b\n")
	c := commit("c.txt", "c\n")
	mustGit(t, "checkout", "-q", "main")
	d := commit("d.txt", "d\n") // trunk advanced past the branch point

	set, err := CommitRange("main", "feat")
	if err != nil {
		t.Fatalf("CommitRange: %v", err)
	}
	if len(set) != 2 || !set[b] || !set[c] || set[d] {
		t.Fatalf("main..feat = %v, want exactly {B,C} (D on the advanced trunk excluded)", set)
	}

	empty, err := CommitRange("feat", "feat")
	if err != nil || len(empty) != 0 {
		t.Fatalf("feat..feat = %v, %v; want empty, nil", empty, err)
	}

	if _, err := CommitRange("--evil", "feat"); err == nil {
		t.Fatal("CommitRange accepted a flag-like exclude ref")
	}
	if _, err := CommitRange("main", "-bad"); err == nil {
		t.Fatal("CommitRange accepted a flag-like include ref")
	}
}

// TestBlamePorcelain pins the porcelain parser over a real repository: each
// final line maps to its full provenance — the commit that last touched it,
// its line number in that commit, and its path there — across two commits.
func TestBlamePorcelain(t *testing.T) {
	newRepo(t)
	writeFile(t, "f.txt", "one\ntwo\nthree\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "c1")
	c1 := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "f.txt", "one\nTWO!\nthree\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "c2")
	c2 := mustGit(t, "rev-parse", "HEAD")

	lines, err := BlamePorcelain("f.txt", "HEAD")
	if err != nil {
		t.Fatalf("BlamePorcelain: %v", err)
	}
	for n, want := range map[int]BlameLine{
		1: {Commit: c1, OriginalLine: 1, FinalLine: 1, Path: "f.txt"},
		2: {Commit: c2, OriginalLine: 2, FinalLine: 2, Path: "f.txt"},
		3: {Commit: c1, OriginalLine: 3, FinalLine: 3, Path: "f.txt"},
	} {
		if lines[n] != want {
			t.Fatalf("line %d = %+v, want %+v", n, lines[n], want)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %v, want exactly 3 entries", lines)
	}
}

// TestBlamePorcelainProvenance exercises the provenance the absorb gate
// depends on, over real git: a descendant that inserts a line reports the
// owned line's ORIGINAL number (shifted from its final position), and a
// rename reports the historical path — both must survive to the caller, not
// be flattened away.
func TestBlamePorcelainProvenance(t *testing.T) {
	newRepo(t)
	writeFile(t, "f.txt", "top\nx\nbottom\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "c1")
	writeFile(t, "f.txt", "top\nOWNED\nbottom\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "c2")
	c2 := mustGit(t, "rev-parse", "HEAD")
	writeFile(t, "f.txt", "inserted\ntop\nOWNED\nbottom\n")
	mustGit(t, "add", "f.txt")
	mustGit(t, "commit", "-q", "-m", "c3 shifts OWNED to line 3")

	lines, err := BlamePorcelain("f.txt", "HEAD")
	if err != nil {
		t.Fatalf("BlamePorcelain: %v", err)
	}
	if got := lines[3]; got != (BlameLine{Commit: c2, OriginalLine: 2, FinalLine: 3, Path: "f.txt"}) {
		t.Fatalf("line 3 = %+v, want c2's original line 2 shifted to 3", got)
	}

	mustGit(t, "mv", "f.txt", "g.txt")
	mustGit(t, "commit", "-q", "-m", "c4 renames")
	lines, err = BlamePorcelain("g.txt", "HEAD")
	if err != nil {
		t.Fatalf("BlamePorcelain after rename: %v", err)
	}
	if got := lines[3]; got.Commit != c2 || got.OriginalLine != 2 || got.FinalLine != 3 || got.Path != "f.txt" {
		t.Fatalf("line 3 = %+v, want historical path f.txt retained", got)
	}
}

// TestParseBlamePorcelain drives the canned-fixture half of the parser:
// unquoted names with spaces, C-quoted UTF-8 and control-byte names, a
// filename record missing entirely, and malformed headers — every one of
// which must land in the map exactly or not at all.
func TestParseBlamePorcelain(t *testing.T) {
	sha := strings.Repeat("a", 40)

	t.Run("unquoted names with spaces and UTF-8", func(t *testing.T) {
		out := sha + " 2 5\nfilename dir with space/f ile.txt\n\tline\n" +
			sha + " 1 1\nfilename utf-é.txt\n\tligne\n"
		got := parseBlamePorcelain(out)
		if got[5] != (BlameLine{Commit: sha, OriginalLine: 2, FinalLine: 5, Path: "dir with space/f ile.txt"}) {
			t.Fatalf("spaced path = %+v", got[5])
		}
		if got[1] != (BlameLine{Commit: sha, OriginalLine: 1, FinalLine: 1, Path: "utf-é.txt"}) {
			t.Fatalf("utf-8 path = %+v", got[1])
		}
	})

	t.Run("C-quoted octal, quote, and control escapes decode", func(t *testing.T) {
		out := sha + " 1 1\nfilename \"utf-\\303\\251.txt\"\n\tx\n" +
			sha + " 2 2\nfilename \"quo\\\"te.txt\"\n\tx\n" +
			sha + " 3 3\nfilename \"tab\\tname.txt\"\n\tx\n" +
			sha + " 4 4\nfilename \"new\\nline.txt\"\n\tx\n" +
			sha + " 5 5\nfilename \"back\\\\slash.txt\"\n\tx\n"
		got := parseBlamePorcelain(out)
		for n, want := range map[int]string{
			1: "utf-é.txt",
			2: `quo"te.txt`,
			3: "tab\tname.txt",
			4: "new\nline.txt",
			5: `back\slash.txt`,
		} {
			if got[n].Path != want {
				t.Fatalf("line %d path = %q, want %q", n, got[n].Path, want)
			}
		}
	})

	t.Run("malformed records are dropped, never guessed", func(t *testing.T) {
		out := sha + " 1 1\n\tcontent with no filename record\n" + // no filename at all
			"garbage header\n" +
			sha + " 2 2\nfilename \"unbalanced\n\tx\n" + // broken quoting
			sha + " 3\nfilename f.txt\n\tx\n" + // header missing finalLine
			sha + " notnum 4\nfilename f.txt\n\tx\n" + // non-numeric origLine
			sha + " 5 5\nfilename f.txt\n\tok\n"
		got := parseBlamePorcelain(out)
		if len(got) != 1 || got[5].Path != "f.txt" {
			t.Fatalf("got = %+v, want only the well-formed line 5 entry", got)
		}
	})
}

// TestLocalBranchRefSkipsProbeForQualifiedAndSHA pins the spawn-count contract:
// localBranchRef must not spawn `show-ref --verify` for arguments that are
// already unambiguous — HEAD, refs/…-qualified refs, and full 40-hex SHAs (a
// hex value can never be a branch name). Only a bare name costs a probe.
func TestLocalBranchRefSkipsProbeForQualifiedAndSHA(t *testing.T) {
	newRepo(t)
	sha := mustGit(t, "rev-parse", "HEAD")
	log := installGitShim(t, "", 0)

	if _, err := IsAncestor(sha, "HEAD"); err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if _, err := MergeBase(sha, "HEAD"); err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if _, err := MergedInto("refs/heads/main"); err != nil {
		t.Fatalf("MergedInto: %v", err)
	}
	for _, call := range shimCalls(t, log) {
		if strings.HasPrefix(call, "show-ref") {
			t.Fatalf("SHA/qualified refs must not spawn show-ref probes; calls:\n%s",
				strings.Join(shimCalls(t, log), "\n"))
		}
	}
}

func TestParseGitVersion(t *testing.T) {
	for _, tc := range []struct {
		out     string
		want    [3]int
		wantErr bool
	}{
		{"git version 2.46.0", [3]int{2, 46, 0}, false},
		{"git version 2.17.0", [3]int{2, 17, 0}, false},
		{"git version 2.39.5 (Apple Git-154)", [3]int{2, 39, 5}, false},
		{"git version 2.17", [3]int{2, 17, 0}, false},
		{"git version 10.1.2", [3]int{10, 1, 2}, false},
		{"git version 2.46.0.windows.1", [3]int{2, 46, 0}, false},
		{"", [3]int{}, true},
		{"git version devel", [3]int{}, true},
	} {
		got, err := parseGitVersion(tc.out)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseGitVersion(%q) = %v, want error", tc.out, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGitVersion(%q): %v", tc.out, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseGitVersion(%q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, tc := range []struct {
		found, want [3]int
		ok          bool
	}{
		{[3]int{2, 46, 0}, MinVersion, true},
		{[3]int{2, 17, 0}, MinVersion, true},
		{[3]int{2, 16, 9}, MinVersion, false},
		{[3]int{1, 99, 0}, MinVersion, false},
		{[3]int{3, 0, 0}, MinVersion, true},
	} {
		if got := versionAtLeast(tc.found, tc.want); got != tc.ok {
			t.Errorf("versionAtLeast(%v, %v) = %v, want %v", tc.found, tc.want, got, tc.ok)
		}
	}
}

func TestRequireMinVersion(t *testing.T) {
	// The dev environment's git is new enough; the check must pass here.
	if err := RequireMinVersion(); err != nil {
		t.Fatalf("RequireMinVersion: %v", err)
	}
}

// TestGitEnvScrubsRepoRouting pins the security property: variables git exports
// inside hooks (or a user exports by hand) must never redirect a spawned git
// to another repository, index, or object store. User-intent variables — ssh,
// identity, config-file pins — survive untouched.
func TestGitEnvScrubsRepoRouting(t *testing.T) {
	blocked := []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_CEILING_DIRECTORIES",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_PREFIX", "GIT_QUARANTINE_PATH",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
		"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_LITERAL_PATHSPECS", "GIT_EXEC_PATH",
	}
	for _, k := range blocked {
		t.Setenv(k, "/tmp/not-a-repo")
	}
	kept := []string{
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_AUTHOR_NAME",
		"GIT_COMMITTER_NAME", "GIT_SSH_COMMAND", "GIT_EDITOR", "GIT_PAGER",
	}
	for _, k := range kept {
		t.Setenv(k, "user-value")
	}

	env := gitEnv()
	have := map[string]string{}
	lcAll := 0
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		have[k] = v
		if k == "LC_ALL" {
			lcAll++
		}
	}
	for _, k := range blocked {
		if _, ok := have[k]; ok {
			t.Errorf("%s survived gitEnv — a spawned git could be routed to another repo", k)
		}
	}
	for _, k := range kept {
		if have[k] != "user-value" {
			t.Errorf("%s = %q, want the inherited value to survive", k, have[k])
		}
	}
	if lcAll != 1 || have["LC_ALL"] != "C" {
		t.Fatalf("LC_ALL entries = %d, value %q — want exactly one LC_ALL=C", lcAll, have["LC_ALL"])
	}
}

// TestGitEnvExtraEnvOverrides pins the mechanism that keeps the scrub safe:
// runWith appends extraEnv AFTER the filtered base, so plumbing that needs a
// blocked name (absorb's GIT_INDEX_FILE temp index) re-adds it itself and wins.
func TestGitEnvExtraEnvOverrides(t *testing.T) {
	t.Setenv("GIT_INDEX_FILE", "/tmp/ambient-index")
	env := append(gitEnv(), "GIT_INDEX_FILE=/tmp/absorb-temp-index")
	var last string
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); k == "GIT_INDEX_FILE" {
			last = e
		}
	}
	if last != "GIT_INDEX_FILE=/tmp/absorb-temp-index" {
		t.Fatalf("last GIT_INDEX_FILE entry = %q — extraEnv must win over a blocked inherited value", last)
	}
}

package git

import (
	"os"
	"os/exec"
	"path/filepath"
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
		if err := PushRemote("origin", "main", false); err != nil {
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
		if err := PushRemote("origin", "main", false); err != nil {
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
		if err := PushRemote("origin", "main", false); err != nil {
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
		if err := PushRemote("origin", "main", false); err != nil {
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
		if err := PushRemote("origin", "main", false); err != nil {
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
	mustGit(t, "init", "-q", "-b", "main")
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
			run:  func() error { return PushBranches("--receive-pack=true", []string{"main"}, false) },
		},
		{
			name: "push branches branch",
			run:  func() error { return PushBranches("origin", []string{"--force"}, false) },
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

// A ref beginning with "-" (e.g. a corrupt or hostile state.json branch name)
// must be rejected at the boundary so git never parses it as an option.
func TestRevParseRejectsFlagLikeRef(t *testing.T) {
	newRepo(t)
	if _, err := RevParse("--git-dir"); err == nil {
		t.Fatal("RevParse accepted a flag-like ref")
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

func TestTipSubjects(t *testing.T) {
	newRepo(t)
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "t.txt", "t\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "feat subject line")

	subjects, err := TipSubjects()
	if err != nil {
		t.Fatalf("TipSubjects: %v", err)
	}
	if subjects["feat"] != "feat subject line" {
		t.Fatalf("TipSubjects[feat] = %q, want %q", subjects["feat"], "feat subject line")
	}
	if _, ok := subjects["main"]; !ok {
		t.Fatalf("TipSubjects missing main: %v", subjects)
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

	if err := PushRemote("origin", "main", false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := Fetch("origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// A force push (force-with-lease) of an unchanged ref is a no-op success.
	if err := PushRemote("origin", "main", true); err != nil {
		t.Fatalf("Push --force-with-lease: %v", err)
	}

	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "feat.txt", "feat\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "feat")
	if err := PushBranches("origin", []string{"main", "feat"}, false); err != nil {
		t.Fatalf("PushBranches: %v", err)
	}
	for _, branch := range []string{"main", "feat"} {
		if got := mustGit(t, "--git-dir", bare, "rev-parse", "--verify", "refs/heads/"+branch); got == "" {
			t.Fatalf("remote ref for %s is empty", branch)
		}
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

	wts := parseWorktrees(fixture)
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

func TestPushUsesBranchRefspecWhenTagHasSameName(t *testing.T) {
	newRepo(t)
	bare := t.TempDir()
	mustGit(t, "init", "-q", "--bare", bare)
	mustGit(t, "remote", "add", "origin", bare)
	mustGit(t, "tag", "main", "HEAD")

	if err := PushRemote("origin", "main", false); err != nil {
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
	if err := PushRemote("origin", "main", false); err != nil {
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
	if err := PushRemote("origin", "main", false); err != nil {
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

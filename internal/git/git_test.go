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
		desc, err := (RemoteShell{}).FastForward("main", "origin")
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
		if _, err := (RemoteShell{}).FastForward("main", "origin"); err != nil {
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
		if _, err := (RemoteShell{}).FastForward("main", "origin"); err == nil {
			t.Fatal("FastForward on a diverged trunk should error")
		}
		if got := mustGit(t, "rev-parse", "refs/heads/main"); got != local {
			t.Fatalf("failed FastForward moved main to %q, want unchanged %q", got, local)
		}
	})
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
	if _, err := (RemoteShell{}).FastForward("main", "origin"); err != nil {
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

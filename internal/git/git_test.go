package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsAlreadyUpToDate(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"Already up to date.", true},
		{"Already up-to-date.", true},
		{"Updating abc..def Fast-forward", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isAlreadyUpToDate(c.out, errors.New("boom")); got != c.want {
			t.Errorf("isAlreadyUpToDate(%q) = %v, want %v", c.out, got, c.want)
		}
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

	mb, err := MergeBase("main", "feat")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if mb != base {
		t.Fatalf("MergeBase = %q, want %s", mb, base)
	}
	if !IsAncestor("main", "feat") {
		t.Fatalf("main should be an ancestor of feat")
	}
	if IsAncestor("feat", "main") {
		t.Fatalf("feat should not be an ancestor of main")
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

func TestCommitSubjects(t *testing.T) {
	newRepo(t)
	mustGit(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "f.txt", "f\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "first subject")
	writeFile(t, "g.txt", "g\n")
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "second subject")

	subs, err := CommitSubjects("main", "feat")
	if err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	if len(subs) != 2 || subs[0] != "second subject" || subs[1] != "first subject" {
		t.Fatalf("CommitSubjects = %v, want [second, first]", subs)
	}
	// Empty range returns no subjects, no error.
	empty, err := CommitSubjects("main", "main")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty CommitSubjects = %v err=%v", empty, err)
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

func TestFetchAndPush(t *testing.T) {
	newRepo(t)
	bare := t.TempDir()
	mustGit(t, "init", "-q", "--bare", bare)
	mustGit(t, "remote", "add", "origin", bare)

	if err := Push("main", false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := Fetch("origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// A force push (force-with-lease) of an unchanged ref is a no-op success.
	if err := Push("main", true); err != nil {
		t.Fatalf("Push --force-with-lease: %v", err)
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

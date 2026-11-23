package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Worktree-awareness journeys: st never provisions or deletes worktrees (git
// owns that), but it cooperates cleanly with externally-managed git worktrees:
// shared metadata, cross-worktree restack cascades, self-worktree omission in
// log, foreign-worktree safety on delete/sync, paused-rebase guards, git
// worktree lock holds, and stale worktree registrations.

// gitWorktree creates a linked git worktree for branch and returns its path.
func gitWorktree(t *testing.T, r *repo, branch string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt-"+branch)
	r.git("worktree", "add", "-q", wt, branch)
	return wt
}

func TestWorktreeSharesStackState(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out

	wt := gitWorktree(t, r, "feat-a")

	res := r.stInOK(wt, "log", "--json")
	if !strings.Contains(res.stdout, "feat-a") {
		t.Fatalf("worktree st did not see the shared stack state:\n%s", res.stdout)
	}
}

// TestWorktreeAnnotationsInLog asserts that in a multi-worktree repo, `st log`
// annotates OTHER linked worktrees with their path (and dirty state), omits the
// caller's own worktree path to avoid noisy self-tags, and `st status` still
// reports the current worktree path.
func TestWorktreeAnnotationsInLog(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out

	wt := gitWorktree(t, r, "feat-a")

	// log --json from the main worktree annotates feat-a with its path.
	out := r.stOK("log", "--json").stdout
	var root logNode
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("decode log json: %v\n%s", err, out)
	}
	feat := findNode(&root, "feat-a")
	if feat == nil || feat.Worktree == "" || feat.Dirty {
		t.Fatalf("feat-a from main = %+v, want clean worktree annotation\n%s", feat, out)
	}

	// log --json from INSIDE wt omits its own worktree path.
	selfOut := r.stInOK(wt, "log", "--json").stdout
	var selfRoot logNode
	if err := json.Unmarshal([]byte(selfOut), &selfRoot); err != nil {
		t.Fatalf("decode self log json: %v\n%s", err, selfOut)
	}
	selfFeat := findNode(&selfRoot, "feat-a")
	if selfFeat == nil || selfFeat.Worktree != "" {
		t.Fatalf("feat-a from its own worktree should omit worktree path: %+v\n%s", selfFeat, selfOut)
	}

	// Dirty a tracked file in the linked worktree and confirm the flag flips.
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}
	out = r.stOK("log", "--json").stdout
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("decode log json: %v\n%s", err, out)
	}
	if feat := findNode(&root, "feat-a"); feat == nil || !feat.Dirty {
		t.Fatalf("feat-a worktree not reported dirty after edit:\n%s", out)
	}

	// status from inside the worktree still reports its own path.
	sOut := r.stInOK(wt, "status", "--json").stdout
	var st statusJSON
	if err := json.Unmarshal([]byte(sOut), &st); err != nil {
		t.Fatalf("decode status json: %v\n%s", err, sOut)
	}
	if st.Branch != "feat-a" || st.Worktree == "" {
		t.Fatalf("status in worktree missing worktree path: %+v\n%s", st, sOut)
	}
}

// TestWorktreeNewlinePathRoundTrip pins the NUL-framed worktree listing end to
// end: a linked worktree whose path contains newline/tab characters comes back
// byte-exact in `st log --json` while terminal output escapes control bytes.
func TestWorktreeNewlinePathRoundTrip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("newline paths are not representable on windows filesystems")
	}
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	leaf := "wt\nnl\ttab'q ☃"
	wt := filepath.Join(t.TempDir(), leaf)
	if err := exec.Command("git", "-C", r.dir, "worktree", "add", "-q", wt, "feat-a").Run(); err != nil {
		t.Skipf("filesystem/git cannot host a newline worktree path: %v", err)
	}
	want, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatalf("resolve %q: %v", wt, err)
	}

	var root logNode
	if err := json.Unmarshal([]byte(r.stOK("log", "--json").stdout), &root); err != nil {
		t.Fatalf("decode log json: %v", err)
	}
	feat := findNode(&root, "feat-a")
	if feat == nil || feat.Worktree != want {
		t.Fatalf("log worktree annotation = %+v, want path %q", feat, want)
	}

	text := r.stOK("log").stdout
	if strings.Contains(text, want) {
		t.Fatalf("terminal output contains the raw newline path %q:\n%s", want, text)
	}
	if !strings.Contains(text, "feat-a") {
		t.Fatalf("log missing feat-a:\n%s", text)
	}
}

func TestCrossWorktreeRestackCascade(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	_ = gitWorktree(t, r, "feat-a")
	featBefore := r.git("rev-parse", "feat-a")

	r.writeFile("trunk.txt", "t\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "advance trunk")
	mainTip := r.git("rev-parse", "main")

	r.stOK("restack")

	featAfter := r.git("rev-parse", "feat-a")
	if featAfter == featBefore {
		t.Fatalf("feat-a tip unchanged (%s); cross-worktree restack did not run", featAfter)
	}
	if parent := r.git("rev-parse", "feat-a~1"); parent != mainTip {
		t.Fatalf("feat-a parent = %s, want new main tip %s", parent, mainTip)
	}
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree branch = %q, want main", cur)
	}
	r.stOK("doctor")
}

func TestCrossWorktreeRestackConflictRollsBack(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "shared.txt", "A\n", "a")
	r.stOK("checkout", "main")

	wt := gitWorktree(t, r, "feat-a")

	r.writeFile("shared.txt", "X\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "trunk conflicts")

	res := r.st("restack")
	if res.exitCode == 0 {
		t.Fatalf("expected restack to fail on cross-worktree conflict, got exit 0:\n%s", res.stdout)
	}
	cmd := exec.Command("git", "-C", wt, "status", "--porcelain=v2", "--branch")
	cmd.Env = cleanEnv(r.home)
	statusOut, _ := cmd.CombinedOutput()
	if strings.Contains(string(statusOut), "rebase") {
		t.Fatalf("owner worktree left mid-rebase after conflict:\n%s", statusOut)
	}
	r.stOK("doctor")
}

func TestCrossWorktreeRestackSkipsDirty(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	wt := gitWorktree(t, r, "feat-a")
	featBefore := r.git("rev-parse", "feat-a")

	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	r.writeFile("trunk.txt", "t\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "advance trunk")

	res := r.stOK("restack")
	if !strings.Contains(res.stdout, "skipped feat-a") {
		t.Fatalf("restack did not report the dirty skip:\n%s", res.stdout)
	}
	if featAfter := r.git("rev-parse", "feat-a"); featAfter != featBefore {
		t.Fatalf("dirty feat-a was clobbered: %s -> %s", featBefore, featAfter)
	}
}

// TestDeleteRefusesForeignWorktree proves `st delete` never deletes another
// worktree on disk (clean or dirty): it refuses naming the worktree path and
// leaves both the worktree and branch untouched.
func TestDeleteRefusesForeignWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "main")

	wtPath := gitWorktree(t, r, "feat-a")
	res := r.st("delete", "feat-a", "-f")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "checked out in another worktree")
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("foreign worktree was removed: %v", err)
	}
	if !r.branchExists("feat-a") {
		t.Fatal("feat-a must still exist after refused delete")
	}
	r.stOK("doctor")
}

func TestCrossWorktreeRestackMultiLevel(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")
	r.stOK("checkout", "main")

	wtPath := gitWorktree(t, r, "feat-b")

	r.writeFile("trunk.txt", "t\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "advance trunk")
	mainTip := r.git("rev-parse", "main")

	bBefore := r.git("rev-parse", "feat-b")
	cBefore := r.git("rev-parse", "feat-c")

	r.stOK("restack")

	if r.git("rev-parse", "feat-b") == bBefore {
		t.Fatal("feat-b tip unchanged; intermediate worktree branch did not rebase")
	}
	if r.git("rev-parse", "feat-c") == cBefore {
		t.Fatal("feat-c tip unchanged; cascade did not reach top branch")
	}
	if parent := r.git("rev-parse", "feat-a~1"); parent != mainTip {
		t.Fatalf("feat-a parent=%s, want new main tip %s", parent, mainTip)
	}
	if !r.isAncestor("feat-a", "feat-b") || !r.isAncestor("feat-b", "feat-c") || !r.isAncestor(mainTip, "feat-c") {
		t.Fatal("rebased chain ancestry broken")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("feat-b worktree missing after restack: %v", err)
	}
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree HEAD=%q, want main", cur)
	}
	r.stOK("doctor")
}

func TestSyncFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	r.stOK("checkout", "main")
	preTrunk := r.rev("main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")
	r.git("reset", "--hard", preTrunk)

	wtPath := gitWorktree(t, r, "feat-b")

	syncOut := r.stInOK(wtPath, "sync").stdout
	if !strings.Contains(syncOut, "sync complete") {
		t.Fatalf("sync output missing completion:\n%s", syncOut)
	}
	if r.branchExists("feat-a") {
		t.Fatal("feat-a should be pruned after sync")
	}
	if r.rev("main") == preTrunk {
		t.Fatal("sync did not fast-forward local main")
	}
	if !r.isAncestor("main", "feat-b") {
		t.Fatal("feat-b was not restacked onto advanced main")
	}
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree branch = %q after sync, want main", cur)
	}
	if got := r.gitIn(wtPath, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-b" {
		t.Fatalf("feat-b worktree branch = %q after sync, want feat-b", got)
	}
	r.stOK("doctor")
}

func TestSyncFromMergedLinkedWorktreeKeepsWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	r.stOK("checkout", "main")
	wtPath := gitWorktree(t, r, "feat-a")
	preTrunk := r.rev("main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")
	r.git("reset", "--hard", preTrunk)

	syncOut := r.stInOK(wtPath, "sync", "--json").stdout
	if !strings.Contains(syncOut, "sync complete") {
		t.Fatalf("sync output missing completion:\n%s", syncOut)
	}
	if r.branchExists("feat-a") {
		t.Fatal("merged feat-a should have been pruned")
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Fatalf("sync deleted the worktree it ran from (%q): %v", wtPath, statErr)
	}
	if !r.isAncestor("main", "feat-b") {
		t.Fatal("feat-b was not restacked onto advanced main")
	}
	r.stOK("doctor")
}

func TestRestackAllFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")
	r.create("feat-x", "x.txt", "x\n", "x")
	r.create("feat-y", "y.txt", "y\n", "y")
	r.stOK("checkout", "main")

	r.writeFile("m.txt", "m\n")
	r.git("add", "m.txt")
	r.git("commit", "-q", "-m", "advance main")

	wtPath := gitWorktree(t, r, "feat-a")

	allRes := r.stInOK(wtPath, "restack", "--all")

	var root logNode
	if err := json.Unmarshal([]byte(r.stOK("log", "--json").stdout), &root); err != nil {
		t.Fatalf("decode log json: %v", err)
	}
	for _, name := range []string{"feat-a", "feat-x", "feat-y"} {
		node := findNode(&root, name)
		if node == nil || node.NeedsRestack {
			t.Fatalf("%s needsRestack after restack --all: %+v\n%s\n%s", name, node, allRes.stdout, allRes.stderr)
		}
	}
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("main worktree branch = %q, want main", cur)
	}
	if got := r.gitIn(wtPath, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-a" {
		t.Fatalf("feat-a worktree branch = %q, want feat-a", got)
	}
}

func TestSyncConflictContinueFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")

	r.stOK("checkout", "main")
	r.writeFile("f.txt", "MAIN\n")
	r.git("add", "f.txt")
	r.git("commit", "-q", "-m", "trunk advances")

	wt := gitWorktree(t, r, "feat-a")

	res := r.stIn(wt, "sync")
	if res.exitCode != 2 {
		t.Fatalf("sync from linked worktree: exit %d, want 2\nstdout:%s\nstderr:%s", res.exitCode, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr+res.stdout, "st continue") {
		t.Fatalf("conflict output missing continue hint:\n%s\n%s", res.stdout, res.stderr)
	}

	gitDir := r.gitIn(wt, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err != nil {
		t.Fatalf("expected rebase in progress under %q: %v", gitDir, err)
	}

	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("MAIN\nA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.gitIn(wt, "add", "f.txt")
	r.stInOK(wt, "continue")

	if !r.isAncestor("main", "feat-a") {
		t.Fatal("feat-a was not restacked onto advanced main after continue")
	}
	if got := r.gitIn(wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-a" {
		t.Fatalf("worktree HEAD = %q after continue, want feat-a", got)
	}
	r.stOK("doctor")
}

func TestRestackSiblingWorktreeConflictRollsBackFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")
	r.create("feat-x", "f.txt", "X\n", "x")
	r.stOK("checkout", "main")

	r.writeFile("f.txt", "MAIN\n")
	r.git("add", "f.txt")
	r.git("commit", "-q", "-m", "trunk advances")

	w1 := gitWorktree(t, r, "feat-a")
	w2 := gitWorktree(t, r, "feat-x")

	res := r.stIn(w1, "restack", "--all")
	if res.exitCode == 0 || res.exitCode == 2 {
		t.Fatalf("restack --all with sibling-worktree conflict: exit %d, want 1\nstdout:%s\nstderr:%s", res.exitCode, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr+res.stdout, "feat-x") {
		t.Fatalf("error should name conflicting sibling feat-x:\n%s\n%s", res.stdout, res.stderr)
	}
	if !r.isAncestor("main", "feat-a") {
		t.Fatal("feat-a should have rebased onto advanced main before feat-x failed")
	}
	if r.isAncestor("main", "feat-x") {
		t.Fatal("feat-x should have been rolled back")
	}
	w2GitDir := r.gitIn(w2, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(w2GitDir, "rebase-merge")); err == nil {
		t.Fatal("feat-x's worktree should have no paused rebase")
	}
}

func TestRestackCacheInvalidationKeepsConflictPaused(t *testing.T) {
	t.Parallel()

	build := func(t *testing.T) (*repo, string) {
		r := newRepo(t)
		r.initStack()
		r.writeFile("shared.txt", "seed\n")
		r.git("add", "shared.txt")
		r.git("commit", "-q", "-m", "seed")
		r.create("feat-a", "a.txt", "a\n", "a")
		r.writeFile("shared.txt", "edited-by-b\n")
		r.git("add", "shared.txt")
		r.create("feat-b", "b.txt", "b\n", "b")

		wt := gitWorktree(t, r, "main")
		if err := os.WriteFile(filepath.Join(wt, "shared.txt"), []byte("trunk-advance\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r.gitIn(wt, "add", "shared.txt")
		r.gitIn(wt, "commit", "-q", "-m", "advance main")
		return r, wt
	}

	assertPaused := func(t *testing.T, r *repo, res result) {
		t.Helper()
		wantExit(t, res, 2)
		if !r.isAncestor("main", "feat-a") {
			t.Fatal("feat-a must have restacked before feat-b's conflict")
		}
		gitDir := r.gitIn(r.dir, "rev-parse", "--absolute-git-dir")
		if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err != nil {
			t.Fatalf("feat-b's conflict must remain paused in caller's worktree: %v", err)
		}
		r.stOK("abort")
	}

	t.Run("text", func(t *testing.T) {
		r, _ := build(t)
		assertPaused(t, r, r.st("restack", "--all"))
	})
	t.Run("json", func(t *testing.T) {
		r, _ := build(t)
		res := r.st("restack", "--all", "--json")
		wantExit(t, res, 2)
		if !strings.Contains(res.stderr, `"conflict"`) || !strings.Contains(res.stderr, "feat-b") {
			t.Fatalf("conflict envelope missing code/branch:\n%s", res.stderr)
		}
		assertPaused(t, r, res)
	})
}

func TestPausedRebaseInLinkedWorktreeBlocksMutations(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	r.stOK("checkout", "main")
	r.writeFile("a.txt", "diverged\n")
	r.git("add", "a.txt")
	r.git("commit", "-q", "-m", "diverge main")

	wt := gitWorktree(t, r, "feat-a")
	rebase := exec.Command("git", "-C", wt, "rebase", "main")
	rebase.Env = cleanEnv(r.home)
	if out, err := rebase.CombinedOutput(); err == nil {
		t.Fatalf("git -C wt rebase main succeeded, want paused conflict:\n%s", out)
	}

	out := r.stOK("log", "--json").stdout
	var root logNode
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("decode log json: %v\n%s", err, out)
	}
	feat := findNode(&root, "feat-a")
	if feat == nil || !feat.Paused || !feat.Dirty {
		t.Fatalf("feat-a lost pause annotation in log --json: %+v\n%s", feat, out)
	}
	wantWT, _ := filepath.EvalSymlinks(wt)
	if gotWT, _ := filepath.EvalSymlinks(feat.Worktree); gotWT != wantWT {
		t.Fatalf("feat-a worktree = %q, want %q", feat.Worktree, wantWT)
	}
	foundConflict := false
	for _, f := range feat.ConflictedFiles {
		if f == "a.txt" {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Fatalf("feat-a conflictedFiles = %v, want a.txt:\n%s", feat.ConflictedFiles, out)
	}

	res := r.st("delete", "feat-a")
	wantExit(t, res, 1)
	if !strings.Contains(res.stderr, "rebase in progress") || !strings.Contains(res.stderr, wt) {
		t.Fatalf("st delete stderr = %q, want paused-worktree refusal naming %q", res.stderr, wt)
	}

	res = r.st("delete", "feat-a", "--json")
	wantExit(t, res, 1)
	var env struct {
		Error struct {
			Code     string `json:"code"`
			Branch   string `json:"branch"`
			Worktree string `json:"worktree"`
		} `json:"error"`
	}
	decodeStderrEnvelope(t, "paused delete --json", res.stderr, &env)
	if env.Error.Code != "paused" || env.Error.Branch != "feat-a" {
		t.Fatalf("paused envelope = %+v, want code paused + branch feat-a", env.Error)
	}
	if env.Error.Worktree != wantWT && env.Error.Worktree != wt {
		t.Fatalf("paused envelope worktree = %q, want %q", env.Error.Worktree, wt)
	}

	r.stInOK(wt, "abort")
	if got := r.gitIn(wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat-a" {
		t.Fatalf("worktree branch after abort = %q, want feat-a", got)
	}
}

func TestWorktreeLockHold(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	wt := gitWorktree(t, r, "feat-a")
	r.git("worktree", "lock", "--reason", "agent holding", wt)

	var root struct {
		Children []struct {
			Name   string `json:"name"`
			Locked bool   `json:"locked"`
		} `json:"children"`
	}
	if err := json.Unmarshal([]byte(r.stOK("log", "--json").stdout), &root); err != nil {
		t.Fatalf("decode log json: %v", err)
	}
	if len(root.Children) != 1 || !root.Children[0].Locked {
		t.Fatalf("log --json did not mark locked worktree: %+v", root.Children)
	}

	r.git("commit", "--allow-empty", "-q", "-m", "trunk moves")
	out := r.stOK("restack")
	if !strings.Contains(out.stdout, "skipped feat-a") || !strings.Contains(out.stdout, "locked") {
		t.Fatalf("st restack stdout = %q, want lock-naming skip", out.stdout)
	}
	if got := r.git("rev-list", "--count", "feat-a"); got != "2" {
		t.Fatalf("locked feat-a was rebased anyway (rev-list = %s, want 2)", got)
	}

	r.git("worktree", "unlock", wt)
	r.stOK("restack")
	if !r.isAncestor("main", "feat-a") {
		t.Fatal("feat-a should restack after git worktree unlock")
	}
}

func TestStaleWorktreeRegistration(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")

	wt := gitWorktree(t, r, "feat-a")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	if out := r.git("worktree", "list", "--porcelain"); !strings.Contains(out, "prunable") {
		t.Fatalf("worktree list missing prunable entry:\n%s", out)
	}

	out := r.stOK("log", "--json").stdout
	var root logNode
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("decode log json: %v\n%s", err, out)
	}
	if feat := findNode(&root, "feat-a"); feat == nil || feat.Worktree == "" {
		t.Fatalf("feat-a lost worktree annotation in log --json: %+v\n%s", feat, out)
	}

	r.git("commit", "--allow-empty", "-q", "-m", "trunk moves")
	res := r.stOK("restack")
	if !strings.Contains(res.stdout, "skipped feat-a") || !strings.Contains(res.stdout, "stale") {
		t.Fatalf("st restack stdout = %q, want stale-naming skip", res.stdout)
	}
	if got := r.git("rev-list", "--count", "feat-a"); got != "2" {
		t.Fatalf("feat-a was rebased into dead worktree (rev-list = %s, want 2)", got)
	}

	res = r.st("delete", "feat-a", "--force")
	wantExit(t, res, 1)
	if !strings.Contains(res.stderr, "git worktree prune") {
		t.Fatalf("st delete stderr = %q, want prune remedy", res.stderr)
	}
	if !r.branchExists("feat-a") {
		t.Fatal("st delete removed stale-owned branch anyway")
	}

	r.git("worktree", "prune")
	r.stOK("delete", "feat-a", "--force")
	if r.branchExists("feat-a") {
		t.Fatal("feat-a should delete cleanly after git worktree prune")
	}
}

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Stack-mutating journeys, driven black-box: the core lifecycle, conflicts and
// recovery, fold/squash/onto/rename/delete, undo, track/untrack, and sync.
// TestWorktreeSharesStackState asserts the stack metadata (kept under the common
// git dir) is shared across linked worktrees, so st run from a second worktree
// sees the same stack (TEST-10).
func TestWorktreeSharesStackState(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main") // free feat-a so a worktree can check it out

	wt := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", "-q", wt, "feat-a")

	cmd := exec.Command(stBin, "log", "--json")
	cmd.Dir = wt
	cmd.Env = cleanEnv(r.home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("st log in worktree: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "feat-a") {
		t.Fatalf("worktree st did not see the shared stack state:\n%s", out)
	}
}

// TestUndoAfterConflictAbort drives a conflict, aborts it (which leaves the
// bottom branch amended), then undoes the whole mutation back to the pre-mutation
// tip and validates clean (TEST-10).
func TestUndoAfterConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")
	aBefore := r.git("rev-parse", "feat-a")

	r.writeFile("f.txt", "X\n")
	wantExit(t, r.st("modify", "-a"), 2) // conflict restacking feat-b
	r.stOK("abort")

	// The amend on feat-a survived the abort; undo reverts the whole modify.
	r.stOK("undo")
	if aAfter := r.git("rev-parse", "feat-a"); aAfter != aBefore {
		t.Fatalf("feat-a tip = %s after undo, want pre-modify %s", aAfter, aBefore)
	}
	r.stOK("validate")
}

// TestLifecycle exercises the core journey end to end: init, create two stacked
// branches, inspect via log and status (text + JSON), navigate up/down/top/
// bottom, modify the bottom branch and confirm the upstack restacks.
func TestLifecycle(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	// init
	res := r.stOK("init", "--trunk", "main")
	wantStdoutContains(t, res, "initialized stacked (trunk: main)")

	// re-init is idempotent and reports the existing trunk.
	res = r.stOK("init")
	wantStdoutContains(t, res, "already initialized")

	// create two stacked branches.
	r.writeFile("a.txt", "a\n")
	res = r.stOK("create", "feat-a", "-a", "-m", "a")
	wantStdoutContains(t, res, "Created feat-a on top of main")
	r.writeFile("b.txt", "b\n")
	res = r.stOK("create", "feat-b", "-a", "-m", "b")
	wantStdoutContains(t, res, "Created feat-b on top of feat-a")

	if got := r.currentBranch(); got != "feat-b" {
		t.Fatalf("after creates, on %q, want feat-b", got)
	}

	// log text shows the tree with the trunk at the bottom.
	res = r.stOK("log")
	for _, sub := range []string{"feat-a", "feat-b", "main"} {
		wantStdoutContains(t, res, sub)
	}

	// log --json: parse and verify the tree shape.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json is not valid JSON: %v\n%s", err, res.stdout)
	}
	if root.Name != "main" {
		t.Fatalf("log --json root = %q, want main", root.Name)
	}
	a := findNode(&root, "feat-a")
	b := findNode(&root, "feat-b")
	if a == nil || a.Parent != "main" {
		t.Fatalf("feat-a node wrong: %+v", a)
	}
	if b == nil || b.Parent != "feat-a" {
		t.Fatalf("feat-b node wrong: %+v", b)
	}
	if !b.Current {
		t.Fatalf("feat-b should be marked current in log --json")
	}
	if a.TopCommit != "a" {
		t.Fatalf("feat-a topCommit = %q, want a", a.TopCommit)
	}

	// status text + JSON for the current (feat-b) branch.
	res = r.stOK("status")
	wantStdoutContains(t, res, "branch:   feat-b")
	wantStdoutContains(t, res, "parent:   feat-a")

	res = r.stOK("status", "--json")
	var sj statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &sj); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, res.stdout)
	}
	if sj.Branch != "feat-b" || sj.Role != "tracked" || sj.Parent != "feat-a" {
		t.Fatalf("status JSON unexpected: %+v", sj)
	}
	if sj.NeedsRestack == nil || *sj.NeedsRestack {
		t.Fatalf("feat-b should not need restack; got %+v", sj.NeedsRestack)
	}
	if !sj.WorktreeClean {
		t.Fatalf("worktree should be clean: %+v", sj)
	}

	// trunk-only status JSON (role=trunk, children listed, needsRestack omitted).
	r.stOK("checkout", "main")
	res = r.stOK("status", "--json")
	var trunkStatus statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &trunkStatus); err != nil {
		t.Fatalf("trunk status --json invalid: %v\n%s", err, res.stdout)
	}
	if trunkStatus.Role != "trunk" {
		t.Fatalf("main role = %q, want trunk", trunkStatus.Role)
	}
	if trunkStatus.NeedsRestack != nil {
		t.Fatalf("trunk needsRestack should be omitted, got %v", *trunkStatus.NeedsRestack)
	}
	if len(trunkStatus.Children) != 1 || trunkStatus.Children[0] != "feat-a" {
		t.Fatalf("trunk children = %v, want [feat-a]", trunkStatus.Children)
	}

	// Navigation: from main go up to feat-a, up to feat-b (top), back down.
	r.stOK("checkout", "feat-a")
	res = r.stOK("up")
	wantStdoutContains(t, res, "switched to feat-b")
	if r.currentBranch() != "feat-b" {
		t.Fatalf("up did not land on feat-b")
	}

	res = r.stOK("down")
	wantStdoutContains(t, res, "feat-a")
	if r.currentBranch() != "feat-a" {
		t.Fatalf("down did not land on feat-a")
	}

	res = r.stOK("top")
	wantStdoutContains(t, res, "feat-b")
	if r.currentBranch() != "feat-b" {
		t.Fatalf("top did not land on feat-b")
	}

	res = r.stOK("bottom")
	wantStdoutContains(t, res, "feat-a")
	if r.currentBranch() != "feat-a" {
		t.Fatalf("bottom did not land on feat-a")
	}

	// modify the bottom branch (feat-a) and confirm feat-b is restacked onto the
	// amended commit, with an independent file so no conflict occurs.
	r.writeFile("a.txt", "a-modified\n")
	res = r.stOK("modify", "-a")
	wantStdoutContains(t, res, "Amended feat-a")
	wantStdoutContains(t, res, "feat-b")

	if got := r.git("show", "feat-b:a.txt"); got != "a-modified" {
		t.Fatalf("feat-b:a.txt = %q, want a-modified (restacked)", got)
	}

	// restack again is a no-op now.
	r.stOK("checkout", "feat-a")
	res = r.stOK("restack")
	wantStdoutContains(t, res, "everything up to date")

	// validate reports a healthy stack and exits 0.
	res = r.stOK("validate")
	wantStdoutContains(t, res, "no problems found")
}

// TestConflictContinue drives a real merge conflict via modify and resolves it
// with `st continue`, asserting the upstack ends up rebased onto the new tip.
func TestConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	// feat-b edits the same file/line so amending feat-a conflicts on restack.
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	// A conflict maps to the dedicated exit code 2 (see docs/AGENT.md).
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	// A real rebase should be in progress.
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after conflict: %v", err)
	}

	// Resolve and continue.
	r.writeFile("f.txt", "X\nB\n")
	r.git("add", "f.txt")
	res = r.stOK("continue")
	wantStdoutContains(t, res, "continued restack")

	if got := r.git("show", "feat-b:f.txt"); got != "X\nB" {
		t.Fatalf("feat-b:f.txt = %q, want X\\nB", got)
	}
	r.stOK("validate")
}

// TestConflictAbort drives the same conflict but backs out with `st abort`,
// asserting the rebase is gone and a second abort errors with "no rebase".
func TestConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	wantExit(t, res, 2) // conflict

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress: %v", err)
	}

	res = r.stOK("abort")
	wantStdoutContains(t, res, "Aborted the in-progress rebase")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase should be gone after abort, stat err = %v", err)
	}

	// A second abort with nothing in progress errors.
	res = r.st("abort")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "no rebase in progress")
}

func TestOntoConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")

	res := r.st("onto", "main")
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after onto conflict: %v", err)
	}

	r.writeFile("f.txt", "B\n")
	r.git("add", "f.txt")
	r.stOK("continue")
	r.stOK("validate")

	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "stacked", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if strings.Contains(string(data), "pendingReparent") {
		t.Fatalf("state.json still contains pendingReparent after continue:\n%s", data)
	}
	var state struct {
		Branches map[string]struct {
			Parent string `json:"parent"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid: %v\n%s", err, data)
	}
	if got := state.Branches["feat-b"].Parent; got != "main" {
		t.Fatalf("feat-b parent after continue = %q, want main", got)
	}
}

func TestOntoConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")

	res := r.st("onto", "main")
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after onto conflict: %v", err)
	}

	r.stOK("abort")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase should be gone after abort, stat err = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "stacked", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if strings.Contains(string(data), "pendingReparent") {
		t.Fatalf("state.json still contains pendingReparent after abort:\n%s", data)
	}
	var state struct {
		Branches map[string]struct {
			Parent string `json:"parent"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid: %v\n%s", err, data)
	}
	if got := state.Branches["feat-b"].Parent; got != "feat-a" {
		t.Fatalf("feat-b parent after abort = %q, want feat-a", got)
	}
	r.stOK("validate")
}

// TestSyncConflictContinue drives a conflict that occurs *during* sync's restack
// (the trunk advances under a stacked branch), then resolves it (TEST-2).
func TestSyncConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	// feat-a adds f.txt; the trunk then adds f.txt with different content, so
	// restacking feat-a onto the advanced trunk during sync conflicts.
	r.create("feat-a", "f.txt", "A\n", "a")
	r.stOK("checkout", "main")
	r.writeFile("f.txt", "MAIN\n")
	r.git("add", "f.txt")
	r.git("commit", "-q", "-m", "trunk advances")
	r.stOK("checkout", "feat-a")

	res := r.st("sync")
	wantExit(t, res, 2) // conflict maps to the dedicated exit code
	wantStderrContains(t, res, "st continue")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress mid-sync: %v", err)
	}

	// Resolve and continue; the stack must reconcile and validate clean.
	r.writeFile("f.txt", "MAIN\nA\n")
	r.git("add", "f.txt")
	r.stOK("continue")
	r.stOK("validate")
}

// TestModifyJSONRestacksDescendants amends the bottom of a 2-deep stack in --json
// mode, exercising the QuietShell rebase path and asserting the descendant
// actually rebased (TEST-6).
func TestModifyJSONRestacksDescendants(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")
	bBefore := r.git("rev-parse", "feat-b")

	r.writeFile("a.txt", "a2\n")
	res := r.stOK("modify", "-a", "--json")
	var payload struct {
		Branch    string   `json:"branch"`
		Restacked []string `json:"restacked"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &payload); err != nil {
		t.Fatalf("modify --json not parseable: %v\n%s", err, res.stdout)
	}
	if len(payload.Restacked) == 0 {
		t.Fatalf("modify --json reported no restack: %+v", payload)
	}
	if bAfter := r.git("rev-parse", "feat-b"); bAfter == bBefore {
		t.Fatalf("feat-b was not rebased (still %s)", bAfter)
	}
}

// TestModifyMessageReword rewords the bottom branch's commit (the AmendMessage
// real-git path) and asserts the subject changed and the descendant restacked
// (TEST-7).
func TestModifyMessageReword(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "original")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")
	bBefore := r.git("rev-parse", "feat-b")

	r.stOK("modify", "-m", "reworded")
	if subj := r.git("log", "-1", "--format=%s", "feat-a"); subj != "reworded" {
		t.Fatalf("feat-a subject = %q, want reworded", subj)
	}
	if bAfter := r.git("rev-parse", "feat-b"); bAfter == bBefore {
		t.Fatal("feat-b was not restacked after the reword")
	}
}

// TestFold folds the top branch into its parent: the parent absorbs the commits
// and the folded branch is removed.
func TestFold(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-b")

	res := r.stOK("fold")
	wantStdoutContains(t, res, "Folded feat-b into feat-a")
	if r.branchExists("feat-b") {
		t.Fatalf("feat-b git branch should be gone after fold")
	}
	if !r.fileOnBranch("feat-a", "b.txt") {
		t.Fatalf("feat-a should contain b.txt after fold")
	}
}

// TestSquash collapses multiple commits on a branch into one.
func TestSquash(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	// Add a second commit via modify --commit.
	r.writeFile("a2.txt", "a2\n")
	r.stOK("modify", "--commit", "-m", "a2")

	if n := r.git("rev-list", "--count", "main..feat-a"); n != "2" {
		t.Fatalf("expected 2 commits before squash, got %s", n)
	}
	res := r.stOK("squash", "-m", "squashed")
	wantStdoutContains(t, res, "Squashed")
	if n := r.git("rev-list", "--count", "main..feat-a"); n != "1" {
		t.Fatalf("expected 1 commit after squash, got %s", n)
	}
	for _, f := range []string{"a.txt", "a2.txt"} {
		if !r.fileOnBranch("feat-a", f) {
			t.Fatalf("feat-a missing %s after squash", f)
		}
	}
}

// TestOnto re-parents a branch onto the trunk, dropping the old parent's commits.
func TestOnto(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b") // on feat-a
	r.stOK("checkout", "feat-b")

	res := r.stOK("onto", "main")
	wantStdoutContains(t, res, "Moved feat-b onto main")
	if r.fileOnBranch("feat-b", "a.txt") {
		t.Fatalf("feat-b should no longer contain a.txt after moving onto main")
	}

	// status JSON should now report feat-b's parent as main.
	r.stOK("checkout", "feat-b")
	res = r.stOK("status", "--json")
	var sj statusJSON
	if err := json.Unmarshal([]byte(res.stdout), &sj); err != nil {
		t.Fatalf("status --json invalid: %v\n%s", err, res.stdout)
	}
	if sj.Parent != "main" {
		t.Fatalf("feat-b parent after onto = %q, want main", sj.Parent)
	}
}

// TestRename renames a branch and updates child parent pointers.
func TestRename(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")

	res := r.stOK("rename", "renamed-a")
	wantStdoutContains(t, res, "Renamed feat-a -> renamed-a")
	if r.branchExists("feat-a") {
		t.Fatalf("old branch feat-a should be gone")
	}
	if !r.branchExists("renamed-a") {
		t.Fatalf("new branch renamed-a should exist")
	}

	// feat-b's parent must now point at renamed-a (verified via log --json).
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "renamed-a" {
		t.Fatalf("feat-b parent not updated after rename: %+v", b)
	}
}

// TestDeleteReparent deletes a middle branch and re-parents its child onto the
// grandparent, dropping the deleted branch's file from the child's history.
func TestDeleteReparent(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")

	res := r.stOK("delete", "feat-b", "--force")
	wantStdoutContains(t, res, "Deleted feat-b")
	if r.branchExists("feat-b") {
		t.Fatalf("feat-b should be deleted")
	}
	if r.fileOnBranch("feat-c", "b.txt") {
		t.Fatalf("feat-c should no longer contain b.txt after deleting feat-b")
	}
	if !r.fileOnBranch("feat-c", "c.txt") {
		t.Fatalf("feat-c lost its own c.txt")
	}

	// feat-c should now be parented on feat-a.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	c := findNode(&root, "feat-c")
	if c == nil || c.Parent != "feat-a" {
		t.Fatalf("feat-c not re-parented onto feat-a: %+v", c)
	}
}

// TestUndo asserts that undo restores a branch tip after a modify.
func TestUndo(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	before := r.rev("feat-a")

	r.writeFile("a.txt", "a-modified\n")
	r.stOK("modify", "-a")
	if r.rev("feat-a") == before {
		t.Fatalf("modify did not change feat-a tip")
	}

	res := r.stOK("undo")
	wantStdoutContains(t, res, "undid: modify")
	if got := r.rev("feat-a"); got != before {
		t.Fatalf("undo did not restore feat-a: got %s want %s", got, before)
	}

	// Undoing repeatedly eventually empties the journal.
	for i := 0; i < 10; i++ {
		res = r.stOK("undo")
		if strings.Contains(res.stdout, "nothing to undo") {
			return
		}
	}
	t.Fatalf("expected the undo journal to drain to 'nothing to undo'")
}

// TestTrackUntrack covers tracking a plain git branch, the guard errors
// (track the trunk, double-track, untrack the trunk / an untracked branch), and
// untracking with child re-parenting.
func TestTrackUntrack(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	// track-the-trunk is refused (the repo starts on main).
	r.stOK("checkout", "main")
	res := r.st("track")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "cannot track the trunk")

	// Create a plain git branch off main with a commit, then track it.
	r.git("checkout", "-q", "-b", "plain")
	r.writeFile("p.txt", "p\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "p")
	res = r.stOK("track")
	wantStdoutContains(t, res, "Tracking plain (parent: main)")

	// Double-track errors.
	res = r.st("track")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "already tracked")

	// untrack the trunk errors.
	res = r.st("untrack", "main")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "cannot untrack the trunk")

	// untrack an unknown branch errors.
	res = r.st("untrack", "nope")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "not tracked")

	// untrack the tracked branch succeeds.
	res = r.stOK("untrack", "plain")
	wantStdoutContains(t, res, "Untracked plain")
}

// TestRestackGuards covers the dirty-tree guard and the untracked checkout guard.
func TestRestackGuards(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// Dirty working tree blocks restack — mapped to the dedicated exit code 4.
	r.writeFile("dirty.txt", "dirty\n")
	r.git("add", "-A") // staged but uncommitted -> dirty index
	res := r.st("restack")
	wantExit(t, res, 4)
	wantStderrContains(t, res, "working tree is dirty")

	// Clean it up, then check out an untracked name errors.
	r.git("reset", "-q", "HEAD")
	if err := os.Remove(filepath.Join(r.dir, "dirty.txt")); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	res = r.st("checkout", "ghost")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "not a tracked branch")
}

// TestValidateRepairDrift forces drift by deleting a tracked branch behind st's
// back, asserts validate exits non-zero, then repair fixes it and validate
// passes.
func TestValidateRepairDrift(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Delete feat-b's git branch outside st.
	r.stOK("checkout", "main")
	r.git("branch", "-D", "feat-b")

	res := r.st("validate")
	wantExit(t, res, 1)
	wantStdoutContains(t, res, "problems:")
	wantStdoutContains(t, res, "feat-b")

	res = r.stOK("repair")
	wantStdoutContains(t, res, "repaired:")

	res = r.stOK("validate")
	wantStdoutContains(t, res, "no problems found")
}

// TestSyncPrunesMerged sets up a bare remote, merges the bottom branch into the
// trunk on the remote, and asserts `st sync` fast-forwards the trunk, prunes the
// merged branch, and restacks the survivor.
func TestSyncPrunesMerged(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.gitIn(filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Merge feat-a into main locally and push, simulating feat-a landing.
	r.stOK("checkout", "main")
	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	r.git("push", "-q", "origin", "main")

	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "deleted: feat-a")

	if r.branchExists("feat-a") {
		t.Fatalf("feat-a should be pruned after sync")
	}
	if !r.branchExists("feat-b") {
		t.Fatalf("feat-b should survive sync")
	}

	// feat-b is now re-parented onto main and the stack validates clean.
	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "main" {
		t.Fatalf("feat-b should be re-parented onto main after prune: %+v", b)
	}
	r.stOK("validate")
}

// TestSyncNoRemote asserts sync is a clean no-op when no remote is configured.
func TestSyncNoRemote(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "skipped (no remote)")
}

// TestRestackDryRun previews what a restack would rebase and changes nothing.
func TestRestackDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "g.txt", "B\n", "b") // independent file: no conflict
	r.stOK("checkout", "feat-a")

	// Amend feat-a so feat-b drifts.
	r.writeFile("f.txt", "A2\n")
	r.git("commit", "-qa", "--amend", "--no-edit")
	before := r.git("rev-parse", "feat-b")

	res := r.stOK("restack", "--dry-run")
	wantStdoutContains(t, res, "would restack: feat-b")
	if after := r.git("rev-parse", "feat-b"); after != before {
		t.Fatalf("dry-run must not move feat-b (before=%s after=%s)", before, after)
	}

	res = r.stOK("restack", "--dry-run", "--json")
	wantStdoutContains(t, res, `"dryRun": true`)
}

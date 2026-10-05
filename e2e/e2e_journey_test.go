package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// recovery, fold/squash/onto/rename/delete, undo, track/untrack, and sync.
// TestWorktreeSharesStackState asserts the stack metadata (kept under the common

// TestShellInstallEmitsShim asserts `st shell install` prints a cd shim that
// references the directive file, for the shell the user names.
func TestShellInstallEmitsShim(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	out := r.stOK("shell", "install", "bash").stdout
	if !strings.Contains(out, "builtin cd") || !strings.Contains(out, "ST_CD_FILE") {
		t.Fatalf("shell install bash did not emit a cd shim:\n%s", out)
	}
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

// TestTrackNamedBranch covers `st track <name>`: a branch is adopted without
// being checked out, its parent inferred from the commit graph exactly as the
// current-branch path does, and bad names are refused.
func TestTrackNamedBranch(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// A plain git branch forked off feat-a, created without checking it out.
	r.git("branch", "feat-b", "feat-a")
	r.git("checkout", "-q", "main")

	res := r.stOK("track", "feat-b")
	wantStdoutContains(t, res, "Tracking feat-b (parent: feat-a)")
	if cur := r.currentBranch(); cur != "main" {
		t.Fatalf("track moved HEAD to %q", cur)
	}

	// The adopted branch shows up in the log under its inferred parent.
	res = r.stOK("log")
	wantStdoutContains(t, res, "feat-b")

	// Unknown and already-tracked names are refused.
	res = r.st("track", "ghost")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "does not exist")
	res = r.st("track", "feat-b")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "already tracked")
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
	wantStderrContains(t, res, `branch "ghost" is not tracked`)
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

func TestFoldDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")
	r.stOK("checkout", "feat-a")
	r.create("feat-d", "d.txt", "d\n", "d")
	r.stOK("checkout", "feat-b")

	beforeLog := r.stOK("log", "--json").stdout
	beforeTips := captureTips(t, r, "feat-a", "feat-b", "feat-c", "feat-d")
	got := decodeDryRunResult(t, r.stOK("fold", "--dry-run", "--json"))
	assertDryRunResult(t, got, "feat-a", []string{"feat-d"}, []string{"feat-b"})

	if afterLog := r.stOK("log", "--json").stdout; afterLog != beforeLog {
		t.Fatalf("fold dry-run changed log\nbefore:\n%s\nafter:\n%s", beforeLog, afterLog)
	}
	assertTips(t, r, beforeTips)
	if !r.branchExists("feat-b") {
		t.Fatal("fold dry-run deleted feat-b")
	}
}

func TestSquashDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.writeFile("a2.txt", "a2\n")
	r.stOK("modify", "--commit", "-m", "a2")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")

	beforeLog := r.stOK("log", "--json").stdout
	beforeTips := captureTips(t, r, "feat-a", "feat-b")
	got := decodeDryRunResult(t, r.stOK("squash", "-m", "squashed", "--dry-run", "--json"))
	assertDryRunResult(t, got, "feat-a", []string{"feat-b"}, nil)

	if afterLog := r.stOK("log", "--json").stdout; afterLog != beforeLog {
		t.Fatalf("squash dry-run changed log\nbefore:\n%s\nafter:\n%s", beforeLog, afterLog)
	}
	assertTips(t, r, beforeTips)
}

func TestSquashDryRunSkipsDirtyLinkedWorktreeDescendant(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.writeFile("a2.txt", "a2\n")
	r.stOK("modify", "--commit", "-m", "a2")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.stOK("checkout", "feat-a")

	wt := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", "-q", wt, "feat-b")
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty linked worktree: %v", err)
	}

	beforeLog := r.stOK("log", "--json").stdout
	beforeTips := captureTips(t, r, "feat-a", "feat-b")
	got := decodeDryRunResult(t, r.stOK("squash", "-m", "squashed", "--dry-run", "--json"))
	assertDryRunResult(t, got, "feat-a", nil, nil)
	wantNote := "skipped feat-b: its worktree is dirty (commit/stash there, then re-run)"
	if !reflect.DeepEqual(got.Notes, []string{wantNote}) {
		t.Fatalf("notes = %v, want [%q]", got.Notes, wantNote)
	}

	if afterLog := r.stOK("log", "--json").stdout; afterLog != beforeLog {
		t.Fatalf("squash dry-run changed log\nbefore:\n%s\nafter:\n%s", beforeLog, afterLog)
	}
	assertTips(t, r, beforeTips)
}

func TestOntoDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")
	r.stOK("checkout", "feat-b")

	beforeLog := r.stOK("log", "--json").stdout
	beforeTips := captureTips(t, r, "feat-b", "feat-c")
	got := decodeDryRunResult(t, r.stOK("onto", "main", "--dry-run", "--json"))
	assertDryRunResult(t, got, "feat-b", []string{"feat-c"}, nil)

	if afterLog := r.stOK("log", "--json").stdout; afterLog != beforeLog {
		t.Fatalf("onto dry-run changed log\nbefore:\n%s\nafter:\n%s", beforeLog, afterLog)
	}
	assertTips(t, r, beforeTips)
}

func TestDeleteDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")
	r.create("feat-c", "c.txt", "c\n", "c")

	beforeLog := r.stOK("log", "--json").stdout
	beforeTips := captureTips(t, r, "feat-b", "feat-c")
	got := decodeDryRunResult(t, r.stOK("delete", "feat-b", "--force", "--dry-run", "--json"))
	assertDryRunResult(t, got, "", []string{"feat-c"}, []string{"feat-b"})

	if afterLog := r.stOK("log", "--json").stdout; afterLog != beforeLog {
		t.Fatalf("delete dry-run changed log\nbefore:\n%s\nafter:\n%s", beforeLog, afterLog)
	}
	assertTips(t, r, beforeTips)
	if !r.branchExists("feat-b") {
		t.Fatal("delete dry-run deleted feat-b")
	}
}

func TestSyncDryRunRefusesDirtyPrunedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	r.stOK("checkout", "main")
	r.git("merge", "-q", "--ff-only", "feat-a")

	wt := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", "-q", wt, "feat-a")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty linked worktree: %v", err)
	}

	res := r.st("sync", "--dry-run", "--json")
	if res.exitCode == 0 {
		t.Fatalf("sync dry-run should fail for a dirty pruned worktree\nstdout:\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr+res.stdout, "uncommitted changes in its worktree") {
		t.Fatalf("sync dry-run error should mention the dirty worktree\nstdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}
	if !r.branchExists("feat-a") {
		t.Fatal("sync dry-run must not delete feat-a")
	}
	out := r.stOK("log", "--json").stdout
	var root logNode
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("log --json invalid: %v\n%s", err, out)
	}
	if findNode(&root, "feat-a") == nil {
		t.Fatalf("feat-a should remain tracked after refused sync dry-run:\n%s", out)
	}
}

type dryRunResult struct {
	Branch    string   `json:"branch"`
	Restacked []string `json:"restacked"`
	Deleted   []string `json:"deleted"`
	Notes     []string `json:"notes"`
	DryRun    bool     `json:"dryRun"`
}

func decodeDryRunResult(t *testing.T, res result) dryRunResult {
	t.Helper()
	var got dryRunResult
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("dry-run JSON invalid: %v\n%s", err, res.stdout)
	}
	return got
}

func assertDryRunResult(t *testing.T, got dryRunResult, branch string, restacked, deleted []string) {
	t.Helper()
	if !got.DryRun {
		t.Fatalf("dryRun = false in %+v", got)
	}
	if got.Branch != branch {
		t.Fatalf("branch = %q, want %q in %+v", got.Branch, branch, got)
	}
	if !reflect.DeepEqual(got.Restacked, restacked) {
		t.Fatalf("restacked = %v, want %v in %+v", got.Restacked, restacked, got)
	}
	if !reflect.DeepEqual(got.Deleted, deleted) {
		t.Fatalf("deleted = %v, want %v in %+v", got.Deleted, deleted, got)
	}
}

func captureTips(t *testing.T, r *repo, names ...string) map[string]string {
	t.Helper()
	tips := make(map[string]string, len(names))
	for _, name := range names {
		tips[name] = r.git("rev-parse", name)
	}
	return tips
}

func assertTips(t *testing.T, r *repo, want map[string]string) {
	t.Helper()
	for name, tip := range want {
		if got := r.git("rev-parse", name); got != tip {
			t.Fatalf("%s tip changed: before=%s after=%s", name, tip, got)
		}
	}
}

// TestAbsorbDryRunMapping drives the absorb attribution black-box on a real
// 3-branch stack: a staged single-target edit maps to the branch whose tip
// owns the line, refusals are empty, and NOTHING mutates (dry-run contract).
func TestAbsorbDryRunMapping(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	// Each branch owns a distinct line of shared.txt.
	r.create("feat-a", "shared.txt", "A\n", "a")
	r.create("feat-b", "shared.txt", "A\nB\n", "b")
	r.create("feat-c", "shared.txt", "A\nB\nC\n", "c")

	// Stage a single-target edit to line 1 (owned by feat-a's tip).
	r.writeFile("shared.txt", "A!\nB\nC\n")
	r.git("add", "shared.txt")

	tipsBefore := map[string]string{}
	for _, b := range []string{"main", "feat-a", "feat-b", "feat-c"} {
		tipsBefore[b] = r.rev(b)
	}

	out := r.stOK("absorb", "--dry-run", "--json").stdout
	var res struct {
		Summary  string `json:"summary"`
		Absorbed []struct {
			File   string `json:"file"`
			Lines  string `json:"lines"`
			Branch string `json:"branch"`
			Commit string `json:"commit"`
		} `json:"absorbed"`
		Refused []struct {
			File   string `json:"file"`
			Lines  string `json:"lines"`
			Reason string `json:"reason"`
		} `json:"refused"`
		DryRun bool `json:"dryRun"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode absorb json: %v\n%s", err, out)
	}
	if !res.DryRun {
		t.Fatal("absorb --dry-run result not marked dryRun")
	}
	if len(res.Absorbed) != 1 || len(res.Refused) != 0 {
		t.Fatalf("result = %+v, want exactly one absorbed hunk", res)
	}
	got := res.Absorbed[0]
	if got.Branch != "feat-a" || got.File != "shared.txt" || got.Lines != "1" {
		t.Fatalf("absorbed = %+v, want feat-a shared.txt:1", got)
	}
	if got.Commit != tipsBefore["feat-a"] {
		t.Fatalf("absorbed commit = %s, want feat-a tip %s", got.Commit, tipsBefore["feat-a"])
	}

	// Zero mutation: every tip unchanged, and the staged hunk still staged.
	for b, tip := range tipsBefore {
		if r.rev(b) != tip {
			t.Fatalf("%s moved during a dry-run absorb", b)
		}
	}
	if diff := r.git("diff", "--cached", "--name-only"); !strings.Contains(diff, "shared.txt") {
		t.Fatalf("dry-run unstaged the hunk; diff --cached = %q", diff)
	}
	r.stOK("validate")
}

// TestTrackAllDryRun previews the bulk adopt through the real binary: the
// inferred parent map is emitted in the JSON `tracked` field while nothing is
// recorded — the apply arm then lands the same map.
func TestTrackAllDryRun(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")

	// Two untracked branches: plain-b hangs off feat-a, plain-c off main.
	r.git("branch", "plain-b", "feat-a")
	r.git("branch", "plain-c", "main")

	res := r.st("track", "--all", "--dry-run", "--json")
	wantExit(t, res, 0)
	var dry map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &dry); err != nil {
		t.Fatalf("track --all --dry-run --json invalid: %v\n%s", err, res.stdout)
	}
	if dry["dryRun"] != true {
		t.Fatalf("dryRun = %v, want true", dry["dryRun"])
	}
	tracked, _ := dry["tracked"].(map[string]any)
	if tracked["plain-b"] != "feat-a" || tracked["plain-c"] != "main" {
		t.Fatalf("tracked = %v, want {plain-b: feat-a, plain-c: main}", tracked)
	}
	// The preview recorded nothing: a bare `st track` on the same repo still
	// sees plain-c as untracked.
	res = r.stOK("track", "plain-c")
	wantStdoutContains(t, res, "Tracking plain-c")
}

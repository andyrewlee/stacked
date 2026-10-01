package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Undo journeys, including the shim-teleport cases (undo run from a
// created worktree must emit/consume the teleport directive correctly).

func TestUndoCreateWorktreeFromCreatedWorktreeWithShim(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	out := r.stOK("create", "feat-a", "--worktree", "--json").stdout
	var created struct {
		Worktree string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode create --worktree json: %v\n%s", err, out)
	}
	directive := filepath.Join(t.TempDir(), "cd")
	res := r.stInEnv(created.Worktree, []string{"ST_CD_FILE=" + directive}, "undo")
	if res.exitCode != 0 {
		t.Fatalf("st undo from created worktree: exit %d\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}
	got, err := os.ReadFile(directive)
	if err != nil {
		t.Fatalf("read cd directive: %v", err)
	}
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	wantResolved, _ := filepath.EvalSymlinks(r.dir)
	if gotResolved != wantResolved {
		t.Fatalf("undo cd directive = %q, want main worktree %q", got, r.dir)
	}
	if r.branchExists("feat-a") {
		t.Fatal("undo left feat-a branch behind")
	}
	if _, err := os.Stat(created.Worktree); !os.IsNotExist(err) {
		t.Fatalf("undo left created worktree at %q: %v", created.Worktree, err)
	}
	r.stOK("validate")
}

func TestFailedUndoFromDirtyCreatedWorktreeDoesNotWriteShimDirective(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	out := r.stOK("create", "feat-a", "--worktree", "--json").stdout
	var created struct {
		Worktree string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode create --worktree json: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(created.Worktree, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty created worktree: %v", err)
	}

	directive := filepath.Join(t.TempDir(), "cd")
	res := r.stInEnv(created.Worktree, []string{"ST_CD_FILE=" + directive}, "undo")
	if res.exitCode == 0 {
		t.Fatalf("st undo from dirty created worktree succeeded; want failure\nstdout:\n%s\nstderr:\n%s",
			res.stdout, res.stderr)
	}
	if b, err := os.ReadFile(directive); err == nil && len(b) > 0 {
		t.Fatalf("failed undo wrote cd directive %q", b)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read cd directive after failed undo: %v", err)
	}
	if !r.branchExists("feat-a") {
		t.Fatal("failed undo deleted feat-a")
	}
	if _, err := os.Stat(created.Worktree); err != nil {
		t.Fatalf("failed undo removed created worktree %q: %v", created.Worktree, err)
	}
}

// TestUndoCreateRemovesManuallyMaterializedWorktree covers the recovery
// sequence the tool itself recommends after a failed `st create --worktree`:
// create the branch plainly, switch away, materialize its worktree with
// `st worktree`, then undo the create. The undo journal never recorded the
// worktree, so undo must still release the clean linked worktree before
// deleting the branch.
func TestUndoCreateRemovesManuallyMaterializedWorktree(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	r.stOK("create", "feat-x")
	r.stOK("checkout", "main")
	out := r.stOK("worktree", "feat-x", "--json").stdout
	var created struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode worktree json: %v\n%s", err, out)
	}

	r.stOK("undo")
	if r.branchExists("feat-x") {
		t.Fatal("undo left feat-x branch behind")
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("undo left created worktree at %q: %v", created.Path, err)
	}
	if lsOut := r.stOK("worktree", "ls").stdout; strings.Contains(lsOut, "feat-x") {
		t.Fatalf("worktree ls still lists feat-x:\n%s", lsOut)
	}
	r.stOK("validate")
}

// TestUndoFromUnrecordedCreatedWorktreeWithShim covers the gap between the two
// journeys above: the branch was created plainly (the journal recorded no
// worktree), its worktree was materialized afterward by `st worktree`, and
// `st undo` runs from INSIDE it. Undo still deletes the branch — and the
// worktree with it — so the shim must teleport the caller to the main
// worktree first rather than letting the removal delete the process's cwd.
func TestUndoFromUnrecordedCreatedWorktreeWithShim(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	r.stOK("create", "feat-x")
	r.stOK("checkout", "main")
	out := r.stOK("worktree", "feat-x", "--json").stdout
	var created struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode worktree json: %v\n%s", err, out)
	}

	directive := filepath.Join(t.TempDir(), "cd")
	res := r.stInEnv(created.Path, []string{"ST_CD_FILE=" + directive}, "undo")
	if res.exitCode != 0 {
		t.Fatalf("st undo from unrecorded created worktree: exit %d\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}
	got, err := os.ReadFile(directive)
	if err != nil {
		t.Fatalf("read cd directive: %v", err)
	}
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	wantResolved, _ := filepath.EvalSymlinks(r.dir)
	if gotResolved != wantResolved {
		t.Fatalf("undo cd directive = %q, want main worktree %q", got, r.dir)
	}
	if r.branchExists("feat-x") {
		t.Fatal("undo left feat-x branch behind")
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("undo left created worktree at %q: %v", created.Path, err)
	}
	r.stOK("validate")
}

func TestUndoCreateWorktreeChildOfLinkedParentWithShim(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	r.create("feat-a", "a.txt", "a\n", "a")
	r.stOK("checkout", "main")
	parentOut := r.stOK("worktree", "feat-a", "--json").stdout
	var parent struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(parentOut), &parent); err != nil {
		t.Fatalf("decode parent worktree json: %v\n%s", err, parentOut)
	}

	createDirective := filepath.Join(t.TempDir(), "create-cd")
	createRes := r.stInEnv(parent.Path, []string{"ST_CD_FILE=" + createDirective},
		"create", "feat-b", "--worktree", "--json")
	if createRes.exitCode != 0 {
		t.Fatalf("st create feat-b --worktree from parent worktree: exit %d\nstdout:\n%s\nstderr:\n%s",
			createRes.exitCode, createRes.stdout, createRes.stderr)
	}
	childOut := createRes.stdout
	var child struct {
		Worktree string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(childOut), &child); err != nil {
		t.Fatalf("decode child worktree json: %v\n%s", err, childOut)
	}

	undoDirective := filepath.Join(t.TempDir(), "undo-cd")
	undoRes := r.stInEnv(child.Worktree, []string{"ST_CD_FILE=" + undoDirective}, "undo")
	if undoRes.exitCode != 0 {
		t.Fatalf("st undo from child worktree: exit %d\nstdout:\n%s\nstderr:\n%s",
			undoRes.exitCode, undoRes.stdout, undoRes.stderr)
	}
	got, err := os.ReadFile(undoDirective)
	if err != nil {
		t.Fatalf("read undo cd directive: %v", err)
	}
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	wantResolved, _ := filepath.EvalSymlinks(parent.Path)
	if gotResolved != wantResolved {
		t.Fatalf("undo cd directive = %q, want parent worktree %q", got, parent.Path)
	}
	if r.branchExists("feat-b") {
		t.Fatal("undo left feat-b branch behind")
	}
	if _, err := os.Stat(child.Worktree); !os.IsNotExist(err) {
		t.Fatalf("undo left child worktree at %q: %v", child.Worktree, err)
	}
	if _, err := os.Stat(parent.Path); err != nil {
		t.Fatalf("undo removed parent worktree %q: %v", parent.Path, err)
	}
	r.stOK("validate")
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

// TestUndoList asserts `st undo --list` previews the journal without reverting:
// newest entry first with index 1 naming what bare undo would revert, an empty
// journal matching bare undo's "nothing to undo", and the journal untouched —
// a bare undo still works after listing.
func TestUndoList(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()

	// Empty journal: same notice as bare undo, and an empty entries list in
	// JSON, both exit 0.
	res := r.stOK("undo", "--list")
	wantStdoutContains(t, res, "nothing to undo")
	res = r.stOK("undo", "--list", "--json")
	var empty struct {
		Entries []struct {
			Index           int      `json:"index"`
			Label           string   `json:"label"`
			CurrentBranch   string   `json:"currentBranch"`
			CreatedBranches []string `json:"createdBranches"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &empty); err != nil {
		t.Fatalf("undo --list --json not parseable: %v\n%s", err, res.stdout)
	}
	if len(empty.Entries) != 0 {
		t.Fatalf("empty journal listed %d entries, want 0:\n%s", len(empty.Entries), res.stdout)
	}

	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	res = r.stOK("undo", "--list")
	wantStdoutContains(t, res, "1: create")
	wantStdoutContains(t, res, "created feat-b")
	wantStdoutContains(t, res, "2: create")
	wantStdoutContains(t, res, "created feat-a")

	res = r.stOK("undo", "--list", "--json")
	var listed struct {
		Entries []struct {
			Index           int      `json:"index"`
			Label           string   `json:"label"`
			CurrentBranch   string   `json:"currentBranch"`
			CreatedBranches []string `json:"createdBranches"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &listed); err != nil {
		t.Fatalf("undo --list --json not parseable: %v\n%s", err, res.stdout)
	}
	if len(listed.Entries) != 2 || listed.Entries[0].Index != 1 ||
		listed.Entries[0].Label != "create" || listed.Entries[0].CurrentBranch != "feat-a" ||
		len(listed.Entries[0].CreatedBranches) != 1 || listed.Entries[0].CreatedBranches[0] != "feat-b" {
		t.Fatalf("undo --list --json = %+v, want index-1 create entry for feat-b", listed.Entries)
	}

	// --list did not consume the journal: bare undo still reverts the create.
	r.stOK("undo")
	if r.branchExists("feat-b") {
		t.Fatal("undo after --list left created branch feat-b behind")
	}
	res = r.stOK("undo", "--list")
	wantStdoutContains(t, res, "1: create")
	wantStdoutContains(t, res, "created feat-a")
	if strings.Contains(res.stdout, "2:") {
		t.Fatalf("undo --list after one undo shows a stale second entry:\n%s", res.stdout)
	}
}

func TestSyncUndoRestoresPrunedBranchesAndTrunk(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.gitIn(filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a.txt", "a\n", "a")
	r.create("feat-b", "b.txt", "b\n", "b")

	preFeatA := r.rev("feat-a")
	preFeatB := r.rev("feat-b")
	r.stOK("checkout", "main")
	preTrunk := r.rev("main")

	r.git("merge", "-q", "--no-ff", "feat-a", "-m", "merge feat-a")
	if r.rev("main") == preTrunk {
		t.Fatal("test setup did not advance local main before push")
	}
	r.git("push", "-q", "origin", "main")
	r.git("reset", "--hard", preTrunk)
	if got := r.rev("main"); got != preTrunk {
		t.Fatalf("local main after reset = %s, want pre-sync trunk %s", got, preTrunk)
	}

	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "deleted: feat-a")
	if r.branchExists("feat-a") {
		t.Fatal("feat-a should be pruned after sync")
	}
	if got := r.rev("main"); got == preTrunk {
		t.Fatalf("sync did not fast-forward main from %s", preTrunk)
	}

	res = r.stOK("undo")
	wantStdoutContains(t, res, "undid: sync")
	if !r.branchExists("feat-a") {
		t.Fatal("undo did not restore pruned feat-a")
	}
	if got := r.rev("feat-a"); got != preFeatA {
		t.Fatalf("feat-a after undo = %s, want %s", got, preFeatA)
	}
	if got := r.rev("main"); got != preTrunk {
		t.Fatalf("main after undo = %s, want pre-sync trunk %s", got, preTrunk)
	}
	if got := r.rev("feat-b"); got != preFeatB {
		t.Fatalf("feat-b after undo = %s, want %s", got, preFeatB)
	}

	out := r.stOK("log", "--json").stdout
	var root logNode
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("log --json invalid after undo: %v\n%s", err, out)
	}
	a := findNode(&root, "feat-a")
	if a == nil || a.Parent != "main" {
		t.Fatalf("feat-a after undo = %+v, want parent main\n%s", a, out)
	}
	b := findNode(&root, "feat-b")
	if b == nil || b.Parent != "feat-a" {
		t.Fatalf("feat-b after undo = %+v, want parent feat-a\n%s", b, out)
	}
	r.stOK("validate")
}

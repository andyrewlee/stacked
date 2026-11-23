package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Undo journeys (1-step undo journal): conflict rollback, basic modify undo,
// sync undo restoring pruned branches + trunk, and rejection of extra args.

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
	r.stOK("doctor")
}

// TestUndo asserts that 1-step undo restores a branch tip after a modify and
// drains to "nothing to undo" on a second call.
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

	// Undoing repeatedly drains the journal one step at a time.
	res = r.stOK("undo")
	wantStdoutContains(t, res, "undid: create")
	res = r.stOK("undo")
	wantStdoutContains(t, res, "nothing to undo")

	// Positional args (old multi-step counts) are rejected.
	res = r.st("undo", "2")
	wantExit(t, res, 1)
	if !strings.Contains(res.stderr, "takes no positional arguments") {
		t.Fatalf("undo 2 stderr = %q, want positional argument refusal", res.stderr)
	}
}

func TestSyncUndoRestoresPrunedBranchesAndTrunk(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.initBare(bare)
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
	r.stOK("doctor")
}

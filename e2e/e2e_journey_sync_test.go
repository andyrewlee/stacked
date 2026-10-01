package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Sync and prune journeys: merged/squash-merged pruning, rename
// preservation, and the no-remote edge.

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

// TestSyncPrunesSquashMerged exercises the content-containment prune: feat-a's
// PR squash-merges "on the host" — its diff lands on the trunk as ONE commit
// with no ancestry link, so `git branch --merged`/`git cherry` cannot see it —
// and `st sync` prunes it anyway because every change it made is already in
// the trunk's tree. feat-b (unique content) survives and re-parents onto main.
func TestSyncPrunesSquashMerged(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.gitIn(filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	r.create("feat-a", "a1.txt", "a1\n", "a1")
	// A second commit on feat-a: the squash-merged PR is multi-commit, the
	// case `git cherry`'s per-commit patch-id mapping provably cannot see.
	r.writeFile("a2.txt", "a2\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "a2")
	r.create("feat-b", "b.txt", "b\n", "b")

	// Squash-merge feat-a on main, push it, then rewind local main so sync has
	// a real fetch + fast-forward before it detects the landed content.
	r.stOK("checkout", "main")
	preTrunk := r.rev("main")
	r.git("merge", "-q", "--squash", "feat-a")
	r.git("commit", "-q", "-m", "squash feat-a")
	r.git("push", "-q", "origin", "main")
	r.git("reset", "-q", "--hard", preTrunk)

	// Premise: feat-a's tip is not an ancestor of the remote trunk.
	if r.isAncestor("feat-a", "origin/main") {
		t.Fatal("test setup broken: squash-merged feat-a must not be an ancestor of origin/main")
	}

	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "deleted: feat-a")

	if r.branchExists("feat-a") {
		t.Fatal("squash-merged feat-a should be pruned after sync")
	}
	if !r.branchExists("feat-b") {
		t.Fatal("feat-b carries unique content and must survive sync")
	}
	if !r.fileOnBranch("feat-b", "b.txt") {
		t.Fatal("feat-b lost its content")
	}

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

// TestSyncPreservesUncontainedRename pins the rename-vs-copy containment fix:
// a branch that renames A to B is NOT contained in an upstream that merely
// copied A to B while keeping A — the branch's deletion of A never landed —
// so sync must keep the branch (ref and stack metadata), while a genuinely
// contained sibling is still pruned.
func TestSyncPreservesUncontainedRename(t *testing.T) {
	t.Parallel()
	r := newRepo(t)

	bare := filepath.Join(t.TempDir(), "remote.git")
	r.gitIn(filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	r.git("remote", "add", "origin", bare)
	r.git("push", "-q", "-u", "origin", "main")

	r.initStack()
	// Shared file A on main before branching.
	r.writeFile("a.txt", "a\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "add a")
	r.git("push", "-q", "origin", "main")

	// renamer (child of main): rename a.txt -> b.txt and nothing else.
	r.stOK("create", "renamer")
	r.git("mv", "a.txt", "b.txt")
	r.git("commit", "-q", "-m", "rename a to b")

	// control (another child of main): adds c.txt, which upstream lands —
	// proves the prune ran and only the uncontained rename survived.
	r.stOK("checkout", "main")
	r.stOK("create", "control")
	r.writeFile("c.txt", "c\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "add c")

	// Upstream keeps a.txt, copies it to b.txt, and adds c.txt: control's
	// content lands, but only the destination half of the rename does.
	r.stOK("checkout", "main")
	preTrunk := r.rev("main")
	r.writeFile("b.txt", "a\n")
	r.writeFile("c.txt", "c\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "copy a to b and add c")
	r.git("push", "-q", "origin", "main")
	r.git("reset", "-q", "--hard", preTrunk)

	res := r.stOK("sync")
	wantStdoutContains(t, res, "sync complete")
	wantStdoutContains(t, res, "deleted: control")
	if strings.Contains(res.stdout, "deleted: renamer") {
		t.Fatalf("renamer was pruned although its rename never landed:\n%s", res.stdout)
	}
	if r.branchExists("control") {
		t.Fatal("control's content landed upstream; it should be pruned")
	}
	if !r.branchExists("renamer") {
		t.Fatal("renamer must survive sync: upstream lacks its a.txt deletion")
	}
	if !r.fileOnBranch("renamer", "b.txt") || r.fileOnBranch("renamer", "a.txt") {
		t.Fatal("renamer lost its rename")
	}

	res = r.stOK("log", "--json")
	var root logNode
	if err := json.Unmarshal([]byte(res.stdout), &root); err != nil {
		t.Fatalf("log --json invalid: %v", err)
	}
	n := findNode(&root, "renamer")
	if n == nil || n.Parent != "main" {
		t.Fatalf("renamer should still be tracked under main: %+v", n)
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

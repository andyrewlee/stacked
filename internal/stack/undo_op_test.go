package stack

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// mustSnapshot captures an in-memory undo entry via the port (no journal, no
// disk).
func mustSnapshot(t *testing.T, s *State, f *fakeGit, label string) *UndoEntry {
	t.Helper()
	entry, err := s.SnapshotUndo(f, label)
	if err != nil {
		t.Fatalf("SnapshotUndo(%s): %v", label, err)
	}
	return entry
}

// assertUndoRestored asserts that the live state and the fake's refs match the
// snapshot exactly: same metadata, every recorded ref back on its recorded
// tip, no branch that did not exist at capture time, and HEAD back on the
// captured branch.
func assertUndoRestored(t *testing.T, f *fakeGit, s *State, entry *UndoEntry) {
	t.Helper()
	var want State
	if err := json.Unmarshal(entry.State, &want); err != nil {
		t.Fatalf("snapshot state does not parse: %v", err)
	}
	if s.Trunk != want.Trunk {
		t.Fatalf("trunk = %q after undo, want %q", s.Trunk, want.Trunk)
	}
	if len(s.Branches) != len(want.Branches) {
		t.Fatalf("tracked = %v after undo, want %v", sortedBranchNames(s), sortedBranchNames(&want))
	}
	for name, wantBranch := range want.Branches {
		got, ok := s.Get(name)
		if !ok || *got != *wantBranch {
			t.Fatalf("branch %q = %+v after undo, want %+v", name, got, wantBranch)
		}
	}
	for name, sha := range entry.Refs {
		got, err := f.RevParse(branchTipRef(name))
		if err != nil || got != sha {
			t.Fatalf("ref %q = %q (%v) after undo, want %q", name, got, err, sha)
		}
	}
	existed := map[string]bool{}
	for _, name := range entry.LocalBranches {
		existed[name] = true
	}
	for name := range f.branches {
		if !existed[name] {
			t.Fatalf("branch %q survived undo but did not exist at capture time", name)
		}
	}
	if entry.CurrentBranch != "" && f.head != entry.CurrentBranch {
		t.Fatalf("HEAD = %q after undo, want %q", f.head, entry.CurrentBranch)
	}
}

func TestUndoCreateDeletesBranchAndRestoresHEAD(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := Create(env, s, "b", "c-b", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if f.BranchExists("b") || s.IsTracked("b") {
		t.Fatal("undo left the created branch behind")
	}
	assertUndoRestored(t, f, s, entry)
}

func TestUndoCreateRemovesLinkedWorktreeBeforeDeletingBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := CreateInWorktreePrep(env, s, "b"); err != nil {
		t.Fatalf("create in worktree prep: %v", err)
	}
	f.addWorktree("/wt/b", "b")
	entry.CreatedWorktrees = map[string]string{"b": "/wt/b"}

	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if f.BranchExists("b") || s.IsTracked("b") {
		t.Fatal("undo left the created branch behind")
	}
	if _, ok := f.linkedWorktrees["b"]; ok {
		t.Fatal("undo left the created branch worktree behind")
	}
	assertUndoRestored(t, f, s, entry)
}

// A created branch whose worktree was materialized AFTER the create (so the
// undo entry never recorded it) must still have its clean linked worktree
// released before the branch delete — this is the recovery sequence the tool
// itself recommends after a failed `st create --worktree`.
func TestUndoCreateRemovesUnrecordedLinkedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := CreateInWorktreePrep(env, s, "b"); err != nil {
		t.Fatalf("create in worktree prep: %v", err)
	}
	f.addWorktree("/wt/unrecorded", "b")

	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if f.BranchExists("b") || s.IsTracked("b") {
		t.Fatal("undo left the created branch behind")
	}
	if _, ok := f.linkedWorktrees["b"]; ok {
		t.Fatal("undo left the unrecorded linked worktree behind")
	}
	assertUndoRestored(t, f, s, entry)
}

// A dirty unrecorded worktree must refuse, leaving both the worktree and the
// branch in place.
func TestUndoCreateRefusesDirtyUnrecordedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := CreateInWorktreePrep(env, s, "b"); err != nil {
		t.Fatalf("create in worktree prep: %v", err)
	}
	f.addWorktree("/wt/unrecorded", "b")
	f.markWorktreeDirty("b")

	_, err := Undo(env, s, entry)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("undo error = %v, want dirty-worktree refusal", err)
	}
	if _, ok := f.linkedWorktrees["b"]; !ok {
		t.Fatal("failed undo removed the dirty worktree")
	}
	if !f.BranchExists("b") {
		t.Fatal("failed undo deleted the branch anyway")
	}
}

func TestUndoCurrentCreatedBranchDetachesWhenParentCheckedOutElsewhere(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := Create(env, s, "b", "c-b", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	f.checkoutErr["a"] = errors.New("fatal: 'a' is already checked out at '/repo'")

	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if f.BranchExists("b") || s.IsTracked("b") {
		t.Fatal("undo left the created branch behind")
	}
	if f.head != "" || f.detachedAt == "" {
		t.Fatalf("HEAD = (%q, %q) after checkout blocked by other worktree, want detached", f.head, f.detachedAt)
	}
}

func TestUndoToleratesFinalCheckoutBranchOwnedByOtherWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := CreateInWorktreePrep(env, s, "b"); err != nil {
		t.Fatalf("create in worktree prep: %v", err)
	}
	f.addWorktree("/wt/b", "b")
	entry.CreatedWorktrees = map[string]string{"b": "/wt/b"}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.checkoutErr["a"] = errors.New("fatal: 'a' is already checked out at '/wt/a'")

	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if f.BranchExists("b") || s.IsTracked("b") {
		t.Fatal("undo left the created branch behind")
	}
	if _, ok := f.linkedWorktrees["b"]; ok {
		t.Fatal("undo left the created branch worktree behind")
	}
}

func TestUndoModifyRestoresEveryRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	// Amending a rewrites a's tip and restacks b — two refs move.
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
}

func TestUndoRenameRestoresOldNameAndChecksItOut(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "rename")
	if _, err := Rename(env, s, "a", "z"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if f.BranchExists("z") || s.IsTracked("z") {
		t.Fatal("undo left the renamed branch behind")
	}
	if !f.BranchExists("a") || !s.IsTracked("a") {
		t.Fatal("undo did not restore the old branch name")
	}
	if f.head != "a" {
		t.Fatalf("HEAD = %q after undoing rename, want a", f.head)
	}
	assertUndoRestored(t, f, s, entry)
}

// TestUndoRenameWithNilStateRecoversOldName covers the cmd loadErr path (Undo is
// called with s==nil when the state can't be loaded): the generic current-branch
// restore must still land on the restored old name (entry.CurrentBranch), with no
// loaded state to consult.
func TestUndoRenameWithNilStateRecoversOldName(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	entry := mustSnapshot(t, s, f, "rename")
	if _, err := Rename(env, s, "a", "z"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	// FinalizeUndo records the created branch on the persisted entry; with s==nil
	// it is the only signal that "z" must be deleted.
	entry.CreatedBranches = []string{"z"}
	if _, err := Undo(env, nil, entry); err != nil { // s==nil: Load failed
		t.Fatalf("undo (nil state): %v", err)
	}
	if f.BranchExists("z") {
		t.Fatal("undo left the renamed branch z behind")
	}
	if !f.BranchExists("a") {
		t.Fatal("undo did not restore the old branch name a")
	}
	if f.head != "a" {
		t.Fatalf("HEAD = %q after undoing rename with nil state, want a", f.head)
	}
}

func TestUndoDeleteResurrectsBranchFromSnapshotRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	aTip, _ := f.RevParse("a")

	entry := mustSnapshot(t, s, f, "delete")
	if _, err := Delete(env, s, "a", true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if got, err := f.RevParse("a"); err != nil || got != aTip {
		t.Fatalf("a = %q (%v) after undo, want resurrected at %q", got, err, aTip)
	}
	if b, _ := s.Get("b"); b == nil || b.Parent != "a" {
		t.Fatalf("b parent = %+v after undo, want a", b)
	}
	assertUndoRestored(t, f, s, entry)
}

// A checkout blocked by local changes must detach HEAD so the created branch
// can still be deleted without touching the working tree.
func TestUndoCreateWithDirtyTreeDetachesHEAD(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := Create(env, s, "b", "c-b", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	f.clean = false
	f.checkoutErr["a"] = errors.New("your local changes would be overwritten")

	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if f.BranchExists("b") {
		t.Fatal("undo did not delete the created branch")
	}
	if f.head != "" || f.detachedAt == "" {
		t.Fatalf("HEAD = (%q, %q) after blocked checkout, want detached", f.head, f.detachedAt)
	}
}

// An unrelated checkout failure must propagate rather than being mistaken for
// a local-change or other-worktree checkout blocker, even when the tree is dirty.
func TestUndoPropagatesCheckoutErrorOnDirtyTree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := Create(env, s, "b", "c-b", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	boom := errors.New("checkout failed: permission denied")
	f.clean = false
	f.checkoutErr["a"] = boom

	if _, err := Undo(env, s, entry); !errors.Is(err, boom) {
		t.Fatalf("undo error = %v, want %v", err, boom)
	}
	if !f.BranchExists("b") {
		t.Fatal("failed undo deleted the created branch anyway")
	}
}

// A clean tree whose checkout still fails must propagate the error rather than
// detaching.
func TestUndoPropagatesCheckoutErrorOnCleanTree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := Create(env, s, "b", "c-b", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	boom := errors.New("checkout refused")
	f.checkoutErr["a"] = boom

	if _, err := Undo(env, s, entry); !errors.Is(err, boom) {
		t.Fatalf("undo error = %v, want %v", err, boom)
	}
	if !f.BranchExists("b") {
		t.Fatal("failed undo deleted the created branch anyway")
	}
}

func TestUndoPropagatesFinalCheckoutErrorOnDirtyTree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	boom := errors.New("checkout failed: permission denied")
	f.clean = false
	f.checkoutErr["a"] = boom

	if _, err := Undo(env, s, entry); !errors.Is(err, boom) {
		t.Fatalf("undo error = %v, want %v", err, boom)
	}
}

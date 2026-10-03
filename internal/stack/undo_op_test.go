package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// mustSnapshot captures an in-memory undo entry via the port (no journal, no
// disk).
func mustSnapshot(t *testing.T, s *State, f *fakeGit, label string) *UndoEntry {
	t.Helper()
	entry, err := s.snapshotUndo(f, label)
	if err != nil {
		t.Fatalf("snapshotUndo(%s): %v", label, err)
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
	if _, err := Undo(env, s, entry, false); err != nil {
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

	if _, err := Undo(env, s, entry, false); err != nil {
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

	if _, err := Undo(env, s, entry, false); err != nil {
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

	_, err := Undo(env, s, entry, false)
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

	if _, err := Undo(env, s, entry, false); err != nil {
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

	if _, err := Undo(env, s, entry, false); err != nil {
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
	if _, err := Undo(env, s, entry, false); err != nil {
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
	if _, err := Undo(env, s, entry, false); err != nil {
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
	if _, err := Undo(env, nil, entry, false); err != nil { // s==nil: Load failed
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
	if _, err := Undo(env, s, entry, false); err != nil {
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

	if _, err := Undo(env, s, entry, false); err != nil {
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

	if _, err := Undo(env, s, entry, false); !errors.Is(err, boom) {
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

	if _, err := Undo(env, s, entry, false); !errors.Is(err, boom) {
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

	if _, err := Undo(env, s, entry, false); !errors.Is(err, boom) {
		t.Fatalf("undo error = %v, want %v", err, boom)
	}
}

// TestUndoCreateRefusesWorktreePathMismatch pins the safety half of undo's
// worktree teardown: when the journal recorded a created worktree at one path
// but the branch's live linked worktree is somewhere else, the journal and
// topology disagree — refuse, and abort before the branch delete.
func TestUndoCreateRefusesWorktreePathMismatch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := CreateInWorktreePrep(env, s, "b"); err != nil {
		t.Fatalf("create in worktree prep: %v", err)
	}
	f.addWorktree("/wt/other", "b")
	entry.CreatedWorktrees = map[string]string{"b": "/wt/b"}

	_, err := Undo(env, s, entry, false)
	if err == nil {
		t.Fatal("undo succeeded despite created-worktree path mismatch")
	}
	if !strings.Contains(err.Error(), "not removing an unexpected worktree") {
		t.Fatalf("error = %v, want the path-mismatch refusal", err)
	}
	if _, ok := f.linkedWorktrees["b"]; !ok {
		t.Fatal("undo removed the mismatched worktree")
	}
	if !f.BranchExists("b") {
		t.Fatal("undo deleted the branch despite refusing its worktree")
	}
}

// TestUndoRefusesInsideUnrecordedDoomedWorktree pins the engine belt under
// cmd's teleport pre-flight: a branch the undone op created whose worktree was
// materialized AFTER the journal entry ran (so the journal recorded none) is
// still doomed — Undo must refuse when the caller's cwd is inside it rather
// than deleting the process's own worktree.
func TestUndoRefusesInsideUnrecordedDoomedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := CreateInWorktreePrep(env, s, "b"); err != nil {
		t.Fatalf("create: %v", err)
	}
	f.addWorktree("/wt/b", "b") // materialized after the op ran: unrecorded
	f.repoRoot = "/wt/b"        // the caller is standing inside it
	// (f.head stays "a" — the fake binds head to the main worktree; the linked
	// worktree is where the process's cwd notionally sits via repoRoot)

	_, err := Undo(env, s, entry, false)
	if err == nil || !strings.Contains(err.Error(), "you are inside it") {
		t.Fatalf("undo = %v, want the cwd-inside refusal", err)
	}
	if _, ok := f.linkedWorktrees["b"]; !ok {
		t.Fatal("undo removed the worktree the caller was inside")
	}
	if !f.BranchExists("b") {
		t.Fatal("undo deleted the branch despite refusing its worktree")
	}
}

// TestUndoPropagatesRepoRootProbeFailure: the cwd guard is fail-closed — a
// broken rev-parse probe surfaces rather than guessing "not inside" and
// deleting the caller's worktree.
func TestUndoPropagatesRepoRootProbeFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	if _, err := CreateInWorktreePrep(env, s, "b"); err != nil {
		t.Fatalf("create: %v", err)
	}
	f.addWorktree("/wt/b", "b")
	boom := errors.New("rev-parse exploded")
	f.failErr["RepoRoot"] = boom

	_, err := Undo(env, s, entry, false)
	if err == nil || !strings.Contains(err.Error(), "rev-parse exploded") {
		t.Fatalf("undo = %v, want the RepoRoot probe failure surfaced", err)
	}
	if _, ok := f.linkedWorktrees["b"]; !ok {
		t.Fatal("undo removed the worktree after its cwd probe failed")
	}
}

// TestUndoRejectsFutureSnapshot pins the schema barrier on the undo path: a
// snapshot — or a supplied nonnil current State — stamped with a version this
// binary does not understand must be refused BEFORE any branch/worktree
// mutation, ref restore, or Save call. An older binary reverting metadata it
// cannot fully interpret could silently drop what a newer st recorded.
func TestUndoRejectsFutureSnapshot(t *testing.T) {
	// stampVersion rewrites only the snapshot's schema version field, leaving
	// the rest of the document byte-identical.
	stampVersion := func(t *testing.T, entry *UndoEntry, version int) {
		t.Helper()
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(entry.State, &doc); err != nil {
			t.Fatalf("snapshot state does not parse: %v", err)
		}
		doc["version"] = json.RawMessage(strconv.Itoa(version))
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("encode stamped snapshot: %v", err)
		}
		entry.State = raw
	}
	// setupCreateUndo snapshots main->a, then creates b with a commit — the
	// entry whose undo would delete b and move refs.
	setupCreateUndo := func(t *testing.T) (*fakeGit, *State, Env, *UndoEntry) {
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		if err := f.Checkout("a"); err != nil {
			t.Fatal(err)
		}
		entry := mustSnapshot(t, s, f, "create")
		if _, err := Create(env, s, "b", "c-b", true); err != nil {
			t.Fatalf("create: %v", err)
		}
		return f, s, env, entry
	}
	// assertNothingMoved proves the refusal happened before the mutation
	// phase: created branch b survives, HEAD stayed put, and Save never ran.
	assertNothingMoved := func(t *testing.T, f *fakeGit, saves int) {
		t.Helper()
		if !f.BranchExists("b") {
			t.Fatal("refused undo deleted the created branch anyway")
		}
		if f.head != "b" {
			t.Fatalf("HEAD = %q, want b — a refused undo must not shuffle checkout", f.head)
		}
		if saves != 0 {
			t.Fatalf("Save called %d times during a refused undo", saves)
		}
	}

	t.Run("future snapshot with supported current state", func(t *testing.T) {
		f, s, env, entry := setupCreateUndo(t)
		stampVersion(t, entry, stateSchemaVersion+1)
		saves := 0
		env.Save = func() error { saves++; return nil }

		_, err := Undo(env, s, entry, false)
		if !errors.Is(err, ErrStateTooNew) {
			t.Fatalf("undo error = %v, want ErrStateTooNew", err)
		}
		assertNothingMoved(t, f, saves)
	})

	t.Run("future snapshot with nil current state", func(t *testing.T) {
		f, _, env, entry := setupCreateUndo(t)
		stampVersion(t, entry, stateSchemaVersion+1)
		saves := 0
		env.Save = func() error { saves++; return nil }

		_, err := Undo(env, nil, entry, false)
		if !errors.Is(err, ErrStateTooNew) {
			t.Fatalf("undo error = %v, want ErrStateTooNew", err)
		}
		assertNothingMoved(t, f, saves)
	})

	t.Run("future nonnil current state is refused as a defensive boundary", func(t *testing.T) {
		f, s, env, entry := setupCreateUndo(t)
		s.Version = stateSchemaVersion + 1
		saves := 0
		env.Save = func() error { saves++; return nil }

		_, err := Undo(env, s, entry, false)
		if !errors.Is(err, ErrStateTooNew) {
			t.Fatalf("undo error = %v, want ErrStateTooNew", err)
		}
		assertNothingMoved(t, f, saves)
	})

	// Controls: legacy v0 (no version field) and current v1 snapshots still
	// undo, and a malformed snapshot errors without reaching ErrStateTooNew.
	for _, version := range []int{0, 1} {
		t.Run(fmt.Sprintf("version %d snapshot still undoes", version), func(t *testing.T) {
			f, s, env, entry := setupCreateUndo(t)
			if version == 0 {
				var doc map[string]json.RawMessage
				if err := json.Unmarshal(entry.State, &doc); err != nil {
					t.Fatal(err)
				}
				delete(doc, "version")
				raw, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				entry.State = raw
			} else {
				stampVersion(t, entry, version)
			}

			if _, err := Undo(env, s, entry, false); err != nil {
				t.Fatalf("undo with schema v%d snapshot: %v", version, err)
			}
			if f.BranchExists("b") {
				t.Fatal("supported-snapshot undo left created branch behind")
			}
		})
	}

	t.Run("malformed snapshot errors without mutations", func(t *testing.T) {
		f, s, env, entry := setupCreateUndo(t)
		entry.State = json.RawMessage("{bad json\n")
		saves := 0
		env.Save = func() error { saves++; return nil }

		_, err := Undo(env, s, entry, false)
		if err == nil || errors.Is(err, ErrStateTooNew) {
			t.Fatalf("undo error = %v, want a parse error (not ErrStateTooNew)", err)
		}
		assertNothingMoved(t, f, saves)
	})
}

// TestUndoTrunkSnapshotRejected pins the structural barrier on the undo path:
// a snapshot whose serialized State tracks the trunk as a branch record must
// be refused at the shared decoder BEFORE any cleanup — deleting the created
// branch, moving refs, or replacing the caller's metadata would turn a corrupt
// journal entry into corrupt live state.
func TestUndoTrunkSnapshotRejected(t *testing.T) {
	// setupCreateUndo snapshots main->a, then creates b — undoing the entry
	// would delete b; the trunk-record State must stop it before that.
	setupCreateUndo := func(t *testing.T) (*fakeGit, *State, Env, *UndoEntry) {
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		if err := f.Checkout("a"); err != nil {
			t.Fatal(err)
		}
		entry := mustSnapshot(t, s, f, "create")
		if _, err := Create(env, s, "b", "c-b", true); err != nil {
			t.Fatalf("create: %v", err)
		}
		entry.State = json.RawMessage(
			`{"version":1,"trunk":"main","branches":{"main":{"parent":"main"},"a":{"name":"a","parent":"main"}}}`)
		return f, s, env, entry
	}
	// assertNothingMoved proves the refusal ran ahead of every mutation:
	// created branch b survives on its tip, a/main tips are unmoved, HEAD
	// stayed on b, Save never ran, and s still describes the live topology.
	assertNothingMoved := func(t *testing.T, f *fakeGit, s *State, saves int) {
		t.Helper()
		tipOf := func(name string) string {
			tip, err := f.RevParse(branchTipRef(name))
			if err != nil {
				t.Fatalf("RevParse(%q): %v", name, err)
			}
			return tip
		}
		for _, name := range []string{"main", "a", "b"} {
			if !f.BranchExists(name) {
				t.Fatalf("refused undo deleted branch %q", name)
			}
			if tipOf(name) == "" {
				t.Fatalf("branch %q lost its tip during a refused undo", name)
			}
		}
		if f.head != "b" {
			t.Fatalf("HEAD = %q, want b — a refused undo must not shuffle checkout", f.head)
		}
		if saves != 0 {
			t.Fatalf("Save called %d times during a refused undo", saves)
		}
		if s != nil {
			if _, ok := s.Get("b"); !ok || s.IsTracked("main") {
				t.Fatalf("current metadata = %+v, want unchanged (b tracked, trunk not)", s.Branches)
			}
		}
	}

	t.Run("nonnil current state", func(t *testing.T) {
		f, s, env, entry := setupCreateUndo(t)
		saves := 0
		env.Save = func() error { saves++; return nil }

		_, err := Undo(env, s, entry, false)
		if err == nil || !strings.Contains(err.Error(), "corrupted") {
			t.Fatalf("undo error = %v, want a corruption error", err)
		}
		assertNothingMoved(t, f, s, saves)
	})

	t.Run("nil current state", func(t *testing.T) {
		f, _, env, entry := setupCreateUndo(t)
		saves := 0
		env.Save = func() error { saves++; return nil }

		_, err := Undo(env, nil, entry, false)
		if err == nil || !strings.Contains(err.Error(), "corrupted") {
			t.Fatalf("undo error = %v, want a corruption error", err)
		}
		assertNothingMoved(t, f, nil, saves)
	})
}

// A failure of the batched ref restore inside Undo must surface as the wrapped
// "restoring branch refs" error, and — because UpdateRefs is transactional —
// leave every ref where it was (no partial restore). The state swap/save runs
// only after a successful batch, so post-modify metadata is untouched and no
// Save call happens.
func TestUndoRefRestoreFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	// Amend a (moves a's tip and restacks b) so the refs differ from the snapshot.
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	postA, _ := f.RevParse("a")
	postB, _ := f.RevParse("b")
	postState, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt one recorded ref to a SHA the fake cannot resolve, so Undo's
	// batched UpdateRefs fails on it.
	entry.Refs["a"] = "0123456789012345678901234567890123456789"

	saves := 0
	env.Save = func() error { saves++; return nil }
	_, err = Undo(env, s, entry, false)
	if err == nil {
		t.Fatal("Undo succeeded despite an unresolvable recorded ref")
	}
	if !strings.Contains(err.Error(), "restoring branch refs") {
		t.Fatalf("error = %q, want it wrapped with %q", err.Error(), "restoring branch refs")
	}
	// Transactional restore: the failed batch moved zero refs, so the fake's
	// branches remain at their post-modify tips (NOT rolled back to the snapshot).
	if got, _ := f.RevParse("a"); got != postA {
		t.Fatalf("a = %q after failed restore, want unchanged %q", got, postA)
	}
	if got, _ := f.RevParse("b"); got != postB {
		t.Fatalf("b = %q after failed restore, want unchanged %q", got, postB)
	}
	// The state assignment and Save are gated on a successful batch: in-memory
	// metadata is still the post-modify value, byte for byte.
	if saves != 0 {
		t.Fatalf("Save ran %d times despite the failed ref restore", saves)
	}
	gotState, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotState) != string(postState) {
		t.Fatalf("state mutated despite the failed ref restore:\npost-op:  %s\nafter:    %s", postState, gotState)
	}
}

// TestUndoRefRestoreFailureNilState covers the same failed-batch ordering when
// the caller could not load current state (s == nil): the raw Save hook —
// cmd's RestoreState fallback — must not run either.
func TestUndoRefRestoreFailureNilState(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	postA, _ := f.RevParse("a")
	entry.Refs["a"] = "0123456789012345678901234567890123456789"

	saves := 0
	env.Save = func() error { saves++; return nil }
	if _, err := Undo(env, nil, entry, false); err == nil {
		t.Fatal("Undo(nil state, false) succeeded despite an unresolvable recorded ref")
	}
	if saves != 0 {
		t.Fatalf("Save ran %d times despite the failed ref restore", saves)
	}
	if got, _ := f.RevParse("a"); got != postA {
		t.Fatalf("a = %q after failed restore, want unchanged %q", got, postA)
	}
}

// TestUndoCleanupThenBatchFailure: the created-branch cleanup legitimately
// runs BEFORE the ref transaction — a batch failure after it can leave a
// created branch already deleted while Save stays uncalled, and retrying the
// retained entry must skip the now-absent branch and complete.
func TestUndoCleanupThenBatchFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	// Snapshot BEFORE creating doomed, then create it through the real op so
	// the entry records it as created (LocalBranches lacks it). Amend a (the
	// recorded branch) so the batch restore has real work on retry.
	entry := mustSnapshot(t, s, f, "create")
	mkBranch(t, env, s, f, "a", "doomed")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	// The recorded a-tip already differs from live (Modify amended it); keep
	// the real value for the retry, then corrupt it so the first attempt
	// fails after the cleanup deleted doomed.
	postA, _ := f.RevParse("a")
	recordedA := entry.Refs["a"]
	entry.Refs["a"] = "0123456789012345678901234567890123456789"

	saves := 0
	env.Save = func() error { saves++; return nil }
	if _, err := Undo(env, s, entry, false); err == nil {
		t.Fatal("Undo succeeded despite an unresolvable recorded ref")
	}
	if saves != 0 {
		t.Fatalf("Save ran %d times despite the failed ref restore", saves)
	}
	// Partial completion is the documented boundary: doomed is already gone —
	// cleanup precedes the transaction — but no ref moved and no Save ran.
	if f.BranchExists("doomed") {
		t.Fatal("created branch survived the failed undo")
	}
	if got, _ := f.RevParse("a"); got != postA {
		t.Fatalf("a = %q after failed restore, want unchanged %q", got, postA)
	}

	// Retry the retained entry with the corruption fixed: the cleanup loop
	// skips the already-deleted branch, the batch restores a to the recorded
	// snapshot tip, and the op completes.
	entry.Refs["a"] = recordedA
	if _, err := Undo(env, s, entry, false); err != nil {
		t.Fatalf("retry after batch failure: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
}

// TestUndoEntryAbsorbedCommitsJSON pins the journal encoding of the absorb
// recovery map: it round-trips through marshal/unmarshal, and entries written
// before the field existed (no key at all) still decode with a nil map —
// journal format is additive, not versioned.
func TestUndoEntryAbsorbedCommitsJSON(t *testing.T) {
	entry := UndoEntry{
		Label:           "absorb",
		State:           json.RawMessage(`{"version":1}`),
		Refs:            map[string]string{"main": "aaa"},
		AbsorbedCommits: map[string]string{"feat-a": "abc123"},
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded UndoEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.AbsorbedCommits["feat-a"] != "abc123" {
		t.Fatalf("absorbedCommits = %v, want feat-a -> abc123", decoded.AbsorbedCommits)
	}

	// An entry written by an st that predates the field: no key, decodes nil.
	var old UndoEntry
	if err := json.Unmarshal([]byte(`{"label":"absorb","state":{},"refs":{}}`), &old); err != nil {
		t.Fatalf("unmarshal old-format entry: %v", err)
	}
	if old.AbsorbedCommits != nil {
		t.Fatalf("old-format absorbedCommits = %v, want nil", old.AbsorbedCommits)
	}
	// And omitempty keeps new entries without absorbed commits byte-identical.
	plain, err := json.Marshal(UndoEntry{Label: "modify", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if strings.Contains(string(plain), "absorbedCommits") {
		t.Fatalf("non-absorb entry carries the key: %s", plain)
	}
}

// TestUndoBatchesExistenceProbes pins plan-016's spawn contract: an undo that
// dooms several branches performs ONE Tips() batch read and zero per-ref
// BranchExists/RevParse probes — previously each doomed branch, its parent,
// and the restore target cost a show-ref spawn apiece.
func TestUndoBatchesExistenceProbes(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	entry := mustSnapshot(t, s, f, "track")
	// Three branches the undone command created — all doomed, all real.
	for _, name := range []string{"b", "c", "d"} {
		if err := f.CreateBranch(name); err != nil {
			t.Fatal(err)
		}
	}
	entry.CreatedBranches = []string{"b", "c", "d"}

	before := f.callsSnapshot()
	if _, err := Undo(env, s, entry, false); err != nil {
		t.Fatalf("undo: %v", err)
	}
	for method, want := range map[string]int{"Tips": 1, "BranchExists": 0, "RevParse": 0} {
		if got := f.calls[method] - before[method]; got != want {
			t.Fatalf("%s calls during Undo = %d, want %d", method, got, want)
		}
	}
	assertUndoRestored(t, f, s, entry)
}

// When the Tips() batch read fails, Undo degrades to per-branch probes rather
// than failing a recovery path — the same tolerance BranchExists' quiet
// show-ref had.
func TestUndoDegradesToPerBranchProbes(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	entry := mustSnapshot(t, s, f, "track")
	for _, name := range []string{"b", "c"} {
		if err := f.CreateBranch(name); err != nil {
			t.Fatal(err)
		}
	}
	entry.CreatedBranches = []string{"b", "c"}

	f.failErr["Tips"] = errors.New("for-each-ref exploded")
	before := f.callsSnapshot()
	if _, err := Undo(env, s, entry, false); err != nil {
		t.Fatalf("undo: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
	if f.calls["BranchExists"]-before["BranchExists"] == 0 {
		t.Fatal("Tips failure did not fall back to per-branch BranchExists probes")
	}
}

// The preview twin: one Tips() batch answers the created-candidate, doomed
// HEAD, restore-tip, and checkout-target questions — zero per-ref spawns.
func TestUndoPreviewBatchesExistenceProbes(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	entry := mustSnapshot(t, s, f, "track")
	for _, name := range []string{"b", "c", "d"} {
		if err := f.CreateBranch(name); err != nil {
			t.Fatal(err)
		}
	}
	entry.CreatedBranches = []string{"b", "c", "d"}

	before := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, true, 1, nil)
	if err != nil {
		t.Fatalf("undo preview: %v", err)
	}
	for method, want := range map[string]int{"Tips": 1, "BranchExists": 0, "RevParse": 0} {
		if got := f.calls[method] - before[method]; got != want {
			t.Fatalf("%s calls during UndoPreview = %d, want %d", method, got, want)
		}
	}
	if len(res.Blockers) != 0 {
		t.Fatalf("preview blockers = %v, want none", res.Blockers)
	}
	if len(res.WouldDelete) != 3 {
		t.Fatalf("WouldDelete = %v, want b, c, d", res.WouldDelete)
	}
}

// pinPostRefs mimics FinalizeUndo's post-operation tip capture on an
// in-memory entry: the tips of every recorded branch plus the op-created
// branches the op left live.
func pinPostRefs(t *testing.T, f *fakeGit, entry *UndoEntry, created ...string) {
	t.Helper()
	tips, err := f.Tips()
	if err != nil {
		t.Fatal(err)
	}
	entry.PostRefs = postRefsFor(entry, tips, true, created)
}

// TestUndoRefusesExternallyMovedRef pins the external-drift refusal: a branch
// committed on outside st between the op and the undo is drift — undo refuses
// before any mutation instead of rewinding the unrecorded commits.
func TestUndoRefusesExternallyMovedRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	pinPostRefs(t, f, entry)
	postB, _ := f.RevParse("b")

	// Someone amends a outside st: the live tip now differs from BOTH the
	// recorded pre-op tip and the post-op tip undo expects to CAS against.
	mustCheckout(t, f, "a")
	f.amend("external work")
	extA, _ := f.RevParse("a")

	before := f.callsSnapshot()
	_, err := Undo(env, s, entry, false)
	if err == nil {
		t.Fatal("Undo succeeded over an externally moved ref")
	}
	if !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), "moved outside st") {
		t.Fatalf("error = %q, want it to name the diverged branch", err)
	}
	// The refusal is a preflight: no cleanup, no ref batch, no save ran.
	for method, want := range map[string]int{"DeleteBranch": 0, "UpdateRefsCas": 0, "Checkout": 0, "WorktreeRemove": 0} {
		if got := f.calls[method] - before[method]; got != want {
			t.Fatalf("%s calls during refused Undo = %d, want %d", method, got, want)
		}
	}
	if got, _ := f.RevParse("a"); got != extA {
		t.Fatalf("a = %q after refused undo, want the external tip %q", got, extA)
	}
	if got, _ := f.RevParse("b"); got != postB {
		t.Fatalf("b = %q after refused undo, want %q", got, postB)
	}
}

// TestUndoForceRestoresDivergedRef pins the escape hatch: the same divergence
// that refuses a plain undo is restored unconditionally under force, with a
// note naming the overwritten refs.
func TestUndoForceRestoresDivergedRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	pinPostRefs(t, f, entry)
	mustCheckout(t, f, "a")
	f.amend("external work")

	res, err := Undo(env, s, entry, true)
	if err != nil {
		t.Fatalf("Undo(force) over a diverged ref: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "a") || !strings.Contains(joined, "force") {
		t.Fatalf("notes = %v, want a --force note naming the overwritten ref", res.Notes)
	}
}

// TestUndoRetryOnAlreadyRestoredRef pins idempotent retry under CAS: when the
// live tip already equals the recorded pre-op tip — an earlier undo restored
// it before failing later — it is skipped rather than CAS'd against the
// post-op tip it no longer matches.
func TestUndoRetryOnAlreadyRestoredRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	pinPostRefs(t, f, entry)

	if _, err := Undo(env, s, entry, false); err != nil {
		t.Fatalf("first undo: %v", err)
	}
	// Live tips now equal the recorded pre-op values while postRefs still
	// name the post-op tips: a naive CAS would refuse the retry.
	if _, err := Undo(env, s, entry, false); err != nil {
		t.Fatalf("retry of an already-restored entry refused: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
}

// TestUndoRefusesRecreatedBranch: the op deleted a recorded branch, so its
// post-op expectation is "absent". An external recreation must refuse rather
// than be silently overwritten by the resurrect restore.
func TestUndoRefusesRecreatedBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "delete")
	// The op deletes b and untracks it — b is absent from postRefs.
	if err := f.DeleteBranch("b", true); err != nil {
		t.Fatal(err)
	}
	delete(s.Branches, "b")
	pinPostRefs(t, f, entry)

	// Someone recreates b at a different commit before the undo.
	aTip, _ := f.RevParse("a")
	if err := f.CreateBranchAt("b", aTip); err != nil {
		t.Fatal(err)
	}

	if _, err := Undo(env, s, entry, false); err == nil {
		t.Fatal("Undo resurrected over an externally recreated branch")
	}
	if got, _ := f.RevParse("b"); got != aTip {
		t.Fatalf("b = %q after refused undo, want the external tip %q", got, aTip)
	}
}

// TestUndoRefusesAmendedCreatedBranch: a branch the op created is doomed to
// deletion, so a post-op commit on it must refuse — undoing would discard
// work that was never recorded anywhere.
func TestUndoRefusesAmendedCreatedBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	mkBranch(t, env, s, f, "a", "doomed")
	pinPostRefs(t, f, entry, "doomed")

	// A post-op commit lands on the doomed branch outside st.
	mustCheckout(t, f, "doomed")
	f.commit("work on the created branch")
	mustCheckout(t, f, "a")

	if _, err := Undo(env, s, entry, false); err == nil {
		t.Fatal("Undo deleted an externally amended created branch")
	}
	if !f.BranchExists("doomed") {
		t.Fatal("doomed branch deleted despite the drift refusal")
	}
}

// TestUndoLegacyEntryRestoresUnconditionally pins the backward-compatible
// path: entries without PostRefs (written before the field existed, or kept
// for a failed op whose refs abort/continue legitimately moved) restore
// unconditionally — and say so.
func TestUndoLegacyEntryRestoresUnconditionally(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	// No pinPostRefs: the entry predates the field.
	mustCheckout(t, f, "a")
	f.amend("external work")

	res, err := Undo(env, s, entry, false)
	if err != nil {
		t.Fatalf("legacy undo over a moved ref: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "unconditionally") {
		t.Fatalf("notes = %v, want the unconditional-restore note", res.Notes)
	}
}

// TestUndoRefusesPausedRebaseRef pins the linked-worktree pause preflight: a
// branch whose ref the entry would restore has a rebase paused in another
// worktree — the worktree lists as detached so owner lookups miss it, git
// refuses branch -D on it mid-cleanup, and the pending --continue/--abort
// will overwrite any restore. Undo refuses up front.
func TestUndoRefusesPausedRebaseRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	pinPostRefs(t, f, entry)

	// A rebase pauses on a inside its linked worktree — the worktree now
	// reports detached, so nothing but the head-name probe can see the owner.
	f.addPausedWorktree("/wt-a", "a")

	before := f.callsSnapshot()
	_, err := Undo(env, s, entry, false)
	if err == nil {
		t.Fatal("Undo restored a ref owned by a paused rebase")
	}
	if !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), "rebase in progress") {
		t.Fatalf("error = %q, want it to name the paused branch", err)
	}
	for method, want := range map[string]int{"DeleteBranch": 0, "UpdateRefsCas": 0, "Checkout": 0, "WorktreeRemove": 0} {
		if got := f.calls[method] - before[method]; got != want {
			t.Fatalf("%s calls during refused Undo = %d, want %d", method, got, want)
		}
	}
}

// TestUndoPausedRebaseForceRestores pins --force over the pause gate: the
// recorded restore proceeds even though the pending rebase may overwrite it.
func TestUndoPausedRebaseForceRestores(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	pinPostRefs(t, f, entry)
	f.addPausedWorktree("/wt-a", "a")

	if _, err := Undo(env, s, entry, true); err != nil {
		t.Fatalf("Undo(force) over a paused ref: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
}

// TestUndoPausedRebaseUnrelated: a pause on a branch the entry never
// rewrites blocks nothing — undo only refuses pauses on refs it would
// restore or delete (a tracked descendant restacked by the op does move and
// IS refused; an untracked pause can never be reached).
func TestUndoPausedRebaseUnrelated(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "main", "b") // tracked, but not under a — untouched by the op
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	// An untracked branch is never in the journal — a pause there can never
	// block. CreateBranchAt does not move HEAD (CreateBranch is checkout -b).
	if err := f.CreateBranchAt("side", "a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	pinPostRefs(t, f, entry)
	f.addPausedWorktree("/wt-side", "side")
	f.addPausedWorktree("/wt-b", "b")

	if _, err := Undo(env, s, entry, false); err != nil {
		t.Fatalf("Undo blocked by an unrelated pause: %v", err)
	}
	assertUndoRestored(t, f, s, entry)
}

// TestUndoRefusesPausedDoomedBranch: a branch the op created — and whose
// worktree a user then paused mid-rebase — must refuse up front: git would
// refuse the branch delete mid-cleanup anyway, after earlier worktree/branch
// deletions already ran.
func TestUndoRefusesPausedDoomedBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "create")
	mkBranch(t, env, s, f, "a", "doomed")
	pinPostRefs(t, f, entry, "doomed")
	f.addPausedWorktree("/wt-doomed", "doomed")
	mustCheckout(t, f, "a")

	_, err := Undo(env, s, entry, false)
	if err == nil {
		t.Fatal("Undo deleted into a paused doomed branch")
	}
	if !strings.Contains(err.Error(), `"doomed"`) {
		t.Fatalf("error = %q, want it to name the paused branch", err)
	}
	if !f.BranchExists("doomed") {
		t.Fatal("doomed branch deleted despite the pause refusal")
	}
}

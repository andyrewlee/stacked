package stack

import (
	"encoding/json"
	"strings"
	"testing"
)

// undoPreviewMutators is every Git port method that must not run during an
// undo preview. `f.calls` counts instrumented calls (each method's fail()
// guard increments it), so the assertion is: the preview made zero of them.
var undoPreviewMutators = []string{
	"Checkout", "CheckoutDetach", "CreateBranch", "CreateBranchAt",
	"DeleteBranch", "ForceBranch", "UpdateRef", "UpdateRefs",
	"ResetSoft", "Commit", "AmendNoEdit", "AmendMessage", "Add",
	"RenameBranch", "RebaseOnto", "RebaseOntoIn", "RebaseAbort",
	"RebaseAbortIn", "RebaseContinue", "WorktreeRemove",
	"AmendTipWithPatch", "ResetHardIn",
}

// callsSnapshot captures the fake's fail-instrumented call counts so a test
// can diff them around the code under test — fixture construction legitimately
// calls instrumented mutators (mkBranch→Create→CreateBranch), so the purity
// check must compare against a baseline taken after the fixture, not zero.
func (f *fakeGit) callsSnapshot() map[string]int {
	cp := make(map[string]int, len(f.calls))
	for k, v := range f.calls {
		cp[k] = v
	}
	return cp
}

func assertNoMutation(t *testing.T, f *fakeGit, before map[string]int) {
	t.Helper()
	for _, m := range undoPreviewMutators {
		if f.calls[m] != before[m] {
			t.Fatalf("preview called mutating port method %s %d time(s)", m, f.calls[m]-before[m])
		}
	}
}

func TestUndoPreviewListsRestore(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	entry := mustSnapshot(t, s, f, "modify feat-a")
	// Modify's op: feat-a's tip moved one commit past the recorded one.
	f.staged = true
	if err := f.Commit("amend", true); err != nil {
		t.Fatal(err)
	}
	liveTip := f.branches["feat-a"]

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.Blockers) != 0 {
		t.Fatalf("blockers = %v, want none", res.Blockers)
	}
	if !res.JournalDrop {
		t.Fatal("journalDrop should be true for a previewable entry")
	}
	// Refs holds trunk + tracked branches — both are listed; only feat-a moved.
	if len(res.WouldRestore) != 2 {
		t.Fatalf("wouldRestore = %+v, want [feat-a main]", res.WouldRestore)
	}
	r := res.WouldRestore[0]
	if r.Branch != "feat-a" || r.From != liveTip || r.To != entry.Refs["feat-a"] {
		t.Fatalf("wouldRestore[0] = %+v", r)
	}
	if r.CommitsLostFromRef != 1 {
		t.Fatalf("commitsLostFromRef = %v, want 1", r.CommitsLostFromRef)
	}
	if res.WouldRestore[1].Branch != "main" || res.WouldRestore[1].CommitsLostFromRef != 0 {
		t.Fatalf("wouldRestore[1] = %+v, want unchanged trunk", res.WouldRestore[1])
	}
	if res.WouldCheckout == nil || *res.WouldCheckout != "feat-a" {
		t.Fatalf("wouldCheckout = %v, want feat-a", res.WouldCheckout)
	}
	if res.Observed == nil || res.Observed.EntryIndex != 1 {
		t.Fatalf("observed = %+v", res.Observed)
	}
	if tip := res.Observed.Tips["feat-a"]; tip == nil || *tip != liveTip {
		t.Fatalf("observed.tips[feat-a] = %v, want %q", tip, liveTip)
	}
}

func TestUndoPreviewDeletesCreatedWithWorktree(t *testing.T) {
	f, s, env := newEnvState()
	entry := mustSnapshot(t, s, f, "create feat-x") // LocalBranches=[main], cur=main
	mkBranch(t, env, s, f, "main", "feat-x")
	f.addWorktree("/wt/feat-x", "feat-x")
	entry.CreatedBranches = []string{"feat-x"}
	entry.CreatedWorktrees = map[string]string{"feat-x": "/wt/feat-x"}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.Blockers) != 0 {
		t.Fatalf("blockers = %v", res.Blockers)
	}
	if len(res.WouldDelete) != 1 {
		t.Fatalf("wouldDelete = %+v", res.WouldDelete)
	}
	d := res.WouldDelete[0]
	if d.Branch != "feat-x" || d.Worktree != "/wt/feat-x" || d.WorktreeDirty || d.IsCurrentWorktree {
		t.Fatalf("wouldDelete[0] = %+v", d)
	}
	if res.WouldCheckout == nil || *res.WouldCheckout != "main" {
		t.Fatalf("wouldCheckout = %v, want main", res.WouldCheckout)
	}
}

func TestUndoPreviewFindsLiveOwnerWithoutRecordedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	entry := mustSnapshot(t, s, f, "create feat-y")
	mkBranch(t, env, s, f, "main", "feat-y")
	// No recorded worktree — one was materialized later via `st worktree`.
	f.addWorktree("/wt/later", "feat-y")
	entry.CreatedBranches = []string{"feat-y"}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.WouldDelete) != 1 || res.WouldDelete[0].Worktree != "/wt/later" {
		t.Fatalf("wouldDelete = %+v, want live owner path", res.WouldDelete)
	}
	if len(res.Blockers) != 0 {
		t.Fatalf("blockers = %v", res.Blockers)
	}
}

func TestUndoPreviewDirtyCreatedWorktreeBlocker(t *testing.T) {
	f, s, env := newEnvState()
	entry := mustSnapshot(t, s, f, "create feat-z")
	mkBranch(t, env, s, f, "main", "feat-z")
	f.addWorktree("/wt/feat-z", "feat-z")
	f.markWorktreeDirty("feat-z")
	entry.CreatedBranches = []string{"feat-z"}
	entry.CreatedWorktrees = map[string]string{"feat-z": "/wt/feat-z"}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.WouldDelete) != 1 || !res.WouldDelete[0].WorktreeDirty {
		t.Fatalf("wouldDelete = %+v, want dirty marked", res.WouldDelete)
	}
	want := "worktree_dirty:feat-z"
	if len(res.Blockers) != 1 || res.Blockers[0] != want {
		t.Fatalf("blockers = %v, want [%q]", res.Blockers, want)
	}
	// Intent is still listed even though the blocker would refuse the run.
	if res.WouldDelete[0].Worktree != "/wt/feat-z" {
		t.Fatalf("worktree = %q", res.WouldDelete[0].Worktree)
	}
}

func TestUndoPreviewCwdInsideCreatedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	entry := mustSnapshot(t, s, f, "create feat-x")
	mkBranch(t, env, s, f, "main", "feat-x") // HEAD lands on feat-x
	f.addWorktree("/wt/feat-x", "feat-x")
	entry.CreatedBranches = []string{"feat-x"}
	entry.CreatedWorktrees = map[string]string{"feat-x": "/wt/feat-x"}
	// HEAD stays on feat-x — the caller's worktree is the doomed one.

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.WouldDelete) != 1 || !res.WouldDelete[0].IsCurrentWorktree {
		t.Fatalf("wouldDelete = %+v, want isCurrentWorktree", res.WouldDelete)
	}
	want := "cwd_inside_created_worktree:feat-x"
	found := false
	for _, b := range res.Blockers {
		if b == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers = %v, want %q", res.Blockers, want)
	}

	// With the shim available the teleport succeeds — no blocker.
	res2, err := UndoPreview(env, s, entry, true, 1)
	if err != nil {
		t.Fatalf("UndoPreview(canTeleport): %v", err)
	}
	for _, b := range res2.Blockers {
		if b == want {
			t.Fatalf("blocker %q present though the shim can teleport", want)
		}
	}
}

func TestUndoPreviewCountsDriftAndMissingRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	entry := mustSnapshot(t, s, f, "restack")
	f.staged = true
	if err := f.Commit("c2", true); err != nil {
		t.Fatal(err)
	}
	f.staged = true
	if err := f.Commit("c3", true); err != nil {
		t.Fatal(err)
	}

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.WouldRestore) == 0 || res.WouldRestore[0].CommitsLostFromRef != 2 {
		t.Fatalf("wouldRestore = %+v, want commitsLostFromRef=2", res.WouldRestore)
	}

	// The branch is gone now: missing live ref → "unknown", not 0, not a
	// blocker (undo restores the ref by name).
	delete(f.branches, "feat-a")
	res2, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview missing ref: %v", err)
	}
	r := res2.WouldRestore[0]
	if r.CommitsLostFromRef != "unknown" || r.From != zeroSHA {
		t.Fatalf("wouldRestore[0] = %+v, want unknown/zero", r)
	}
	if tip := res2.Observed.Tips["feat-a"]; tip != nil {
		t.Fatalf("observed.tips[feat-a] = %v, want null", *tip)
	}
	if len(res2.Blockers) != 0 {
		t.Fatalf("missing ref is not a blocker, got %v", res2.Blockers)
	}
}

func TestUndoPreviewSchemaBarriers(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	entry := mustSnapshot(t, s, f, "modify feat-a")

	// Current state written by a newer st.
	s.Version = stateSchemaVersion + 1
	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.Blockers) != 1 || res.Blockers[0] != "state_too_new" {
		t.Fatalf("blockers = %v, want [state_too_new]", res.Blockers)
	}
	if res.WouldRestore != nil || res.WouldDelete != nil {
		t.Fatal("schema barrier must not compute a speculative preview")
	}
	assertNoMutation(t, f, callsBefore)
	s.Version = stateSchemaVersion

	// Snapshot written by a newer st.
	var doc map[string]any
	if err := json.Unmarshal(entry.State, &doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = stateSchemaVersion + 1
	raw, _ := json.Marshal(doc)
	future := *entry
	future.State = raw
	res, err = UndoPreview(env, s, &future, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview future snapshot: %v", err)
	}
	if len(res.Blockers) != 1 || res.Blockers[0] != "state_too_new" {
		t.Fatalf("blockers = %v, want [state_too_new]", res.Blockers)
	}

	// Malformed snapshot bytes: label is still readable from the journal.
	bad := *entry
	bad.State = json.RawMessage("{bad json\n")
	res, err = UndoPreview(env, s, &bad, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview malformed: %v", err)
	}
	if len(res.Blockers) != 1 || res.Blockers[0] != "malformed_snapshot" {
		t.Fatalf("blockers = %v, want [malformed_snapshot]", res.Blockers)
	}
	if res.Label != "modify feat-a" {
		t.Fatalf("label = %q, want the journal label", res.Label)
	}
}

func TestUndoPreviewRebaseInProgressBlocker(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	entry := mustSnapshot(t, s, f, "restack")
	f.rebaseActive = true

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	if len(res.Blockers) != 1 || res.Blockers[0] != "rebase_in_progress" {
		t.Fatalf("blockers = %v, want [rebase_in_progress]", res.Blockers)
	}
	if res.WouldRestore != nil {
		t.Fatal("rebase blocker must short-circuit the preview")
	}
	assertNoMutation(t, f, callsBefore)
}

func TestUndoPreviewRecordedWorktreeMismatch(t *testing.T) {
	f, s, env := newEnvState()
	entry := mustSnapshot(t, s, f, "create feat-m")
	mkBranch(t, env, s, f, "main", "feat-m")
	f.addWorktree("/wt/actual", "feat-m")
	entry.CreatedBranches = []string{"feat-m"}
	entry.CreatedWorktrees = map[string]string{"feat-m": "/wt/recorded-elsewhere"}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	want := "recorded_worktree_mismatch:feat-m"
	found := false
	for _, b := range res.Blockers {
		if b == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers = %v, want %q", res.Blockers, want)
	}
	if res.WouldDelete[0].Worktree != "/wt/actual" {
		t.Fatalf("worktree = %q, want the live owner path", res.WouldDelete[0].Worktree)
	}
	// The mismatch replaces the dirty probe, matching removeCreatedWorktree's
	// refusal order.
	for _, b := range res.Blockers {
		if strings.HasPrefix(b, "worktree_dirty:") {
			t.Fatalf("mismatched branch must not also report dirty: %v", res.Blockers)
		}
	}
}

func TestUndoPreviewAbsorbedCommitsWarning(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	entry := mustSnapshot(t, s, f, "absorb")
	// The recorded amended tip is the commit undo is about to orphan — the
	// preview must name it before the user decides.
	const amended = "abc123def456abc123def456abc123def456abc1"
	entry.AbsorbedCommits = map[string]string{"feat-a": amended}

	callsBefore := f.callsSnapshot()
	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	assertNoMutation(t, f, callsBefore)
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, amended) || !strings.Contains(joined, "feat-a") || !strings.Contains(joined, "git cherry-pick") {
		t.Fatalf("notes = %v, want the amended SHA, branch, and cherry-pick pointer", res.Notes)
	}
	for _, b := range res.Blockers {
		if strings.Contains(b, amended) {
			t.Fatalf("absorb advisory is not a refusal, but landed in blockers: %v", res.Blockers)
		}
	}
}

// TestUndoPreviewRefMovedSince pins the dry-run side of the external-drift
// preflight: a ref that no longer sits where the op left it surfaces as a
// ref_moved_since blocker, while entries without PostRefs carry the
// unconditional-restore note instead.
func TestUndoPreviewRefMovedSince(t *testing.T) {
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

	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	found := false
	for _, b := range res.Blockers {
		if b == "ref_moved_since:a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers = %v, want ref_moved_since:a", res.Blockers)
	}

	// Step-2+ previews do not compare live tips: a real run would restore the
	// step-1 refs first, so drift there would be a ghost, not a blocker.
	deeper, err := UndoPreview(env, s, entry, false, 2)
	if err != nil {
		t.Fatalf("UndoPreview step 2: %v", err)
	}
	for _, b := range deeper.Blockers {
		if strings.HasPrefix(b, "ref_moved_since:") {
			t.Fatalf("deeper preview reported ghost drift: %v", deeper.Blockers)
		}
	}

	// A legacy entry (no PostRefs) reports the unconditional restore note.
	legacy := mustSnapshot(t, s, f, "modify")
	res, err = UndoPreview(env, s, legacy, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview legacy: %v", err)
	}
	joined := strings.Join(res.Notes, "\n")
	if !strings.Contains(joined, "unconditionally") {
		t.Fatalf("notes = %v, want the unconditional-restore note", res.Notes)
	}
}

// TestUndoPreviewPausedRebase pins the dry-run side of the pause gate: a
// branch with a rebase paused in a linked worktree surfaces as a
// paused_rebase blocker — the real undo refuses it upfront, and the preview
// must name the same refusal.
func TestUndoPreviewPausedRebase(t *testing.T) {
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

	res, err := UndoPreview(env, s, entry, false, 1)
	if err != nil {
		t.Fatalf("UndoPreview: %v", err)
	}
	found := false
	for _, b := range res.Blockers {
		if b == "paused_rebase:a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers = %v, want paused_rebase:a", res.Blockers)
	}
}

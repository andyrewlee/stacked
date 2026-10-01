package stack

// Failure-injection coverage: these tests exercise engine arms that only the
// fakeGit failErr/failAfter/calls knobs (and the Env.Save hook) can reach —
// probe failures, rollback double-faults, and save-failure windows. Each pins
// the observable contract (error text, journal/ref state), not internals.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRestackSurfacesRebaseProbeError pins rebaseFailure's probe-error arm:
// when RebaseOnto fails AND the RebaseInProgress probe itself fails, the
// returned error must carry BOTH — never a "no rebase in progress" misreport
// (which would tell the user nothing conflicts and nothing is paused).
func TestRestackSurfacesRebaseProbeError(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.commit("a2")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}

	f.rebaseErr["b"] = errors.New("rebase exploded")
	f.failErr["RebaseInProgress"] = errors.New("probe dead")

	_, err := Restack(env, s)
	if err == nil {
		t.Fatal("Restack should fail when the rebase errors and the probe dies")
	}
	msg := err.Error()
	if !strings.Contains(msg, "checking rebase state") || !strings.Contains(msg, "probe dead") {
		t.Fatalf("error %q must surface the probe failure, not claim no rebase", msg)
	}
	if !strings.Contains(msg, "rebase exploded") {
		t.Fatalf("error %q must preserve the original rebase error", msg)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("error %v must not be classified as a conflict — nothing is paused", err)
	}
}

// TestAbortSurfacesProbeError: Abort's first arm — the RebaseInProgress probe
// failing must surface as the operation error, not be misread as "no rebase".
func TestAbortSurfacesProbeError(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.rebaseActive = true
	f.rebaseBranch = "a"
	f.failErr["RebaseInProgress"] = errors.New("probe dead")

	if _, err := Abort(env, s); err == nil || !strings.Contains(err.Error(), "probe dead") {
		t.Fatalf("Abort with a dead probe = %v, want the probe error", err)
	}
}

// TestAbortSurfacesAbortFailure: RebaseAbort failing must surface wrapped as
// the operation error — the paused rebase is still there and the user must
// not be told it cleared.
func TestAbortSurfacesAbortFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.rebaseActive = true
	f.rebaseBranch = "a"
	f.rebaseAbortErr = errors.New("abort exploded")

	_, err := Abort(env, s)
	if err == nil || !strings.Contains(err.Error(), "aborting rebase") || !strings.Contains(err.Error(), "abort exploded") {
		t.Fatalf("Abort with a failed abort = %v, want the abort error surfaced", err)
	}
	if !f.rebaseActive {
		t.Fatal("a failed RebaseAbort must leave the rebase marked in progress")
	}
}

// TestAbortHeadNameFailureFallsBackToPendingReparent: when git's head-name
// file is unreadable (RebaseHeadName errors → ""), the pending reparent is
// the lone source of truth — abort still clears it.
func TestAbortHeadNameFailureFallsBackToPendingReparent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.rebaseActive = true
	f.rebaseBranch = "a"
	f.failErr["RebaseHeadName"] = errors.New("head-name unreadable")
	s.PendingReparent = &PendingReparent{Branch: "a", Parent: "main", ParentSHA: "deadbeef"}

	res, err := Abort(env, s)
	if err != nil {
		t.Fatalf("Abort with an unreadable head-name should still abort: %v", err)
	}
	if s.PendingReparent != nil {
		t.Fatal("pending reparent must be cleared after aborting its rebase")
	}
	if res == nil || !strings.Contains(res.Summary, "abort") {
		t.Fatalf("result = %+v, want an abort summary", res)
	}
}

// TestCreateCommitFailureAlsoFailsDelete pins the double-fault arm: the commit
// fails AND the compensating branch delete fails — the error must name both.
func TestCreateCommitFailureAlsoFailsDelete(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.commitErr = errors.New("commit exploded")
	f.deleteErr["x"] = errors.New("delete exploded")

	_, err := Create(env, s, "x", "msg", true)
	if err == nil {
		t.Fatal("Create should fail when the commit fails")
	}
	msg := err.Error()
	if !strings.Contains(msg, "commit exploded") || !strings.Contains(msg, "delete exploded") {
		t.Fatalf("error %q must name both the commit and the rollback failure", msg)
	}
	// The rollback could not run: the created branch is still there and the
	// state must NOT have tracked it.
	if _, ok := s.Get("x"); ok {
		t.Fatal("failed create must not track the branch")
	}
	if !f.BranchExists("x") {
		t.Fatal("failed delete rollback must leave the branch on disk")
	}
}

// TestSquashCommitFailureAlsoFailsRestore pins the squash rollback arm: the
// squashed commit fails AND the compensating ResetSoft back to the original
// tip fails — the error must name both. (Delete's parallel restoreHEAD arm is
// unreachable with per-name checkoutErr: the pre-delete parent checkout and
// the restore checkout hit the same name.)
func TestSquashCommitFailureAlsoFailsRestore(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.commit("a2") // squash needs >1 commit

	f.commitErr = errors.New("commit exploded")
	// First ResetSoft (to base) succeeds; the rollback ResetSoft fails.
	f.failAfter["ResetSoft"] = 1
	f.failErr["ResetSoft"] = errors.New("restore exploded")

	_, err := Squash(env, s, "squashed")
	if err == nil {
		t.Fatal("Squash should fail when the commit fails")
	}
	msg := err.Error()
	if !strings.Contains(msg, "commit exploded") || !strings.Contains(msg, "restore exploded") {
		t.Fatalf("error %q must name both the commit and the restore failure", msg)
	}
}

// TestOntoConflictSaveFailAbortFail pins the triple-fault: rebase pauses on a
// conflict, persisting the pending reparent fails, AND the compensating abort
// fails — both cleanup failures must be named alongside the conflict.
func TestOntoConflictSaveFailAbortFail(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	// From here every env.save() fails — the pending-reparent checkpoint in
	// the conflict arm is the first one Onto reaches.
	env, _ = envWithSaveErr(f, errors.New("disk full"), 0)
	f.conflictOn("b")
	f.rebaseAbortErr = errors.New("abort exploded")

	_, err := Onto(env, s, "main")
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("Onto with conflict+save+abort failures = %v, want ErrConflict", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "record pending reparent") || !strings.Contains(msg, "abort the in-progress rebase") {
		t.Fatalf("error %q must name both cleanup failures", msg)
	}
	if s.PendingReparent != nil {
		t.Fatal("in-memory pending reparent must be dropped to match the unpersisted disk")
	}
	if !f.rebaseActive {
		t.Fatal("a failed abort must leave the rebase marked in progress")
	}
}

// TestUndoSaveFailureStopsBeforeRefRestore: env.save() inside Undo runs
// BEFORE UpdateRefs — a persistence failure must abort the whole op with the
// refs untouched (a half-applied undo that saved nothing is worse than none).
func TestUndoSaveFailureStopsBeforeRefRestore(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	prev := &State{Trunk: "main", Branches: map[string]*Branch{}}
	raw, err := json.Marshal(prev)
	if err != nil {
		t.Fatal(err)
	}
	entry := &UndoEntry{
		Label:         "create a",
		State:         raw,
		Refs:          map[string]string{"main": f.branches["main"], "a": f.branches["a"]},
		LocalBranches: []string{"main", "a"},
	}

	saveErr := errors.New("disk full")
	env2, saves := envWithSaveErr(f, saveErr, 0)
	if _, err := Undo(env2, s, entry); !errors.Is(err, saveErr) {
		t.Fatalf("Undo with a failing save = %v, want %v", err, saveErr)
	}
	if *saves == 0 {
		t.Fatal("the checkpoint save was never attempted")
	}
	if f.calls["UpdateRefs"] != 0 {
		t.Fatalf("UpdateRefs ran %d times despite the save failure — refs moved with nothing persisted", f.calls["UpdateRefs"])
	}
}

// TestRepairedParentSHAMergeBaseFallback pins the silent-fallback arm: when
// MergeBase fails, repair falls back to the trunk tip — the test makes the
// fallback OBSERVED instead of silent.
func TestRepairedParentSHAMergeBaseFallback(t *testing.T) {
	f := newFakeGit()
	f.failErr["MergeBase"] = errors.New("merge-base dead")
	if got := repairedParentSHA(f, "main", "b", "trunk-tip"); got != "trunk-tip" {
		t.Fatalf("repairedParentSHA on MergeBase failure = %q, want the trunk-tip fallback", got)
	}
}

// TestRestackAgainstTipsRevParseFallback pins the other silent arm: a parent
// missing from the shared tips map falls back to a direct RevParse. The map
// only covers state branches, so a parent outside it must still resolve.
func TestRestackAgainstTipsRevParseFallback(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}

	// An empty tips map forces every parent lookup down the RevParse arm.
	_, _, err := s.restackAgainstTips(env, "b", map[string]string{}, "b")
	if err != nil {
		t.Fatalf("restackAgainstTips with an empty tips map: %v", err)
	}
	if f.calls["RevParse"] == 0 {
		t.Fatal("the missing-parent fallback never RevParsed")
	}
}

// TestRestackRestoreFailureComposesTwice pins the doubly-wrapped AlsoFailed:
// the cascade's per-branch rebase of b fails non-conflict (rebaseErr), the
// in-loop restore of the expected HEAD (a) ALSO fails (checkoutErr), and then
// Restack's outer restoreHEADAfterNonConflict fails AGAIN on the same name —
// composing an AlsoFailed-of-AlsoFailed. Both sentinels must stay errors.Is-
// matchable through the nesting, and HEAD stays on the failed branch.
func TestRestackRestoreFailureComposesTwice(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance-main") // both a and b now drift
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	rebaseErr := errors.New("rebase exploded")
	checkoutErr := errors.New("cannot restore")
	f.rebaseErr["b"] = rebaseErr
	f.checkoutErr["a"] = checkoutErr

	_, err := Restack(env, s)
	if err == nil {
		t.Fatal("Restack should fail when the rebase and both restores fail")
	}
	if !errors.Is(err, rebaseErr) {
		t.Fatalf("error %q must keep the rebase sentinel matchable", err)
	}
	if !errors.Is(err, checkoutErr) {
		t.Fatalf("error %q must keep the checkout sentinel matchable", err)
	}
	// The double composition is ugly but honest: the restore was attempted and
	// failed at BOTH layers, so both arms report it.
	if n := strings.Count(err.Error(), "additionally failed to restore"); n != 2 {
		t.Fatalf("error %q should name the restore failure at both layers (got %d)", err, n)
	}
	// The fake leaves HEAD wherever the failed rebase parked it — restore
	// genuinely could not run, so the caller sees b, not a.
	if f.head != "b" {
		t.Fatalf("HEAD = %q, want b (the failed-rebase parking spot)", f.head)
	}
}

// TestRestackCascadeSaveCheckpointFailure pins the mid-cascade save arm:
// restackBranch checkpoints state after EVERY branch it rebases, so a failure
// on the Nth save leaves earlier reparents durable and the Nth branch's in
// memory only. The sentinel must surface and HEAD must be restored.
func TestRestackCascadeSaveCheckpointFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance-main")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	saveErr := errors.New("save exploded")
	env2, saves := envWithSaveErr(f, saveErr, 1) // a's checkpoint ok, b's fails

	_, err := Restack(env2, s)
	if !errors.Is(err, saveErr) {
		t.Fatalf("Restack = %v, want the save sentinel surfaced", err)
	}
	if *saves != 2 {
		t.Fatalf("saves = %d, want 2 (one checkpoint per rebased branch)", *saves)
	}
	if f.head != "a" {
		t.Fatalf("HEAD = %q, want a restored after the save failure", f.head)
	}
	// Characterize, don't aspire: b's ParentSHA was mutated in memory BEFORE
	// its save failed — the state object says rebased while the journal
	// protocol's cleanup decides what persists.
	if b, _ := s.Get("b"); b == nil || b.ParentSHA == "" {
		t.Fatal("b should still be tracked with a (rebased) ParentSHA in memory")
	}
}

// TestFoldSaveCheckpointFailure pins the epilogue save arm: fold persists the
// deletion (save 1), then finishUpstack's final checkpoint (save 2) fails —
// the fold already happened; the error must surface, not be swallowed.
func TestFoldSaveCheckpointFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}

	saveErr := errors.New("save exploded")
	env2, saves := envWithSaveErr(f, saveErr, 1) // fold's save ok, epilogue fails

	_, err := Fold(env2, s)
	if !errors.Is(err, saveErr) {
		t.Fatalf("Fold = %v, want the save sentinel surfaced", err)
	}
	if *saves != 2 {
		t.Fatalf("saves = %d, want 2 (post-delete + epilogue)", *saves)
	}
	// The fold committed: b is gone from the in-memory state and HEAD sits on
	// the parent (the epilogue's restoreHEAD never ran — the save failed first).
	if s.IsTracked("b") {
		t.Fatal("b should be untracked in memory — the fold applied before the save failure")
	}
	if f.head != "a" {
		t.Fatalf("HEAD = %q, want a (fold's parent checkout)", f.head)
	}
}

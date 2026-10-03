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

// TestUndoStateSaveFailureRefsAlreadyRestored pins the post-transaction save
// arm: Undo restores every recorded ref in ONE UpdateRefs batch BEFORE it
// assigns/persists the snapshot, so when env.save() fails the refs are already
// restored (the error must say so), in-memory metadata is the snapshot, the
// simulated persisted state keeps the post-operation bytes, and retrying the
// retained entry completes.
func TestUndoStateSaveFailureRefsAlreadyRestored(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	entry := mustSnapshot(t, s, f, "modify")
	// Amend a so every recorded tip differs from the live post-operation ref —
	// the restore must be observable, not a same-value no-op.
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	postStateRaw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	saveErr := errors.New("disk full")
	saves := 0
	orderOK := true
	disk := postStateRaw // simulated persisted metadata
	env.Save = func() error {
		saves++
		// Each save attempt must follow exactly one UpdateRefs batch.
		if f.calls["UpdateRefs"] != saves {
			orderOK = false
		}
		if saves == 1 {
			return saveErr
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		disk = raw
		return nil
	}

	_, err = Undo(env, s, entry)
	if !errors.Is(err, saveErr) {
		t.Fatalf("Undo with a failing save = %v, want %v", err, saveErr)
	}
	if !strings.Contains(err.Error(), "already restored") {
		t.Fatalf("error %q must report that the refs were already restored", err)
	}
	if saves != 1 || f.calls["UpdateRefs"] != 1 || !orderOK {
		t.Fatalf("saves=%d UpdateRefs=%d orderOK=%v, want 1/1/true", saves, f.calls["UpdateRefs"], orderOK)
	}
	// Every recorded ref was restored even though the save failed.
	for name, sha := range entry.Refs {
		got, err := f.RevParse(branchTipRef(name))
		if err != nil || got != sha {
			t.Fatalf("ref %q = %q (%v) after failed save, want restored %q", name, got, err, sha)
		}
	}
	// In-memory metadata is the snapshot; the simulated disk still holds the
	// post-operation bytes.
	gotState, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var prev State
	if err := json.Unmarshal(entry.State, &prev); err != nil {
		t.Fatal(err)
	}
	wantState, err := json.Marshal(&prev)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotState) != string(wantState) {
		t.Fatalf("in-memory state after failed save:\n got  %s\nwant %s", gotState, wantState)
	}
	if string(disk) != string(postStateRaw) {
		t.Fatalf("persisted state moved despite the failed save:\n got  %s\nwant %s", disk, postStateRaw)
	}

	// Retry the same retained entry with a working save: the batch re-runs
	// idempotently, the snapshot persists, and the op completes.
	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("retry after save failure: %v", err)
	}
	gotState, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != string(gotState) {
		t.Fatalf("retry persisted %s, want the in-memory snapshot %s", disk, gotState)
	}
	assertUndoRestored(t, f, s, entry)
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
	_, _, err := s.restackAgainstTips(env, "b", map[string]string{}, s.ChildIndex(), "b")
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

// TestCreateStageFailurePropagates drives the Add knob: `st create -a` whose
// staging step fails must surface the error wrapped, and leave no branch.
func TestCreateStageFailurePropagates(t *testing.T) {
	f, s, env := newEnvState()
	boom := errors.New("add exploded")
	f.failErr["Add"] = boom

	if _, err := Create(env, s, "x", "msg", true); !errors.Is(err, boom) {
		t.Fatalf("Create -a = %v, want wrapped %v", err, boom)
	}
	if f.BranchExists("x") || s.IsTracked("x") {
		t.Fatal("a failed stage must not create or track the branch")
	}
}

// TestCreateBranchFailurePropagates drives the CreateBranch knob: the branch
// create itself fails (rather than only its precondition checks).
func TestCreateBranchFailurePropagates(t *testing.T) {
	f, s, env := newEnvState()
	boom := errors.New("create exploded")
	f.failErr["CreateBranch"] = boom

	_, err := Create(env, s, "x", "", false)
	if !errors.Is(err, boom) {
		t.Fatalf("Create = %v, want wrapped %v", err, boom)
	}
	if !strings.Contains(err.Error(), `creating branch "x"`) {
		t.Fatalf("Create = %v, want the branch named", err)
	}
	if s.IsTracked("x") {
		t.Fatal("a failed create must not track the branch")
	}
}

// TestRenameBranchFailurePropagates drives the RenameBranch knob: the git-side
// rename fails after all preconditions pass — state must be untouched.
func TestRenameBranchFailurePropagates(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	boom := errors.New("rename exploded")
	f.failErr["RenameBranch"] = boom

	_, err := Rename(env, s, "a", "a2")
	if !errors.Is(err, boom) {
		t.Fatalf("Rename = %v, want wrapped %v", err, boom)
	}
	if !strings.Contains(err.Error(), "renaming branch") {
		t.Fatalf("Rename = %v, want the rename step named", err)
	}
	if !s.IsTracked("a") || s.IsTracked("a2") || !f.BranchExists("a") {
		t.Fatal("a failed rename must leave the branch and its record intact")
	}
}

// TestRebaseContinueGenericFailure pins a non-restall RebaseContinue failure:
// while a rebase is paused on a known branch, a generic continue error is
// reported as a ConflictError (the rebase is still in progress — 'conflicted'
// is the truthful classification of the state the user is in).
func TestRebaseContinueGenericFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	f.conflictOn("feat-b")

	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "", true, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("Modify = %v, want the pause conflict", err)
	}
	boom := errors.New("continue exploded")
	f.failErr["RebaseContinue"] = boom

	_, err := Continue(env, s)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Continue = %v, want it classified as still-conflicted", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Branch != "feat-b" {
		t.Fatalf("Continue = %#v, want ConflictError on feat-b", err)
	}
	if inProgress, _ := f.RebaseInProgress(); !inProgress {
		t.Fatal("the paused rebase must survive a failed continue")
	}
}

// TestProbeFailuresSurface is the inventory table: every instrumented port
// probe whose failure arm was dormant gets driven once — the injected error
// must surface wrapped (never a panic, never swallowed into a wrong
// classification). `setup` builds the fixture; `run` invokes the op.
func TestProbeFailuresSurface(t *testing.T) {
	boom := errors.New("probe exploded")

	stack2 := func(t *testing.T) (*fakeGit, *State, Env) {
		t.Helper()
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		mkBranch(t, env, s, f, "a", "b")
		return f, s, env
	}
	drift := func(t *testing.T, f *fakeGit) {
		t.Helper()
		if err := f.Checkout("main"); err != nil {
			t.Fatal(err)
		}
		f.commit("advance-main")
	}

	cases := []struct {
		name    string
		method  string
		after   int // failAfter; -1 = fail every call
		setup   func(t *testing.T) (*fakeGit, *State, Env)
		run     func(env Env, f *fakeGit, s *State) error
		wantSub string
	}{
		{
			name:   "restack forest tips read",
			method: "Tips",
			after:  -1,
			setup: func(t *testing.T) (*fakeGit, *State, Env) {
				f, s, env := stack2(t)
				drift(t, f)
				return f, s, env
			},
			run:     func(env Env, _ *fakeGit, s *State) error { _, err := Restack(env, s); return err },
			wantSub: "read branch tips",
		},
		{
			name:   "post-rebase tip refresh",
			method: "RevParse",
			after:  0, // the refresh is the first RevParse on this path
			setup: func(t *testing.T) (*fakeGit, *State, Env) {
				f, s, env := stack2(t)
				drift(t, f)
				return f, s, env
			},
			run:     func(env Env, _ *fakeGit, s *State) error { _, err := Restack(env, s); return err },
			wantSub: `resolve "a" after restack`,
		},
		{
			name:   "prune tip read",
			method: "TipsFor",
			after:  -1,
			setup:  stack2,
			run: func(env Env, f *fakeGit, s *State) error {
				_, err := PruneMergedAgainst(env, s, branchTipRef(s.Trunk))
				return err
			},
			wantSub: "read tracked branch tips",
		},
		{
			name:   "prune merged probe",
			method: "MergedInto",
			after:  -1,
			setup:  stack2,
			run: func(env Env, f *fakeGit, s *State) error {
				_, err := PruneMergedAgainst(env, s, branchTipRef(s.Trunk))
				return err
			},
			wantSub: boom.Error(),
		},
		{
			name:   "squash subject read",
			method: "CommitSubjects",
			after:  -1,
			setup: func(t *testing.T) (*fakeGit, *State, Env) {
				f, s, env := newEnvState()
				mkBranch(t, env, s, f, "main", "a")
				f.commit("a2") // squash needs >1 commit
				return f, s, env
			},
			run:     func(env Env, _ *fakeGit, s *State) error { _, err := Squash(env, s, "sq"); return err },
			wantSub: boom.Error(),
		},
		{
			name:    "requireClean probe",
			method:  "IsClean",
			after:   -1,
			setup:   stack2,
			run:     func(env Env, _ *fakeGit, s *State) error { _, err := Restack(env, s); return err },
			wantSub: "checking working tree",
		},
		{
			name:   "staged-changes probe",
			method: "HasStagedChanges",
			after:  -1,
			setup: func(t *testing.T) (*fakeGit, *State, Env) {
				f, s, env := newEnvState()
				return f, s, env
			},
			run:     func(env Env, _ *fakeGit, s *State) error { _, err := Create(env, s, "x", "", false); return err },
			wantSub: "checking staged changes",
		},
		{
			name:   "unstaged-changes probe",
			method: "HasUnstagedChanges",
			after:  -1,
			setup:  stack2, // descendants(cur) non-empty only from a or main
			run: func(env Env, f *fakeGit, s *State) error {
				if err := f.Checkout("a"); err != nil {
					return err
				}
				_, err := Modify(env, s, "m", false, false)
				return err
			},
			wantSub: "checking unstaged changes",
		},
		{
			name:   "inferParent merged probe",
			method: "MergedInto",
			after:  -1,
			setup: func(t *testing.T) (*fakeGit, *State, Env) {
				f, s, env := stack2(t)
				if err := f.Checkout("b"); err != nil {
					t.Fatal(err)
				}
				if err := f.CreateBranch("loose"); err != nil {
					t.Fatal(err)
				}
				return f, s, env
			},
			run:     func(env Env, _ *fakeGit, s *State) error { _, err := TrackBranch(env, s, "loose", ""); return err },
			wantSub: boom.Error(),
		},
		{
			name:   "sync worktree snapshot",
			method: "Worktrees",
			after:  -1,
			setup: func(t *testing.T) (*fakeGit, *State, Env) {
				f, s, env := stack2(t)
				if err := f.Checkout("a"); err != nil {
					t.Fatal(err)
				}
				return f, s, env
			},
			run: func(env Env, _ *fakeGit, s *State) error {
				_, err := Sync(env, &fakeRemote{}, s, "origin", true, true)
				return err
			},
			wantSub: boom.Error(),
		},
		{
			name:   "undo created-worktree probe",
			method: "Worktrees",
			after:  -1,
			setup: func(t *testing.T) (*fakeGit, *State, Env) {
				f, s, env := stack2(t)
				if err := f.CreateBranchAt("c", "main"); err != nil {
					t.Fatal(err)
				}
				f.addWorktree("/wt/c", "c")
				return f, s, env
			},
			run: func(env Env, f *fakeGit, s *State) error {
				entry := mustSnapshot(t, s, f, "worktree")
				entry.CreatedBranches = []string{"c"}
				_, err := Undo(env, s, entry)
				return err
			},
			wantSub: boom.Error(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, s, env := tc.setup(t)
			f.failErr[tc.method] = boom
			if tc.after >= 0 {
				f.failAfter[tc.method] = tc.after
			}
			err := tc.run(env, f, s)
			if !errors.Is(err, boom) {
				t.Fatalf("%s probe failure = %v, want wrapped %v", tc.method, err, boom)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("%s probe failure = %v, want message containing %q", tc.method, err, tc.wantSub)
			}
		})
	}
}

// TestSnapshotUndoCurrentBranchDegrade pins the deliberate degrade the audit
// found unpinned: snapshotUndo swallows a CurrentBranch failure and records ""
// — indistinguishable from detached HEAD — so the undo of that operation skips
// the final checkout restore entirely.
func TestSnapshotUndoCurrentBranchDegrade(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	f.failErr["CurrentBranch"] = errors.New("probe exploded")
	entry, err := s.snapshotUndo(f, "create")
	if err != nil {
		t.Fatalf("snapshotUndo: %v — a dead CurrentBranch probe must not fail the snapshot", err)
	}
	if entry.CurrentBranch != "" {
		t.Fatalf("entry.CurrentBranch = %q, want the swallowed-probe degrade", entry.CurrentBranch)
	}
	f.failErr["CurrentBranch"] = nil

	// Undoing with the degraded entry must not check a branch back out: HEAD
	// stays wherever the user left it (here main, not the restored a).
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(env, s, entry); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if f.head == "a" {
		t.Fatal("undo restored HEAD to a despite the snapshot recording no current branch")
	}
}

// TestCascadeExpectedHeadDegrade pins the sibling degrade: currentBranchOr
// swallows the same probe for the cascade's expectedHEAD, so a mid-cascade
// rebase failure skips the in-loop restore ("" = never restore). The OUTER
// restoreHEAD still tries the original start — the difference from the
// healthy run is one "additionally failed to restore" layer, not two (contrast
// TestRestackRestoreFailureComposesTwice).
func TestCascadeExpectedHeadDegrade(t *testing.T) {
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

	rebaseErr := errors.New("rebase exploded")
	checkoutErr := errors.New("cannot restore")
	f.rebaseErr["b"] = rebaseErr
	f.checkoutErr["a"] = checkoutErr
	// f.calls counts fixture calls too (mkBranch→Create probes CurrentBranch).
	// Anchor on the live count: Restack's own start read and restackBranch's
	// expectedHEAD read stay healthy (+2); the forest's currentBranchOr fails.
	f.failErr["CurrentBranch"] = errors.New("probe exploded")
	f.failAfter["CurrentBranch"] = f.calls["CurrentBranch"] + 2

	_, err := Restack(env, s)
	if !errors.Is(err, rebaseErr) {
		t.Fatalf("Restack = %v, want the rebase sentinel matchable", err)
	}
	if !errors.Is(err, checkoutErr) {
		t.Fatalf("Restack = %v, want the outer restore failure matchable", err)
	}
	if n := strings.Count(err.Error(), "additionally failed to restore"); n != 1 {
		t.Fatalf("Restack = %q, want exactly ONE restore arm (the in-loop restore was skipped), got %d", err, n)
	}
	if f.head != "b" {
		t.Fatalf("HEAD = %q, want b — the failed rebase parked it and no restore landed", f.head)
	}
}

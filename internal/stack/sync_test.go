package stack

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeRemote is an in-memory Remote port for exercising Sync without a real
// remote. The trunk fast-forward is whatever ff is set to.
type fakeRemote struct {
	exists   bool
	ff       string
	err      error
	checkout *fakeGit
}

func (r *fakeRemote) Exists(string) bool { return r.exists }
func (r *fakeRemote) Fetch(string) error { return nil }
func (r *fakeRemote) FastForward(trunk, _ string) (string, error) {
	if r.checkout != nil {
		if err := r.checkout.Checkout(trunk); err != nil {
			return "", err
		}
	}
	if r.err != nil {
		return "", r.err
	}
	return r.ff, nil
}

func TestContinueResolvesConflict(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")

	// feat-b will conflict the next time it is restacked.
	f.conflictOn("feat-b")

	// Amend feat-a; Modify restacks the upstack and hits the conflict on feat-b.
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "", true, false); err == nil {
		t.Fatal("expected a conflict from the upstack restack")
	} else if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if inProgress, _ := f.RebaseInProgress(); !inProgress {
		t.Fatal("expected a rebase in progress after the conflict")
	}
	if name, _ := f.RebaseHeadName(); name != "feat-b" {
		t.Fatalf("RebaseHeadName = %q, want feat-b", name)
	}

	// Resolve + continue.
	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	aTip, _ := f.RevParse("feat-a")
	bb, _ := s.Get("feat-b")
	if bb.ParentSHA != aTip {
		t.Fatalf("feat-b.ParentSHA = %s, want amended feat-a tip %s", bb.ParentSHA, aTip)
	}
	if !mustFakeIsAncestor(t, f, aTip, "feat-b") {
		t.Fatal("feat-b was not rebased onto the amended feat-a")
	}
}

func TestContinueWithoutRebase(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	if _, err := Continue(env, s); err == nil {
		t.Fatal("continue with no rebase in progress should error")
	}
}

func TestRestackBranchReturnsNonConflictRebaseErrors(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	f.commit("new-a")
	errBoom := errors.New("pre-rebase hook rejected")
	f.rebaseErr["feat-b"] = errBoom

	_, err := s.RestackBranch(env, "feat-b")
	if !errors.Is(err, errBoom) {
		t.Fatalf("RestackBranch error = %v, want %v", err, errBoom)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("RestackBranch returned ErrConflict for non-started rebase: %v", err)
	}
	if f.head != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a restored", f.head)
	}
	if inProgress, _ := f.RebaseInProgress(); inProgress {
		t.Fatal("rebase should not be in progress")
	}
}

func TestSyncPrunesMergedAndRestacks(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")

	// Simulate feat-a having merged into the trunk: advance main to feat-a's tip
	// (main is not checked out, so this is allowed).
	aTip, _ := f.RevParse("feat-a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if s.IsTracked("feat-a") {
		t.Fatal("feat-a should have been pruned as merged")
	}
	if got := res.Deleted; len(got) != 1 || got[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", got)
	}
	if b, _ := s.Get("feat-b"); b == nil || b.Parent != "main" {
		t.Fatalf("feat-b should be re-parented onto main: %+v", b)
	}
}

func TestSyncPrunesCurrentMergedBranchWithoutRemote(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	aTip, _ := f.RevParse("feat-a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if s.IsTracked("feat-a") || f.BranchExists("feat-a") {
		t.Fatal("feat-a should have been pruned as merged")
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q, want main after pruning current branch", f.head)
	}
	if got := res.Deleted; len(got) != 1 || got[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", got)
	}
}

func TestSyncPersistsEachSuccessfulPrune(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "main", "b")
	aTip, _ := f.RevParse("a")
	bTip, _ := f.RevParse("b")
	if err := f.ForceBranch("main", bTip); err != nil {
		t.Fatal(err)
	}
	// Make both branches ancestors of trunk while keeping the branch names sorted
	// so a prunes successfully before b fails deletion.
	f.commits[f.branches["main"]].parent = aTip
	f.deleteErr["b"] = errors.New("branch checked out elsewhere")

	var savedAfterA bool
	env.Save = func() error {
		savedAfterA = savedAfterA || (!s.IsTracked("a") && s.IsTracked("b"))
		return nil
	}

	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false); err == nil {
		t.Fatal("sync should fail when pruning b fails")
	}
	if !savedAfterA {
		t.Fatal("sync did not persist after successfully pruning a")
	}
	if f.head != "b" {
		t.Fatalf("HEAD = %q, want b restored after prune failure", f.head)
	}
}

func TestSyncRestoresOriginalBranchWhenFastForwardFails(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")

	errBoom := errors.New("fast-forward failed")
	if _, err := Sync(env, &fakeRemote{exists: true, err: errBoom, checkout: f}, s, "origin", false); !errors.Is(err, errBoom) {
		t.Fatalf("Sync error = %v, want %v", err, errBoom)
	}
	if f.head != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a restored", f.head)
	}
}

func TestSyncRestoresOriginalBranchWhenRestackFailsWithoutConflict(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	f.commit("new-a")
	errBoom := errors.New("pre-rebase hook rejected")
	f.rebaseErr["feat-b"] = errBoom
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}

	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false); !errors.Is(err, errBoom) {
		t.Fatalf("Sync error = %v, want %v", err, errBoom)
	}
	if f.head != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a restored", f.head)
	}
}

func TestSyncPlanSimulatesPruneBeforeRestackPlan(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	aTip, _ := f.RevParse("feat-a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}

	res, err := SyncPlan(env, s, false)
	if err != nil {
		t.Fatalf("SyncPlan: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-a" {
		t.Fatalf("Deleted = %v, want [feat-a]", res.Deleted)
	}
	if len(res.Restacked) != 0 {
		t.Fatalf("Restacked = %v, want empty after simulated prune", res.Restacked)
	}
	if b, _ := s.Get("feat-b"); b.Parent != "feat-a" {
		t.Fatalf("SyncPlan mutated state parent to %q", b.Parent)
	}
}

func TestSyncPlanRefusesDirtyMergedBranchWorktree(t *testing.T) {
	setup := func(t *testing.T) (*fakeGit, *State, Env) {
		t.Helper()
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "feat-a")
		aTip, _ := f.RevParse("feat-a")
		if err := f.ForceBranch("main", aTip); err != nil {
			t.Fatal(err)
		}
		if err := f.Checkout("main"); err != nil {
			t.Fatal(err)
		}
		f.addWorktree("/wt/feat-a", "feat-a")
		f.markWorktreeDirty("feat-a")
		return f, s, env
	}

	f, s, env := setup(t)
	before := cloneState(s)
	_, planErr := SyncPlan(env, s, false)
	if planErr == nil {
		t.Fatal("SyncPlan must refuse a merged branch with a dirty linked worktree")
	}
	if !strings.Contains(planErr.Error(), "uncommitted changes in its worktree") {
		t.Fatalf("SyncPlan error = %v, want dirty worktree validation", planErr)
	}
	if after := cloneState(s); !reflect.DeepEqual(after, before) {
		t.Fatalf("SyncPlan mutated state: before=%+v after=%+v", before, after)
	}
	if !f.BranchExists("feat-a") || !s.IsTracked("feat-a") {
		t.Fatal("SyncPlan must not delete or untrack feat-a")
	}

	_, liveS, liveEnv := setup(t)
	_, liveErr := Sync(liveEnv, &fakeRemote{exists: false}, liveS, "origin", false)
	if liveErr == nil {
		t.Fatal("live Sync must refuse the same dirty linked worktree")
	}
	if planErr.Error() != liveErr.Error() {
		t.Fatalf("SyncPlan error = %q, live Sync error = %q", planErr.Error(), liveErr.Error())
	}
}

func TestSyncPlanErrorsWhenTrackedBranchTipIsMissing(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	delete(f.branches, "feat-a")

	before := cloneState(s)
	if _, err := SyncPlan(env, s, false); err == nil {
		t.Fatal("SyncPlan with a missing tracked branch returned nil error")
	}
	if after := cloneState(s); !reflect.DeepEqual(after, before) {
		t.Fatalf("SyncPlan mutated state: before=%+v after=%+v", before, after)
	}
}

func TestSyncPlanAgainstRemoteTrunkRestacks(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")

	local, err := SyncPlan(env, s, false)
	if err != nil {
		t.Fatalf("SyncPlan: %v", err)
	}
	if len(local.Restacked) != 0 {
		t.Fatalf("local Restacked = %v, want none", local.Restacked)
	}

	mainTip, _ := f.RevParse("main")
	remoteTip := f.newID()
	f.commits[remoteTip] = &fakeCommit{id: remoteTip, parent: mainTip, subject: "remote main"}
	remote, err := SyncPlanAgainst(env, s, false, remoteTip)
	if err != nil {
		t.Fatalf("SyncPlanAgainst: %v", err)
	}
	if len(remote.Restacked) != 1 || remote.Restacked[0] != "feat-a" {
		t.Fatalf("remote Restacked = %v, want [feat-a]", remote.Restacked)
	}
}

func TestSyncPlanAgainstMergedAndRestackPreview(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-merged")
	mergedTip, _ := f.RevParse("feat-merged")
	mkBranch(t, env, s, f, "main", "feat-restack")
	remoteTip := f.newID()
	f.commits[remoteTip] = &fakeCommit{id: remoteTip, parent: mergedTip, subject: "remote main"}

	res, err := SyncPlanAgainst(env, s, false, remoteTip)
	if err != nil {
		t.Fatalf("SyncPlanAgainst: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-merged" {
		t.Fatalf("Deleted = %v, want [feat-merged]", res.Deleted)
	}
	if len(res.Restacked) != 1 || res.Restacked[0] != "feat-restack" {
		t.Fatalf("Restacked = %v, want [feat-restack]", res.Restacked)
	}
}

func TestSyncNoDeleteKeepsMerged(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	aTip, _ := f.RevParse("feat-a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", true); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !s.IsTracked("feat-a") {
		t.Fatal("feat-a should be kept with --no-delete")
	}
}

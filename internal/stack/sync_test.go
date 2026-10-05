package stack

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fakeRemote is an in-memory Remote port for exercising Sync without a real
// remote. The trunk fast-forward is whatever ff is set to. It records the
// owner-resolution arguments the engine passed and mirrors the production
// contract's dirty-owner refusal so the engine wiring is testable.
type fakeRemote struct {
	exists   bool
	ff       string
	err      error
	checkout *fakeGit

	git *fakeGit // when set, enforce the clean-owner contract for ownerDir

	gotOwnerDir       string
	gotCheckedOutHere bool
	called            bool
	fetches           int
	fetchErr          error
}

func (r *fakeRemote) Exists(string) bool { return r.exists }
func (r *fakeRemote) Fetch(string) error {
	r.fetches++
	return r.fetchErr
}

func (r *fakeRemote) FastForward(trunk, _, ownerDir string, checkedOutHere bool) (string, error) {
	r.called = true
	r.gotOwnerDir = ownerDir
	r.gotCheckedOutHere = checkedOutHere
	if r.checkout != nil {
		if err := r.checkout.Checkout(trunk); err != nil {
			return "", err
		}
	}
	if r.err != nil {
		return "", r.err
	}
	if ownerDir != "" && r.git != nil {
		clean, err := r.git.IsCleanIn(ownerDir)
		if err != nil {
			return "", err
		}
		if !clean {
			return "", fmt.Errorf("trunk %q has uncommitted changes in its worktree %s; commit or stash there before syncing", trunk, ownerDir)
		}
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

	_, err := s.restackBranch(env, "feat-b")
	if !errors.Is(err, errBoom) {
		t.Fatalf("restackBranch error = %v, want %v", err, errBoom)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("restackBranch returned ErrConflict for non-started rebase: %v", err)
	}
	if f.head != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a restored", f.head)
	}
	if inProgress, _ := f.RebaseInProgress(); inProgress {
		t.Fatal("rebase should not be in progress")
	}
}

func TestSyncFetchFailurePropagates(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	f.remoteRefs["refs/remotes/origin/main"] = "ignored" // remote exists for the exists check
	boom := errors.New("fetch exploded")

	_, err := Sync(env, &fakeRemote{exists: true, fetchErr: boom}, s, "origin", false, false)
	if !errors.Is(err, boom) {
		t.Fatalf("Sync = %v, want wrapped %v", err, boom)
	}
	if !strings.Contains(err.Error(), `fetch "origin"`) {
		t.Fatalf("Sync = %v, want the remote named", err)
	}
}

func TestSyncPrunesMergedAndRestacks(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")

	// Simulate feat-a having merged into the trunk: advance main to feat-a's tip
	// (main is not checked out, so this is allowed).
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
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

// TestSyncPrunesSquashMerged exercises the content-containment prune: feat-a's
// PR squash-merged on the host, so its commits are NOT ancestors of main
// (MergedInto misses it), but its whole diff landed as one trunk commit —
// ChangesContainedIn catches it. feat-b is re-parented and feat-c, carrying
// unique content, survives.
func TestSyncPrunesSquashMerged(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	f.commit("a2") // second commit on feat-a: the squash is multi-commit
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	mkBranch(t, env, s, f, "main", "feat-c")

	f.squashInto(t, "main", "feat-a")

	// Sanity: feat-a's tip is genuinely not an ancestor of main — this prune
	// can only come from content containment.
	if mustFakeIsAncestor(t, f, "feat-a", "main") {
		t.Fatal("test setup: feat-a should not be an ancestor of main after a squash-merge")
	}

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := res.Deleted; len(got) != 1 || got[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", got)
	}
	if s.IsTracked("feat-a") || f.BranchExists("feat-a") {
		t.Fatal("squash-merged feat-a should have been pruned")
	}
	if b, _ := s.Get("feat-b"); b == nil || b.Parent != "main" {
		t.Fatalf("feat-b should be re-parented onto main: %+v", b)
	}
	if !s.IsTracked("feat-c") || !f.BranchExists("feat-c") {
		t.Fatal("feat-c has unique content and must survive the prune")
	}
}

// TestSyncKeepsSquashMergedBranchWithNewContent is the false-positive guard: a
// branch that was squash-merged but then gained a commit whose content is not
// on the trunk is NOT contained and must be kept.
func TestSyncKeepsSquashMergedBranchWithNewContent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	f.squashInto(t, "main", "feat-a")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	f.commit("wip") // new work on top of the squash-merged commits

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Deleted) != 0 {
		t.Fatalf("deleted = %v, want none — feat-a has content main lacks", res.Deleted)
	}
	if !s.IsTracked("feat-a") || !f.BranchExists("feat-a") {
		t.Fatal("feat-a must survive: it carries content the trunk does not have")
	}
}

// TestSyncPlanPreviewsSquashMergedPrune pins the dry-run path to the same
// detection the live sync runs: a squash-merged branch shows up in Deleted
// without anything being mutated.
func TestSyncPlanPreviewsSquashMergedPrune(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	f.squashInto(t, "main", "feat-a")

	res, err := SyncPlanAgainst(env, s, false, branchTipRef(s.Trunk))
	if err != nil {
		t.Fatalf("sync preview: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-a" {
		t.Fatalf("Deleted = %v, want [feat-a]", res.Deleted)
	}
	if !s.IsTracked("feat-a") || !f.BranchExists("feat-a") {
		t.Fatal("sync preview must not prune feat-a")
	}
	if b, _ := s.Get("feat-b"); b.Parent != "feat-a" {
		t.Fatalf("sync preview mutated state parent to %q", b.Parent)
	}
}

// TestSyncPlanAgainstSquashMergedOnRemoteTrunk runs the prune preview against
// a remote-tracking tip (a bare SHA carrying the squash commit) — the
// after-fetch dry-run shape — and confirms the squash-merge is still caught.
func TestSyncPlanAgainstSquashMergedOnRemoteTrunk(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "main", "feat-b")

	mainTip, err := f.RevParse("main")
	if err != nil {
		t.Fatal(err)
	}
	// The remote trunk gained one commit: the host-side squash of feat-a.
	remoteTip := f.newID()
	f.commits[remoteTip] = &fakeCommit{id: remoteTip, parent: mainTip, subject: "squash feat-a", content: f.squashTokens(t, "main", "feat-a")}

	res, err := SyncPlanAgainst(env, s, false, remoteTip)
	if err != nil {
		t.Fatalf("SyncPlanAgainst: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-a" {
		t.Fatalf("Deleted = %v, want [feat-a]", res.Deleted)
	}
	if len(res.Restacked) != 1 || res.Restacked[0] != "feat-b" {
		t.Fatalf("Restacked = %v, want [feat-b]", res.Restacked)
	}
}

func TestSyncPrunesCurrentMergedBranchWithoutRemote(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
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

	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false); err == nil {
		t.Fatal("sync should fail when pruning b fails")
	}
	if !savedAfterA {
		t.Fatal("sync did not persist after successfully pruning a")
	}
	if f.head != "b" {
		t.Fatalf("HEAD = %q, want b restored after prune failure", f.head)
	}
}

// Plan-019's spawn contract: a multi-branch prune issues ONE batched
// `git branch -D` (DeleteBranches) instead of a spawn per branch, and the
// delete fallback never fires when the batch succeeds.
func TestSyncPruneDeletesInOneBatch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "main", "b")
	mkBranch(t, env, s, f, "main", "c")
	// All three tracked under main in state, but merged in git: splice the
	// commit ancestry so main's tip contains every branch's tip.
	aTip, _ := f.RevParse("a")
	bTip, _ := f.RevParse("b")
	cTip, _ := f.RevParse("c")
	f.commits[cTip].parent = bTip
	f.commits[bTip].parent = aTip
	if err := f.ForceBranch("main", cTip); err != nil {
		t.Fatal(err)
	}

	before := f.callsSnapshot()
	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := f.calls["DeleteBranches"] - before["DeleteBranches"]; got != 1 {
		t.Fatalf("DeleteBranches calls = %d, want 1 batched delete for 3 pruned branches", got)
	}
	if got := f.calls["DeleteBranch"] - before["DeleteBranch"]; got != 0 {
		t.Fatalf("DeleteBranch calls = %d, want 0 — the fallback only runs on batch failure", got)
	}
	for _, name := range []string{"a", "b", "c"} {
		if f.BranchExists(name) || s.IsTracked(name) {
			t.Fatalf("merged branch %q survived the batched prune", name)
		}
	}
}

// When the batched delete partially fails, applyPrune must retry the
// SURVIVORS per-branch (git's multi-delete already removed the others — a
// blanket retry would report "no such branch" on them), name the real
// failure, and still untrack+checkpoint the branches the batch did delete.
func TestSyncPruneBatchFailureRetriesSurvivors(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "main", "b")
	mkBranch(t, env, s, f, "main", "c")
	aTip, _ := f.RevParse("a")
	bTip, _ := f.RevParse("b")
	cTip, _ := f.RevParse("c")
	f.commits[cTip].parent = bTip
	f.commits[bTip].parent = aTip
	if err := f.ForceBranch("main", cTip); err != nil {
		t.Fatal(err)
	}
	delErr := errors.New("branch checked out elsewhere")
	f.deleteErr["b"] = delErr

	_, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if !errors.Is(err, delErr) {
		t.Fatalf("Sync = %v, want %v", err, delErr)
	}
	if !strings.Contains(err.Error(), `delete merged branch "b"`) {
		t.Fatalf("Sync = %v, want the failing branch named", err)
	}
	// The batch deleted a and c before b's failure — both must be untracked
	// and reported, not left half-pruned.
	if f.BranchExists("a") || f.BranchExists("c") {
		t.Fatal("branches the batch deleted are still live in the fake")
	}
	if s.IsTracked("a") || s.IsTracked("c") {
		t.Fatal("deleted branches still tracked — checkpoint phase skipped them")
	}
	if !f.BranchExists("b") {
		t.Fatal("the failing branch was deleted anyway")
	}
}

func TestSyncRestoresOriginalBranchWhenFastForwardFails(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")

	errBoom := errors.New("fast-forward failed")
	if _, err := Sync(env, &fakeRemote{exists: true, err: errBoom, checkout: f}, s, "origin", false, false); !errors.Is(err, errBoom) {
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

	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false); !errors.Is(err, errBoom) {
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
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}

	res, err := SyncPlanAgainst(env, s, false, branchTipRef(s.Trunk))
	if err != nil {
		t.Fatalf("sync preview: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-a" {
		t.Fatalf("Deleted = %v, want [feat-a]", res.Deleted)
	}
	if len(res.Restacked) != 0 {
		t.Fatalf("Restacked = %v, want empty after simulated prune", res.Restacked)
	}
	if b, _ := s.Get("feat-b"); b.Parent != "feat-a" {
		t.Fatalf("sync preview mutated state parent to %q", b.Parent)
	}
}

func TestSyncPlanRefusesDirtyMergedBranchWorktree(t *testing.T) {
	setup := func(t *testing.T) (*fakeGit, *State, Env) {
		t.Helper()
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "feat-a")
		aTip, _ := f.RevParse("feat-a")
		if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
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
	_, planErr := SyncPlanAgainst(env, s, false, branchTipRef(s.Trunk))
	if planErr == nil {
		t.Fatal("sync preview must refuse a merged branch with a dirty linked worktree")
	}
	if !strings.Contains(planErr.Error(), "uncommitted changes in its worktree") {
		t.Fatalf("sync preview error = %v, want dirty worktree validation", planErr)
	}
	if after := cloneState(s); !reflect.DeepEqual(after, before) {
		t.Fatalf("sync preview mutated state: before=%+v after=%+v", before, after)
	}
	if !f.BranchExists("feat-a") || !s.IsTracked("feat-a") {
		t.Fatal("sync preview must not delete or untrack feat-a")
	}

	_, liveS, liveEnv := setup(t)
	_, liveErr := Sync(liveEnv, &fakeRemote{exists: false}, liveS, "origin", false, false)
	if liveErr == nil {
		t.Fatal("live Sync must refuse the same dirty linked worktree")
	}
	if planErr.Error() != liveErr.Error() {
		t.Fatalf("sync preview error = %q, live Sync error = %q", planErr.Error(), liveErr.Error())
	}
}

func TestSyncPlanRejectsPrunedMainWorktreeOwnerFromLinkedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "main", "feat-b")
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	env.Git = mainOwnerFromLinkedGit(f, "feat-a", "feat-b")

	before := cloneState(s)
	_, err := SyncPlanAgainst(env, s, false, branchTipRef(s.Trunk))
	if err == nil {
		t.Fatal("sync preview pruning a branch checked out in the main worktree returned nil error")
	}
	if !strings.Contains(err.Error(), "main worktree") {
		t.Fatalf("sync preview error = %v, want main worktree context", err)
	}
	if after := cloneState(s); !reflect.DeepEqual(after, before) {
		t.Fatalf("sync preview mutated state: before=%+v after=%+v", before, after)
	}
}

func TestSyncPlanErrorsWhenTrackedBranchTipIsMissing(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	delete(f.branches, "feat-a")

	before := cloneState(s)
	if _, err := SyncPlanAgainst(env, s, false, branchTipRef(s.Trunk)); err == nil {
		t.Fatal("sync preview with a missing tracked branch returned nil error")
	}
	if after := cloneState(s); !reflect.DeepEqual(after, before) {
		t.Fatalf("sync preview mutated state: before=%+v after=%+v", before, after)
	}
}

func TestSyncPlanAgainstRemoteTrunkRestacks(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")

	local, err := SyncPlanAgainst(env, s, false, branchTipRef(s.Trunk))
	if err != nil {
		t.Fatalf("sync preview: %v", err)
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
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(env, &fakeRemote{exists: false}, s, "origin", true, false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !s.IsTracked("feat-a") {
		t.Fatal("feat-a should be kept with --no-delete")
	}
}

// The four tests below pin sync's owner-aware trunk handling: the engine
// resolves where the trunk is checked out and drives the fast-forward there,
// detaching HEAD (instead of checking out the trunk) before pruning when the
// trunk lives in another worktree.

func TestSyncFastForwardsTrunkInItsOwnWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	f.addWorktree("/wt/trunk", "main")
	if err := f.Checkout("feat-b"); err != nil {
		t.Fatal(err)
	}
	// Simulate feat-a merged into the trunk, and arm git's refusal to check the
	// trunk out a second time so any stray Checkout(trunk) fails the test.
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	f.checkoutErr["main"] = errors.New("fatal: 'main' is already checked out at '/wt/trunk'")

	remote := &fakeRemote{exists: true, ff: "main fast-forwarded to refs/remotes/origin/main", git: f}
	res, err := Sync(env, remote, s, "origin", false, false)
	if err != nil {
		t.Fatalf("sync from linked worktree: %v", err)
	}
	if remote.gotOwnerDir != "/wt/trunk" || remote.gotCheckedOutHere {
		t.Fatalf("FastForward got (ownerDir=%q, here=%v), want (/wt/trunk, false)", remote.gotOwnerDir, remote.gotCheckedOutHere)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", res.Deleted)
	}
	if f.head != "feat-b" {
		t.Fatalf("HEAD = %q after sync, want feat-b restored", f.head)
	}
}

func TestSyncEndsDetachedWhenOrigPrunedAndTrunkOwnedElsewhere(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	f.addWorktree("/wt/trunk", "main")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	f.checkoutErr["main"] = errors.New("fatal: 'main' is already checked out at '/wt/trunk'")

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if f.BranchExists("feat-a") {
		t.Fatal("merged feat-a should have been pruned")
	}
	if f.head != "" || f.detachedAt == "" {
		t.Fatalf("HEAD = (%q, %q), want detached after orig was pruned", f.head, f.detachedAt)
	}
	wantNote := "HEAD left detached; trunk is checked out in /wt/trunk"
	found := false
	for _, note := range res.Notes {
		if note == wantNote {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %v, want %q", res.Notes, wantNote)
	}
}

func TestSyncFailsWhenTrunkWorktreeDirty(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "feat-a", "feat-b")
	f.addWorktree("/wt/trunk", "main")
	if err := f.Checkout("feat-b"); err != nil {
		t.Fatal(err)
	}
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	f.markWorktreeDirty("main")

	remote := &fakeRemote{exists: true, ff: "unused", git: f}
	_, err := Sync(env, remote, s, "origin", false, false)
	if err == nil {
		t.Fatal("sync with a dirty trunk worktree should fail")
	}
	if !strings.Contains(err.Error(), "/wt/trunk") {
		t.Fatalf("error %v does not name the trunk worktree path", err)
	}
	if !s.IsTracked("feat-a") {
		t.Fatal("failed sync pruned feat-a anyway")
	}
	if f.head != "feat-b" {
		t.Fatalf("HEAD = %q, want feat-b restored after failed sync", f.head)
	}
}

func TestSyncPassesTrunkCheckoutLocationToFastForward(t *testing.T) {
	// Trunk checked out here: checkedOutHere=true, no owner dir.
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{exists: true, ff: "main already up to date"}
	if _, err := Sync(env, remote, s, "origin", false, false); err != nil {
		t.Fatalf("sync on trunk: %v", err)
	}
	if !remote.gotCheckedOutHere || remote.gotOwnerDir != "" {
		t.Fatalf("FastForward got (ownerDir=%q, here=%v), want (\"\", true)", remote.gotOwnerDir, remote.gotCheckedOutHere)
	}

	// Trunk checked out nowhere (single worktree, HEAD on a feature branch):
	// both zero — the shell takes the guarded ref-only path.
	f2, s2, env2 := newEnvState()
	mkBranch(t, env2, s2, f2, "main", "feat-a")
	if err := f2.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	remote2 := &fakeRemote{exists: true, ff: "main already up to date"}
	if _, err := Sync(env2, remote2, s2, "origin", false, false); err != nil {
		t.Fatalf("sync off trunk: %v", err)
	}
	if remote2.gotCheckedOutHere || remote2.gotOwnerDir != "" {
		t.Fatalf("FastForward got (ownerDir=%q, here=%v), want (\"\", false)", remote2.gotOwnerDir, remote2.gotCheckedOutHere)
	}
}

// TestSyncNoteReportsReattachedBranchWhenSurvivorRebases pins the note's
// accuracy: when orig is pruned and a SURVIVING branch then rebases in place,
// git re-attaches HEAD to that branch — the note must name it instead of
// claiming "detached" (the no-survivor case keeps the detached note).
func TestSyncNoteReportsReattachedBranchWhenSurvivorRebases(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "main", "feat-b") // survivor: sibling of feat-a
	f.addWorktree("/wt/trunk", "main")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	// feat-a merged into main AND main advanced past it, so feat-a prunes and
	// the surviving feat-b needs (and gets) an in-place rebase.
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	f.checkoutErr["main"] = errors.New("fatal: 'main' is already checked out at '/wt/trunk'")

	res, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if f.BranchExists("feat-a") {
		t.Fatal("merged feat-a should have been pruned")
	}
	if f.head != "feat-b" {
		t.Fatalf("HEAD = %q, want re-attached to the rebased survivor feat-b", f.head)
	}
	wantNote := "HEAD is on feat-b; trunk is checked out in /wt/trunk"
	found := false
	for _, note := range res.Notes {
		if note == wantNote {
			found = true
		}
		if strings.Contains(note, "left detached") {
			t.Fatalf("note claims detached while HEAD is on %q: %v", f.head, res.Notes)
		}
	}
	if !found {
		t.Fatalf("notes = %v, want %q", res.Notes, wantNote)
	}
}

// --no-fetch: the remote port is never touched, the trunk is not moved, and the
// LOCAL trunk is the single basis for both pruning and restacking — a branch
// merged only into the cached remote ref survives until local trunk advances.

func TestSyncNoFetchNeverTouchesRemote(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	aTip, _ := f.RevParse("feat-a")
	localMain, _ := f.RevParse("main")
	// The remote-tracking ref contains feat-a; the local trunk does not.
	// Offline sync must NOT prune on the remote basis: feat-a stays tracked
	// until local main advances to contain it.
	f.remoteRefs["refs/remotes/origin/main"] = aTip

	remote := &fakeRemote{exists: true, ff: "must not be used"}
	res, err := Sync(env, remote, s, "origin", false, true)
	if err != nil {
		t.Fatalf("sync --no-fetch: %v", err)
	}
	if remote.fetches != 0 || remote.called {
		t.Fatalf("--no-fetch touched the remote: fetches=%d fastForward=%v", remote.fetches, remote.called)
	}
	if !f.BranchExists("feat-a") || !s.IsTracked("feat-a") {
		t.Fatal("feat-a merged only into the remote-tracking ref must survive offline sync")
	}
	if tip, _ := f.RevParse("main"); tip != localMain {
		t.Fatalf("local main moved under --no-fetch: %s → %s", localMain, tip)
	}
	found := false
	for _, note := range res.Notes {
		if note == "trunk: skipped (--no-fetch)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %v, want 'trunk: skipped (--no-fetch)'", res.Notes)
	}
}

func TestSyncNoFetchFallsBackToLocalTrunk(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	// feat-a merged into the LOCAL trunk; no remote-tracking ref exists, so the
	// prune basis falls back to it even though a remote is configured.
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}

	remote := &fakeRemote{exists: true, ff: "must not be used"}
	res, err := Sync(env, remote, s, "origin", false, true)
	if err != nil {
		t.Fatalf("sync --no-fetch: %v", err)
	}
	if remote.fetches != 0 || remote.called {
		t.Fatalf("--no-fetch touched the remote: fetches=%d fastForward=%v", remote.fetches, remote.called)
	}
	if f.BranchExists("feat-a") {
		t.Fatal("feat-a merged into the local trunk should have been pruned")
	}
	found := false
	for _, note := range res.Notes {
		if note == "trunk: skipped (--no-fetch)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %v, want 'trunk: skipped (--no-fetch)'", res.Notes)
	}
}

// Standalone prune (st prune): never touches a remote, never moves HEAD.

func TestPruneMergedAgainstRemoteTrackingRef(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	// The remote-tracking ref contains feat-a but the local trunk does not —
	// proves the basis is the supplied ref, not s.Trunk.
	aTip, _ := f.RevParse("feat-a")
	f.remoteRefs["refs/remotes/origin/main"] = aTip
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	deleted, err := PruneMergedAgainst(env, s, "refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("PruneMergedAgainst: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "feat-a" {
		t.Fatalf("deleted = %v, want [feat-a]", deleted)
	}
	if f.BranchExists("feat-a") || s.IsTracked("feat-a") {
		t.Fatal("feat-a should be deleted and untracked")
	}
}

func TestPruneDeletesMergedKeepsHEAD(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-merged")
	mkBranch(t, env, s, f, "main", "feat-live")
	aTip, _ := f.RevParse("feat-merged")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("feat-live"); err != nil {
		t.Fatal(err)
	}

	res, err := Prune(env, s, s.Trunk)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-merged" {
		t.Fatalf("deleted = %v, want [feat-merged]", res.Deleted)
	}
	if f.head != "feat-live" {
		t.Fatalf("HEAD = %q, want feat-live (prune never moves HEAD)", f.head)
	}
	if !f.BranchExists("feat-live") || !s.IsTracked("feat-live") {
		t.Fatal("unmerged feat-live should be kept and tracked")
	}
}

func TestPruneRefusesWhenCurrentBranchMerged(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}

	_, err := Prune(env, s, s.Trunk)
	if err == nil || !strings.Contains(err.Error(), "check out another branch or run st sync") {
		t.Fatalf("Prune on merged current branch: err=%v", err)
	}
	if !f.BranchExists("feat-a") || !s.IsTracked("feat-a") {
		t.Fatal("refused prune must not delete feat-a")
	}
	// The dry-run preview must report the same refusal.
	if _, err := PrunePlan(env, s, s.Trunk); err == nil || !strings.Contains(err.Error(), "check out another branch") {
		t.Fatalf("PrunePlan on merged current branch: err=%v", err)
	}
}

func TestPrunePlanListsWithoutDeleting(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	aTip, _ := f.RevParse("feat-a")
	if err := f.UpdateRef("refs/heads/main", aTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	res, err := PrunePlan(env, s, s.Trunk)
	if err != nil {
		t.Fatalf("PrunePlan: %v", err)
	}
	if !res.DryRun {
		t.Fatal("PrunePlan should mark DryRun")
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "feat-a" {
		t.Fatalf("preview deleted = %v, want [feat-a]", res.Deleted)
	}
	if !f.BranchExists("feat-a") || !s.IsTracked("feat-a") {
		t.Fatal("dry run deleted feat-a")
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q, want main", f.head)
	}
}

// TestSyncFastForwardRestoreDoubleFault pins the post-fast-forward restore
// arm: the remote fast-forward fails AND putting HEAD back on the original
// branch fails too — the caller must see BOTH wrapped in one AlsoFailed,
// each sentinel still errors.Is-matchable.
func TestSyncFastForwardRestoreDoubleFault(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")

	ffErr := errors.New("fast-forward exploded")
	checkoutErr := errors.New("cannot check out feat-a")
	f.checkoutErr["feat-a"] = checkoutErr
	// checkout=f models the failed fast-forward parking HEAD on the trunk, so
	// the restore really does attempt the checkout (no already-on-target
	// shortcut).
	remote := &fakeRemote{exists: true, err: ffErr, checkout: f}

	_, err := Sync(env, remote, s, "origin", false, false)
	if !errors.Is(err, ffErr) {
		t.Fatalf("Sync = %v, want the fast-forward sentinel matchable", err)
	}
	if !errors.Is(err, checkoutErr) {
		t.Fatalf("Sync = %v, want the restore sentinel matchable", err)
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q, want main — the failed ff left it parked and the restore never landed", f.head)
	}
}

// TestSyncDetachParkingProbeFailures pins the detach-parking arm: with the
// trunk checked out in a linked worktree, sync parks HEAD detached before
// pruning — failures of BOTH probes (resolving HEAD, then the detach itself)
// must surface wrapped instead of silently proceeding to prune.
func TestSyncDetachParkingProbeFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arm     func(f *fakeGit, boom error)
		wantSub string
	}{
		{"resolve HEAD", func(f *fakeGit, boom error) { f.failErr["RevParse"] = boom }, "resolving HEAD before pruning"},
		{"detach", func(f *fakeGit, boom error) { f.failErr["CheckoutDetach"] = boom }, "detaching HEAD before pruning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, env := newEnvState()
			mkBranch(t, env, s, f, "main", "feat-a")
			f.addWorktree("/wt/trunk", "main")
			boom := errors.New("probe exploded")
			tc.arm(f, boom)

			_, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
			if !errors.Is(err, boom) {
				t.Fatalf("Sync = %v, want wrapped %v", err, boom)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Sync = %v, want %q in the message", err, tc.wantSub)
			}
			if f.head != "feat-a" {
				t.Fatalf("HEAD = %q, want feat-a — the failed park must not strand HEAD elsewhere", f.head)
			}
			if !f.BranchExists("feat-a") {
				t.Fatal("the failed park deleted feat-a anyway")
			}
		})
	}
}

// TestSyncTrunkCheckoutFailure pins the single-tree arm: with the trunk owned
// nowhere, sync checks out the trunk before pruning — a refusal there must
// surface wrapped, leaving HEAD and every branch untouched.
func TestSyncTrunkCheckoutFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	boom := errors.New("cannot switch to main")
	f.checkoutErr["main"] = boom

	_, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if !errors.Is(err, boom) {
		t.Fatalf("Sync = %v, want wrapped %v", err, boom)
	}
	if !strings.Contains(err.Error(), `checkout trunk "main" before pruning`) {
		t.Fatalf("Sync = %v, want the trunk-checkout step named", err)
	}
	if f.head != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a", f.head)
	}
	if !s.IsTracked("feat-a") || !f.BranchExists("feat-a") {
		t.Fatal("a failed trunk checkout must not touch feat-a")
	}
}

// TestSyncPruneRestoreDoubleFault pins the post-prune restore arm: the merged
// enumeration fails AND restoring the original branch fails — the caller sees
// one AlsoFailed carrying both sentinels.
func TestSyncPruneRestoreDoubleFault(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")

	pruneErr := errors.New("merged enumeration exploded")
	checkoutErr := errors.New("cannot restore feat-a")
	f.failErr["MergedInto"] = pruneErr
	f.checkoutErr["feat-a"] = checkoutErr

	_, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if !errors.Is(err, pruneErr) {
		t.Fatalf("Sync = %v, want the prune sentinel matchable", err)
	}
	if !errors.Is(err, checkoutErr) {
		t.Fatalf("Sync = %v, want the restore sentinel matchable", err)
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q, want main — sync parked on the trunk and the restore failed", f.head)
	}
}

// TestSyncSaveCheckpointFailures pins both sync save arms — the post-prune
// checkpoint and the post-restack checkpoint. Each must surface the save
// error instead of swallowing it (the mutations behind them already
// committed).
func TestSyncSaveCheckpointFailures(t *testing.T) {
	boom := errors.New("save exploded")
	for _, tc := range []struct {
		name       string
		failAfterN int
		wantSaves  int
	}{
		{"post-prune checkpoint", 0, 1},
		{"post-restack checkpoint", 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, env := newEnvState()
			mkBranch(t, env, s, f, "main", "feat-a")
			env2, saves := envWithSaveErr(f, boom, tc.failAfterN)

			// feat-a is unmerged and undrifted: nothing prunes, nothing
			// restacks — only the two unconditional checkpoints run.
			_, err := Sync(env2, &fakeRemote{exists: false}, s, "origin", false, false)
			if !errors.Is(err, boom) {
				t.Fatalf("Sync = %v, want wrapped %v", err, boom)
			}
			if *saves != tc.wantSaves {
				t.Fatalf("saves = %d, want %d", *saves, tc.wantSaves)
			}
		})
	}
}

// TestSyncFinalRestoreFailure pins the epilogue restoreHEAD: everything else
// succeeded but checking out the original branch fails — the error surfaces
// and HEAD stays on the trunk sync parked it on.
func TestSyncFinalRestoreFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	boom := errors.New("cannot restore feat-a")
	f.checkoutErr["feat-a"] = boom

	_, err := Sync(env, &fakeRemote{exists: false}, s, "origin", false, false)
	if !errors.Is(err, boom) {
		t.Fatalf("Sync = %v, want wrapped %v", err, boom)
	}
	if !strings.Contains(err.Error(), `restore branch "feat-a"`) {
		t.Fatalf("Sync = %v, want the restore step named", err)
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q, want main — sync parked there for pruning", f.head)
	}
}

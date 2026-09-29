package stack

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func newEnvState() (*fakeGit, *State, Env) {
	f := newFakeGit()
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	return f, s, Env{Git: f}
}

// envWithSaveErr returns an Env whose Save hook counts invocations and
// returns err once more than failAfterN saves have run (0 = the first save
// fails). The counter lets tests assert a checkpoint save was attempted.
func envWithSaveErr(g Git, err error, failAfterN int) (Env, *int) {
	saves := new(int)
	return Env{Git: g, Save: func() error {
		*saves++
		if *saves > failAfterN {
			return err
		}
		return nil
	}}, saves
}

func mkBranch(t *testing.T, env Env, s *State, f *fakeGit, parent, name string) {
	t.Helper()
	if err := f.Checkout(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(env, s, name, "c-"+name, true); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

// TestAlsoFailedKeepsBothErrorsMatchable asserts the rollback double-error
// helper preserves errors.Is matching for the primary AND the secondary error.
func TestAlsoFailedKeepsBothErrorsMatchable(t *testing.T) {
	primary := errors.New("op failed")
	secondary := errors.New("rollback failed")
	err := AlsoFailed(fmt.Errorf("wrapping: %w", primary), "roll back", secondary)

	if !errors.Is(err, primary) {
		t.Fatalf("errors.Is(err, primary) = false for %v", err)
	}
	if !errors.Is(err, secondary) {
		t.Fatalf("errors.Is(err, secondary) = false for %v", err)
	}
	want := "wrapping: op failed; additionally failed to roll back: rollback failed"
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestEngineCreateTracksParent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if b, _ := s.Get("a"); b.Parent != "main" {
		t.Fatalf("a parent=%q, want main", b.Parent)
	}
	if b, _ := s.Get("b"); b.Parent != "a" {
		t.Fatalf("b parent=%q, want a", b.Parent)
	}
	for _, n := range []string{"a", "b"} {
		b, _ := s.Get(n)
		if !mustFakeIsAncestor(t, f, b.ParentSHA, n) {
			t.Fatalf("%s parentSHA is not an ancestor of its tip", n)
		}
	}
}

func TestCreateInWorktreePrepTracksWithoutSwitching(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	parentTip, err := f.RevParse("a")
	if err != nil {
		t.Fatal(err)
	}

	res, err := CreateInWorktreePrep(env, s, "b")
	if err != nil {
		t.Fatalf("CreateInWorktreePrep: %v", err)
	}
	if f.head != "a" {
		t.Fatalf("HEAD = %q, want a", f.head)
	}
	if got, _ := f.RevParse("b"); got != parentTip {
		t.Fatalf("b tip = %s, want parent tip %s", got, parentTip)
	}
	b, ok := s.Get("b")
	if !ok {
		t.Fatal("b was not tracked")
	}
	if b.Parent != "a" || b.ParentSHA != parentTip {
		t.Fatalf("tracked b = %+v, want parent a at %s", b, parentTip)
	}
	if res.Summary != "Created b on top of a" || res.Branch != "b" {
		t.Fatalf("result = %+v", res)
	}
}

func TestCreateInWorktreePrepRefusesExistingBranch(t *testing.T) {
	f, s, env := newEnvState()
	if err := f.CreateBranchAt("a", "main"); err != nil {
		t.Fatal(err)
	}

	_, err := CreateInWorktreePrep(env, s, "a")
	want := `branch "a" already exists`
	if err == nil || err.Error() != want {
		t.Fatalf("CreateInWorktreePrep error = %v, want %q", err, want)
	}
}

func TestCreateInWorktreePrepRefusesUntrackedCurrentBranch(t *testing.T) {
	f, s, env := newEnvState()
	if err := f.CreateBranch("loose"); err != nil {
		t.Fatal(err)
	}

	_, err := CreateInWorktreePrep(env, s, "a")
	want := `current branch "loose" is not the trunk or a tracked branch`
	if err == nil || err.Error() != want {
		t.Fatalf("CreateInWorktreePrep error = %v, want %q", err, want)
	}
}

func TestEngineSquashCollapses(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "c2", true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "c3", true, true); err != nil {
		t.Fatal(err)
	}
	if subs, _ := f.CommitSubjects("main", "a"); len(subs) != 3 {
		t.Fatalf("want 3 commits before squash, got %d", len(subs))
	}
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Squash(env, s, "squashed"); err != nil {
		t.Fatalf("squash: %v", err)
	}
	if subs, _ := f.CommitSubjects("main", "a"); len(subs) != 1 {
		t.Fatalf("want 1 commit after squash, got %d", len(subs))
	}
}

func TestSquashRestoresBranchTipWhenCommitFails(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "c2", true, true); err != nil {
		t.Fatal(err)
	}
	before, _ := f.RevParse("a")
	errBoom := errors.New("commit hook rejected")
	f.commitErr = errBoom

	if _, err := Squash(env, s, "squashed"); !errors.Is(err, errBoom) {
		t.Fatalf("Squash error = %v, want %v", err, errBoom)
	}
	after, _ := f.RevParse("a")
	if after != before {
		t.Fatalf("a tip changed to %s, want %s", after, before)
	}
}

func TestEngineFoldAbsorbs(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Fold(env, s); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if s.IsTracked("b") {
		t.Fatal("b still tracked after fold")
	}
	if cb, _ := s.Get("c"); cb.Parent != "a" {
		t.Fatalf("c parent=%q, want a", cb.Parent)
	}
	if subs, _ := f.CommitSubjects("main", "a"); len(subs) != 2 {
		t.Fatalf("a should have 2 commits after fold, got %d", len(subs))
	}
}

func TestFoldRefusesParentInOtherWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	f.addWorktree("/wt/a", "a")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	aTip, _ := f.RevParse("a")
	bTip, _ := f.RevParse("b")

	_, err := Fold(env, s)
	want := `cannot fold into "a" because it is checked out in another worktree "/wt/a"`
	if err == nil || err.Error() != want {
		t.Fatalf("Fold error = %v, want %q", err, want)
	}
	if after, _ := f.RevParse("a"); after != aTip {
		t.Fatalf("a tip = %s after refused fold, want %s", after, aTip)
	}
	if after, _ := f.RevParse("b"); after != bTip {
		t.Fatalf("b tip = %s after refused fold, want %s", after, bTip)
	}
	if !f.BranchExists("b") || !s.IsTracked("b") {
		t.Fatal("refused fold removed branch b or its metadata")
	}
	if f.head != "b" {
		t.Fatalf("HEAD = %q after refused fold, want b", f.head)
	}
}

// If the branch delete fails mid-fold, the git side must roll back: the parent
// ref must not have absorbed cur's commits, cur must survive, HEAD must be
// restored, and the metadata must be untouched (ENG-4). Otherwise git and
// state.json would silently disagree.
func TestFoldRollsBackWhenDeleteFails(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	aTip, _ := f.RevParse("a")
	errBoom := errors.New("branch checked out elsewhere")
	f.deleteErr["b"] = errBoom

	if _, err := Fold(env, s); !errors.Is(err, errBoom) {
		t.Fatalf("Fold error = %v, want %v", err, errBoom)
	}
	// Parent must not have advanced.
	if after, _ := f.RevParse("a"); after != aTip {
		t.Fatalf("a tip = %s after failed fold, want %s (rolled back)", after, aTip)
	}
	// cur must survive, stay tracked, and keep its child.
	if !f.BranchExists("b") || !s.IsTracked("b") {
		t.Fatal("failed fold destroyed branch b or its metadata")
	}
	if cb, _ := s.Get("c"); cb.Parent != "b" {
		t.Fatalf("c parent = %q after failed fold, want b (unchanged)", cb.Parent)
	}
	// HEAD must be restored to cur.
	if f.head != "b" {
		t.Fatalf("HEAD = %q after failed fold, want b restored", f.head)
	}
}

func TestFoldRollsBackParentWhenDeleteAndRestoreCheckoutFail(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	aTip, _ := f.RevParse("a")
	deleteErr := errors.New("branch checked out elsewhere")
	checkoutErr := errors.New("cannot restore checked-out branch")
	f.deleteErr["b"] = deleteErr
	f.checkoutErr["b"] = checkoutErr

	err := func() error {
		_, err := Fold(env, s)
		return err
	}()
	if !errors.Is(err, deleteErr) {
		t.Fatalf("Fold error = %v, want wrapped %v", err, deleteErr)
	}
	// The secondary restore failure must stay matchable with errors.Is (wrapped
	// with %w via AlsoFailed), not merely rendered into the message with %v.
	if !errors.Is(err, checkoutErr) {
		t.Fatalf("Fold error = %v, want restore checkout error matchable with errors.Is", err)
	}
	if !strings.Contains(err.Error(), checkoutErr.Error()) {
		t.Fatalf("Fold error = %v, want restore checkout failure", err)
	}
	if after, _ := f.RevParse("a"); after != aTip {
		t.Fatalf("a tip = %s after failed fold, want %s (rolled back)", after, aTip)
	}
	if !f.BranchExists("b") || !s.IsTracked("b") {
		t.Fatal("failed fold destroyed branch b or its metadata")
	}
	if cb, _ := s.Get("c"); cb.Parent != "b" {
		t.Fatalf("c parent = %q after failed fold, want b (unchanged)", cb.Parent)
	}
	if f.head != "a" {
		t.Fatalf("HEAD = %q after failed fold, want rolled-back parent", f.head)
	}
}

// TestInferParentDeterministic checks inferParent picks the closest tracked
// ancestor and returns a stable result across runs (candidates are iterated in
// sorted order, not map order) — ENG-5. The fake git models a single-parent DAG,
// so this exercises the closest-ancestor + stability properties; the
// incomparable-ancestors case it guards needs a merge history git can't fake.
func TestInferParentDeterministic(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch("c"); err != nil { // untracked branch off b's tip
		t.Fatal(err)
	}

	for i := 0; i < 25; i++ {
		got, err := inferParent(f, s, "c")
		if err != nil {
			t.Fatalf("inferParent: %v", err)
		}
		if got != "b" {
			t.Fatalf("inferParent(c) = %q, want b (closest tracked ancestor)", got)
		}
	}
}

func TestRestackUpstackUsesSingleTipsReadWhenClean(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	mkBranch(t, env, s, f, "c", "d")

	counting := &countingSnapshotGit{Git: f}
	env.Git = counting
	rebased, err := s.restackUpstack(env, "main")
	if err != nil {
		t.Fatalf("restackUpstack: %v", err)
	}
	if len(rebased) != 0 {
		t.Fatalf("rebased = %v, want none", rebased)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls = %d, want 1", counting.tipsCalls)
	}
	if counting.revParseCalls != 0 {
		t.Fatalf("RevParse calls = %d, want 0", counting.revParseCalls)
	}
}

func TestRestackUpstackRefreshesMovedParentTips(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.amend("a2")

	counting := &countingSnapshotGit{Git: f}
	env.Git = counting
	rebased, err := s.restackUpstack(env, "a")
	if err != nil {
		t.Fatalf("restackUpstack: %v", err)
	}
	if len(rebased) != 2 || rebased[0] != "b" || rebased[1] != "c" {
		t.Fatalf("rebased = %v, want [b c]", rebased)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls = %d, want 1", counting.tipsCalls)
	}
	// One refresh, not two: b's tip is refreshed because c consumes it, but c
	// is a leaf — nothing reads tips["c"], so its post-rebase refresh is
	// skipped (the c.ParentSHA assertion below still proves c rebased onto the
	// REFRESHED b tip).
	if counting.revParseCalls != 1 {
		t.Fatalf("RevParse calls = %d, want 1 refresh (b only; leaf c skipped)", counting.revParseCalls)
	}
	b, _ := s.Get("b")
	c, _ := s.Get("c")
	if b.ParentSHA != f.branches["a"] {
		t.Fatalf("b ParentSHA = %q, want a tip %q", b.ParentSHA, f.branches["a"])
	}
	if c.ParentSHA != f.branches["b"] {
		t.Fatalf("c ParentSHA = %q, want refreshed b tip %q", c.ParentSHA, f.branches["b"])
	}
}

func TestEngineDeleteDropsCommits(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	bTip, _ := f.RevParse("b")

	res, err := Delete(env, s, "b", true)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Like every restacking op, delete reports the re-parented children it
	// actually moved.
	if len(res.Restacked) != 1 || res.Restacked[0] != "c" {
		t.Fatalf("delete Restacked = %v, want [c]", res.Restacked)
	}
	if s.IsTracked("b") {
		t.Fatal("b still tracked after delete")
	}
	if cb, _ := s.Get("c"); cb.Parent != "a" {
		t.Fatalf("c parent=%q, want a", cb.Parent)
	}
	if mustFakeIsAncestor(t, f, bTip, "c") {
		t.Fatal("c still contains deleted b's commit")
	}
	if subs, _ := f.CommitSubjects("a", "c"); len(subs) != 1 {
		t.Fatalf("c should have 1 commit on a, got %d", len(subs))
	}
}

func TestDeleteCurrentRefusesParentInOtherWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	f.addWorktree("/wt/a", "a")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	aTip, _ := f.RevParse("a")
	bTip, _ := f.RevParse("b")

	_, err := Delete(env, s, "b", true)
	want := `cannot delete current branch "b" because its parent "a" is checked out in another worktree "/wt/a"`
	if err == nil || err.Error() != want {
		t.Fatalf("Delete error = %v, want %q", err, want)
	}
	if after, _ := f.RevParse("a"); after != aTip {
		t.Fatalf("a tip = %s after refused delete, want %s", after, aTip)
	}
	if after, _ := f.RevParse("b"); after != bTip {
		t.Fatalf("b tip = %s after refused delete, want %s", after, bTip)
	}
	if !f.BranchExists("b") || !s.IsTracked("b") {
		t.Fatal("refused delete removed branch b or its metadata")
	}
	if f.head != "b" {
		t.Fatalf("HEAD = %q after refused delete, want b", f.head)
	}
}

func TestCrossWorktreeConflictAbortFailureSurfaces(t *testing.T) {
	f, s, env := setupCascade(t)
	f.conflictOn("feat-a")
	abortErr := errors.New("abort failed")
	f.rebaseAbortErr = abortErr

	_, err := s.restackBranch(env, "feat-a")
	if err == nil {
		t.Fatal("cross-worktree conflict with abort failure returned nil error")
	}
	if !errors.Is(err, abortErr) {
		t.Fatalf("restackBranch error = %v, want abort failure matchable", err)
	}
	for _, want := range []string{
		`rebasing "feat-a" in its worktree "/wt/feat-a"`,
		`conflict rebasing "feat-a"`,
		`abort the paused rebase in "/wt/feat-a" (it is still in progress there)`,
		"abort failed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("restackBranch error = %v, want it to contain %q", err, want)
		}
	}
	if inProgress, _ := f.RebaseInProgress(); !inProgress {
		t.Fatal("failed abort should leave the owner worktree rebase in progress")
	}
}

func TestCrossWorktreeEarlyRebaseFailureDoesNotReportAbort(t *testing.T) {
	f, s, env := setupCascade(t)
	rebaseErr := errors.New("pre-rebase hook rejected")
	f.rebaseErr["feat-a"] = rebaseErr

	_, err := s.restackBranch(env, "feat-a")
	if err == nil {
		t.Fatal("cross-worktree early rebase failure returned nil error")
	}
	if !errors.Is(err, rebaseErr) {
		t.Fatalf("restackBranch error = %v, want original rebase failure matchable", err)
	}
	if strings.Contains(err.Error(), "still in progress") {
		t.Fatalf("early rebase failure reported a paused rebase: %v", err)
	}
	if inProgress, _ := f.RebaseInProgress(); inProgress {
		t.Fatal("early rebase failure should not leave a rebase in progress")
	}
}

func TestEngineOntoReparents(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Onto(env, s, "main"); err != nil {
		t.Fatalf("onto: %v", err)
	}
	if bb, _ := s.Get("b"); bb.Parent != "main" {
		t.Fatalf("b parent=%q, want main", bb.Parent)
	}
	bb, _ := s.Get("b")
	mainTip, _ := f.RevParse("main")
	if bb.ParentSHA != mainTip {
		t.Fatalf("b.ParentSHA=%s, want main tip %s", bb.ParentSHA, mainTip)
	}
}

func TestEngineTrackUntrackRename(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	// A branch created outside st, off a.
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch("manual"); err != nil {
		t.Fatal(err)
	}
	f.commit("m1")

	if _, err := TrackBranch(env, s, "", ""); err != nil {
		t.Fatalf("track: %v", err)
	}
	if mb, _ := s.Get("manual"); mb.Parent != "a" {
		t.Fatalf("manual parent=%q, want inferred a", mb.Parent)
	}
	if _, err := Rename(env, s, "manual", "renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if s.IsTracked("manual") || !s.IsTracked("renamed") {
		t.Fatal("rename did not update tracking")
	}
	if _, err := UntrackBranch(env, s, "renamed"); err != nil {
		t.Fatalf("untrack: %v", err)
	}
	if s.IsTracked("renamed") {
		t.Fatal("still tracked after untrack")
	}
}

func TestTrackBranchInfersStaleTrackedAncestorAfterTrunkAdvances(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance-main")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch("manual"); err != nil {
		t.Fatal(err)
	}
	f.commit("manual")

	if _, err := TrackBranch(env, s, "", ""); err != nil {
		t.Fatalf("track: %v", err)
	}
	if mb, _ := s.Get("manual"); mb.Parent != "a" {
		t.Fatalf("manual parent=%q, want stale tracked ancestor a", mb.Parent)
	}
}

func TestTrackBranchKeepsTrunkParentWhenTrackedAncestorAlreadyMerged(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	aTip, _ := f.RevParse("a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch("manual"); err != nil {
		t.Fatal(err)
	}
	f.commit("manual")

	if _, err := TrackBranch(env, s, "", ""); err != nil {
		t.Fatalf("track: %v", err)
	}
	if mb, _ := s.Get("manual"); mb.Parent != "main" {
		t.Fatalf("manual parent=%q, want main", mb.Parent)
	}
}

func TestTrackBranchKeepsTrunkParentWhenMergedAncestorAndTrunkAdvanced(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	aTip, _ := f.RevParse("a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance-main")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch("manual"); err != nil {
		t.Fatal(err)
	}
	f.commit("manual")

	if _, err := TrackBranch(env, s, "", ""); err != nil {
		t.Fatalf("track: %v", err)
	}
	if mb, _ := s.Get("manual"); mb.Parent != "main" {
		t.Fatalf("manual parent=%q, want main", mb.Parent)
	}
}

// TestTrackNamedBranch covers tracking a branch other than the checked-out
// one: inference roots at the named branch's tip (same result as
// checkout-then-track), HEAD does not move, and the refusals mirror the
// current-branch path.
func TestTrackNamedBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	// An untracked branch forked off a, then HEAD returns to the trunk.
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch("manual"); err != nil {
		t.Fatal(err)
	}
	f.commit("m1")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	res, err := TrackBranch(env, s, "manual", "")
	if err != nil {
		t.Fatalf("track named: %v", err)
	}
	if res.Branch != "manual" {
		t.Fatalf("result branch=%q, want manual", res.Branch)
	}
	if mb, _ := s.Get("manual"); mb.Parent != "a" {
		t.Fatalf("manual parent=%q, want inferred a", mb.Parent)
	}
	if cur, _ := f.CurrentBranch(); cur != "main" {
		t.Fatalf("track moved HEAD to %q", cur)
	}

	// Refusals mirror the current-branch path: trunk, already-tracked, and a
	// branch that does not exist.
	for _, tc := range []struct{ name, want string }{
		{"main", "cannot track the trunk"},
		{"manual", "already tracked"},
		{"ghost", "does not exist"},
	} {
		if _, err := TrackBranch(env, s, tc.name, ""); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Fatalf("TrackBranch(%q) err=%v, want %q", tc.name, err, tc.want)
		}
	}

	// An explicit parent is honored for a named branch, and a named branch
	// cannot be its own parent.
	if err := f.CreateBranchAt("selfie", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := TrackBranch(env, s, "selfie", "selfie"); err == nil ||
		!strings.Contains(err.Error(), "own parent") {
		t.Fatalf("self-parent err=%v, want own-parent refusal", err)
	}
	if _, err := TrackBranch(env, s, "selfie", "manual"); err != nil {
		t.Fatalf("track selfie --parent manual: %v", err)
	}
	if sb, _ := s.Get("selfie"); sb.Parent != "manual" {
		t.Fatalf("selfie parent=%q, want manual", sb.Parent)
	}
}

func TestUntrackPreservesOldBaseForChildren(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	a, _ := s.Get("a")
	oldBase := a.ParentSHA
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance-main")

	if _, err := UntrackBranch(env, s, "a"); err != nil {
		t.Fatalf("untrack: %v", err)
	}
	b, _ := s.Get("b")
	if b.Parent != "main" || b.ParentSHA != oldBase {
		t.Fatalf("b metadata = (%s, %s), want (main, %s)", b.Parent, b.ParentSHA, oldBase)
	}
}

func TestUntrackMergedParentKeepsChildBase(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	bBefore, _ := s.Get("b")
	oldChildBase := bBefore.ParentSHA
	if err := f.ForceBranch("main", oldChildBase); err != nil {
		t.Fatal(err)
	}

	if _, err := UntrackBranch(env, s, "a"); err != nil {
		t.Fatalf("untrack: %v", err)
	}
	b, _ := s.Get("b")
	if b.Parent != "main" || b.ParentSHA != oldChildBase {
		t.Fatalf("b metadata = (%s, %s), want (main, %s)", b.Parent, b.ParentSHA, oldChildBase)
	}
}

func TestUntrackLeafDoesNotRequireLiveGitRef(t *testing.T) {
	f, s, env := newEnvState()
	mainTip, _ := f.RevParse("main")
	s.Track("ghost", "main", mainTip)

	if _, err := UntrackBranch(env, s, "ghost"); err != nil {
		t.Fatalf("untrack leaf with missing ref: %v", err)
	}
	if s.IsTracked("ghost") {
		t.Fatal("ghost still tracked after untrack")
	}
}

func TestUntrackMissingParentRefReparentsChildrenFromRecordedBase(t *testing.T) {
	f, s, env := newEnvState()
	mainTip, _ := f.RevParse("main")
	s.Track("ghost", "main", mainTip)
	s.Track("child", "ghost", "ghost-tip")

	if _, err := UntrackBranch(env, s, "ghost"); err != nil {
		t.Fatalf("untrack missing parent ref: %v", err)
	}
	child, _ := s.Get("child")
	if child.Parent != "main" || child.ParentSHA != mainTip {
		t.Fatalf("child metadata = (%s, %s), want (main, %s)", child.Parent, child.ParentSHA, mainTip)
	}
}

func TestEngineModifyRestacksDescendants(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "", true, false); err != nil {
		t.Fatalf("modify: %v", err)
	}
	aTip, _ := f.RevParse("a")
	bb, _ := s.Get("b")
	if bb.ParentSHA != aTip {
		t.Fatalf("b.ParentSHA=%s, not updated to amended a tip %s", bb.ParentSHA, aTip)
	}
	if !mustFakeIsAncestor(t, f, aTip, "b") {
		t.Fatal("b was not rebased onto the amended a")
	}
}

func TestModifyRejectsUnstagedChangesBeforeRestackingDescendants(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	before, _ := f.RevParse("a")
	f.clean = false

	if _, err := Modify(env, s, "amend", false, false); !errors.Is(err, ErrDirty) {
		t.Fatalf("Modify dirty error = %v, want ErrDirty", err)
	}
	after, _ := f.RevParse("a")
	if after != before {
		t.Fatalf("a tip changed to %s, want %s", after, before)
	}
}

func TestModifyRestoresBranchAfterNonConflictDescendantRestackFailure(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	errBoom := errors.New("pre-rebase hook rejected")
	f.rebaseErr["c"] = errBoom

	if _, err := Modify(env, s, "amend", true, false); !errors.Is(err, errBoom) {
		t.Fatalf("Modify error = %v, want %v", err, errBoom)
	}
	if f.head != "a" {
		t.Fatalf("HEAD = %q, want a", f.head)
	}
}

func TestModifyRejectsUntrackedBranch(t *testing.T) {
	f, s, env := newEnvState()
	if err := f.CreateBranch("scratch"); err != nil {
		t.Fatal(err)
	}
	before, _ := f.RevParse("scratch")

	if _, err := Modify(env, s, "change", true, true); err == nil {
		t.Fatal("modify on untracked branch should error")
	}
	after, _ := f.RevParse("scratch")
	if after != before {
		t.Fatalf("scratch tip changed to %s, want %s", after, before)
	}
}

func TestOntoRollsBackMetadataWhenRebaseDoesNotStart(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b")
	oldParent, oldParentSHA := b.Parent, b.ParentSHA
	errBoom := errors.New("pre-rebase hook rejected")
	f.rebaseErr["b"] = errBoom

	if _, err := Onto(env, s, "c"); !errors.Is(err, errBoom) {
		t.Fatalf("Onto error = %v, want %v", err, errBoom)
	}
	if b.Parent != oldParent || b.ParentSHA != oldParentSHA {
		t.Fatalf("b metadata = (%s, %s), want (%s, %s)", b.Parent, b.ParentSHA, oldParent, oldParentSHA)
	}
}

func TestOntoConflictRecordsPendingReparentWithoutChangingParent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b")
	oldParent, oldParentSHA := b.Parent, b.ParentSHA
	targetSHA, _ := f.RevParse("c")
	f.conflictOn("b")

	if _, err := Onto(env, s, "c"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Onto error = %v, want %v", err, ErrConflict)
	}
	if b.Parent != oldParent || b.ParentSHA != oldParentSHA {
		t.Fatalf("b metadata = (%s, %s), want (%s, %s)", b.Parent, b.ParentSHA, oldParent, oldParentSHA)
	}
	if s.PendingReparent == nil || s.PendingReparent.Branch != "b" || s.PendingReparent.Parent != "c" || s.PendingReparent.ParentSHA != targetSHA {
		t.Fatalf("pending reparent = %+v, want b onto c at %s", s.PendingReparent, targetSHA)
	}

	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if b.Parent != "c" || b.ParentSHA != targetSHA {
		t.Fatalf("b metadata after continue = (%s, %s), want (c, %s)", b.Parent, b.ParentSHA, targetSHA)
	}
	if s.PendingReparent != nil {
		t.Fatalf("pending reparent after continue = %+v, want nil", s.PendingReparent)
	}
}

// emptyHeadNameGit forces RebaseHeadName to "" while a rebase is in progress,
// modeling git's head-name file being unreadable, so Continue must fall back to
// the pending reparent to decide which branch finished.
type emptyHeadNameGit struct{ *fakeGit }

func (emptyHeadNameGit) RebaseHeadName() (string, error) { return "", nil }

// When the pending reparent checkpoint cannot be persisted mid-conflict, Onto
// must abort the paused rebase so git and metadata both return to the pre-Onto
// state instead of diverging — otherwise a later `st continue` would recover
// against the old parent.
func TestOntoAbortsWhenPendingReparentCannotPersist(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b")
	oldParent, oldParentSHA := b.Parent, b.ParentSHA
	f.conflictOn("b")
	saveErr := errors.New("disk full")
	env.Save = func() error { return saveErr }

	_, err := Onto(env, s, "c")
	if !errors.Is(err, saveErr) {
		t.Fatalf("Onto error = %v, want wrapped %v", err, saveErr)
	}
	// Aborted, so there is nothing to continue: the error must not pose as a
	// resolvable conflict.
	if errors.Is(err, ErrConflict) {
		t.Fatalf("Onto error = %v, should not wrap ErrConflict after a successful abort", err)
	}
	if inProgress, _ := f.RebaseInProgress(); inProgress {
		t.Fatal("rebase left in progress after failed reparent persist; want aborted")
	}
	if s.PendingReparent != nil {
		t.Fatalf("PendingReparent = %+v, want nil (matches unpersisted disk)", s.PendingReparent)
	}
	if b.Parent != oldParent || b.ParentSHA != oldParentSHA {
		t.Fatalf("b metadata = (%s, %s), want unchanged (%s, %s)", b.Parent, b.ParentSHA, oldParent, oldParentSHA)
	}
}

// When persisting the pending reparent fails AND the follow-up rebase abort
// also fails, the rebase is still paused — so the error must keep ErrConflict
// matchable (st continue/abort can still recover) while carrying BOTH failures.
func TestOntoDoubleFaultKeepsConflictAndBothFailures(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b")
	oldParent, oldParentSHA := b.Parent, b.ParentSHA
	f.conflictOn("b")
	saveErr := errors.New("disk full")
	env.Save = func() error { return saveErr }
	abortErr := errors.New("abort failed")
	f.rebaseAbortErr = abortErr

	_, err := Onto(env, s, "c")
	if err == nil {
		t.Fatal("Onto returned nil error on conflict + failed save + failed abort")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Onto error = %v, want ErrConflict matchable (rebase still in progress)", err)
	}
	if !errors.Is(err, saveErr) {
		t.Fatalf("Onto error = %v, want save failure %v matchable", err, saveErr)
	}
	if !errors.Is(err, abortErr) {
		t.Fatalf("Onto error = %v, want abort failure %v matchable", err, abortErr)
	}
	for _, want := range []string{
		`moving "b" onto "c"`,
		"record pending reparent",
		"abort the in-progress rebase",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Onto error = %v, want it to contain %q", err, want)
		}
	}
	if inProgress, _ := f.RebaseInProgress(); !inProgress {
		t.Fatal("failed abort should leave the rebase in progress")
	}
	// The pending entry is dropped in memory to match the unpersisted disk.
	if s.PendingReparent != nil {
		t.Fatalf("PendingReparent = %+v, want nil (matches unpersisted disk)", s.PendingReparent)
	}
	if b.Parent != oldParent || b.ParentSHA != oldParentSHA {
		t.Fatalf("b metadata = (%s, %s), want unchanged (%s, %s)", b.Parent, b.ParentSHA, oldParent, oldParentSHA)
	}
}

// Continue must still promote a pending reparent (and restore HEAD) when git's
// head-name file is unreadable: a paused onto rebase is unambiguous.
func TestContinuePromotesPendingReparentWhenHeadNameEmpty(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get("b")
	targetSHA, _ := f.RevParse("c")
	f.conflictOn("b")

	if _, err := Onto(env, s, "c"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Onto error = %v, want %v", err, ErrConflict)
	}
	// Simulate git's head-name file being unreadable during continue.
	env.Git = emptyHeadNameGit{f}

	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if b.Parent != "c" || b.ParentSHA != targetSHA {
		t.Fatalf("b after continue = (%s, %s), want (c, %s)", b.Parent, b.ParentSHA, targetSHA)
	}
	if s.PendingReparent != nil {
		t.Fatalf("PendingReparent after continue = %+v, want nil", s.PendingReparent)
	}
}

// The dry-run previews must enforce the same clean-tree precondition as the real
// ops, so they don't promise restacks the real command would refuse.
func TestRestackPlanRefusesDirtyTree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.clean = false

	if _, err := RestackPlan(env, s); !errors.Is(err, ErrDirty) {
		t.Fatalf("RestackPlan on dirty tree = %v, want ErrDirty", err)
	}
}

func TestSyncPlanRefusesDirtyTree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.clean = false

	if _, err := SyncPlanAgainst(env, s, false, branchTipRef("main")); !errors.Is(err, ErrDirty) {
		t.Fatalf("SyncPlanAgainst on dirty tree = %v, want ErrDirty", err)
	}
}

func TestRestackConflictContinueRecovers(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.commit("a2")
	f.conflictOn("b")

	if _, err := Restack(env, s); !errors.Is(err, ErrConflict) {
		t.Fatalf("Restack error = %v, want %v", err, ErrConflict)
	}
	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	checkInvariants(t, f, s, 0)
}

// TestConflictErrorCarriesBranch asserts a stopped rebase returns a typed
// *ConflictError naming the branch and parent, while still matching ErrConflict.
func TestConflictErrorCarriesBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.commit("a2")
	f.conflictOn("b")

	_, err := Restack(env, s)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Restack error = %v, want ErrConflict", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("Restack error %v is not a *ConflictError", err)
	}
	if ce.Branch != "b" || ce.Onto != "a" {
		t.Errorf("ConflictError = {Branch:%q Onto:%q}, want {b a}", ce.Branch, ce.Onto)
	}
}

// TestConflictErrorMessageOmitsEmptyOnto: with a parent the message names it;
// without one (the rare re-stall of an untracked branch) the "onto …" clause is
// dropped rather than rendering an empty quoted parent.
func TestConflictErrorMessageOmitsEmptyOnto(t *testing.T) {
	withOnto := (&ConflictError{Action: "rebasing", Branch: "b", Onto: "a"}).Error()
	if !strings.Contains(withOnto, `"b" onto "a"`) {
		t.Errorf("with onto: %q, want it to name `\"b\" onto \"a\"`", withOnto)
	}
	noOnto := (&ConflictError{Action: "continuing", Branch: "b"}).Error()
	if strings.Contains(noOnto, "onto") {
		t.Errorf("empty onto: %q, want no \"onto\" clause", noOnto)
	}
	if !strings.Contains(noOnto, `"b"`) {
		t.Errorf("empty onto: %q, want it to still name the branch", noOnto)
	}
}

// TestContinueRestallCarriesBranch: when `st continue` re-stalls on the same
// conflict, the error is still a typed *ConflictError naming the branch, so the
// --json envelope carries branch/onto like the other conflict paths.
func TestContinueRestallCarriesBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.commit("a2")
	f.conflictOn("b")
	if _, err := Restack(env, s); !errors.Is(err, ErrConflict) {
		t.Fatalf("Restack error = %v, want ErrConflict", err)
	}

	f.rebaseRestall = true // the resolution attempt re-stalls
	_, err := Continue(env, s)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Continue error = %v, want ErrConflict", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Branch != "b" {
		t.Fatalf("Continue re-stall error = %v, want *ConflictError on branch b", err)
	}
}

// TestContinueUsesActualRebaseOnto: a parent can move while a child's rebase
// sits paused. Continue must record the target the rebase ACTUALLY completed
// onto (A1) — captured from the worktree-local rebase metadata — not the
// parent's moved tip (A2); otherwise the catch-up restack that incorporates
// A2 is suppressed because the recorded base already claims it.
func TestContinueUsesActualRebaseOnto(t *testing.T) {
	setup := func(t *testing.T) (*fakeGit, *State, Env, string, string) {
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		mkBranch(t, env, s, f, "a", "b")

		// Advance the parent to A1 so the restack has work, then pause b on it.
		if err := f.Checkout("a"); err != nil {
			t.Fatal(err)
		}
		f.commit("a1")
		a1, _ := f.RevParse("a")
		f.conflictOn("b")
		if _, err := Restack(env, s); !errors.Is(err, ErrConflict) {
			t.Fatalf("Restack error = %v, want ErrConflict", err)
		}

		// The parent moves AGAIN while b's rebase sits paused.
		f.commit("a2")
		a2, _ := f.RevParse("a")
		return f, s, env, a1, a2
	}

	t.Run("catch-up restack lands the moved parent", func(t *testing.T) {
		f, s, env, a1, a2 := setup(t)

		// A repeated conflict on the first continue attempt must leave the
		// paused rebase and the recorded bases untouched.
		f.rebaseRestall = true
		if _, err := Continue(env, s); !errors.Is(err, ErrConflict) {
			t.Fatalf("re-stall Continue error = %v, want ErrConflict", err)
		}
		f.rebaseRestall = false

		var saved []*State
		env.Save = func() error { saved = append(saved, cloneState(s)); return nil }

		if _, err := Continue(env, s); err != nil {
			t.Fatalf("Continue: %v", err)
		}

		// The first checkpoint after the completed rebase must record A1 —
		// the target b actually incorporated — not the moved tip A2.
		if len(saved) == 0 {
			t.Fatal("Continue persisted no checkpoint")
		}
		if b, _ := saved[0].Get("b"); b.ParentSHA != a1 {
			t.Fatalf("first checkpoint b.ParentSHA = %s, want the actual rebase target %s", b.ParentSHA, a1)
		}

		// The follow-up cascade must recognize the moved parent and rebase b
		// onto A2, so the final recorded base is legitimately A2.
		last := f.rebaseLog[len(f.rebaseLog)-1]
		if last.branch != "b" || last.newBase != a2 || last.oldBase != a1 {
			t.Fatalf("final rebase = (%s onto %s from %s), want b onto %s from %s",
				last.branch, last.newBase, last.oldBase, a2, a1)
		}
		if b, _ := s.Get("b"); b.ParentSHA != a2 {
			t.Fatalf("final b.ParentSHA = %s, want %s after the catch-up rebase", b.ParentSHA, a2)
		}
		checkInvariants(t, f, s, 0)
	})

	t.Run("conflicting catch-up keeps the completed checkpoint", func(t *testing.T) {
		f, s, env, a1, a2 := setup(t)

		var saved []*State
		env.Save = func() error { saved = append(saved, cloneState(s)); return nil }

		// Keep b conflicting so the catch-up rebase onto A2 stalls too: the
		// recorded base must stay A1 — the target actually incorporated.
		f.alwaysConflictOn("b")
		_, err := Continue(env, s)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("Continue error = %v, want ErrConflict from the catch-up rebase", err)
		}
		if len(saved) == 0 {
			t.Fatal("Continue persisted no checkpoint before the catch-up conflict")
		}
		if b, _ := saved[0].Get("b"); b.ParentSHA != a1 {
			t.Fatalf("checkpoint b.ParentSHA = %s, want %s", b.ParentSHA, a1)
		}
		if b, _ := s.Get("b"); b.ParentSHA != a1 {
			t.Fatalf("b.ParentSHA = %s after a conflicting catch-up, want %s retained", b.ParentSHA, a1)
		}
		if inProgress, _ := f.RebaseInProgress(); !inProgress {
			t.Fatal("catch-up rebase should still be paused on its conflict")
		}
		// The paused catch-up targets A2 — continuing it must record A2.
		if sha, err := f.RebaseOntoSHA(); err != nil || sha != a2 {
			t.Fatalf("paused catch-up target = %q err=%v, want %s", sha, err, a2)
		}
	})
}

// TestContinueRefusesWhenRebaseTargetUnreadable: for an ordinary tracked
// branch, a missing/corrupt rebase-merge/onto value is an actionable error
// BEFORE continuation — the paused rebase, refs, and state are untouched and
// nothing is saved.
func TestContinueRefusesWhenRebaseTargetUnreadable(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.commit("a1")
	f.conflictOn("b")
	if _, err := Restack(env, s); !errors.Is(err, ErrConflict) {
		t.Fatalf("Restack error = %v, want ErrConflict", err)
	}
	b, _ := s.Get("b")
	beforeSHA := b.ParentSHA
	aTip, _ := f.RevParse("a")

	f.rebaseOntoErr = errors.New("corrupt onto metadata")
	saves := 0
	env.Save = func() error { saves++; return nil }

	_, err := Continue(env, s)
	if !errors.Is(err, f.rebaseOntoErr) {
		t.Fatalf("Continue error = %v, want wrapped metadata failure", err)
	}
	if saves != 0 {
		t.Fatalf("Save called %d times during a refused continue", saves)
	}
	if inProgress, _ := f.RebaseInProgress(); !inProgress {
		t.Fatal("refused continue must leave the rebase paused")
	}
	if b, _ := s.Get("b"); b.ParentSHA != beforeSHA {
		t.Fatalf("b.ParentSHA changed during refused continue: %s -> %s", beforeSHA, b.ParentSHA)
	}
	if tip, _ := f.RevParse("b"); tip == "" || tip == aTip {
		t.Fatalf("b tip = %q after refused continue, want its pre-rebase ref", tip)
	}
}

func TestFoldConflictContinueRecovers(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	f.commit("b2")
	f.conflictOn("c")

	if _, err := Fold(env, s); !errors.Is(err, ErrConflict) {
		t.Fatalf("Fold error = %v, want %v", err, ErrConflict)
	}
	if s.IsTracked("b") {
		t.Fatal("b still tracked after conflicted fold")
	}
	if cb, _ := s.Get("c"); cb.Parent != "a" {
		t.Fatalf("c parent=%q after conflicted fold, want a", cb.Parent)
	}

	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	checkInvariants(t, f, s, 0)
}

func TestSquashConflictContinueRecovers(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	f.commit("a2")
	f.conflictOn("b")

	if _, err := Squash(env, s, "squashed"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Squash error = %v, want %v", err, ErrConflict)
	}
	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	checkInvariants(t, f, s, 0)
}

func TestDeleteConflictContinueRecovers(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.conflictOn("b")

	if _, err := Delete(env, s, "a", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("Delete error = %v, want %v", err, ErrConflict)
	}
	if s.IsTracked("a") {
		t.Fatal("a still tracked after conflicted delete")
	}
	if bb, _ := s.Get("b"); bb.Parent != "main" {
		t.Fatalf("b parent=%q after conflicted delete, want main", bb.Parent)
	}

	if _, err := Continue(env, s); err != nil {
		t.Fatalf("continue: %v", err)
	}
	checkInvariants(t, f, s, 0)
}

func TestOntoPersistsReparentBeforeRestackingDescendants(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "d")
	mkBranch(t, env, s, f, "main", "c")
	if err := f.Checkout("b"); err != nil {
		t.Fatal(err)
	}
	var saved []*State
	env.Save = func() error {
		saved = append(saved, cloneState(s))
		return nil
	}
	f.conflictOn("d")

	if _, err := Onto(env, s, "c"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Onto error = %v, want %v", err, ErrConflict)
	}
	if len(saved) == 0 {
		t.Fatal("Onto did not save the current branch reparent before descendant restack")
	}
	b, _ := saved[len(saved)-1].Get("b")
	cTip, _ := f.RevParse("c")
	if b.Parent != "c" || b.ParentSHA != cTip {
		t.Fatalf("saved b metadata = (%s, %s), want (c, %s)", b.Parent, b.ParentSHA, cTip)
	}
}

func TestEngineGuards(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := Modify(env, s, "x", true, false); err == nil {
		t.Fatal("modify on trunk should error")
	}
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Fold(env, s); err == nil {
		t.Fatal("fold of a bottom branch into the trunk should error")
	}
	if _, err := Onto(env, s, "a"); err == nil {
		t.Fatal("onto self should error")
	}
	if _, err := Delete(env, s, "main", true); err == nil {
		t.Fatal("delete trunk should error")
	}
}

func TestCreateRejectsUntrackedParentBeforeBranchCreation(t *testing.T) {
	f, s, env := newEnvState()
	if err := f.CreateBranch("scratch"); err != nil {
		t.Fatal(err)
	}
	f.staged = true

	if _, err := Create(env, s, "child", "child", false); err == nil {
		t.Fatal("create on untracked parent should error")
	}
	if f.BranchExists("child") {
		t.Fatal("create left child branch behind")
	}
}

func TestCreateRemovesBranchWhenInitialCommitFails(t *testing.T) {
	f, s, env := newEnvState()
	f.staged = true
	errBoom := errors.New("commit hook rejected")
	f.commitErr = errBoom

	if _, err := Create(env, s, "a", "msg", false); !errors.Is(err, errBoom) {
		t.Fatalf("Create error = %v, want %v", err, errBoom)
	}
	if f.BranchExists("a") {
		t.Fatal("failed create left branch a behind")
	}
	if s.IsTracked("a") {
		t.Fatal("failed create tracked branch a")
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q, want main", f.head)
	}
}

func TestCreateValidatesStagedStateBeforeBranchCreation(t *testing.T) {
	f, s, env := newEnvState()
	if _, err := Create(env, s, "all-no-message", "", true); err == nil {
		t.Fatal("create -a without message should error")
	}
	if f.BranchExists("all-no-message") || f.staged {
		t.Fatal("create -a without message mutated branch or staged state")
	}

	f.staged = true
	if _, err := Create(env, s, "no-message", "", false); err == nil {
		t.Fatal("create with staged changes and no message should error")
	}
	if f.BranchExists("no-message") {
		t.Fatal("create left no-message branch behind")
	}

	f.staged = false
	if _, err := Create(env, s, "no-staged", "msg", false); err == nil {
		t.Fatal("create with message and no staged changes should error")
	}
	if f.BranchExists("no-staged") {
		t.Fatal("create left no-staged branch behind")
	}
	if f.head != "main" {
		t.Fatalf("HEAD = %q, want main", f.head)
	}
}

// TestRequireCleanGuards asserts every mutator that requires a clean working
// tree returns ErrDirty — and moves no branch — when the tree is dirty.
// requireClean is the first thing each does, so a minimal stack suffices (TEST-1).
func TestRequireCleanGuards(t *testing.T) {
	cases := []struct {
		name string
		op   func(Env, *State) (*OpResult, error)
	}{
		{"Restack", func(env Env, s *State) (*OpResult, error) { return Restack(env, s) }},
		{"Fold", func(env Env, s *State) (*OpResult, error) { return Fold(env, s) }},
		{"Squash", func(env Env, s *State) (*OpResult, error) { return Squash(env, s, "m") }},
		{"Onto", func(env Env, s *State) (*OpResult, error) { return Onto(env, s, "main") }},
		{"Sync", func(env Env, s *State) (*OpResult, error) {
			return Sync(env, &fakeRemote{exists: false}, s, "origin", false)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s, env := newEnvState()
			mkBranch(t, env, s, f, "main", "a")
			mkBranch(t, env, s, f, "a", "b")
			if err := f.Checkout("b"); err != nil {
				t.Fatal(err)
			}
			before := map[string]string{}
			for k, v := range f.branches {
				before[k] = v
			}
			f.clean = false

			if _, err := c.op(env, s); !errors.Is(err, ErrDirty) {
				t.Fatalf("%s with a dirty tree = %v, want ErrDirty", c.name, err)
			}
			if len(f.branches) != len(before) {
				t.Fatalf("%s changed the branch set despite a dirty tree", c.name)
			}
			for k, v := range f.branches {
				if before[k] != v {
					t.Fatalf("%s moved branch %q despite a dirty tree", c.name, k)
				}
			}
		})
	}
}

func TestDeleteRequiresCleanTreeBeforeMutation(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	f.clean = false

	if _, err := Delete(env, s, "a", true); !errors.Is(err, ErrDirty) {
		t.Fatalf("Delete dirty tree error = %v, want ErrDirty", err)
	}
	if !s.IsTracked("a") || !f.BranchExists("a") {
		t.Fatal("Delete mutated branch or metadata despite dirty tree")
	}
}

func TestDeleteNonForceChecksMergedIntoParent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if _, err := Delete(env, s, "a", false); err == nil {
		t.Fatal("non-forced delete should fail when a is not merged into parent")
	}
	if !s.IsTracked("a") || !f.BranchExists("a") {
		t.Fatal("non-forced delete mutated branch or metadata")
	}
	if f.head != "b" {
		t.Fatalf("HEAD = %q, want b restored", f.head)
	}
}

func TestPruneMergedDoesNotMutateStateWhenDeleteFails(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	aTip, _ := f.RevParse("a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}
	errBoom := errors.New("branch checked out elsewhere")
	f.deleteErr["a"] = errBoom

	if _, err := PruneMerged(env, s); !errors.Is(err, errBoom) {
		t.Fatalf("PruneMerged error = %v, want %v", err, errBoom)
	}
	if !s.IsTracked("a") {
		t.Fatal("failed prune untracked a")
	}
	b, _ := s.Get("b")
	if b.Parent != "a" {
		t.Fatalf("b parent = %q, want a", b.Parent)
	}
	if !f.BranchExists("a") {
		t.Fatal("failed prune deleted branch a")
	}
}

func TestPruneMergedBatchesMergedSet(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")

	bTip, _ := f.RevParse("b")
	if err := f.ForceBranch("main", bTip); err != nil {
		t.Fatal(err)
	}

	deleted, err := PruneMerged(env, s)
	if err != nil {
		t.Fatalf("PruneMerged: %v", err)
	}
	if len(deleted) != 2 || deleted[0] != "a" || deleted[1] != "b" {
		t.Fatalf("PruneMerged deleted = %v, want [a b]", deleted)
	}
	if f.isAncestorCalls != 0 {
		t.Fatalf("PruneMerged made %d IsAncestor calls, want 0", f.isAncestorCalls)
	}
	if f.mergedIntoCalls != 1 {
		t.Fatalf("PruneMerged made %d MergedInto calls, want 1", f.mergedIntoCalls)
	}
	if s.IsTracked("a") || s.IsTracked("b") {
		t.Fatal("merged branches a/b should have been pruned")
	}
	if !s.IsTracked("c") {
		t.Fatal("unmerged branch c should remain tracked")
	}
}

func TestPruneMergedErrorsWhenTrackedBranchTipMissing(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	delete(f.branches, "a")

	_, err := PruneMerged(env, s)
	if err == nil {
		t.Fatal("PruneMerged with a missing tracked branch returned nil error")
	}
	if !strings.Contains(err.Error(), `tracked branch "a" does not exist`) {
		t.Fatalf("PruneMerged error = %v, want missing branch context", err)
	}
	if !s.IsTracked("a") {
		t.Fatal("failed prune untracked missing branch a")
	}
}

// TestRestackAllOpReachesSiblingStacks pins --all's reason to exist: from a
// leaf of one stack it restacks a SIBLING stack plain Restack would not touch,
// and the dry-run plan lists exactly the branch set the op rebases.
func TestRestackAllOpReachesSiblingStacks(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "main", "feat-x")
	mkBranch(t, env, s, f, "feat-x", "feat-y")

	// Advance main so every stack needs a restack.
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance-main")
	// Run from feat-a's leaf: plain restack would never reach feat-x/feat-y.
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}

	plan, err := RestackAllPlan(env, s)
	if err != nil {
		t.Fatalf("RestackAllPlan: %v", err)
	}
	res, err := RestackAllOp(env, s)
	if err != nil {
		t.Fatalf("RestackAllOp: %v", err)
	}
	if !reflect.DeepEqual(plan.Restacked, res.Restacked) {
		t.Fatalf("plan/op disagree: plan %v, op %v", plan.Restacked, res.Restacked)
	}
	want := map[string]bool{"feat-a": true, "feat-x": true, "feat-y": true}
	if len(res.Restacked) != len(want) {
		t.Fatalf("restacked = %v, want all of %v", res.Restacked, want)
	}
	for _, name := range res.Restacked {
		if !want[name] {
			t.Fatalf("restacked unexpected branch %q (%v)", name, res.Restacked)
		}
	}
	mainTip, _ := f.RevParse("main")
	if !mustFakeIsAncestor(t, f, mainTip, "feat-x") {
		t.Fatal("feat-x was not restacked onto the advanced main")
	}
	if f.head != "feat-a" {
		t.Fatalf("HEAD = %q after restack --all, want feat-a restored", f.head)
	}
}

// TestRestackAllOpSkipsDirtyOwnedWorktree pins the orchestrator contract: a
// branch living in a dirty linked worktree is skipped with a note, not
// clobbered and not an error.
func TestRestackAllOpSkipsDirtyOwnedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	mkBranch(t, env, s, f, "main", "feat-x")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.commit("advance-main")
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	f.addWorktree("/wt/x", "feat-x")
	f.markWorktreeDirty("feat-x")

	res, err := RestackAllOp(env, s)
	if err != nil {
		t.Fatalf("RestackAllOp: %v", err)
	}
	for _, name := range res.Restacked {
		if name == "feat-x" {
			t.Fatal("dirty-owned feat-x was restacked")
		}
	}
	found := false
	for _, note := range res.Notes {
		if strings.Contains(note, "feat-x") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %v, want a skip note naming feat-x", res.Notes)
	}
}

// TestDeleteSingleTipsRead pins delete's spawn diet: restacking the
// re-parented children (and their descendants) reads the full branch-tips map
// exactly ONCE, however many children the deleted branch had.
func TestDeleteSingleTipsRead(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "victim")
	mkBranch(t, env, s, f, "victim", "child-a")
	mkBranch(t, env, s, f, "child-a", "grand-a")
	mkBranch(t, env, s, f, "victim", "child-b")
	mkBranch(t, env, s, f, "victim", "child-c")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	spy := &tipReadSpyGit{Git: f}
	env.Git = spy
	res, err := Delete(env, s, "victim", true)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if spy.tipsCalls != 1 {
		t.Fatalf("Tips() called %d times during delete, want 1", spy.tipsCalls)
	}
	want := []string{"child-a", "grand-a", "child-b", "child-c"}
	if !reflect.DeepEqual(res.Restacked, want) {
		t.Fatalf("restacked = %v, want %v (per-child walk order)", res.Restacked, want)
	}
}

// --- track --all ------------------------------------------------------------

// mkUntracked creates a git branch (untracked by the state) forking at the
// current tip of parent and committing one fake commit on it.
func mkUntracked(t *testing.T, f *fakeGit, parent, name string) {
	t.Helper()
	if err := f.Checkout(parent); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranch(name); err != nil {
		t.Fatal(err)
	}
	f.commit("c-" + name)
}

func TestTrackAllBranchesAdoptsLinearChain(t *testing.T) {
	f, s, env := newEnvState()
	mkUntracked(t, f, "main", "a")
	mkUntracked(t, f, "a", "b")
	mkUntracked(t, f, "b", "c")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	res, err := TrackAllBranches(env, s)
	if err != nil {
		t.Fatalf("track --all: %v", err)
	}
	want := map[string]string{"a": "main", "b": "a", "c": "b"}
	for name, parent := range want {
		b, ok := s.Get(name)
		if !ok {
			t.Fatalf("%s was not tracked", name)
		}
		if b.Parent != parent {
			t.Fatalf("%s parent=%q, want %q", name, b.Parent, parent)
		}
		if res.Tracked[name] != parent {
			t.Fatalf("res.Tracked[%q]=%q, want %q", name, res.Tracked[name], parent)
		}
	}
}

// TestTrackAllBranchesParentsBeforeChildren builds an untracked fork off an
// untracked base and asserts the base is recorded before the child — the
// child can only be tracked once its parent is in the forest.
func TestTrackAllBranchesParentsBeforeChildren(t *testing.T) {
	f, s, env := newEnvState()
	mkUntracked(t, f, "main", "base")
	mkUntracked(t, f, "base", "feat")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	if _, err := TrackAllBranches(env, s); err != nil {
		t.Fatalf("track --all: %v", err)
	}
	if b, _ := s.Get("feat"); b.Parent != "base" {
		t.Fatalf("feat parent=%q, want base", b.Parent)
	}
	if b, _ := s.Get("base"); b.Parent != "main" {
		t.Fatalf("base parent=%q, want main", b.Parent)
	}
}

// TestTrackAllBranchesCycleRefusal: two branches pointing at the same commit
// are mutual ancestors, so inference proposes x->y and y->x. The batch must
// refuse naming both rather than recording a cycle into the forest.
func TestTrackAllBranchesCycleRefusal(t *testing.T) {
	f, s, env := newEnvState()
	mkUntracked(t, f, "main", "x")
	xTip, _ := f.RevParse("x")
	if err := f.CreateBranchAt("y", xTip); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	_, err := TrackAllBranches(env, s)
	if err == nil {
		t.Fatal("track --all accepted a cyclic parent proposal")
	}
	for _, name := range []string{"x", "y"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("cycle error %v does not name %s", err, name)
		}
		if s.IsTracked(name) {
			t.Fatalf("%s was tracked despite the refused batch", name)
		}
	}
}

// TestTrackAllBranchesFallbacks: a branch already merged into the trunk lands
// on the trunk (the same fallback as single-track inference), while a true
// orphan — no merge base with the trunk, nothing to record as its base — is
// skipped with a note rather than failing the batch.
func TestTrackAllBranchesFallbacks(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "tracked-a")
	mkUntracked(t, f, "main", "feat")

	// `old` sits at the trunk tip — already merged, never a parent candidate
	// for anyone; its own parent is the trunk.
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateBranchAt("old", "main"); err != nil {
		t.Fatal(err)
	}
	// `orphan` has no commits on the trunk's line and nothing merges into it.
	f.commits["r0"] = &fakeCommit{id: "r0", subject: "unrelated", content: map[string]bool{}}
	if err := f.CreateBranchAt("orphan", "r0"); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	res, err := TrackAllBranches(env, s)
	if err != nil {
		t.Fatalf("track --all: %v", err)
	}
	for _, name := range []string{"old", "feat"} {
		b, ok := s.Get(name)
		if !ok {
			t.Fatalf("%s was not tracked", name)
		}
		if b.Parent != "main" {
			t.Fatalf("%s parent=%q, want main", name, b.Parent)
		}
	}
	if s.IsTracked("orphan") {
		t.Fatal("orphan was tracked despite having no base to record")
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "orphan") {
		t.Fatalf("Notes = %v, want a skip note naming orphan", res.Notes)
	}
}

// TestTrackAllBranchesSkipsTrackedAndTrunk: already-tracked branches and the
// trunk are never re-adopted; an empty untracked set is a clean no-op result.
func TestTrackAllBranchesSkipsTrackedAndTrunk(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")

	res, err := TrackAllBranches(env, s)
	if err != nil {
		t.Fatalf("track --all with nothing untracked: %v", err)
	}
	if len(res.Tracked) != 0 {
		t.Fatalf("Tracked = %v, want empty", res.Tracked)
	}
}

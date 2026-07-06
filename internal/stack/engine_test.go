package stack

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func newEnvState() (*fakeGit, *State, Env) {
	f := newFakeGit()
	s := &State{Trunk: "main", Branches: map[string]*Branch{}}
	return f, s, Env{Git: f}
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
	rebased, err := s.RestackUpstack(env, "main")
	if err != nil {
		t.Fatalf("RestackUpstack: %v", err)
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
	rebased, err := s.RestackUpstack(env, "a")
	if err != nil {
		t.Fatalf("RestackUpstack: %v", err)
	}
	if len(rebased) != 2 || rebased[0] != "b" || rebased[1] != "c" {
		t.Fatalf("rebased = %v, want [b c]", rebased)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls = %d, want 1", counting.tipsCalls)
	}
	if counting.revParseCalls != 2 {
		t.Fatalf("RevParse calls = %d, want 2 refreshes", counting.revParseCalls)
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

	_, err := s.RestackBranch(env, "feat-a")
	if err == nil {
		t.Fatal("cross-worktree conflict with abort failure returned nil error")
	}
	if !errors.Is(err, abortErr) {
		t.Fatalf("RestackBranch error = %v, want abort failure matchable", err)
	}
	for _, want := range []string{
		`rebasing "feat-a" in its worktree "/wt/feat-a"`,
		`conflict rebasing "feat-a"`,
		`abort the paused rebase in "/wt/feat-a" (it is still in progress there)`,
		"abort failed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RestackBranch error = %v, want it to contain %q", err, want)
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

	_, err := s.RestackBranch(env, "feat-a")
	if err == nil {
		t.Fatal("cross-worktree early rebase failure returned nil error")
	}
	if !errors.Is(err, rebaseErr) {
		t.Fatalf("RestackBranch error = %v, want original rebase failure matchable", err)
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

	if _, err := TrackBranch(env, s, ""); err != nil {
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

	if _, err := TrackBranch(env, s, ""); err != nil {
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

	if _, err := TrackBranch(env, s, ""); err != nil {
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

	if _, err := TrackBranch(env, s, ""); err != nil {
		t.Fatalf("track: %v", err)
	}
	if mb, _ := s.Get("manual"); mb.Parent != "main" {
		t.Fatalf("manual parent=%q, want main", mb.Parent)
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

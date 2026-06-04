package stack

import (
	"errors"
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

func TestEngineDeleteDropsCommits(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	bTip, _ := f.RevParse("b")

	if _, err := Delete(env, s, "b", true); err != nil {
		t.Fatalf("delete: %v", err)
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

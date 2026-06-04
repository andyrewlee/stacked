package stack

import "testing"

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
	if _, err := Create(env, s, name, "c-"+name, false); err != nil {
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
		if !f.IsAncestor(b.ParentSHA, n) {
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
	if f.IsAncestor(bTip, "c") {
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
	if !f.IsAncestor(aTip, "b") {
		t.Fatal("b was not rebased onto the amended a")
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

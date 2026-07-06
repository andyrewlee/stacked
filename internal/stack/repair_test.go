package stack

import (
	"fmt"
	"reflect"
	"testing"
)

type noTipsForRepairGit struct {
	Git
}

func (g noTipsForRepairGit) TipsFor([]string) (map[string]string, error) {
	return nil, fmt.Errorf("repair should not call TipsFor")
}

// TestInconsistenciesClassifiesEveryKind asserts the single classifier reports
// each kind, with the parent kinds kept distinct, in validate's order.
func TestInconsistenciesClassifiesEveryKind(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "main", "c")
	mkBranch(t, env, s, f, "main", "x")
	mkBranch(t, env, s, f, "x", "y")

	s.Branches["c"].Parent = "ghost" // ParentUntracked
	s.Branches["x"].Parent = "y"     // x <-> y cycle
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteBranch("a", true); err != nil { // a -> BranchMissing, b -> ParentMissing
		t.Fatal(err)
	}

	tips, _ := f.Tips()
	want := []Problem{
		{Kind: BranchMissing, Branch: "a"},
		{Kind: ParentMissing, Branch: "b", Detail: "a"},
		{Kind: ParentUntracked, Branch: "c", Detail: "ghost"},
		{Kind: ParentCycle, Branch: "x", Detail: "x -> y -> x"},
		{Kind: ParentCycle, Branch: "y", Detail: "y -> x -> y"},
	}
	if got := s.Inconsistencies(tips); !reflect.DeepEqual(got, want) {
		t.Fatalf("Inconsistencies =\n %+v\nwant\n %+v", got, want)
	}

	if err := f.Checkout("y"); err != nil { // move off main so its ref can be deleted
		t.Fatal(err)
	}
	if err := f.DeleteBranch("main", true); err != nil {
		t.Fatal(err)
	}
	tips, _ = f.Tips()
	if got := s.Inconsistencies(tips); len(got) == 0 || got[0].Kind != TrunkMissing {
		t.Fatalf("after deleting trunk, first problem = %+v, want TrunkMissing first", got)
	}
}

// reconcileAndCheck runs a full restack from the trunk and asserts the topology
// invariants hold — the same bar the random model enforces — so each repair test
// proves Repair leaves a state a restack can reconcile.
func reconcileAndCheck(t *testing.T, f *fakeGit, s *State, env Env) {
	t.Helper()
	f.head = "main"
	if _, err := Restack(env, s); err != nil {
		t.Fatalf("restack after repair: %v", err)
	}
	checkInvariants(t, f, s, 0)
}

// TestRepairUntracksMissingBranch: a branch whose git ref was deleted outside st
// is untracked and its children re-parented.
func TestRepairUntracksMissingBranch(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteBranch("a", true); err != nil { // delete a's git ref behind the engine's back
		t.Fatal(err)
	}

	res, err := Repair(env, s)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(res.Notes) == 0 {
		t.Fatal("Repair reported no fixes for a missing branch")
	}
	if s.IsTracked("a") {
		t.Errorf("missing branch %q is still tracked after repair", "a")
	}
	if b, ok := s.Get("b"); !ok || b.Parent != "main" {
		t.Errorf("child b parent = %+v, want re-parented onto main", b)
	}
	reconcileAndCheck(t, f, s, env)
}

// TestRepairUntracksCorruptMissingBranchWithoutScopedLookup covers recovery
// from hostile or corrupt metadata: the name is tracked in state, absent from
// git, and unsafe to pass back as a scoped ref argument.
func TestRepairUntracksCorruptMissingBranchWithoutScopedLookup(t *testing.T) {
	f, s, env := newEnvState()
	mainTip, err := f.RevParse("main")
	if err != nil {
		t.Fatal(err)
	}
	mkBranch(t, env, s, f, "main", "child")

	corrupt := "--exec=touch pwned"
	s.Track(corrupt, "main", mainTip)
	s.Branches["child"].Parent = corrupt
	s.Branches["child"].ParentSHA = ""
	env.Git = noTipsForRepairGit{Git: env.Git}

	res, err := Repair(env, s)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(res.Notes) == 0 {
		t.Fatal("Repair reported no fixes for a corrupt missing branch")
	}
	if s.IsTracked(corrupt) {
		t.Errorf("corrupt missing branch %q is still tracked after repair", corrupt)
	}
	child, ok := s.Get("child")
	if !ok {
		t.Fatal("child branch is no longer tracked")
	}
	if child.Parent != "main" || child.ParentSHA != mainTip {
		t.Errorf("child parent = (%s, %s), want (main, %s)", child.Parent, child.ParentSHA, mainTip)
	}
	reconcileAndCheck(t, f, s, env)
}

// TestRepairReparentsInvalidParent: a branch whose parent is neither the trunk
// nor a tracked branch is re-parented onto the trunk.
func TestRepairReparentsInvalidParent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	s.Branches["b"].Parent = "ghost" // untracked, nonexistent parent

	res, err := Repair(env, s)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(res.Notes) == 0 {
		t.Fatal("Repair reported no fixes for an invalid parent")
	}
	if got := s.Branches["b"].Parent; got != "main" {
		t.Errorf("b parent = %q, want main", got)
	}
	reconcileAndCheck(t, f, s, env)
}

// TestRepairBreaksCycle: a parent cycle is broken by re-parenting onto the trunk.
func TestRepairBreaksCycle(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	s.Branches["a"].Parent = "b" // a -> b -> a

	res, err := Repair(env, s)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(res.Notes) == 0 {
		t.Fatal("Repair reported no fixes for a cycle")
	}
	if p := CyclePath(s, "a"); p != "" {
		t.Errorf("cycle remains at a after repair: %q", p)
	}
	reconcileAndCheck(t, f, s, env)
}

// TestRepairMissingTrunkErrors: with no trunk ref there is nothing to repair onto.
func TestRepairMissingTrunkErrors(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteBranch("main", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Repair(env, s); err == nil {
		t.Fatal("Repair with a missing trunk should error")
	}
}

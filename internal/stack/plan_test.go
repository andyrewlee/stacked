package stack

import "testing"

func TestRestackPlanListsOutOfDateAndDescendants(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	// Everything in sync: the plan from a is empty.
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}
	plan, err := RestackPlan(env, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Restacked) != 0 {
		t.Fatalf("expected empty plan, got %v", plan.Restacked)
	}
	if !plan.DryRun {
		t.Fatal("plan should be marked DryRun")
	}

	// Amend a: b is now out of date, which forces c. Plan = [b, c]; nothing moves.
	f.amend("a2")
	bTip, _ := f.RevParse("b")
	plan, err = RestackPlan(env, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Restacked; len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("plan = %v, want [b c]", got)
	}
	if after, _ := f.RevParse("b"); after != bTip {
		t.Fatal("a dry-run plan must not move any branch")
	}
}

func TestRestackPlanUsesSingleTipsRead(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	mkBranch(t, env, s, f, "c", "d")
	if err := f.Checkout("a"); err != nil {
		t.Fatal(err)
	}

	counting := &countingSnapshotGit{Git: f}
	env.Git = counting
	if _, err := RestackPlan(env, s); err != nil {
		t.Fatal(err)
	}
	if counting.revParseCalls != 0 {
		t.Fatalf("RevParse calls = %d, want 0", counting.revParseCalls)
	}
	if counting.tipsCalls != 1 {
		t.Fatalf("Tips calls = %d, want 1", counting.tipsCalls)
	}
}

func TestRestackPlanErrorsWhenParentTipIsMissing(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	delete(f.branches, "a")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}

	if _, err := RestackPlan(env, s); err == nil {
		t.Fatal("RestackPlan with a missing parent branch returned nil error")
	}
}

func TestRestackPlanRejectsUntrackedBranch(t *testing.T) {
	f, s, env := newEnvState()
	if err := f.CreateBranch("scratch"); err != nil {
		t.Fatal(err)
	}

	if _, err := RestackPlan(env, s); err == nil {
		t.Fatal("RestackPlan on untracked branch should error")
	}
}

func TestSyncPlanPreviewsPrune(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	// Simulate a merged into the trunk.
	aTip, _ := f.RevParse("a")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatal(err)
	}

	plan, err := SyncPlan(env, s, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Deleted) != 1 || plan.Deleted[0] != "a" {
		t.Fatalf("would-delete = %v, want [a]", plan.Deleted)
	}
	if !plan.DryRun {
		t.Fatal("sync plan should be marked DryRun")
	}
	if !s.IsTracked("a") {
		t.Fatal("a dry-run sync must not actually untrack anything")
	}
}

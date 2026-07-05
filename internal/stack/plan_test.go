package stack

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

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

func TestFoldPlanMatchesActualAndDoesNotMutate(t *testing.T) {
	setup := func(t *testing.T) (*fakeGit, *State, Env) {
		t.Helper()
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		mkBranch(t, env, s, f, "a", "b")
		mkBranch(t, env, s, f, "b", "c")
		mkBranch(t, env, s, f, "a", "d")
		if err := f.Checkout("b"); err != nil {
			t.Fatal(err)
		}
		return f, s, env
	}

	f, s, env := setup(t)
	before := capturePreviewState(t, f, s)
	preview, err := FoldPlan(env, s)
	if err != nil {
		t.Fatalf("FoldPlan: %v", err)
	}
	assertPreviewDidNotMutate(t, f, s, before)
	if !preview.DryRun {
		t.Fatal("FoldPlan should be marked DryRun")
	}
	if got := preview.Deleted; !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("FoldPlan Deleted = %v, want [b]", got)
	}

	f2, s2, env2 := setup(t)
	actual, err := Fold(env2, s2)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if preview.Branch != actual.Branch {
		t.Fatalf("Branch preview=%q actual=%q", preview.Branch, actual.Branch)
	}
	if !reflect.DeepEqual(preview.Restacked, actual.Restacked) {
		t.Fatalf("Restacked preview=%v actual=%v", preview.Restacked, actual.Restacked)
	}
	if s2.IsTracked("b") || f2.BranchExists("b") {
		t.Fatal("actual fold should delete b")
	}
}

func TestSquashPlanMatchesActualAndDoesNotMutate(t *testing.T) {
	setup := func(t *testing.T) (*fakeGit, *State, Env) {
		t.Helper()
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		if err := f.Checkout("a"); err != nil {
			t.Fatal(err)
		}
		f.commit("a2")
		mkBranch(t, env, s, f, "a", "b")
		if err := f.Checkout("a"); err != nil {
			t.Fatal(err)
		}
		return f, s, env
	}

	f, s, env := setup(t)
	before := capturePreviewState(t, f, s)
	preview, err := SquashPlan(env, s, "squashed")
	if err != nil {
		t.Fatalf("SquashPlan: %v", err)
	}
	assertPreviewDidNotMutate(t, f, s, before)
	if !preview.DryRun {
		t.Fatal("SquashPlan should be marked DryRun")
	}

	_, s2, env2 := setup(t)
	actual, err := Squash(env2, s2, "squashed")
	if err != nil {
		t.Fatalf("Squash: %v", err)
	}
	assertPlanResultFields(t, preview, actual)
}

func TestOntoPlanMatchesActualAndDoesNotMutate(t *testing.T) {
	setup := func(t *testing.T) (*fakeGit, *State, Env) {
		t.Helper()
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		mkBranch(t, env, s, f, "a", "b")
		mkBranch(t, env, s, f, "b", "d")
		mkBranch(t, env, s, f, "main", "c")
		if err := f.Checkout("b"); err != nil {
			t.Fatal(err)
		}
		return f, s, env
	}

	f, s, env := setup(t)
	before := capturePreviewState(t, f, s)
	preview, err := OntoPlan(env, s, "c")
	if err != nil {
		t.Fatalf("OntoPlan: %v", err)
	}
	assertPreviewDidNotMutate(t, f, s, before)
	if !preview.DryRun {
		t.Fatal("OntoPlan should be marked DryRun")
	}

	_, s2, env2 := setup(t)
	actual, err := Onto(env2, s2, "c")
	if err != nil {
		t.Fatalf("Onto: %v", err)
	}
	assertPlanResultFields(t, preview, actual)
}

func TestDeletePlanMatchesActualAndDoesNotMutate(t *testing.T) {
	setup := func(t *testing.T) (*fakeGit, *State, Env) {
		t.Helper()
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		mkBranch(t, env, s, f, "a", "b")
		mkBranch(t, env, s, f, "b", "c")
		if err := f.Checkout("main"); err != nil {
			t.Fatal(err)
		}
		return f, s, env
	}

	f, s, env := setup(t)
	before := capturePreviewState(t, f, s)
	preview, err := DeletePlan(env, s, "a", true)
	if err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	assertPreviewDidNotMutate(t, f, s, before)
	if !preview.DryRun {
		t.Fatal("DeletePlan should be marked DryRun")
	}

	_, s2, env2 := setup(t)
	actual, err := Delete(env2, s2, "a", true)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	assertPlanResultFields(t, preview, actual)
}

func TestTopologyPlansMatchValidationErrors(t *testing.T) {
	t.Run("fold into trunk", func(t *testing.T) {
		f, s, env := newEnvState()
		mkBranch(t, env, s, f, "main", "a")
		if err := f.Checkout("a"); err != nil {
			t.Fatal(err)
		}
		assertSameError(t, "cannot fold", func() error {
			_, err := FoldPlan(env, s)
			return err
		}, func() error {
			_, err := Fold(env, s)
			return err
		})
	})

	t.Run("squash needing restack", func(t *testing.T) {
		setup := func(t *testing.T) (*State, Env) {
			t.Helper()
			f, s, env := newEnvState()
			mkBranch(t, env, s, f, "main", "a")
			if err := f.Checkout("main"); err != nil {
				t.Fatal(err)
			}
			f.commit("main2")
			if err := f.Checkout("a"); err != nil {
				t.Fatal(err)
			}
			return s, env
		}
		s, env := setup(t)
		s2, env2 := setup(t)
		assertSameError(t, "needs restack before squashing", func() error {
			_, err := SquashPlan(env, s, "")
			return err
		}, func() error {
			_, err := Squash(env2, s2, "")
			return err
		})
	})

	t.Run("onto own descendant", func(t *testing.T) {
		setup := func(t *testing.T) (*State, Env) {
			t.Helper()
			f, s, env := newEnvState()
			mkBranch(t, env, s, f, "main", "a")
			mkBranch(t, env, s, f, "a", "b")
			if err := f.Checkout("a"); err != nil {
				t.Fatal(err)
			}
			return s, env
		}
		s, env := setup(t)
		s2, env2 := setup(t)
		assertSameError(t, "own descendant", func() error {
			_, err := OntoPlan(env, s, "b")
			return err
		}, func() error {
			_, err := Onto(env2, s2, "b")
			return err
		})
	})

	t.Run("delete non-force unmerged", func(t *testing.T) {
		setup := func(t *testing.T) (*State, Env) {
			t.Helper()
			f, s, env := newEnvState()
			mkBranch(t, env, s, f, "main", "a")
			if err := f.Checkout("main"); err != nil {
				t.Fatal(err)
			}
			return s, env
		}
		s, env := setup(t)
		s2, env2 := setup(t)
		assertSameError(t, "not merged into its stack parent", func() error {
			_, err := DeletePlan(env, s, "a", false)
			return err
		}, func() error {
			_, err := Delete(env2, s2, "a", false)
			return err
		})
	})
}

type previewSnapshot struct {
	tips  map[string]string
	head  string
	state *State
}

func capturePreviewState(t *testing.T, f *fakeGit, s *State) previewSnapshot {
	t.Helper()
	tips, err := f.Tips()
	if err != nil {
		t.Fatal(err)
	}
	head, err := f.CurrentBranch()
	if err != nil {
		t.Fatal(err)
	}
	return previewSnapshot{tips: tips, head: head, state: cloneState(s)}
}

func assertPreviewDidNotMutate(t *testing.T, f *fakeGit, s *State, before previewSnapshot) {
	t.Helper()
	afterTips, err := f.Tips()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterTips, before.tips) {
		t.Fatalf("preview changed branch tips: before=%v after=%v", before.tips, afterTips)
	}
	head, err := f.CurrentBranch()
	if err != nil {
		t.Fatal(err)
	}
	if head != before.head {
		t.Fatalf("preview changed HEAD: before=%q after=%q", before.head, head)
	}
	if after := cloneState(s); !reflect.DeepEqual(after, before.state) {
		t.Fatalf("preview changed state: before=%+v after=%+v", before.state, after)
	}
}

func assertPlanResultFields(t *testing.T, preview, actual *OpResult) {
	t.Helper()
	if preview.Branch != actual.Branch {
		t.Fatalf("Branch preview=%q actual=%q", preview.Branch, actual.Branch)
	}
	if !reflect.DeepEqual(preview.Restacked, actual.Restacked) {
		t.Fatalf("Restacked preview=%v actual=%v", preview.Restacked, actual.Restacked)
	}
	if !reflect.DeepEqual(preview.Deleted, actual.Deleted) {
		t.Fatalf("Deleted preview=%v actual=%v", preview.Deleted, actual.Deleted)
	}
}

func assertSameError(t *testing.T, want string, preview func() error, actual func() error) {
	t.Helper()
	previewErr := preview()
	actualErr := actual()
	for label, err := range map[string]error{"preview": previewErr, "actual": actualErr} {
		if err == nil {
			t.Fatalf("%s error is nil, want it to contain %q", label, want)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%s error = %v, want it to contain %q", label, err, want)
		}
	}
	if errors.Is(previewErr, ErrDirty) != errors.Is(actualErr, ErrDirty) {
		t.Fatalf("dirty sentinel mismatch: preview=%v actual=%v", previewErr, actualErr)
	}
}

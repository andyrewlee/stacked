package stack

import (
	"strings"
	"testing"
)

// setupCascade builds main -> feat-a where feat-a is checked out in a linked
// worktree, then advances main so feat-a needs a restack. It returns the fake
// git, the state, and the env, positioned with HEAD on main (the "main worktree"
// is where the restack runs from).
func setupCascade(t *testing.T) (*fakeGit, *State, Env) {
	t.Helper()
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	// feat-a now lives in its own worktree; the main worktree is back on main.
	f.addWorktree("/wt/feat-a", "feat-a")
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	// Advance main so feat-a is out of date.
	f.Add()
	if err := f.Commit("main moves", true); err != nil {
		t.Fatalf("commit on main: %v", err)
	}
	return f, s, env
}

func TestRestackCascadeRebasesOwnerWorktree(t *testing.T) {
	f, s, env := setupCascade(t)

	needs, err := s.NeedsRestack(f, "feat-a")
	if err != nil {
		t.Fatalf("NeedsRestack: %v", err)
	}
	if !needs {
		t.Fatal("feat-a should need a restack after main moved")
	}

	did, err := s.RestackBranch(env, "feat-a")
	if err != nil {
		t.Fatalf("RestackBranch (cross-worktree): %v", err)
	}
	if !did {
		t.Fatal("RestackBranch should have rebased feat-a in its worktree")
	}
	// The main worktree's HEAD must NOT have moved to feat-a (the rebase ran in
	// the owner worktree, git -C <path>).
	if cur, _ := f.CurrentBranch(); cur != "main" {
		t.Fatalf("main worktree HEAD = %q, want main (cross-worktree rebase must not move it)", cur)
	}
	// feat-a is now reconciled.
	needs, err = s.NeedsRestack(f, "feat-a")
	if err != nil {
		t.Fatalf("NeedsRestack after: %v", err)
	}
	if needs {
		t.Fatal("feat-a should be up to date after the cross-worktree restack")
	}
}

func TestRestackCascadeSkipsDirtyWorktree(t *testing.T) {
	f, s, env := setupCascade(t)
	f.markWorktreeDirty("feat-a")

	did, err := s.RestackBranch(env, "feat-a")
	if err != nil {
		t.Fatalf("RestackBranch with dirty owner: %v", err)
	}
	if did {
		t.Fatal("a dirty owner worktree must be skipped, not rebased")
	}
	if skipped := s.SkippedWorktrees(); len(skipped) != 1 || skipped[0] != "feat-a" {
		t.Fatalf("SkippedWorktrees = %v, want [feat-a]", skipped)
	}
	// feat-a is left needing a restack (never clobbered).
	needs, _ := s.NeedsRestack(f, "feat-a")
	if !needs {
		t.Fatal("skipped feat-a should still need a restack")
	}
}

func TestRestackCascadeConflictRollsBack(t *testing.T) {
	f, s, env := setupCascade(t)
	f.conflictOn("feat-a") // the owner-worktree rebase will conflict

	_, err := s.RestackBranch(env, "feat-a")
	if err == nil {
		t.Fatal("a cross-worktree conflict should surface an error")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("error should point at the worktree: %v", err)
	}
	// The owner worktree's rebase was aborted (not left paused).
	if inProgress, _ := f.RebaseInProgress(); inProgress {
		t.Fatal("the owner worktree's rebase should have been aborted, not left paused")
	}
}

// TestRestackCascadeMultiLevelMixedOwnership builds main -> a -> b -> c with the
// INTERMEDIATE branch b owned by another worktree and a, c local, advances main,
// and runs the real Restack (NOT RestackBranch). It pins the depth-1 gap: the
// whole stack must reconcile in topological order across the worktree boundary —
// a rebases in place onto the new main, b rebases IN its worktree onto the rebased
// a, c rebases in place onto the rebased b — and the main worktree's HEAD must end
// up back on main, never dragged into b's worktree.
func TestRestackCascadeMultiLevelMixedOwnership(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	// b lives in its own (clean) worktree; a and c stay local. The main worktree
	// is on main, where Restack runs from.
	f.addWorktree("/wt/b", "b")
	mustCheckout(t, f, "main")

	// Advance main so the whole stack is out of date.
	f.Add()
	if err := f.Commit("main moves", true); err != nil {
		t.Fatalf("commit on main: %v", err)
	}
	mainTip, _ := f.RevParse("main")

	// Sanity: only a is directly out of date yet (its parent main moved); b and c
	// become out of date transitively as the cascade rebases their parents.
	if needs, _ := s.NeedsRestack(f, "a"); !needs {
		t.Fatal("a should need a restack after main moved")
	}

	res, err := Restack(env, s)
	if err != nil {
		t.Fatalf("multi-level cross-worktree Restack: %v", err)
	}

	// The whole stack reconciled in order: a on main, b on a, c on b.
	aBranch, _ := s.Get("a")
	bBranch, _ := s.Get("b")
	cBranch, _ := s.Get("c")
	if aBranch.ParentSHA != mainTip {
		t.Fatalf("a.ParentSHA=%s, want new main tip %s", aBranch.ParentSHA, mainTip)
	}
	aTip, _ := f.RevParse("a")
	if bBranch.ParentSHA != aTip {
		t.Fatalf("b.ParentSHA=%s, want rebased a tip %s", bBranch.ParentSHA, aTip)
	}
	bTip, _ := f.RevParse("b")
	if cBranch.ParentSHA != bTip {
		t.Fatalf("c.ParentSHA=%s, want rebased b tip %s", cBranch.ParentSHA, bTip)
	}

	// Chain integrity across the worktree boundary: c descends from the rebased b,
	// which descends from the rebased a, which descends from the new main.
	if !mustFakeIsAncestor(t, f, aTip, "b") {
		t.Fatal("rebased b does not contain the rebased a")
	}
	if !mustFakeIsAncestor(t, f, bTip, "c") {
		t.Fatal("rebased c does not contain the rebased b")
	}
	if !mustFakeIsAncestor(t, f, mainTip, "c") {
		t.Fatal("rebased c does not descend from the new main")
	}

	// Nothing is left needing a restack.
	for _, name := range []string{"a", "b", "c"} {
		if needs, _ := s.NeedsRestack(f, name); needs {
			t.Fatalf("%q still needs a restack after the multi-level cascade", name)
		}
	}
	// All three were rebased and reported.
	for _, name := range []string{"a", "b", "c"} {
		if !sliceContains(res.Restacked, name) {
			t.Fatalf("Restacked=%v, want it to include %q", res.Restacked, name)
		}
	}

	// The main worktree's HEAD ended up back on main (never dragged into b's
	// worktree by the cross-worktree rebase).
	if cur, _ := f.CurrentBranch(); cur != "main" {
		t.Fatalf("main worktree HEAD=%q, want main", cur)
	}

	// And a second Restack is idempotent.
	mustCheckout(t, f, "main")
	if res2, err := Restack(env, s); err != nil {
		t.Fatalf("idempotence Restack: %v", err)
	} else if len(res2.Restacked) != 0 {
		t.Fatalf("multi-level cascade not idempotent, rebased %v", res2.Restacked)
	}
}

// sliceContains is a tiny local helper (the fake-git engine tests avoid pulling
// in generics-heavy stdlib helpers for one membership check).
func sliceContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestModifyCascadesIntoDependentWorktree proves the cross-worktree cascade is
// reached through Modify (not just Restack): amending a base branch whose
// dependent lives in another worktree must rebase that dependent IN its worktree
// via Modify's finishUpstack epilogue, and restore the current worktree's HEAD to
// the amended branch (never the dependent).
func TestModifyCascadesIntoDependentWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	// b lives in its own (clean) worktree; we work on a in the main worktree.
	f.addWorktree("/wt/b", "b")
	mustCheckout(t, f, "a")
	bBefore, _ := f.RevParse("b")

	// Amend a: its commit is rewritten, so b (in its worktree) must rebase onto a's
	// new tip.
	res, err := Modify(env, s, "", true, false)
	if err != nil {
		t.Fatalf("Modify with a cross-worktree dependent: %v", err)
	}
	if res.Branch != "a" {
		t.Fatalf("Modify result Branch=%q, want a", res.Branch)
	}
	if !sliceContains(res.Restacked, "b") {
		t.Fatalf("Modify Restacked=%v, want it to include the cross-worktree dependent b", res.Restacked)
	}

	// b was actually rebased in its worktree (new tip) onto a's new tip.
	if bAfter, _ := f.RevParse("b"); bAfter == bBefore {
		t.Fatal("b's tip unchanged; the cross-worktree dependent did not rebase")
	}
	aTip, _ := f.RevParse("a")
	if bBranch, _ := s.Get("b"); bBranch.ParentSHA != aTip {
		t.Fatalf("b.ParentSHA=%s, want amended a tip %s", bBranch.ParentSHA, aTip)
	}
	if !mustFakeIsAncestor(t, f, aTip, "b") {
		t.Fatal("rebased b does not contain the amended a")
	}
	if needs, _ := s.NeedsRestack(f, "b"); needs {
		t.Fatal("b should be reconciled after the cross-worktree cascade")
	}

	// Modify's epilogue restored the current worktree's HEAD to a (the amended
	// branch), never into b's worktree.
	if cur, _ := f.CurrentBranch(); cur != "a" {
		t.Fatalf("current worktree HEAD=%q, want a (the dependent's rebase ran in its worktree)", cur)
	}
}

// TestRestackInPlaceWhenBranchIsCurrent confirms that a branch checked out in
// the CURRENT (main) worktree still rebases in place even in a multi-worktree
// repo — the cascade only diverts branches owned elsewhere.
func TestRestackInPlaceWhenBranchIsCurrent(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "feat-a")
	// Register a second, unrelated worktree so the repo is multi-worktree, but
	// keep feat-a checked out HERE.
	f.addWorktree("/wt/other", "other")
	f.branches["other"] = f.branches["main"]
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}
	// Advance main via a detour: go to main, commit, return to feat-a.
	if err := f.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	f.Add()
	if err := f.Commit("main moves", true); err != nil {
		t.Fatal(err)
	}
	if err := f.Checkout("feat-a"); err != nil {
		t.Fatal(err)
	}

	did, err := s.RestackBranch(env, "feat-a")
	if err != nil {
		t.Fatalf("in-place RestackBranch: %v", err)
	}
	if !did {
		t.Fatal("feat-a should rebase in place")
	}
}

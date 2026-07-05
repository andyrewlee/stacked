package stack

import (
	"strings"
	"testing"
)

// These tests pin the worktree-aware lifecycle: Delete, Fold, and PruneMerged
// must tear down a CLEAN worktree owning the branch they remove (git refuses to
// delete a branch checked out elsewhere) but must REFUSE — changing nothing —
// when that worktree is dirty, so in-progress work is never silently discarded.

// ownsWorktree reports whether the fake still has a linked worktree for branch.
func ownsWorktree(f *fakeGit, branch string) bool {
	_, ok := f.linkedWorktrees[branch]
	return ok
}

func TestDeleteRemovesCleanOwnedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")
	bTip, _ := f.RevParse("b")

	// b lives in its own (clean) worktree; the current worktree is on main.
	f.addWorktree("/wt/b", "b")
	mustCheckout(t, f, "main")

	res, err := Delete(env, s, "b", true)
	if err != nil {
		t.Fatalf("delete with clean owned worktree: %v", err)
	}
	if ownsWorktree(f, "b") {
		t.Fatal("b's worktree should have been removed before deleting the branch")
	}
	if f.BranchExists("b") {
		t.Fatal("b's git branch should be deleted")
	}
	if s.IsTracked("b") {
		t.Fatal("b should be untracked after delete")
	}
	if cb, _ := s.Get("c"); cb.Parent != "a" {
		t.Fatalf("c parent=%q, want a (re-parented onto b's parent)", cb.Parent)
	}
	if len(res.Restacked) != 1 || res.Restacked[0] != "c" {
		t.Fatalf("delete Restacked=%v, want [c]", res.Restacked)
	}
	if mustFakeIsAncestor(t, f, bTip, "c") {
		t.Fatal("c still contains deleted b's commit")
	}
	checkInvariants(t, f, s, 0)
}

func TestDeleteRefusesUnmergedOwnedWorktreeWithoutRemovingIt(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	bTip, err := f.RevParse("b")
	if err != nil {
		t.Fatalf("rev-parse b before delete: %v", err)
	}
	cTip, err := f.RevParse("c")
	if err != nil {
		t.Fatalf("rev-parse c before delete: %v", err)
	}
	beforeB, ok := s.Get("b")
	if !ok {
		t.Fatal("b should be tracked before delete")
	}
	beforeBCopy := *beforeB
	beforeC, ok := s.Get("c")
	if !ok {
		t.Fatal("c should be tracked before delete")
	}
	beforeCCopy := *beforeC

	f.addWorktree("/wt/b", "b")
	mustCheckout(t, f, "main")

	_, err = Delete(env, s, "b", false)
	if err == nil {
		t.Fatal("non-force delete of an unmerged branch must error")
	}
	if !strings.Contains(err.Error(), "not merged into its stack parent") {
		t.Fatalf("error should explain unmerged stack parent: %v", err)
	}
	if !ownsWorktree(f, "b") {
		t.Fatal("failed non-force delete must leave b's worktree intact")
	}
	if got, err := f.RevParse("b"); err != nil || got != bTip {
		t.Fatalf("b tip after failed delete = %q, %v; want %q, nil", got, err, bTip)
	}
	if got, err := f.RevParse("c"); err != nil || got != cTip {
		t.Fatalf("c tip after failed delete = %q, %v; want %q, nil", got, err, cTip)
	}
	afterB, ok := s.Get("b")
	if !ok {
		t.Fatal("b should remain tracked after failed delete")
	}
	if *afterB != beforeBCopy {
		t.Fatalf("b stack state changed: got %+v, want %+v", *afterB, beforeBCopy)
	}
	afterC, ok := s.Get("c")
	if !ok {
		t.Fatal("c should remain tracked after failed delete")
	}
	if *afterC != beforeCCopy {
		t.Fatalf("c stack state changed: got %+v, want %+v", *afterC, beforeCCopy)
	}
}

func TestDeleteRefusesDirtyOwnedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")
	mkBranch(t, env, s, f, "b", "c")

	f.addWorktree("/wt/b", "b")
	f.markWorktreeDirty("b")
	mustCheckout(t, f, "main")

	_, err := Delete(env, s, "b", true)
	if err == nil {
		t.Fatal("deleting a branch whose worktree is dirty must error")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("error should point at the worktree: %v", err)
	}
	// Nothing changed: the worktree, the branch, and the tracking all remain.
	if !ownsWorktree(f, "b") {
		t.Fatal("a dirty worktree must NOT be removed")
	}
	if !f.BranchExists("b") {
		t.Fatal("b must not be deleted when its worktree is dirty")
	}
	if !s.IsTracked("b") {
		t.Fatal("b must stay tracked when its worktree is dirty")
	}
	if cb, _ := s.Get("c"); cb.Parent != "b" {
		t.Fatalf("c parent=%q, want b (nothing should have moved)", cb.Parent)
	}
}

// Fold deletes the CURRENT branch, which git only lets you do when it is checked
// out here — so the folded branch can never itself live in another worktree (a
// branch is checked out in at most one worktree). The worktree-relevant Fold case
// is therefore a folded branch whose CHILD is owned elsewhere: folding re-parents
// that child and must restack it IN its worktree, not move the main HEAD.
func TestFoldCascadesIntoChildWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b") // b is the branch we'll fold (into a)
	mkBranch(t, env, s, f, "b", "c") // c is b's child, owned by another worktree

	f.addWorktree("/wt/c", "c")
	mustCheckout(t, f, "b")

	res, err := Fold(env, s)
	if err != nil {
		t.Fatalf("fold cascading into a child worktree: %v", err)
	}
	if !f.BranchExists("a") || f.BranchExists("b") {
		t.Fatal("fold should advance a and delete b")
	}
	if cb, _ := s.Get("c"); cb.Parent != "a" {
		t.Fatalf("c parent=%q, want a (re-parented onto the fold target)", cb.Parent)
	}
	_ = res
	// The main worktree's HEAD is back on the fold target a, never moved into c's
	// worktree — folding a parent must never drag the main HEAD across worktrees.
	if cur, _ := f.CurrentBranch(); cur != "a" {
		t.Fatalf("main worktree HEAD=%q, want a (fold must not move HEAD into c's worktree)", cur)
	}
	// Fold deletes only the folded branch (b); c's worktree is left intact.
	if !ownsWorktree(f, "c") {
		t.Fatal("c's worktree should be left intact by fold (only b is removed)")
	}
	// The stack reconciles fully, and a follow-up restack — which must drive c's
	// rebase through its worktree, never moving the main HEAD — is invariant-clean
	// and idempotent.
	f.head = "main"
	if _, err := Restack(env, s); err != nil {
		t.Fatalf("restack after fold: %v", err)
	}
	if cur, _ := f.CurrentBranch(); cur != "main" {
		t.Fatalf("main worktree HEAD=%q after restack, want main", cur)
	}
	checkInvariants(t, f, s, 0)
}

func TestPruneMergedRemovesCleanOwnedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	// Simulate a having merged into the trunk: advance main to a's tip (from b, so
	// main is not the current branch — git/the fake forbids forcing the current).
	aTip, _ := f.RevParse("a")
	mustCheckout(t, f, "b")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatalf("force trunk to a: %v", err)
	}
	// a lives in its own clean worktree; pruning it must tear that down first.
	f.addWorktree("/wt/a", "a")

	deleted, err := PruneMerged(env, s)
	if err != nil {
		t.Fatalf("prune with clean owned worktree: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "a" {
		t.Fatalf("PruneMerged deleted=%v, want [a]", deleted)
	}
	if ownsWorktree(f, "a") {
		t.Fatal("a's worktree should have been removed during prune")
	}
	if f.BranchExists("a") || s.IsTracked("a") {
		t.Fatal("a should be deleted and untracked after prune")
	}
	if bb, _ := s.Get("b"); bb.Parent != "main" {
		t.Fatalf("b parent=%q, want main (re-parented onto a's parent)", bb.Parent)
	}
	// Reconcile and assert invariants on the survivor.
	f.head = "main"
	if _, err := Restack(env, s); err != nil {
		t.Fatalf("restack after prune: %v", err)
	}
	checkInvariants(t, f, s, 0)
}

func TestPruneMergedRefusesDirtyOwnedWorktree(t *testing.T) {
	f, s, env := newEnvState()
	mkBranch(t, env, s, f, "main", "a")
	mkBranch(t, env, s, f, "a", "b")

	aTip, _ := f.RevParse("a")
	mustCheckout(t, f, "b")
	if err := f.ForceBranch("main", aTip); err != nil {
		t.Fatalf("force trunk to a: %v", err)
	}
	f.addWorktree("/wt/a", "a")
	f.markWorktreeDirty("a")

	deleted, err := PruneMerged(env, s)
	if err == nil {
		t.Fatal("pruning a merged branch whose worktree is dirty must error")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("error should point at the worktree: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("nothing should have been deleted, got %v", deleted)
	}
	if !ownsWorktree(f, "a") {
		t.Fatal("a dirty worktree must NOT be removed")
	}
	if !f.BranchExists("a") || !s.IsTracked("a") {
		t.Fatal("a must remain when its worktree is dirty")
	}
}

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stacked/internal/git"
	"stacked/internal/stack"
)

// Stack-mutating commands over real git: track/untrack, restack, abort,
// repair, undo and the undo journal protocol, sync, and init.
// --- track / untrack -------------------------------------------------------

func TestTrackAndUntrack(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Create a plain git branch off feat-a, then track it explicitly.
	mustRun(t, "git", "checkout", "-q", "-b", "feat-b")
	write(t, "b.txt", "b\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "b")

	if err := runTrack([]string{"--parent", "feat-a"}); err != nil {
		t.Fatalf("track: %v", err)
	}
	if b, _ := stateT(t).Get("feat-b"); b == nil || b.Parent != "feat-a" {
		t.Fatalf("feat-b not tracked with parent feat-a: %+v", b)
	}

	// Untrack feat-a; feat-b should be re-parented onto main (feat-a's parent).
	if err := runUntrack([]string{"feat-a"}); err != nil {
		t.Fatalf("untrack: %v", err)
	}
	s := stateT(t)
	if s.IsTracked("feat-a") {
		t.Fatalf("feat-a still tracked after untrack")
	}
	if b, _ := s.Get("feat-b"); b == nil || b.Parent != "main" {
		t.Fatalf("feat-b not re-parented onto main: %+v", b)
	}
}

func TestTrackInferParent(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	mustRun(t, "git", "checkout", "-q", "-b", "feat-b")
	write(t, "b.txt", "b\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "b")

	// No --parent: inferParent should pick feat-a (closest ancestor) over main.
	if err := runTrack(nil); err != nil {
		t.Fatalf("track (infer): %v", err)
	}
	if b, _ := stateT(t).Get("feat-b"); b == nil || b.Parent != "feat-a" {
		t.Fatalf("inferred parent wrong: %+v", b)
	}
}

func TestTrackGuards(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Tracking the trunk is rejected.
	mustCheckout(t, "main")
	if err := runTrack(nil); err == nil {
		t.Fatalf("expected error tracking the trunk")
	}

	// Tracking an already-tracked branch is rejected.
	mustCheckout(t, "feat-a")
	if err := runTrack(nil); err == nil {
		t.Fatalf("expected error tracking an already-tracked branch")
	}

	// Bad explicit parent is rejected.
	mustRun(t, "git", "checkout", "-q", "-b", "feat-c")
	write(t, "c.txt", "c\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "c")
	if err := runTrack([]string{"--parent", "no-such-branch"}); err == nil {
		t.Fatalf("expected error for unknown parent")
	}
	if err := runTrack([]string{"--parent", "feat-c"}); err == nil {
		t.Fatalf("expected error for self-parent")
	}
}

func TestUntrackGuards(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	if err := runUntrack([]string{"main"}); err == nil {
		t.Fatalf("expected error untracking the trunk")
	}
	if err := runUntrack([]string{"ghost"}); err == nil {
		t.Fatalf("expected error untracking an unknown branch")
	}
	if err := runUntrack([]string{"a", "b"}); err == nil {
		t.Fatalf("expected error for too many args")
	}
}

// --- restack ---------------------------------------------------------------

func TestRestackUpToDate(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustCheckout(t, "feat-b")

	out := captureStdout(t, func() {
		if err := runRestack(nil); err != nil {
			t.Fatalf("restack: %v", err)
		}
	})
	if !strings.Contains(out, "up to date") {
		t.Fatalf("restack on clean stack should say up to date, got:\n%s", out)
	}
}

func TestRestackAfterParentMoves(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "g.txt", "B\n", "b") // independent file: no conflict

	// Advance feat-a by a raw commit (no auto-restack), so feat-b drifts.
	mustCheckout(t, "feat-a")
	write(t, "f.txt", "A2\n")
	mustRun(t, "git", "commit", "-aqm", "a2")

	// From the trunk, restack should rebase the whole stack and report feat-b.
	mustCheckout(t, "main")
	out := captureStdout(t, func() {
		if err := runRestack(nil); err != nil {
			t.Fatalf("restack: %v", err)
		}
	})
	if !strings.Contains(out, "restacked") || !strings.Contains(out, "feat-b") {
		t.Fatalf("restack should report feat-b restacked, got:\n%s", out)
	}
	if got := mustRun(t, "git", "show", "feat-b:f.txt"); got != "A2" {
		t.Fatalf("feat-b not rebased onto advanced feat-a: f.txt = %q", got)
	}
}

func TestRestackDirtyGuard(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	write(t, "a.txt", "dirty\n")
	if err := runRestack(nil); err == nil {
		t.Fatalf("expected restack to refuse a dirty working tree")
	}
}

// --- abort -----------------------------------------------------------------

func TestAbortNoRebase(t *testing.T) {
	newRepo(t)
	mustInit(t)
	if err := runAbort(nil); err == nil {
		t.Fatalf("expected error: no rebase in progress")
	}
}

func TestAbortRestoresState(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	write(t, "f.txt", "A\nB\n")
	if err := runCreate([]string{"feat-b", "-a", "-m", "b"}); err != nil {
		t.Fatal(err)
	}
	mustCheckout(t, "feat-a")

	// Force a conflict via modify's auto-restack, then abort.
	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatalf("expected a conflict")
	}
	if inProgress, _ := git.RebaseInProgress(); !inProgress {
		t.Fatalf("expected a rebase in progress")
	}
	s := stateT(t)
	s.PendingReparent = &stack.PendingReparent{Branch: "feat-b", Parent: "main", ParentSHA: "pending"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := runAbort(nil); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if inProgress, _ := git.RebaseInProgress(); inProgress {
		t.Fatalf("rebase still in progress after abort")
	}
	if s := stateT(t); s.PendingReparent != nil {
		t.Fatalf("pending reparent after abort = %+v, want nil", s.PendingReparent)
	}
}

func TestUndoRejectsActiveRebase(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	write(t, "f.txt", "A\nB\n")
	if err := runCreate([]string{"feat-b", "-a", "-m", "b"}); err != nil {
		t.Fatal(err)
	}
	mustCheckout(t, "feat-a")

	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatalf("expected a conflict")
	}
	if err := runUndo(nil); err == nil {
		t.Fatal("undo succeeded while rebase was active")
	}
	if _, ok, err := stack.PeekUndo(); err != nil || !ok {
		t.Fatalf("undo entry after rejected undo = ok %v err %v, want preserved entry", ok, err)
	}
}

func TestContinueKeepsOriginalUndoEntry(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	write(t, "f.txt", "A\nB\n")
	if err := runCreate([]string{"feat-b", "-a", "-m", "b"}); err != nil {
		t.Fatal(err)
	}
	mustCheckout(t, "feat-a")

	write(t, "f.txt", "X\n")
	if err := runModify([]string{"-a"}); err == nil {
		t.Fatalf("expected a conflict")
	}
	entry, ok, err := stack.PeekUndo()
	if err != nil || !ok {
		t.Fatalf("peek undo after conflict = ok %v err %v", ok, err)
	}
	if entry.Label != "modify" {
		t.Fatalf("undo label after conflict = %q, want modify", entry.Label)
	}

	write(t, "f.txt", "X\nB\n")
	mustRun(t, "git", "add", "f.txt")
	if err := runContinue(nil); err != nil {
		t.Fatalf("continue: %v", err)
	}
	entry, ok, err = stack.PeekUndo()
	if err != nil || !ok {
		t.Fatalf("peek undo after continue = ok %v err %v", ok, err)
	}
	if entry.Label != "modify" {
		t.Fatalf("undo label after continue = %q, want modify", entry.Label)
	}
}

// --- repair / undo ---------------------------------------------------------

func TestRepairInvalidParentUsesMergeBase(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	base, _ := git.MergeBase("main", "feat-a")
	mustCheckout(t, "main")
	write(t, "advance.txt", "advance\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "advance main")

	s := stateT(t)
	a, _ := s.Get("feat-a")
	a.Parent = "missing-parent"
	a.ParentSHA = "missing"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	if err := runRepair(nil); err != nil {
		t.Fatalf("repair: %v", err)
	}
	a, _ = stateT(t).Get("feat-a")
	if a.Parent != "main" || a.ParentSHA != base {
		t.Fatalf("repaired feat-a = (%s, %s), want (main, %s)", a.Parent, a.ParentSHA, base)
	}
}

func TestRepairMissingParentPreservesChildBase(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	before := stateT(t)
	childBefore, _ := before.Get("feat-b")
	oldBase := childBefore.ParentSHA
	mustCheckout(t, "main")
	mustRun(t, "git", "branch", "-D", "feat-a")

	if err := runRepair(nil); err != nil {
		t.Fatalf("repair: %v", err)
	}
	child, _ := stateT(t).Get("feat-b")
	if child.Parent != "main" || child.ParentSHA != oldBase {
		t.Fatalf("repaired feat-b = (%s, %s), want (main, %s)", child.Parent, child.ParentSHA, oldBase)
	}
}

func TestUndoDeletesCreatedBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo create: %v", err)
	}
	if git.BranchExists("feat-b") {
		t.Fatal("undo left created branch feat-b behind")
	}
	if stateT(t).IsTracked("feat-b") {
		t.Fatal("undo left feat-b tracked")
	}
	if !stateT(t).IsTracked("feat-a") {
		t.Fatal("undo removed parent branch feat-a from state")
	}
	if got := curBranch(t); got != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a", got)
	}
}

func TestUndoKeepsUnrelatedBranchCreatedAfterSnapshot(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")
	mustRun(t, "git", "branch", "scratch")

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo create: %v", err)
	}
	if git.BranchExists("feat-b") {
		t.Fatal("undo left created branch feat-b behind")
	}
	if !git.BranchExists("scratch") {
		t.Fatal("undo deleted unrelated branch scratch")
	}
}

func TestUndoDeleteCurrentRestoresCheckout(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	if err := runDelete([]string{"feat-b", "--force"}); err != nil {
		t.Fatalf("delete current branch: %v", err)
	}
	if got := curBranch(t); got != "feat-a" {
		t.Fatalf("after delete HEAD = %q, want feat-a", got)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo delete: %v", err)
	}
	if got := curBranch(t); got != "feat-b" {
		t.Fatalf("after undo HEAD = %q, want feat-b", got)
	}
	if !git.BranchExists("feat-b") {
		t.Fatal("undo did not restore deleted branch")
	}
}

func TestUndoCreateAfterModifyUndoPreservesDirtyWorktree(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	write(t, "a.txt", "a-modified\n")
	if err := runModify([]string{"-a"}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if err := runUndo(nil); err != nil {
		t.Fatalf("undo modify: %v", err)
	}
	if err := runUndo(nil); err != nil {
		t.Fatalf("undo create with dirty worktree: %v", err)
	}
	if stateT(t).IsTracked("feat-a") {
		t.Fatal("undo create left feat-a tracked")
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo create left feat-a branch behind")
	}
	if got, err := os.ReadFile("a.txt"); err != nil || string(got) != "a-modified\n" {
		t.Fatalf("worktree a.txt = %q err %v, want preserved modification", got, err)
	}
}

func TestUndoFoldCurrentRestoresCheckout(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCreate(t, "feat-b", "b.txt", "b\n", "b")

	if err := runFold(nil); err != nil {
		t.Fatalf("fold current branch: %v", err)
	}
	if got := curBranch(t); got != "feat-a" {
		t.Fatalf("after fold HEAD = %q, want feat-a", got)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo fold: %v", err)
	}
	if got := curBranch(t); got != "feat-b" {
		t.Fatalf("after undo HEAD = %q, want feat-b", got)
	}
	if !git.BranchExists("feat-b") {
		t.Fatal("undo did not restore folded branch")
	}
}

func TestUndoTrackKeepsExistingBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "checkout", "-q", "-b", "loose")
	write(t, "loose.txt", "loose\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "loose")
	if err := runTrack(nil); err != nil {
		t.Fatalf("track loose: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo track: %v", err)
	}
	if !git.BranchExists("loose") {
		t.Fatal("undo track deleted pre-existing branch loose")
	}
	if stateT(t).IsTracked("loose") {
		t.Fatal("undo track left loose tracked")
	}
	if got := curBranch(t); got != "loose" {
		t.Fatalf("HEAD = %q, want loose", got)
	}
}

func TestUndoDeletesRenamedTrunkBranch(t *testing.T) {
	newRepo(t)
	mustInit(t)
	if err := runRename([]string{"master"}); err != nil {
		t.Fatalf("rename trunk: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo rename trunk: %v", err)
	}
	if git.BranchExists("master") {
		t.Fatal("undo left renamed trunk branch master behind")
	}
	if !git.BranchExists("main") {
		t.Fatal("undo did not restore main")
	}
	if stateT(t).Trunk != "main" {
		t.Fatalf("trunk = %q, want main", stateT(t).Trunk)
	}
	if got := curBranch(t); got != "main" {
		t.Fatalf("HEAD = %q, want main", got)
	}
}

func TestUndoCurrentBranchRenameChecksOutRestoredName(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runRename([]string{"renamed"}); err != nil {
		t.Fatalf("rename current branch: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo rename: %v", err)
	}
	if git.BranchExists("renamed") {
		t.Fatal("undo left renamed branch behind")
	}
	if !git.BranchExists("feat-a") {
		t.Fatal("undo did not restore feat-a")
	}
	if got := curBranch(t); got != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a", got)
	}
}

func TestUndoDeletesPartialRenameWhenStateNotSaved(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	s := stateT(t)
	if err := s.RecordUndo(gitShell, "rename"); err != nil {
		t.Fatalf("record undo: %v", err)
	}
	if err := git.RenameBranch("feat-a", "renamed"); err != nil {
		t.Fatalf("rename git branch: %v", err)
	}
	if err := stack.SetLastUndoCreatedBranches([]string{"renamed"}); err != nil {
		t.Fatalf("record created branch: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo partial rename: %v", err)
	}
	if git.BranchExists("renamed") {
		t.Fatal("undo left partially renamed branch behind")
	}
	if !git.BranchExists("feat-a") {
		t.Fatal("undo did not restore original branch")
	}
	if !stateT(t).IsTracked("feat-a") {
		t.Fatal("undo did not keep original branch tracked")
	}
	if got := curBranch(t); got != "feat-a" {
		t.Fatalf("HEAD = %q, want feat-a", got)
	}
}

func TestUndoRestoresSnapshotWhenCurrentStateIsMalformed(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "stacked", "state.json"), []byte("{bad json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo with malformed current state: %v", err)
	}
	if stateT(t).IsTracked("feat-a") {
		t.Fatal("undo did not restore the previous state snapshot")
	}
}

func TestFailedMutationDoesNotReplacePreviousUndo(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")
	if err := runModify(nil); err == nil {
		t.Fatal("modify on trunk should fail")
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after failed modify: %v", err)
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo did not remove branch from the last successful create")
	}
	if stateT(t).IsTracked("feat-a") {
		t.Fatal("undo did not restore state from before the last successful create")
	}
}

func TestSuccessfulNoopMutationDoesNotReplacePreviousUndo(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runRestack(nil); err != nil {
		t.Fatalf("noop restack: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after noop restack: %v", err)
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo did not remove branch from the last successful create")
	}
}

func TestSuccessfulDirectNoopMutationDoesNotReplacePreviousUndo(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runSync(nil); err != nil {
		t.Fatalf("noop sync: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after noop sync: %v", err)
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo did not remove branch from the last successful create")
	}
}

func TestFailedDirectMutationDoesNotReplacePreviousUndo(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runContinue(nil); err == nil {
		t.Fatal("continue without a rebase should fail")
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after failed continue: %v", err)
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo did not remove branch from the last successful create")
	}
}

func TestFailedConflictNoopDoesNotReplacePreviousUndo(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	s := stateT(t)
	if err := s.RecordUndo(gitShell, "continue"); err != nil {
		t.Fatalf("record undo: %v", err)
	}
	if err := stack.CleanupUndoOnError(gitShell, s, stack.ErrConflict); err != nil {
		t.Fatalf("cleanup failed conflict: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after failed conflict: %v", err)
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo did not remove branch from the last successful create")
	}
}

func TestNoopRepairDoesNotReplacePreviousUndo(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	if err := runRepair(nil); err != nil {
		t.Fatalf("noop repair: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after noop repair: %v", err)
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo did not remove branch from the last successful create")
	}
}

func TestNoArgMutatorsRejectPositionalArgs(t *testing.T) {
	tests := []struct {
		name string
		run  func([]string) error
	}{
		{"abort", runAbort},
		{"bottom", runBottom},
		{"continue", runContinue},
		{"fold", runFold},
		{"repair", runRepair},
		{"restack", runRestack},
		{"squash", runSquash},
		{"top", runTop},
		{"track", runTrack},
		{"undo", runUndo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run([]string{"unexpected"}); err == nil {
				t.Fatalf("%s accepted a positional argument", tt.name)
			}
		})
	}
}

// --- sync with a real remote ----------------------------------------------

func TestSyncDryRunDoesNotFetch(t *testing.T) {
	newRepo(t)
	mustInit(t)

	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustRun(t, "git", "push", "-q", "-u", "origin", "main")
	mustRun(t, "git", "--git-dir", remoteDir, "symbolic-ref", "HEAD", "refs/heads/main")

	clone := t.TempDir()
	mustRun(t, "git", "clone", "-q", remoteDir, clone)
	mustRun(t, "git", "-C", clone, "config", "user.email", "t@e.com")
	mustRun(t, "git", "-C", clone, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(clone, "up.txt"), []byte("up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "git", "-C", clone, "add", "-A")
	mustRun(t, "git", "-C", clone, "commit", "-q", "-m", "upstream advance")
	mustRun(t, "git", "-C", clone, "push", "-q", "origin", "main")
	mustRun(t, "git", "update-ref", "-d", "refs/remotes/origin/main")
	if _, err := git.RevParse("refs/remotes/origin/main"); err == nil {
		t.Fatal("test setup unexpectedly left origin/main fetched")
	}

	if err := runSync([]string{"--dry-run"}); err != nil {
		t.Fatalf("sync --dry-run: %v", err)
	}
	if _, err := git.RevParse("refs/remotes/origin/main"); err == nil {
		t.Fatal("sync --dry-run fetched origin/main")
	}
}

func TestSyncFastForwardsTrunk(t *testing.T) {
	newRepo(t)
	mustInit(t)

	// Set up a bare origin and publish main.
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustRun(t, "git", "push", "-q", "-u", "origin", "main")
	mustRun(t, "git", "--git-dir", remoteDir, "symbolic-ref", "HEAD", "refs/heads/main")

	// Build a stack on the local main.
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Advance origin/main from a separate clone so the local trunk is behind.
	clone := t.TempDir()
	mustRun(t, "git", "clone", "-q", remoteDir, clone)
	mustRun(t, "git", "-C", clone, "config", "user.email", "t@e.com")
	mustRun(t, "git", "-C", clone, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(clone, "up.txt"), []byte("up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "git", "-C", clone, "add", "-A")
	mustRun(t, "git", "-C", clone, "commit", "-q", "-m", "upstream advance")
	mustRun(t, "git", "-C", clone, "push", "-q", "origin", "main")

	// Sync: fetch + fast-forward trunk, then restack feat-a onto the new tip.
	mustCheckout(t, "feat-a")
	out := captureStdout(t, func() {
		if err := runSync(nil); err != nil {
			t.Fatalf("sync: %v", err)
		}
	})
	if !strings.Contains(out, "fast-forwarded") {
		t.Fatalf("sync should report a fast-forward, got:\n%s", out)
	}
	// The upstream file must now be present on the local trunk and on feat-a.
	if !hasFile("main", "up.txt") {
		t.Fatalf("trunk did not fast-forward to include up.txt")
	}
	if !hasFile("feat-a", "up.txt") {
		t.Fatalf("feat-a was not restacked onto the advanced trunk")
	}
}

// --- detectTrunk -----------------------------------------------------------

func TestDetectTrunkFallsBackToCurrentBranch(t *testing.T) {
	newRepo(t) // current branch is "main", no origin/HEAD configured
	if got := detectTrunk(); got != "main" {
		t.Fatalf("detectTrunk = %q, want main (current branch)", got)
	}
}

// --- init guards -----------------------------------------------------------

func TestInitAlreadyInitialized(t *testing.T) {
	newRepo(t)
	mustInit(t)
	out := captureStdout(t, func() {
		if err := runInit(nil); err != nil {
			t.Fatalf("second init: %v", err)
		}
	})
	if !strings.Contains(out, "already initialized") {
		t.Fatalf("re-init should report already initialized, got:\n%s", out)
	}
}

func TestInitRejectsMissingTrunk(t *testing.T) {
	newRepo(t)
	if err := runInit([]string{"--trunk", "mian"}); err == nil {
		t.Fatal("init accepted a missing trunk branch")
	}
	if _, err := stack.Load(); !errors.Is(err, stack.ErrNotInitialized) {
		t.Fatalf("state after failed init = %v, want ErrNotInitialized", err)
	}
}

func TestInitRejectsPositionalArgs(t *testing.T) {
	newRepo(t)
	if err := runInit([]string{"typo", "--trunk", "main"}); err == nil {
		t.Fatal("init accepted a positional argument")
	}
	if _, err := stack.Load(); !errors.Is(err, stack.ErrNotInitialized) {
		t.Fatalf("state after failed init = %v, want ErrNotInitialized", err)
	}
}

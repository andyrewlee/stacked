package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
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

func TestTrackNamedArg(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// A plain git branch forked off feat-a, created without switching to it.
	mustRun(t, "git", "branch", "feat-b", "feat-a")
	mustCheckout(t, "main")

	if err := runTrack([]string{"feat-b"}); err != nil {
		t.Fatalf("track feat-b: %v", err)
	}
	if b, _ := stateT(t).Get("feat-b"); b == nil || b.Parent != "feat-a" {
		t.Fatalf("feat-b not tracked with parent feat-a: %+v", b)
	}
	if cur := curBranch(t); cur != "main" {
		t.Fatalf("track moved HEAD to %q", cur)
	}

	// A flag after the positional still parses.
	mustRun(t, "git", "branch", "feat-c", "main")
	if err := runTrack([]string{"feat-c", "--parent", "main"}); err != nil {
		t.Fatalf("track feat-c --parent main: %v", err)
	}
	if c, _ := stateT(t).Get("feat-c"); c == nil || c.Parent != "main" {
		t.Fatalf("feat-c not tracked with parent main: %+v", c)
	}

	// Refusals: unknown branch and too many positionals.
	if err := runTrack([]string{"ghost"}); err == nil {
		t.Fatalf("expected error tracking an unknown branch")
	}
	if err := runTrack([]string{"a", "b"}); err == nil {
		t.Fatalf("expected error for too many args")
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

// TestUndoRefFailureRetainsEntryAndState proves the real-Git retry contract: a
// ref lock makes the snapshot-ref transaction fail atomically, so undo leaves
// post-operation refs, state, and journal bytes untouched and keeps the
// entry — and once the lock clears, retrying that same entry restores the
// recorded refs and metadata and drops exactly that entry.
func TestUndoRefFailureRetainsEntryAndState(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "f.txt", "A\n", "a")
	mustCreate(t, "feat-b", "f.txt", "A\nB\n", "b")
	mustCheckout(t, "feat-a")

	// A modify-style entry: no created branches, refs = pre-op tips. The
	// amend touches a file feat-b does not, so the cascade restack is clean.
	write(t, "a2.txt", "C\n")
	mustRun(t, "git", "add", "a2.txt")
	if err := runModify(nil); err != nil {
		t.Fatalf("modify: %v", err)
	}
	newest, ok, err := stack.PeekUndo()
	if err != nil || !ok {
		t.Fatalf("PeekUndo: ok=%v err=%v", ok, err)
	}
	if newest.Label != "modify" {
		t.Fatalf("newest undo entry = %q, want the modify", newest.Label)
	}
	postA := mustRun(t, "git", "rev-parse", "feat-a")
	postB := mustRun(t, "git", "rev-parse", "feat-b")
	postState, err := os.ReadFile(".git/stacked/state.json")
	if err != nil {
		t.Fatal(err)
	}
	postUndo, err := os.ReadFile(".git/stacked/undo.json")
	if err != nil {
		t.Fatal(err)
	}

	// Force the ref transaction to fail: a .lock file on one recorded ref
	// makes git's lock acquisition fail, so the whole batch aborts.
	commonDir := mustRun(t, "git", "rev-parse", "--git-common-dir")
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(commonDir, "refs", "heads", "feat-a.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create ref lock: %v", err)
	}
	_ = lockFile.Close()
	t.Cleanup(func() { _ = os.Remove(lockPath) })

	assertUnchanged := func(t *testing.T) {
		t.Helper()
		if got := mustRun(t, "git", "rev-parse", "feat-a"); got != postA {
			t.Fatalf("feat-a = %s after failed undo, want post-op %s", got, postA)
		}
		if got := mustRun(t, "git", "rev-parse", "feat-b"); got != postB {
			t.Fatalf("feat-b = %s after failed undo, want post-op %s", got, postB)
		}
		got, err := os.ReadFile(".git/stacked/state.json")
		if err != nil || !bytes.Equal(got, postState) {
			t.Fatalf("state.json changed despite the failed undo (err=%v)", err)
		}
		got, err = os.ReadFile(".git/stacked/undo.json")
		if err != nil || !bytes.Equal(got, postUndo) {
			t.Fatalf("undo.json changed: the failed entry must be retained (err=%v)", err)
		}
	}

	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{}
			if mode == "json" {
				args = append(args, "--json")
			}
			if err := runUndo(args); err == nil {
				t.Fatal("undo succeeded despite the locked ref")
			}
			assertUnchanged(t)
		})
	}

	// Clear only the fixture lock and retry: every snapshot ref and the
	// metadata restore, and exactly the modify entry drops.
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove ref lock: %v", err)
	}
	if err := runUndo(nil); err != nil {
		t.Fatalf("retry after clearing the ref lock: %v", err)
	}
	for name, sha := range newest.Refs {
		if got := mustRun(t, "git", "rev-parse", name); got != sha {
			t.Fatalf("ref %s = %s after retry, want recorded %s", name, got, sha)
		}
	}
	s := stateT(t)
	var want stack.State
	if err := json.Unmarshal(newest.State, &want); err != nil {
		t.Fatalf("snapshot state: %v", err)
	}
	gotState, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	wantState, err := json.Marshal(&want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotState, wantState) {
		t.Fatalf("state after retry:\n got  %s\nwant %s", gotState, wantState)
	}
	if after, ok, err := stack.PeekUndo(); err != nil || !ok || after.Label == "modify" {
		t.Fatalf("after retry PeekUndo = %+v ok=%v err=%v, want modify dropped", after, ok, err)
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

// TestMutateRefusesPausedRebase pins the mutateState gate: every mutating
// command funnels through it, so a paused rebase refuses even a command that
// itself performs no rebasing — and no undo entry is recorded for the refusal.
func TestMutateRefusesPausedRebase(t *testing.T) {
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
	if inProgress, _ := git.RebaseInProgress(); !inProgress {
		t.Fatalf("expected a rebase in progress")
	}

	beforeBytes, err := os.ReadFile(".git/stacked/undo.json")
	if err != nil {
		t.Fatalf("read undo.json before: %v", err)
	}
	for _, run := range [][]string{
		// Args chosen so each command parses cleanly and reaches the gate.
		{"rename", "feat-b", "feat-renamed"},
		{"track", "feat-a"},
		{"untrack", "feat-b"},
	} {
		var err error
		switch run[0] {
		case "rename":
			err = runRename(run[1:])
		case "track":
			err = runTrack(run[1:])
		case "untrack":
			err = runUntrack(run[1:])
		}
		if err == nil || !strings.Contains(err.Error(), "rebase is in progress") {
			t.Fatalf("%s mid-rebase = %v, want the paused-rebase refusal", run[0], err)
		}
	}
	// The refusal happens before any undo entry is recorded.
	afterBytes, err := os.ReadFile(".git/stacked/undo.json")
	if err != nil {
		t.Fatalf("read undo.json after: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("refused mutations modified undo.json")
	}
}

// An empty undo journal is a success-shaped refusal, not an error: the JSON
// arm emits {"undone": false} (and the text arm prints "nothing to undo"),
// both exiting 0.
func TestUndoEmptyJournalJSONAndText(t *testing.T) {
	newRepo(t)
	mustInit(t)

	out := captureStdout(t, func() {
		if err := runUndo([]string{"--json"}); err != nil {
			t.Fatalf("undo --json on an empty journal: %v", err)
		}
	})
	var payload struct {
		Undone bool `json:"undone"`
	}
	decodeStrictJSON(t, "undo --json (empty journal)", out, &payload)
	if payload.Undone {
		t.Fatalf("undo --json emitted %s, want {\"undone\": false}", strings.TrimSpace(out))
	}

	out = captureStdout(t, func() {
		if err := runUndo(nil); err != nil {
			t.Fatalf("undo on an empty journal: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to undo") {
		t.Fatalf("undo on an empty journal = %q, want %q", out, "nothing to undo")
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

	if err := runDoctor([]string{"--fix"}); err != nil {
		t.Fatalf("doctor --fix: %v", err)
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

	if err := runDoctor([]string{"--fix"}); err != nil {
		t.Fatalf("doctor --fix: %v", err)
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

func TestUndoFromUnrelatedLinkedWorktreeDoesNotTreatItAsCreated(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")
	mustRun(t, "git", "branch", "loose", "main")
	linked := filepath.Join(t.TempDir(), "loose")
	mustRun(t, "git", "worktree", "add", "-q", linked, "loose")
	t.Chdir(linked)
	resetProcCaches()

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo from unrelated linked worktree: %v", err)
	}
	if git.BranchExists("feat-a") {
		t.Fatal("undo left created branch feat-a behind")
	}
	if !git.BranchExists("loose") {
		t.Fatal("undo deleted unrelated linked branch loose")
	}
	if stateT(t).IsTracked("feat-a") {
		t.Fatal("undo left feat-a tracked")
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

func TestUndoCreatePreservesDirtyWorktree(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Dirty a.txt locally after create; undoing create should still preserve the
	// working tree modification instead of clobbering it.
	write(t, "a.txt", "a-modified\n")
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
	if _, err := s.RecordUndo(newGitPort(), "rename"); err != nil {
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

// bumpVersionJSON returns doc with its "version" field set to v (deleting
// the key when v < 0, which is how a legacy v0 file reads), preserving the
// document's other bytes.
func bumpVersionJSON(t *testing.T, doc map[string]json.RawMessage, v int) map[string]json.RawMessage {
	t.Helper()
	if v < 0 {
		delete(doc, "version")
	} else {
		doc["version"] = json.RawMessage(strconv.Itoa(v))
	}
	return doc
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestUndoRejectsFutureCurrentState pins the current-state barrier on the
// undo path: a state file stamped by a newer st must NOT fall back to
// reverting the journal snapshot — an older binary cannot know what the
// newer metadata records. Everything must be left byte-identical: state,
// journal, refs, index, and cwd.
func TestUndoRejectsFutureCurrentState(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(gitDir, "stacked", "state.json")
	undoFile := filepath.Join(gitDir, "stacked", "undo.json")
	undoBefore, err := os.ReadFile(undoFile)
	if err != nil {
		t.Fatal(err)
	}
	indexBefore := mustRun(t, "git", "ls-files", "--stage")
	aTip := mustRun(t, "git", "rev-parse", "feat-a")
	cwdBefore, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	stateBefore, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(stateBefore, &doc); err != nil {
		t.Fatalf("state does not parse: %v", err)
	}
	writeJSONFile(t, stateFile, bumpVersionJSON(t, doc, 99))
	stateAfter, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}

	err = runUndo(nil)
	if !errors.Is(err, stack.ErrStateTooNew) {
		t.Fatalf("undo error = %v, want wrapped ErrStateTooNew", err)
	}
	if got, _ := os.ReadFile(stateFile); !bytes.Equal(got, stateAfter) {
		t.Fatal("refused undo rewrote the future-schema state file")
	}
	if got, _ := os.ReadFile(undoFile); !bytes.Equal(got, undoBefore) {
		t.Fatal("refused undo touched the journal")
	}
	if _, ok, err := stack.PeekUndo(); err != nil || !ok {
		t.Fatal("refused undo dropped the journal entry — the undo is still pending")
	}
	if got := mustRun(t, "git", "rev-parse", "feat-a"); got != aTip {
		t.Fatalf("feat-a tip = %s after refused undo, want %s", got, aTip)
	}
	if got := mustRun(t, "git", "ls-files", "--stage"); got != indexBefore {
		t.Fatal("index changed during refused undo")
	}
	if cwd, _ := os.Getwd(); cwd != cwdBefore {
		t.Fatalf("cwd = %q after refused undo, want %q", cwd, cwdBefore)
	}
}

// TestUndoPeekFailureSurfaces pins the PeekUndo error arm in runUndo: when the
// journal file itself is unreadable (here: a directory where a file is
// expected — loadUndo only swallows malformed JSON, not I/O errors), undo
// surfaces the read failure instead of claiming an empty journal.
func TestUndoPeekFailureSurfaces(t *testing.T) {
	newRepo(t)
	mustInit(t)

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	undoDir := filepath.Join(gitDir, "stacked", "undo.json")
	if err := os.Mkdir(undoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runUndo(nil); err == nil || !strings.Contains(err.Error(), "undo") {
		t.Fatalf("undo with an unreadable journal = %v, want a journal error", err)
	}
}

// TestUndoRejectsFutureSnapshot pins that a future-schema snapshot in undo.json
// refuses with ErrStateTooNew and leaves refs, state, and journal untouched,
// while legacy v0 and current v1 snapshots still undo cleanly.
func TestUndoRejectsFutureSnapshot(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-wt", "wt.txt", "wt\n", "wt")

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(gitDir, "stacked", "state.json")
	undoFile := filepath.Join(gitDir, "stacked", "undo.json")

	// Bump ONLY the newest journal entry's snapshot version.
	data, err := os.ReadFile(undoFile)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil || len(entries) == 0 {
		t.Fatalf("undo journal does not parse (%v) or is empty", err)
	}
	var snap map[string]json.RawMessage
	if err := json.Unmarshal(entries[len(entries)-1]["state"], &snap); err != nil {
		t.Fatalf("snapshot does not parse: %v", err)
	}
	raw, err := json.Marshal(bumpVersionJSON(t, snap, 99))
	if err != nil {
		t.Fatal(err)
	}
	entries[len(entries)-1]["state"] = json.RawMessage(raw)
	writeJSONFile(t, undoFile, entries)
	undoBefore, err := os.ReadFile(undoFile)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	wtTip := mustRun(t, "git", "rev-parse", "feat-wt")

	err = runUndo(nil)
	if !errors.Is(err, stack.ErrStateTooNew) {
		t.Fatalf("undo error = %v, want wrapped ErrStateTooNew", err)
	}
	if !git.BranchExists("feat-wt") {
		t.Fatal("refused undo deleted the created branch")
	}
	if got := mustRun(t, "git", "rev-parse", "feat-wt"); got != wtTip {
		t.Fatalf("feat-wt tip = %s after refused undo, want %s", got, wtTip)
	}
	if got, _ := os.ReadFile(stateFile); !bytes.Equal(got, stateBefore) {
		t.Fatal("refused undo rewrote the state file")
	}
	if got, _ := os.ReadFile(undoFile); !bytes.Equal(got, undoBefore) {
		t.Fatal("refused undo touched the journal")
	}

	// Controls: the same layout with legacy v0 (version field absent) and
	// current v1 snapshots still undoes — the barrier rejects only what this
	// binary cannot read.
	for _, v := range []int{-1, 1} {
		t.Run(fmt.Sprintf("snapshot version %d still undoes", v), func(t *testing.T) {
			newRepo(t)
			t.Setenv("HOME", t.TempDir())
			mustInit(t)
			mustCreate(t, "feat-a", "a.txt", "a\n", "a")

			gitDir, err := git.GitCommonDir()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(gitDir, "stacked", "undo.json"))
			if err != nil {
				t.Fatal(err)
			}
			var entries []map[string]json.RawMessage
			if err := json.Unmarshal(data, &entries); err != nil || len(entries) == 0 {
				t.Fatalf("undo journal does not parse (%v) or is empty", err)
			}
			var snap map[string]json.RawMessage
			if err := json.Unmarshal(entries[len(entries)-1]["state"], &snap); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(bumpVersionJSON(t, snap, v))
			if err != nil {
				t.Fatal(err)
			}
			entries[len(entries)-1]["state"] = json.RawMessage(raw)
			writeJSONFile(t, filepath.Join(gitDir, "stacked", "undo.json"), entries)

			if err := runUndo(nil); err != nil {
				t.Fatalf("undo with snapshot version %d: %v", v, err)
			}
			if stateT(t).IsTracked("feat-a") {
				t.Fatal("supported-snapshot undo left feat-a tracked")
			}
		})
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
	if _, err := s.RecordUndo(newGitPort(), "continue"); err != nil {
		t.Fatalf("record undo: %v", err)
	}
	if err := stack.CleanupUndoOnError(newGitPort(), s, stack.ErrConflict); err != nil {
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
	if err := runDoctor([]string{"--fix"}); err != nil {
		t.Fatalf("noop doctor --fix: %v", err)
	}

	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after noop doctor --fix: %v", err)
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
		{"doctor", runDoctor},
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

// TestSubmitInvalidBranchNameCleanError pins the nil-PushResult path: a state
// file naming a branch git can never have ("--evil" fails refname rules) makes
// PushBranches return (nil, err); submit must surface that error, not panic
// dereferencing the empty result.
func TestSubmitInvalidBranchNameCleanError(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	// Rewrite state so the stack path carries a refname-illegal branch:
	// consistent key/name (passes the decodeState check) but "-" -leading.
	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(gitDir, "stacked", "state.json"), map[string]any{
		"version": 1,
		"trunk":   "main",
		"branches": map[string]any{
			"--evil": map[string]any{"name": "--evil", "parent": "main"},
			"feat-a": map[string]any{"name": "feat-a", "parent": "--evil"},
		},
	})

	err = runSubmit(nil)
	if err == nil || !strings.Contains(err.Error(), "not a valid git ref name") {
		t.Fatalf("submit with an invalid branch name = %v, want a clean usage-class error", err)
	}
}

// --- submit --all -----------------------------------------------------------

// newStackedForest builds two tracked stacks — a→b→c and x→y — plus a bare
// "origin" remote, returning the remote's path.
func newStackedForest(t *testing.T) string {
	t.Helper()
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	mustCreate(t, "a", "a.txt", "a\n", "a")
	mustCreate(t, "b", "b.txt", "b\n", "b")
	mustCreate(t, "c", "c.txt", "c\n", "c")
	mustCheckout(t, "main")
	mustCreate(t, "x", "x.txt", "x\n", "x")
	mustCreate(t, "y", "y.txt", "y\n", "y")
	return remoteDir
}

// remoteHasRef reports whether refs/heads/name exists on the remote repo.
func remoteHasRef(remoteDir, name string) bool {
	return exec.Command("git", "--git-dir", remoteDir, "show-ref", "--verify", "refs/heads/"+name).Run() == nil
}

// TestSubmitAllPushesForest pins the scope flag: from a mid-stack checkout the
// whole forest lands on the remote — every tracked branch, siblings included —
// and a second run is idempotent.
func TestSubmitAllPushesForest(t *testing.T) {
	remoteDir := newStackedForest(t)
	mustCheckout(t, "b")

	if err := runSubmit([]string{"--all"}); err != nil {
		t.Fatalf("submit --all: %v", err)
	}
	for _, name := range []string{"a", "b", "c", "x", "y"} {
		if !remoteHasRef(remoteDir, name) {
			t.Fatalf("submit --all did not create refs/heads/%s on the remote", name)
		}
	}
	if err := runSubmit([]string{"--all"}); err != nil {
		t.Fatalf("second submit --all: %v", err)
	}
}

// TestSubmitAllDryRunTopoOrder pins parents-before-children ordering: the dry
// run lists every tracked branch with each parent ahead of its children and
// pushes nothing.
func TestSubmitAllDryRunTopoOrder(t *testing.T) {
	remoteDir := newStackedForest(t)
	mustCheckout(t, "b")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--all", "--dry-run"}); err != nil {
			t.Fatalf("submit --all --dry-run: %v", err)
		}
	})
	for _, name := range []string{"a", "b", "c", "x", "y"} {
		if !strings.Contains(out, "would push "+name) {
			t.Fatalf("dry run omitted %s:\n%s", name, out)
		}
	}
	pos := map[string]int{}
	for _, name := range []string{"a", "b", "c", "x", "y"} {
		pos[name] = strings.Index(out, "would push "+name)
	}
	ordered := pos["a"] < pos["b"] && pos["b"] < pos["c"] && pos["x"] < pos["y"]
	if !ordered {
		t.Fatalf("dry-run order not parents-first: %v\n%s", pos, out)
	}
	for _, name := range []string{"a", "b", "c", "x", "y"} {
		if remoteHasRef(remoteDir, name) {
			t.Fatalf("dry run pushed %s", name)
		}
	}
}

// TestSubmitAllFromTrunk pins that the trunk early-return does not fire under
// --all: there may be nothing on the current path but a whole forest to push.
func TestSubmitAllFromTrunk(t *testing.T) {
	remoteDir := newStackedForest(t)
	mustCheckout(t, "main")

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--all", "--dry-run"}); err != nil {
			t.Fatalf("submit --all from trunk: %v", err)
		}
	})
	if strings.Contains(out, "at trunk; nothing to submit") {
		t.Fatalf("--all hit the trunk early return:\n%s", out)
	}
	for _, name := range []string{"a", "b", "c", "x", "y"} {
		if !strings.Contains(out, "would push "+name) {
			t.Fatalf("dry run from trunk omitted %s:\n%s", name, out)
		}
	}
	_ = remoteDir
}

// TestSubmitAllEmptyForest pins the empty early return: an initialized repo
// tracking nothing reports cleanly rather than pushing zero refs.
func TestSubmitAllEmptyForest(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)

	out := captureStdout(t, func() {
		if err := runSubmit([]string{"--all"}); err != nil {
			t.Fatalf("submit --all on empty forest: %v", err)
		}
	})
	if !strings.Contains(out, "nothing tracked") {
		t.Fatalf("expected the empty-forest message, got:\n%s", out)
	}
}

// TestSubmitAllCyclicState pins corrupt-state safety: branches unreachable
// from the trunk (a cycle or a dangling parent) are named in an error, never
// silently skipped and never a hang.
func TestSubmitAllCyclicState(t *testing.T) {
	newRepo(t)
	mustInit(t)
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(gitDir, "stacked", "state.json"), map[string]any{
		"version": 1,
		"trunk":   "main",
		"branches": map[string]any{
			"a":      map[string]any{"name": "a", "parent": "b"},
			"b":      map[string]any{"name": "b", "parent": "a"},
			"feat-a": map[string]any{"name": "feat-a", "parent": "main"},
		},
	})

	err = runSubmit([]string{"--all"})
	if err == nil {
		t.Fatal("submit --all accepted a cyclic state")
	}
	if !strings.Contains(err.Error(), "do not descend") ||
		!strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("cyclic state error = %v, want it naming the unreachable branches", err)
	}
}

// TestSubmitAllPartialFailure pins the confirmed-per-ref contract under --all:
// a mid-stack rejection names the failed branch and still reports every
// confirmed push — including branches off the current path.
func TestSubmitAllPartialFailure(t *testing.T) {
	remoteDir := newStackedForest(t)
	mustCheckout(t, "b")

	hook := filepath.Join(remoteDir, "hooks", "update")
	script := "#!/bin/sh\n[ \"$1\" = refs/heads/b ] && exit 1\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() {
		runErr = runSubmit([]string{"--all", "--json"})
	})
	if runErr == nil {
		t.Fatal("submit --all with a rejected branch should return a non-nil error")
	}
	var got submitResult
	decodeStrictJSON(t, "partial submit --all", out, &got)
	if got.Failed != "b" {
		t.Fatalf("partial result Failed = %q, want b", got.Failed)
	}
	want := map[string]bool{"a": true, "c": true, "x": true, "y": true}
	for _, name := range got.Pushed {
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("partial result Pushed = %v, missing %v", got.Pushed, want)
	}
	if !remoteHasRef(remoteDir, "a") || remoteHasRef(remoteDir, "b") {
		t.Fatal("remote state disagrees with the reported partial outcome")
	}
}

// --- track --all ------------------------------------------------------------

// TestTrackAllMutualExclusion pins the usage contract: --all names the whole
// untracked set, so a positional name or --parent is a usage error.
func TestTrackAllMutualExclusion(t *testing.T) {
	newRepo(t)
	mustInit(t)
	for _, args := range [][]string{
		{"--all", "feat"},
		{"--all", "--parent", "main"},
	} {
		if err := runTrack(args); err == nil {
			t.Fatalf("track %v succeeded; want a usage error", args)
		}
	}
}

// TestTrackAllAdoptsExistingStack is the flagship case over real git: two
// pre-existing stacks (a→b→c and lone x) are adopted in one command with the
// inferred topology — parents before children — and a single undo entry.
func TestTrackAllAdoptsExistingStack(t *testing.T) {
	newRepo(t)
	mustInit(t)

	mkLocal := func(parent, name, file string) {
		mustRun(t, "git", "checkout", "-q", parent)
		mustRun(t, "git", "checkout", "-q", "-b", name)
		write(t, file, name+"\n")
		mustRun(t, "git", "add", "-A")
		mustRun(t, "git", "commit", "-q", "-m", name)
	}
	mkLocal("main", "a", "a.txt")
	mkLocal("a", "b", "b.txt")
	mkLocal("b", "c", "c.txt")
	mkLocal("main", "x", "x.txt")
	mustRun(t, "git", "checkout", "-q", "main")

	out := captureStdout(t, func() {
		if err := runTrack([]string{"--all"}); err != nil {
			t.Fatalf("track --all: %v", err)
		}
	})
	s := stateT(t)
	for name, want := range map[string]string{"a": "main", "b": "a", "c": "b", "x": "main"} {
		b, ok := s.Get(name)
		if !ok {
			t.Fatalf("%s was not adopted:\n%s", name, out)
		}
		if b.Parent != want {
			t.Fatalf("%s parent=%q, want %q", name, b.Parent, want)
		}
	}
	// One undo entry reverts the whole adoption.
	if err := runUndo(nil); err != nil {
		t.Fatalf("undo after track --all: %v", err)
	}
	for _, name := range []string{"a", "b", "c", "x"} {
		if stateT(t).IsTracked(name) {
			t.Fatalf("%s still tracked after undo", name)
		}
	}
}

// TestTrackAllDryRun pins the preview contract: `track --all --dry-run`
// reports the inferred parent map under the "would track" label and records
// nothing — no state mutation, no journal entry.
func TestTrackAllDryRun(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "checkout", "-q", "-b", "a")
	write(t, "a.txt", "a\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "a")
	mustRun(t, "git", "checkout", "-q", "-b", "b")
	write(t, "b.txt", "b\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "b")
	mustRun(t, "git", "checkout", "-q", "main")

	out := captureStdout(t, func() {
		if err := runTrack([]string{"--all", "--dry-run"}); err != nil {
			t.Fatalf("track --all --dry-run: %v", err)
		}
	})
	if !strings.Contains(out, "would track: a (parent: main), b (parent: a)") {
		t.Fatalf("dry-run output = %q, want the inferred parent map", out)
	}
	for _, name := range []string{"a", "b"} {
		if stateT(t).IsTracked(name) {
			t.Fatalf("dry-run tracked %s", name)
		}
	}
	if _, ok, err := stack.PeekUndo(); err != nil || ok {
		t.Fatalf("journal after preview: ok=%v err=%v, want empty", ok, err)
	}

	// The JSON arm is the shared dryRun shape: dryRun true plus the tracked
	// parent map.
	out = captureStdout(t, func() {
		if err := runTrack([]string{"--all", "--dry-run", "--json"}); err != nil {
			t.Fatalf("track --all --dry-run --json: %v", err)
		}
	})
	var got stack.OpResult
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("track --all --dry-run --json did not decode: %v\n%s", err, out)
	}
	if !got.DryRun || got.Tracked["a"] != "main" || got.Tracked["b"] != "a" {
		t.Fatalf("payload = %+v, want dryRun with {a:main, b:a}", got)
	}

	// --dry-run without --all is a usage error — a single track's parent is
	// already explicit or trivially the infer call.
	if err := runTrack([]string{"a", "--dry-run"}); err == nil {
		t.Fatal("track a --dry-run succeeded; want a usage error")
	}
	if stateT(t).IsTracked("a") {
		t.Fatal("refused dry-run tracked a")
	}
}

// TestTrackAllJSONShape pins the aggregate payload: tracked maps each adopted
// name to its inferred parent.
func TestTrackAllJSONShape(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustRun(t, "git", "checkout", "-q", "-b", "a")
	write(t, "a.txt", "a\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "a")
	mustRun(t, "git", "checkout", "-q", "-b", "b")
	write(t, "b.txt", "b\n")
	mustRun(t, "git", "add", "-A")
	mustRun(t, "git", "commit", "-q", "-m", "b")
	mustRun(t, "git", "checkout", "-q", "main")

	out := captureStdout(t, func() {
		if err := runTrack([]string{"--all", "--json"}); err != nil {
			t.Fatalf("track --all --json: %v", err)
		}
	})
	var got stack.OpResult
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("track --all --json did not decode as OpResult: %v\n%s", err, out)
	}
	if got.Tracked["a"] != "main" || got.Tracked["b"] != "a" {
		t.Fatalf("tracked = %v, want {a:main, b:a}", got.Tracked)
	}
}

// --- prune + sync --no-fetch -------------------------------------------------

func TestSyncNoFetchKeepsRemoteOnlyMerged(t *testing.T) {
	newRepo(t)
	mustInit(t)

	// Bare origin; push main so refs/remotes/origin/main exists locally.
	remoteDir := t.TempDir()
	mustRun(t, "git", "init", "-q", "--bare", remoteDir)
	mustRun(t, "git", "remote", "add", "origin", remoteDir)
	mustRun(t, "git", "push", "-q", "-u", "origin", "main")
	mustRun(t, "git", "--git-dir", remoteDir, "symbolic-ref", "HEAD", "refs/heads/main")

	// Stack a branch, then make the REMOTE-tracking ref contain it while the
	// local trunk stays behind: push feat-a's tip as origin/main and fetch so
	// refs/remotes/origin/main advances past local main.
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustRun(t, "git", "push", "-q", "origin", "feat-a:main")
	mustRun(t, "git", "fetch", "-q", "origin")
	localMain := mustRun(t, "git", "rev-parse", "main")

	out := captureStdout(t, func() {
		if err := runSync([]string{"--no-fetch"}); err != nil {
			t.Fatalf("sync --no-fetch: %v", err)
		}
	})
	if !strings.Contains(out, "skipped (--no-fetch)") {
		t.Fatalf("sync --no-fetch should note the skipped trunk step, got:\n%s", out)
	}
	// feat-a is contained in origin/main but NOT local main — under --no-fetch
	// the local trunk is the only prune basis, so the branch survives until
	// the local trunk advances to contain it.
	if exec.Command("git", "rev-parse", "--verify", "-q", "feat-a").Run() != nil {
		t.Fatal("feat-a merged only into origin/main must survive offline sync")
	}
	s, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if !s.IsTracked("feat-a") {
		t.Fatal("feat-a was untracked by an offline sync that never fetched it")
	}
	// No fetch, no fast-forward: the local trunk must not have moved.
	if got := mustRun(t, "git", "rev-parse", "main"); got != localMain {
		t.Fatalf("local main moved under --no-fetch: %s → %s", localMain, got)
	}
}

// TestSyncNoFetchNotFooledByTrunkTag pins plan-007: a tag named "main" shadows
// the bare name in gitrevisions order (refs/tags/ precedes refs/heads/), so an
// unqualified prune basis would measure mergedness against the TAG's commit —
// here the tag sits on feat-a's tip, which would make feat-a look merged and
// prune live work. Dry-run and apply must agree: both keep feat-a.
func TestSyncNoFetchNotFooledByTrunkTag(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	mustCheckout(t, "main")

	// Tag "main" at feat-a's tip. `git rev-parse main` resolves to the TAG
	// (and warns "refname 'main' is ambiguous", which pollutes stdout).
	mustRun(t, "git", "tag", "main", "feat-a")
	tagSHA := mustRun(t, "git", "rev-parse", "refs/tags/main")
	if got := mustRun(t, "git", "rev-parse", "main"); !strings.Contains(got, tagSHA) {
		t.Fatalf("test setup: bare main should resolve to the tag, got %q want %s", got, tagSHA)
	}

	// Dry-run: preview says nothing to prune (feat-a is not merged into the
	// real trunk branch).
	dryOut := captureStdout(t, func() {
		if err := runSync([]string{"--dry-run", "--no-fetch"}); err != nil {
			t.Fatalf("sync --dry-run --no-fetch: %v", err)
		}
	})
	if strings.Contains(dryOut, "feat-a") && strings.Contains(dryOut, "prun") {
		t.Fatalf("dry-run would prune feat-a — tag shadowed the trunk basis:\n%s", dryOut)
	}

	// Apply: the branch must survive — mergedness is against refs/heads/main,
	// not the tag sitting on feat-a's tip.
	if err := runSync([]string{"--no-fetch"}); err != nil {
		t.Fatalf("sync --no-fetch: %v", err)
	}
	if exec.Command("git", "rev-parse", "--verify", "-q", "refs/heads/feat-a").Run() != nil {
		t.Fatal("feat-a was pruned — a tag named main shadowed the prune basis")
	}
	s, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if !s.IsTracked("feat-a") {
		t.Fatal("feat-a was untracked — a tag named main shadowed the prune basis")
	}
}

// --- undo --dry-run ---------------------------------------------------------

// --- multi-step undo (st undo <n>) ------------------------------------------

// TestUndoRefusesExternalDrift pins the CAS behavior end to end on real git:
// a branch committed on outside st between the op and the undo refuses —
// before any mutation — while --force restores the recorded tip anyway and
// reports the override.
func TestUndoRefusesExternalDrift(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	recorded := mustRun(t, "git", "rev-parse", "feat-a")

	write(t, "a.txt", "a2\n")
	if err := runModify([]string{"-a"}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	// An external commit lands on feat-a between the op and the undo.
	write(t, "a.txt", "a3\n")
	mustRun(t, "git", "add", "a.txt")
	mustRun(t, "git", "commit", "-q", "-m", "external work")
	external := mustRun(t, "git", "rev-parse", "feat-a")

	err := runUndo(nil)
	if err == nil {
		t.Fatal("undo succeeded over an externally moved ref")
	}
	if !strings.Contains(err.Error(), "moved outside st") || !strings.Contains(err.Error(), "feat-a") {
		t.Fatalf("error = %q, want the diverged branch named", err)
	}
	if got := mustRun(t, "git", "rev-parse", "feat-a"); got != external {
		t.Fatalf("feat-a = %s after refused undo, want the external tip %s", got, external)
	}
	// The refused entry stays in the journal for the --force retry.
	out := captureStdout(t, func() {
		if err := runUndo([]string{"--force"}); err != nil {
			t.Fatalf("undo --force over a diverged ref: %v", err)
		}
	})
	if got := mustRun(t, "git", "rev-parse", "feat-a"); got != recorded {
		t.Fatalf("feat-a = %s after --force, want the recorded tip %s", got, recorded)
	}
	if !strings.Contains(out, "force") || !strings.Contains(out, "feat-a") {
		t.Fatalf("force output = %q, want a note naming the overwritten ref", out)
	}
}

// A poisoned journal entry — ref values that are not full commit ids — is
// ignored everywhere the journal is read, so undo skips it and restores only
// the healthy entry without echoing raw journal bytes.
func TestUndoJournalRejectsNonOIDRefValues(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "a\n", "a")
	tip := mustRun(t, "git", "rev-parse", "feat-a")

	gitDir, err := git.GitCommonDir()
	if err != nil {
		t.Fatal(err)
	}
	poisoned := []map[string]any{
		{
			"label": "modify",
			"state": json.RawMessage(`{"version":1,"trunk":"main","branches":{}}`),
			"refs":  map[string]string{"feat-a": "HEAD~2", "main": tip},
		},
		{
			"label": "healthy",
			"state": json.RawMessage(`{"version":1,"trunk":"main","branches":{}}`),
			"refs":  map[string]string{"feat-a": tip},
		},
	}
	writeJSONFile(t, filepath.Join(gitDir, "stacked", "undo.json"), poisoned)

	out := captureStdout(t, func() {
		if err := runUndo(nil); err != nil {
			t.Fatalf("undo over a poisoned entry: %v", err)
		}
	})
	if strings.Contains(out, "HEAD~2") || strings.Contains(out, "\x1b") {
		t.Fatalf("undo echoed poisoned journal values: %q", out)
	}
	if !strings.Contains(out, "undid: healthy") {
		t.Fatalf("undo = %q, want only the healthy entry applied", out)
	}
}

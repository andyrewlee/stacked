package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Conflict journeys: a paused rebase left by restack/onto/sync, driven
// through `st continue` (finish) and `st abort` (rollback).

// TestConflictContinue drives a real merge conflict via modify and resolves it
// with `st continue`, asserting the upstack ends up rebased onto the new tip.
func TestConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	// feat-b edits the same file/line so amending feat-a conflicts on restack.
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	// A conflict maps to the dedicated exit code 2 (see docs/AGENT.md).
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	// A real rebase should be in progress.
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after conflict: %v", err)
	}

	// Resolve and continue.
	r.writeFile("f.txt", "X\nB\n")
	r.git("add", "f.txt")
	res = r.stOK("continue")
	wantStdoutContains(t, res, "continued restack")

	if got := r.git("show", "feat-b:f.txt"); got != "X\nB" {
		t.Fatalf("feat-b:f.txt = %q, want X\\nB", got)
	}
	r.stOK("validate")
}

// TestConflictAbort drives the same conflict but backs out with `st abort`,
// asserting the rebase is gone and a second abort errors with "no rebase".
func TestConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	wantExit(t, res, 2) // conflict

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress: %v", err)
	}

	res = r.stOK("abort")
	wantStdoutContains(t, res, "Aborted the in-progress rebase")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase should be gone after abort, stat err = %v", err)
	}

	// A second abort with nothing in progress errors.
	res = r.st("abort")
	wantExit(t, res, 1)
	wantStderrContains(t, res, "no rebase in progress")
}

// TestContinueAfterParentMoves drives a real conflict, advances the parent ref
// while the child's rebase is paused, then continues. Continue must record the
// target the rebase actually completed onto — the paused metadata's onto SHA —
// not the parent's moved tip, so the follow-up cascade still recognizes the
// move and lands the parent's new commits on the child.
func TestContinueAfterParentMoves(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")
	r.stOK("checkout", "feat-a")

	r.writeFile("f.txt", "X\n")
	res := r.st("modify", "-a")
	wantExit(t, res, 2) // feat-b's rebase onto feat-a pauses on the conflict

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress: %v", err)
	}
	a1 := r.rev("feat-a")

	// Advance feat-a to A2 while feat-b's rebase is paused. feat-a is not
	// checked out (the rebase detached HEAD onto feat-b), so a linked worktree
	// can own it and commit a real change there.
	wt := filepath.Join(t.TempDir(), "wt")
	r.git("worktree", "add", wt, "feat-a")
	if err := os.WriteFile(filepath.Join(wt, "a2.txt"), []byte("A2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.gitIn(wt, "add", "a2.txt")
	r.gitIn(wt, "commit", "-q", "-m", "a2")
	r.git("worktree", "remove", wt)
	a2 := r.rev("feat-a")
	if a2 == a1 {
		t.Fatal("test setup: feat-a did not advance while feat-b was paused")
	}

	// Resolve and continue. The completed rebase incorporated A1; the cascade
	// must then pick up A2.
	r.writeFile("f.txt", "X\nB\n")
	r.git("add", "f.txt")
	res = r.stOK("continue")
	wantStdoutContains(t, res, "continued restack")

	if got := r.git("show", "feat-b:f.txt"); got != "X\nB" {
		t.Fatalf("feat-b:f.txt = %q, want X\\nB", got)
	}
	// The parent's post-pause commit must have landed on the child — only
	// possible when the recorded base was the actual rebase target (A1), not
	// the moved tip the rebase never incorporated.
	if got := r.git("show", "feat-b:a2.txt"); got != "A2" {
		t.Fatalf("feat-b:a2.txt = %q, want A2 — the moved parent was not restacked in", got)
	}
	if !r.isAncestor(a2, "feat-b") {
		t.Fatal("feat-a's moved tip is not an ancestor of feat-b after continue")
	}
	r.stOK("validate")
}

func TestOntoConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")

	res := r.st("onto", "main")
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after onto conflict: %v", err)
	}

	r.writeFile("f.txt", "B\n")
	r.git("add", "f.txt")
	r.stOK("continue")
	r.stOK("validate")

	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "stacked", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if strings.Contains(string(data), "pendingReparent") {
		t.Fatalf("state.json still contains pendingReparent after continue:\n%s", data)
	}
	var state struct {
		Branches map[string]struct {
			Parent string `json:"parent"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid: %v\n%s", err, data)
	}
	if got := state.Branches["feat-b"].Parent; got != "main" {
		t.Fatalf("feat-b parent after continue = %q, want main", got)
	}
}

func TestOntoConflictAbort(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	r.create("feat-a", "f.txt", "A\n", "a")
	r.create("feat-b", "f.txt", "A\nB\n", "b")

	res := r.st("onto", "main")
	wantExit(t, res, 2)
	wantStderrContains(t, res, "st continue")

	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress after onto conflict: %v", err)
	}

	r.stOK("abort")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf("rebase should be gone after abort, stat err = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "stacked", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if strings.Contains(string(data), "pendingReparent") {
		t.Fatalf("state.json still contains pendingReparent after abort:\n%s", data)
	}
	var state struct {
		Branches map[string]struct {
			Parent string `json:"parent"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("state.json invalid: %v\n%s", err, data)
	}
	if got := state.Branches["feat-b"].Parent; got != "feat-a" {
		t.Fatalf("feat-b parent after abort = %q, want feat-a", got)
	}
	r.stOK("validate")
}

// TestSyncConflictContinue drives a conflict that occurs *during* sync's restack
// (the trunk advances under a stacked branch), then resolves it (TEST-2).
func TestSyncConflictContinue(t *testing.T) {
	t.Parallel()
	r := newRepo(t)
	r.initStack()
	// feat-a adds f.txt; the trunk then adds f.txt with different content, so
	// restacking feat-a onto the advanced trunk during sync conflicts.
	r.create("feat-a", "f.txt", "A\n", "a")
	r.stOK("checkout", "main")
	r.writeFile("f.txt", "MAIN\n")
	r.git("add", "f.txt")
	r.git("commit", "-q", "-m", "trunk advances")
	r.stOK("checkout", "feat-a")

	res := r.st("sync")
	wantExit(t, res, 2) // conflict maps to the dedicated exit code
	wantStderrContains(t, res, "st continue")
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("expected a rebase in progress mid-sync: %v", err)
	}

	// Resolve and continue; the stack must reconcile and validate clean.
	r.writeFile("f.txt", "MAIN\nA\n")
	r.git("add", "f.txt")
	r.stOK("continue")
	r.stOK("validate")
}

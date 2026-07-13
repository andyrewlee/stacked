package cmd

import (
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/stack"
)

// This file pins the Phase-3 UX/logistical fixes (F1-F3, F6) found by driving
// every user story against the real binary. Each test reproduces the prior bad
// behaviour's trigger and asserts the friendly outcome, so a regression is caught
// by `make ci` rather than only by the out-of-tree story harness.

func errContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
	}
}

// F1: `st worktree rm <branch>` where the branch lives only in the MAIN worktree
// must explain that plainly instead of leaking git's "fatal: ... is a main
// working tree" error.
func TestWorktreeRemoveMainWorktreeFriendly(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat", "f.txt", "x\n", "feat") // leaves HEAD on feat in the main worktree
	resetWorktreeCache()
	err := runWorktree([]string{"rm", "feat"})
	errContains(t, err, "checked out in the main worktree, not a separate one")
	if strings.Contains(err.Error(), "is a main working tree") {
		t.Errorf("raw git error leaked through: %v", err)
	}
}

// F2: `st worktree <current-branch>` must say the branch is in the main worktree
// rather than reporting "worktree already exists" pointing at the main repo path.
func TestWorktreeAddCurrentBranchFriendly(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat", "f.txt", "x\n", "feat")
	resetWorktreeCache()
	err := runWorktree([]string{"feat"})
	errContains(t, err, "checked out in the main worktree")
	if strings.Contains(err.Error(), "already exists") {
		t.Errorf("misleading 'already exists' message: %v", err)
	}
}

// F3: create/rename reject an invalid branch name with a one-liner, not git's
// raw multi-line "fatal: ... is not a valid branch name" + advice hints.
func TestCreateRejectsInvalidName(t *testing.T) {
	newRepo(t)
	mustInit(t)
	err := runCreate([]string{"bad name"})
	errContains(t, err, "is not a valid branch name")
	if strings.Contains(err.Error(), "fatal:") || strings.Contains(err.Error(), "hint:") {
		t.Errorf("raw git error leaked through: %v", err)
	}
}

func TestRenameRejectsInvalidName(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat", "f.txt", "x\n", "feat")
	err := runRename([]string{"bad name"})
	errContains(t, err, "is not a valid branch name")
}

// A valid nested name still works (guards against an over-eager validator).
func TestCreateAcceptsNestedName(t *testing.T) {
	newRepo(t)
	mustInit(t)
	if err := runCreate([]string{"feat/foo"}); err != nil {
		t.Fatalf("create feat/foo should succeed: %v", err)
	}
	if !stateT(t).IsTracked("feat/foo") {
		t.Errorf("feat/foo was not tracked after create")
	}
}

// F6: `st sync --remote <missing>` fails loudly (like submit) when the remote is
// explicitly named, but a missing DEFAULT origin still syncs locally.
func TestSyncRejectsMissingExplicitRemote(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat", "f.txt", "x\n", "feat")
	err := runSync([]string{"--remote", "nope"})
	errContains(t, err, `remote "nope" does not exist`)
}

func TestSyncLocalWhenDefaultRemoteAbsent(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat", "f.txt", "x\n", "feat")
	if err := runSync(nil); err != nil {
		t.Fatalf("local sync with no remote should succeed: %v", err)
	}
}

// TestRenameInvalidatesWorktreeCache pins the cachedShell RenameBranch
// override: after an in-process rename of a branch owning a linked worktree,
// a fresh worktrees() read reports the NEW name, not the cached pre-rename
// ownership.
func TestRenameInvalidatesWorktreeCache(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat", "f.txt", "x\n", "feat")
	mustRun(t, "git", "checkout", "-q", "main")
	resetWorktreeCache()
	if err := runWorktree([]string{"feat"}); err != nil {
		t.Fatalf("worktree feat: %v", err)
	}
	// Warm the cache, then rename through the cached port (as engine ops do).
	if _, err := worktrees(); err != nil {
		t.Fatal(err)
	}
	if err := (cachedShell{}).RenameBranch("feat", "feat-renamed"); err != nil {
		t.Fatalf("RenameBranch: %v", err)
	}
	wts, err := worktrees()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.LinkedOwnerOf(wts, "feat"); ok {
		t.Fatal("stale cache: old name still owns a worktree after rename")
	}
	if _, ok := stack.LinkedOwnerOf(wts, "feat-renamed"); !ok {
		t.Fatal("fresh read is missing the renamed branch's worktree ownership")
	}

	// The quiet (JSON-mode) shell carries the same override: rename back
	// through it and assert the cache refreshes again.
	if err := (cachedQuietShell{}).RenameBranch("feat-renamed", "feat"); err != nil {
		t.Fatalf("quiet RenameBranch: %v", err)
	}
	wts, err = worktrees()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.LinkedOwnerOf(wts, "feat"); !ok {
		t.Fatal("quiet-shell rename did not refresh the cache")
	}
}

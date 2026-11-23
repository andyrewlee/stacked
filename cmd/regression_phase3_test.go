package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func errContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
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

// TestRenameInvalidatesWorktreeCache pins the cachedPort RenameBranch
// override: after an in-process rename of a branch owning a linked worktree,
// a fresh worktrees() read reports the NEW name, not the cached pre-rename
// ownership.
func TestRenameInvalidatesWorktreeCache(t *testing.T) {
	newRepo(t)
	t.Setenv("HOME", t.TempDir())
	mustInit(t)
	mustCreate(t, "feat", "f.txt", "x\n", "feat")
	mustRun(t, "git", "checkout", "-q", "main")
	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	mustRun(t, "git", "worktree", "add", "-q", wtPath, "feat")
	resetProcCaches()
	// Warm the cache, then rename through the cached port (as engine ops do).
	if _, err := worktrees(); err != nil {
		t.Fatal(err)
	}
	if err := (&cachedPort{Git: git.Shell{}}).RenameBranch("feat", "feat-renamed"); err != nil {
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

	// The quiet (JSON-mode) port carries the same override: rename back
	// through it and assert the cache refreshes again.
	if err := (&cachedPort{Git: git.QuietShell{}}).RenameBranch("feat-renamed", "feat"); err != nil {
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

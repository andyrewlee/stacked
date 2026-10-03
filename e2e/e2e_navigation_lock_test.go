package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/andyrewlee/stacked/internal/stack"
)

// not-parallel: changes process cwd/environment to hold the repository lock across child-process calls
func TestNavigationLockAcrossProcesses(t *testing.T) {
	r := newRepo(t)

	// stack.Lock() probes the repo (git rev-parse --git-common-dir) with the
	// PARENT's inherited environment; unset every GIT_* so host git state —
	// GIT_DIR, GIT_CONFIG_*, config-count variables — cannot redirect that
	// probe before the first parent-side probe runs. Save for restore, never
	// log the values, and unset rather than empty-assign (Git distinguishes
	// an absent variable from an empty one).
	var saved []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			saved = append(saved, kv)
			os.Unsetenv(name)
		}
	}
	t.Cleanup(func() {
		for _, kv := range saved {
			name, val, _ := strings.Cut(kv, "=")
			os.Setenv(name, val)
		}
	})
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	r.initStack()
	r.create("feat-a", "a.txt", "A\n", "a") // leaves HEAD on feat-a

	// Hold the repository lock in this (parent) process while a separate st
	// binary tries the same commands: cross-process proof that navigation and
	// init participate in the advisory lock. ST_LOCK_WAIT=0 makes the child's
	// contended acquire fail immediately rather than retry.
	t.Chdir(r.dir)
	release, err := stack.Lock()
	if err != nil {
		t.Fatalf("parent Lock: %v", err)
	}
	t.Cleanup(release) // after t.Chdir: lock releases before cwd is restored

	lockEnv := []string{"ST_LOCK_WAIT=0"}

	res := r.stInEnv(r.dir, lockEnv, "checkout", "main")
	wantExit(t, res, 5)
	if got := r.currentBranch(); got != "feat-a" {
		t.Fatalf("locked checkout moved HEAD to %q", got)
	}
	res = r.stInEnv(r.dir, lockEnv, "up")
	wantExit(t, res, 5)
	res = r.stInEnv(r.dir, lockEnv, "top")
	wantExit(t, res, 5)
	res = r.stInEnv(r.dir, lockEnv, "init", "--trunk", "main")
	wantExit(t, res, 5)

	// Bare `st checkout` only lists — a reader, not a navigator — so it stays
	// available while the lock is held.
	res = r.stInEnv(r.dir, lockEnv, "checkout")
	wantExit(t, res, 0)

	release()
	res = r.stInEnv(r.dir, lockEnv, "checkout", "main")
	wantExit(t, res, 0)
	if got := r.currentBranch(); got != "main" {
		t.Fatalf("checkout after release: HEAD = %q, want main", got)
	}

	// Same contract before state exists: a held lock refuses init's
	// check-then-create without creating state, and init succeeds after.
	r2 := newRepo(t)
	t.Chdir(r2.dir)
	release2, err := stack.Lock()
	if err != nil {
		t.Fatalf("parent Lock (fresh repo): %v", err)
	}
	t.Cleanup(release2)

	res = r2.stInEnv(r2.dir, lockEnv, "init", "--trunk", "main")
	wantExit(t, res, 5)
	if _, err := os.Stat(r2.dir + "/.git/stacked/state.json"); !os.IsNotExist(err) {
		t.Fatalf("refused init created state.json (stat err = %v)", err)
	}
	release2()
	res = r2.stInEnv(r2.dir, lockEnv, "init", "--trunk", "main")
	wantExit(t, res, 0)
}

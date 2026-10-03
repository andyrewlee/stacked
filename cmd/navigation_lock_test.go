package cmd

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/andyrewlee/stacked/internal/stack"
)

// These tests pin plan-022's locking contract: navigation commands that move
// HEAD (checkout <target>, up, down, top, bottom) and init run under the same
// repository lock mutations take — they refuse with stack.ErrLocked while
// another st holds it, change nothing, and never record undo entries. Pure
// observation (bare `st checkout`, status/log, --dry-run previews) stays
// available under the held lock. The tests hold the lock in-process — flock is
// per open-file-description, so the command's own Lock() attempt contends —
// and force ST_LOCK_WAIT=0 so a contended acquire fails immediately rather
// than retrying.

// repoSnapshot captures everything a refused navigation/init call must leave
// untouched: HEAD (name and commit), every ref, the persisted state, and the
// undo journal.
type repoSnapshot struct {
	headRef string
	headSHA string
	refs    string
	state   []byte
	undo    []byte
}

func takeRepoSnapshot(t *testing.T) repoSnapshot {
	t.Helper()
	state, err := os.ReadFile(".git/stacked/state.json")
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	// undo.json is absent until the first mutation records an entry; treat
	// missing as nil so the check also covers "no undo entry was added".
	undo, err := os.ReadFile(".git/stacked/undo.json")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read undo.json: %v", err)
	}
	return repoSnapshot{
		headRef: mustRun(t, "git", "symbolic-ref", "HEAD"),
		headSHA: mustRun(t, "git", "rev-parse", "HEAD"),
		refs:    mustRun(t, "git", "for-each-ref"),
		state:   state,
		undo:    undo,
	}
}

func (s repoSnapshot) check(t *testing.T) {
	t.Helper()
	if got := mustRun(t, "git", "symbolic-ref", "HEAD"); got != s.headRef {
		t.Fatalf("HEAD ref = %q, want unchanged %q", got, s.headRef)
	}
	if got := mustRun(t, "git", "rev-parse", "HEAD"); got != s.headSHA {
		t.Fatalf("HEAD = %q, want unchanged %q", got, s.headSHA)
	}
	if got := mustRun(t, "git", "for-each-ref"); got != s.refs {
		t.Fatalf("refs changed under refused command:\nbefore:\n%s\nafter:\n%s", s.refs, got)
	}
	if got, err := os.ReadFile(".git/stacked/state.json"); err != nil || !bytes.Equal(got, s.state) {
		t.Fatalf("state.json changed under refused command (err=%v)", err)
	}
	got, err := os.ReadFile(".git/stacked/undo.json")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read undo.json: %v", err)
	}
	if !bytes.Equal(got, s.undo) {
		t.Fatal("undo.json changed: a navigation/init command must not record undo entries")
	}
}

// TestNavigationLockHeldRefuses drives every HEAD-moving navigation command
// and init against a held lock: each must refuse with stack.ErrLocked and
// leave HEAD, refs, state, and undo untouched.
func TestNavigationLockHeldRefuses(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "A\n", "a")
	mustCreate(t, "feat-b", "b.txt", "B\n", "b")
	mustCheckout(t, "feat-a")

	t.Setenv("ST_LOCK_WAIT", "0")
	release, err := stack.Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()

	before := takeRepoSnapshot(t)
	for name, fn := range map[string]func() error{
		"checkout": func() error { return runCheckout([]string{"feat-b"}) },
		"co":       func() error { return runCheckout([]string{"feat-b"}) },
		"up":       func() error { return runUp(nil) },
		"u":        func() error { return runUp(nil) },
		"down":     func() error { return runDown(nil) },
		"d":        func() error { return runDown(nil) },
		"top":      func() error { return runTop(nil) },
		"t":        func() error { return runTop(nil) },
		"bottom":   func() error { return runBottom(nil) },
		"b":        func() error { return runBottom(nil) },
		"init":     func() error { return runInit([]string{"--trunk", "main"}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(); !errors.Is(err, stack.ErrLocked) {
				t.Fatalf("under held lock: err = %v, want stack.ErrLocked", err)
			}
			before.check(t)
		})
	}
}

// TestNavigationLockHeldAllowsReaders proves the lock change did not widen:
// bare `st checkout` (listing), status/log, and the --dry-run previews are
// pure readers and must still succeed while the lock is held.
func TestNavigationLockHeldAllowsReaders(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "A\n", "a")
	mustCreate(t, "feat-b", "b.txt", "B\n", "b")
	mustCheckout(t, "feat-a")

	t.Setenv("ST_LOCK_WAIT", "0")
	release, err := stack.Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()

	for name, fn := range map[string]func() error{
		"checkout-list":   func() error { return runCheckout(nil) },
		"status":          func() error { return runStatus(nil) },
		"log":             func() error { return runLog(nil) },
		"restack-dry-run": func() error { return runRestack([]string{"--dry-run"}) },
		"sync-dry-run":    func() error { return runSync([]string{"--dry-run"}) },
		"validate":        func() error { return runValidate(nil) },
	} {
		if err := fn(); err != nil {
			t.Fatalf("%s under held lock: err = %v, want success", name, err)
		}
	}
}

// TestInitLockFreshRepo covers init's check-then-create before state exists:
// a held lock must refuse the whole operation without creating state.json;
// after release, init creates the stack and a repeated init with a different
// (valid) trunk reports the existing trunk and preserves tracked metadata.
func TestInitLockFreshRepo(t *testing.T) {
	newRepo(t) // not initialized

	t.Setenv("ST_LOCK_WAIT", "0")
	release, err := stack.Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if err := runInit([]string{"--trunk", "main"}); !errors.Is(err, stack.ErrLocked) {
		release()
		t.Fatalf("init under held lock: err = %v, want stack.ErrLocked", err)
	}
	if _, err := os.Stat(".git/stacked/state.json"); !errors.Is(err, os.ErrNotExist) {
		release()
		t.Fatalf("refused init created state.json (stat err = %v)", err)
	}
	release()

	// A failed init (nonexistent trunk) releases the lock it took for the
	// check-then-create — the probe must acquire immediately.
	if err := runInit([]string{"--trunk", "ghost-trunk"}); err == nil {
		t.Fatal("init with ghost trunk succeeded, want error")
	}
	if release, err := stack.Lock(); err != nil {
		t.Fatalf("lock stranded after failed init: %v", err)
	} else {
		release()
	}

	if err := runInit([]string{"--trunk", "main"}); err != nil {
		t.Fatalf("init after release: %v", err)
	}
	mustCreate(t, "feat-a", "a.txt", "A\n", "a")

	// A second init against a different real branch must report the existing
	// trunk and leave the tracked stack alone.
	mustRun(t, "git", "branch", "develop")
	if err := runInit([]string{"--trunk", "develop"}); err != nil {
		t.Fatalf("repeat init: %v", err)
	}
	s := stateT(t)
	if s.Trunk != "main" {
		t.Fatalf("repeat init changed trunk to %q", s.Trunk)
	}
	if !s.IsTracked("feat-a") {
		t.Fatal("repeat init lost tracked branch feat-a")
	}
}

// TestNavigationLockReleasedOnError proves the defer-release covers the error
// paths too: a refused command (unknown target, untracked/detached HEAD,
// missing trunk for init) must not strand the lock — a fresh stack.Lock
// succeeds immediately after each failure.
func TestNavigationLockReleasedOnError(t *testing.T) {
	newRepo(t)
	mustInit(t)
	mustCreate(t, "feat-a", "a.txt", "A\n", "a")
	mustCreate(t, "feat-b", "b.txt", "B\n", "b")
	mustCheckout(t, "feat-a")

	// ST_LOCK_WAIT=0 makes a stranded lock observable deterministically: the
	// probe fails the moment the command's lock is still held.
	t.Setenv("ST_LOCK_WAIT", "0")
	mustLockFree := func() {
		t.Helper()
		release, err := stack.Lock()
		if err != nil {
			t.Fatalf("lock stranded after failed command: %v", err)
		}
		release()
	}

	// Unknown checkout target: validation error under a released lock.
	if err := runCheckout([]string{"ghost"}); err == nil {
		t.Fatal("checkout ghost succeeded, want tracked-check error")
	}
	mustLockFree()

	// Untracked current branch: runUp reads state+HEAD under the lock, then
	// fails the tracked check.
	mustRun(t, "git", "checkout", "-q", "-b", "untracked")
	if err := runUp(nil); err == nil {
		t.Fatal("up on untracked branch succeeded, want error")
	}
	mustLockFree()

	// Detached HEAD: currentBranch fails inside lockAndLoadCurrent; the
	// helper must release before returning that error.
	mustRun(t, "git", "checkout", "-q", "--detach")
	if err := runTop(nil); err == nil {
		t.Fatal("top on detached HEAD succeeded, want error")
	}
	mustLockFree()
	mustCheckout(t, "feat-a")

	// And a successful navigation leaves the lock free as well.
	if err := runDown(nil); err != nil {
		t.Fatalf("down: %v", err)
	}
	mustLockFree()
}

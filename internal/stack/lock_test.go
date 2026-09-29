package stack

// Portable lock-subsystem tests: no build tag, so they run on the unix and
// windows CI legs and compile under `make vet-cross` for plan9. They pin the
// build-tag-free pieces — lock file contents/owner parsing, the contention
// classifiers, and acquireExclLock, the composition Lock() delegates to on
// non-flock platforms (see lock_stale.go). The per-platform lockOwnerIsGone
// bodies are reached through the helpers here on unix and windows; the plan9
// owner check (lock_owner_plan9.go) is compile-only coverage via vet-cross —
// GOOS=plan9 go vet ./... compiles test files too.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLockFileContentRoundTrip pins the lock file's wire format — "<pid>
// <RFC3339 UTC timestamp> <token>\n" — and its parse-back through the shared
// owner helpers. A racing process on another platform must parse the same
// bytes, so the field order is the contract, not an implementation detail.
func TestLockFileContentRoundTrip(t *testing.T) {
	now := time.Now()
	content := lockFileContent(4242, now, "tok-1")

	if !strings.HasSuffix(content, "\n") || strings.Count(content, "\n") != 1 {
		t.Fatalf("lock content %q should be a single newline-terminated line", content)
	}
	fields := strings.Fields(content)
	if len(fields) != 3 {
		t.Fatalf("lock content %q has %d fields, want 3", content, len(fields))
	}
	if fields[0] != "4242" {
		t.Errorf("pid field = %q, want 4242", fields[0])
	}
	ts, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		t.Fatalf("timestamp field %q is not RFC3339: %v", fields[1], err)
	}
	if !ts.Equal(now.UTC().Truncate(time.Second)) {
		t.Errorf("timestamp = %v, want %v (RFC3339 truncates sub-second)", ts, now.UTC().Truncate(time.Second))
	}
	if fields[2] != "tok-1" {
		t.Errorf("token field = %q, want tok-1", fields[2])
	}

	pid, ok := lockOwnerPID(content)
	if !ok || pid != 4242 {
		t.Fatalf("lockOwnerPID(%q) = %d,%v, want 4242,true", content, pid, ok)
	}
	if !lockContentHasOwner(content) {
		t.Error("lockContentHasOwner(valid content) = false")
	}
	if lockContentHasOwner("garbage") || lockContentHasOwner("") {
		t.Error("lockContentHasOwner should reject ownerless content")
	}
}

// TestLockOwnerPID pins the owner-pid parser: it reads only the first
// whitespace-separated field and requires a positive integer — anything else
// fails closed (no owner), which is what keeps malformed locks out of the
// pid-liveness path entirely.
func TestLockOwnerPID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
		ok      bool
	}{
		{name: "full line", content: "1234 2024-01-01T00:00:00Z tok", want: 1234, ok: true},
		{name: "bare pid", content: "7", want: 7, ok: true},
		{name: "leading whitespace", content: "  99 rest", want: 99, ok: true},
		{name: "signed pid", content: "+42 t", want: 42, ok: true},
		{name: "large dead pid", content: "999999999 t", want: 999999999, ok: true},
		{name: "zero", content: "0 t"},
		// The parsed value is returned even when ok=false; only ok is the
		// validity signal.
		{name: "negative", content: "-9 t", want: -9},
		{name: "non-numeric", content: "abc t"},
		{name: "pid suffix junk", content: "12x4 t"},
		{name: "empty", content: ""},
		{name: "whitespace only", content: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, ok := lockOwnerPID(tt.content)
			if ok != tt.ok || pid != tt.want {
				t.Fatalf("lockOwnerPID(%q) = %d,%v, want %d,%v", tt.content, pid, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestNewLockToken pins uniqueness across calls — the token is what stops a
// release from deleting another process's replacement lock.
func TestNewLockToken(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		tok := newLockToken()
		if tok == "" {
			t.Fatal("newLockToken returned empty")
		}
		if seen[tok] {
			t.Fatalf("newLockToken repeated %q", tok)
		}
		seen[tok] = true
	}
}

// TestLockOwnerIsGoneDetectsDeadPID is the positive half of the owner-liveness
// check (the live/fail-closed half is TestLockOwnerIsGoneKeepsCurrentProcess).
// 999999999 is guaranteed nonexistent — above linux's 2^22 pid_max, not a
// multiple of 4 as windows pids require, absent from plan9's /proc — so this
// asserts without racing pid reuse.
func TestLockOwnerIsGoneDetectsDeadPID(t *testing.T) {
	if !lockOwnerIsGone(lockFileContent(999999999, time.Now(), "dead")) {
		t.Fatal("dead owner pid should be reported gone")
	}
}

// TestLockOwnerGoneAt pins the file-reading wrapper around the owner checks:
// anything it cannot interpret fails closed (possibly-live owner), while a
// provably-dead owner or an abandoned malformed file reports gone.
func TestLockOwnerGoneAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.excl")

	if lockOwnerGoneAt(path) {
		t.Fatal("missing lock file should fail closed")
	}

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(lockFileContent(os.Getpid(), time.Now(), "live"))
	if lockOwnerGoneAt(path) {
		t.Fatal("live owner should not be gone")
	}

	write(lockFileContent(999999999, time.Now(), "dead"))
	if !lockOwnerGoneAt(path) {
		t.Fatal("dead owner should be gone")
	}

	write("partial")
	if lockOwnerGoneAt(path) {
		t.Fatal("fresh malformed lock should fail closed")
	}
	old := time.Now().Add(-malformedLockReclaimAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if !lockOwnerGoneAt(path) {
		t.Fatal("abandoned malformed lock should be gone")
	}
}

// TestRemoveLockFileIfContentErrMissing pins the error-returning variant: a
// missing lock file is a real (false, ErrNotExist) result, not a silent
// no-op — callers use it to tell contention from permission failures.
func TestRemoveLockFileIfContentErrMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.excl")
	removed, err := removeLockFileIfContentErr(path, "anything")
	if removed {
		t.Fatal("removed a nonexistent lock file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

// TestLockCreateConflict pins the contention switch on the O_EXCL create
// failure: an existing file is contention (reclaim may proceed); any other
// error is a hard failure so permission and path problems surface instead of
// masquerading as "another command is running".
func TestLockCreateConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.excl")
	if !lockCreateConflict(path, os.ErrExist) {
		t.Fatal("ErrExist should classify as a create conflict")
	}
	if !lockCreateConflict(path, fmt.Errorf("create: %w", os.ErrExist)) {
		t.Fatal("wrapped ErrExist should classify as a create conflict")
	}
	if lockCreateConflict(path, os.ErrNotExist) {
		t.Fatal("a missing parent dir is a hard failure, not contention")
	}
}

// TestAcquireExclLockMissingParent pins the not-a-repo-style failure: when the
// lock's parent directory does not exist the create fails hard with an open
// error, not the busy sentinel.
func TestAcquireExclLockMissingParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-such-dir")
	release, err := acquireExclLock(dir)
	if err == nil {
		release()
		t.Fatal("acquire with a missing parent dir succeeded")
	}
	if isBusyLockErr(err) {
		t.Fatalf("missing parent dir reported as contention: %v", err)
	}
	if !strings.Contains(err.Error(), "open lock file") {
		t.Fatalf("err = %v, want the open lock file failure", err)
	}
}

// TestAcquireExclLockDirIsFile pins the symmetric malformed state: the "dir"
// being a regular file fails the create with a hard error, not busy.
func TestAcquireExclLockDirIsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stacked")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireExclLock(file)
	if err == nil {
		release()
		t.Fatal("acquire inside a regular file succeeded")
	}
	if isBusyLockErr(err) {
		t.Fatalf("dir-as-file reported as contention: %v", err)
	}
	if !strings.Contains(err.Error(), "open lock file") {
		t.Fatalf("err = %v, want the open lock file failure", err)
	}
}

// TestAcquireExclLockPathIsDirectory documents the one malformed state that
// reports busy rather than a hard error: a DIRECTORY at lock.excl is created
// as a create-conflict, but its "contents" are unreadable, so the owner can
// never be proven gone and the reclaimer can never remove it — acquisition
// reports the busy sentinel until the directory is deleted by hand. The pin
// asserts the failure is clean (busy, no panic, reclaim guard released), not
// that it resolves.
func TestAcquireExclLockPathIsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "lock.excl"), 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := acquireExclLock(dir)
	if err == nil {
		release()
		t.Fatal("acquire succeeded over a lock.excl directory")
	}
	if !isBusyLockErr(err) {
		t.Fatalf("lock.excl directory = %v, want the busy sentinel", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "lock.reclaim")); !os.IsNotExist(statErr) {
		t.Fatalf("reclaim guard file left behind: %v", statErr)
	}
}

// TestAcquireExclLockReleaseTwice pins release idempotence: a second call is a
// silent no-op (the lock file is already gone), never a panic.
func TestAcquireExclLockReleaseTwice(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireExclLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	release()
}

// TestAcquireExclLockRecordsOwnerContent pins the on-disk contract a held lock
// exposes to other processes: the file exists, names this process's pid as
// owner, and is removed by release.
func TestAcquireExclLockRecordsOwnerContent(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireExclLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "lock.excl"))
	if err != nil {
		release()
		t.Fatalf("read held lock file: %v", err)
	}
	pid, ok := lockOwnerPID(string(content))
	if !ok || pid != os.Getpid() {
		release()
		t.Fatalf("held lock owner = %d,%v, want pid %d", pid, ok, os.Getpid())
	}

	release()
	if _, err := os.Stat(filepath.Join(dir, "lock.excl")); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed after release, stat err = %v", err)
	}
}

// TestLockReleaseIsIdempotent pins the public Lock() release: safe to call
// twice on either implementation (ignored flock/close errors on unix, a
// missing-file remove off-flock), and the lock stays acquirable afterward.
func TestLockReleaseIsIdempotent(t *testing.T) {
	initGitRepo(t)

	release, err := Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	release()
	release() // second call must not panic

	release2, err := Lock()
	if err != nil {
		t.Fatalf("Lock after double release: %v", err)
	}
	release2()
}

// TestLockStackedDirIsFile pins the malformed .git/stacked state: when the
// stacked metadata path exists as a regular file, Lock() fails at MkdirAll
// with a clean error — identical on both lock implementations because it runs
// before the flock/excl split.
func TestLockStackedDirIsFile(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".git", "stacked"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	release, err := Lock()
	if err == nil {
		release()
		t.Fatal("Lock succeeded with .git/stacked as a regular file")
	}
	if isBusyLockErr(err) {
		t.Fatalf("malformed stacked dir reported as contention: %v", err)
	}
	if !strings.Contains(err.Error(), "create stacked dir") {
		t.Fatalf("err = %v, want the create stacked dir failure", err)
	}
}

// TestLockOutsideGitRepo pins the outermost failure: outside a repository
// Lock() cannot even locate .git, and reports that rather than a lock error.
func TestLockOutsideGitRepo(t *testing.T) {
	t.Chdir(t.TempDir())

	release, err := Lock()
	if err == nil {
		release()
		t.Fatal("Lock succeeded outside a git repository")
	}
	if !strings.Contains(err.Error(), "locate git dir") {
		t.Fatalf("err = %v, want the locate git dir failure", err)
	}
}

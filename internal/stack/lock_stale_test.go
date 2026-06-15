package stack

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoveLockFileIfContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.excl")
	if err := os.WriteFile(path, []byte("owner-a"), 0o600); err != nil {
		t.Fatal(err)
	}

	if removeLockFileIfContent(path, "owner-b") {
		t.Fatal("removed lock with different owner content")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should remain after mismatched remove: %v", err)
	}
	if !removeLockFileIfContent(path, "owner-a") {
		t.Fatal("did not remove matching lock content")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed, stat err = %v", err)
	}
}

func TestLockOwnerIsGoneKeepsCurrentProcess(t *testing.T) {
	content := lockFileContent(os.Getpid(), time.Now(), "token")
	if lockOwnerIsGone(content) {
		t.Fatal("current process should be treated as live")
	}
	if lockOwnerIsGone("not a lock file") {
		t.Fatal("unparseable owner should fail closed")
	}
}

func TestMalformedLockIsAbandonedOnlyWhenOldAndUnowned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.excl")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if malformedLockIsAbandoned(path, "partial", now) {
		t.Fatal("fresh malformed lock should fail closed")
	}
	old := now.Add(-malformedLockReclaimAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if !malformedLockIsAbandoned(path, "partial", now) {
		t.Fatal("old malformed lock should be abandoned")
	}
	owned := lockFileContent(os.Getpid(), old, "token")
	if malformedLockIsAbandoned(path, owned, now) {
		t.Fatal("parseable owner lock should not use malformed recovery")
	}
}

func TestAcquireReclaimGuardDoesNotRecoverOldLockWithLiveOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.reclaim")
	oldLiveOwner := lockFileContent(os.Getpid(), time.Now().Add(-2*time.Hour), "token")
	if err := os.WriteFile(path, []byte(oldLiveOwner), 0o600); err != nil {
		t.Fatal(err)
	}
	if release, ok := acquireReclaimGuard(dir); ok {
		release()
		t.Fatal("reclaim guard should not recover an old lock while its owner pid is live")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("old live-owner lock should remain: %v", err)
	}
	if string(got) != oldLiveOwner {
		t.Fatalf("old live-owner lock changed:\n got %q\nwant %q", got, oldLiveOwner)
	}
}

func TestAcquireReclaimGuard(t *testing.T) {
	dir := t.TempDir()
	release, ok := acquireReclaimGuard(dir)
	if !ok {
		t.Fatal("first reclaim guard acquisition failed")
	}
	if _, ok := acquireReclaimGuard(dir); ok {
		t.Fatal("second reclaim guard acquisition should fail")
	}
	release()
	if _, err := os.Stat(filepath.Join(dir, "lock.reclaim")); !os.IsNotExist(err) {
		t.Fatalf("guard file should be removed, stat err = %v", err)
	}
}

func TestAcquireReclaimGuardRecoversMalformedOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.reclaim")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-malformedLockReclaimAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	release, ok := acquireReclaimGuard(dir)
	if !ok {
		t.Fatal("reclaim guard should recover old malformed file")
	}
	release()
}

func TestAcquireReclaimGuardRecoversDeadOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.reclaim")
	deadOwner := lockFileContent(999999999, time.Now(), "dead")
	if err := os.WriteFile(path, []byte(deadOwner), 0o600); err != nil {
		t.Fatal(err)
	}

	release, ok := acquireReclaimGuard(dir)
	if !ok {
		t.Fatal("reclaim guard should recover dead owner")
	}
	release()
}

// The following exercise the composed exclusive-lock acquisition (the Lock body
// that ships on non-flock platforms) directly, so the path is covered by the
// unix test binary even though lock_other.go's Lock is build-tagged off it.

func TestAcquireExclLockSecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireExclLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := acquireExclLock(dir); err == nil {
		t.Fatal("second acquire while held should fail")
	}
	release()
	if _, err := os.Stat(filepath.Join(dir, "lock.excl")); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed after release, stat err = %v", err)
	}
	// After release a fresh acquire succeeds.
	release2, err := acquireExclLock(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

func TestAcquireExclLockReclaimsDeadOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.excl")
	dead := lockFileContent(999999999, time.Now(), "dead")
	if err := os.WriteFile(path, []byte(dead), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireExclLock(dir)
	if err != nil {
		t.Fatalf("acquire should reclaim a dead owner's lock: %v", err)
	}
	release()
}

func TestAcquireExclLockKeepsFreshMalformedLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.excl")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireExclLock(dir); err == nil {
		release()
		t.Fatal("a fresh malformed lock should not be reclaimed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fresh malformed lock should remain: %v", err)
	}
}

func TestAcquireExclLockReleaseLeavesReplacement(t *testing.T) {
	dir := t.TempDir()
	release, err := acquireExclLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Another process replaces the lock with its own token; our release must not
	// remove it.
	path := filepath.Join(dir, "lock.excl")
	other := lockFileContent(os.Getpid(), time.Now(), "other-token")
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	release()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement lock should remain after our release: %v", err)
	}
	if string(got) != other {
		t.Fatalf("release removed or altered another owner's lock: %q", got)
	}
}

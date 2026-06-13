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

func TestOwnedLockIsStale(t *testing.T) {
	now := time.Now()
	if fresh := lockFileContent(os.Getpid(), now, "token"); ownedLockIsStale(fresh, now) {
		t.Fatal("fresh owned lock should not be stale")
	}
	old := lockFileContent(os.Getpid(), now.Add(-ownedLockReclaimAfter-time.Minute), "token")
	if !ownedLockIsStale(old, now) {
		t.Fatal("owned lock past the age bound should be stale (guards against pid reuse)")
	}
	if ownedLockIsStale("partial", now) {
		t.Fatal("malformed content should not be treated as a stale owned lock")
	}
	if ownedLockIsStale("123 not-a-timestamp token", now) {
		t.Fatal("owned lock with an unparseable timestamp should not be treated as stale")
	}
}

// A live-looking owner (our own pid) whose recorded acquisition time is past the
// bound is the pid-reuse case the age bound exists to recover: the original
// holder died and its pid was recycled.
func TestAcquireReclaimGuardRecoversStaleOwnedLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.reclaim")
	stale := lockFileContent(os.Getpid(), time.Now().Add(-ownedLockReclaimAfter-time.Minute), "token")
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	release, ok := acquireReclaimGuard(dir)
	if !ok {
		t.Fatal("reclaim guard should recover a stale owned lock with a live pid")
	}
	release()
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

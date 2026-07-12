package stack

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func isBusyLockErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "another st command is running in this repository")
}

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
	if release, err := acquireReclaimGuard(dir); err == nil && release != nil {
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
	release, err := acquireReclaimGuard(dir)
	if err != nil || release == nil {
		t.Fatalf("first reclaim guard acquisition failed: %v", err)
	}
	if second, err := acquireReclaimGuard(dir); err == nil && second != nil {
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

	release, err := acquireReclaimGuard(dir)
	if err != nil || release == nil {
		t.Fatalf("reclaim guard should recover old malformed file: %v", err)
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

	release, err := acquireReclaimGuard(dir)
	if err != nil || release == nil {
		t.Fatalf("reclaim guard should recover dead owner: %v", err)
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

func TestAcquireExclLockMutualExclusion(t *testing.T) {
	dir := t.TempDir()
	const goroutines = 8
	const iterations = 25

	var holders atomic.Int32
	errCh := make(chan string, goroutines*iterations*2)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				var release func()
				for {
					var err error
					release, err = acquireExclLock(dir)
					if err == nil {
						break
					}
					if !isBusyLockErr(err) {
						errCh <- err.Error()
						return
					}
					time.Sleep(10 * time.Microsecond)
				}
				if got := holders.Add(1); got != 1 {
					errCh <- "concurrent lock holders observed"
				}
				time.Sleep(100 * time.Microsecond)
				if got := holders.Add(-1); got != 0 {
					errCh <- "holder count did not return to zero"
				}
				release()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "lock.excl")); !os.IsNotExist(err) {
		t.Fatalf("lock.excl should be removed after all releases, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lock.reclaim")); !os.IsNotExist(err) {
		t.Fatalf("lock.reclaim should be removed after all releases, stat err = %v", err)
	}
}

func TestStaleLockSingleReclaimer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.excl")
	dead := lockFileContent(999999999, time.Now(), "dead")
	if err := os.WriteFile(path, []byte(dead), 0o600); err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	start := make(chan struct{})
	type result struct {
		release func()
		err     error
	}
	results := make(chan result, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			<-start
			release, err := acquireExclLock(dir)
			results <- result{release: release, err: err}
		}()
	}
	close(start)

	var releases []func()
	for i := 0; i < goroutines; i++ {
		res := <-results
		switch {
		case res.err == nil:
			releases = append(releases, res.release)
		case isBusyLockErr(res.err):
		default:
			t.Fatalf("unexpected acquire error: %v", res.err)
		}
	}
	if len(releases) != 1 {
		for _, release := range releases {
			release()
		}
		t.Fatalf("successful reclaimers = %d, want 1", len(releases))
	}
	releases[0]()
	if _, err := os.Stat(filepath.Join(dir, "lock.excl")); !os.IsNotExist(err) {
		t.Fatalf("lock.excl should be removed after winner release, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lock.reclaim")); !os.IsNotExist(err) {
		t.Fatalf("lock.reclaim should be removed after reclaim race, stat err = %v", err)
	}
}

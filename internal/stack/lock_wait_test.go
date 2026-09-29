package stack

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLockWaitBudget pins the ST_LOCK_WAIT parser: Go durations and bare
// seconds are accepted, empty/zero disable waiting, and anything else is a
// loud startup error naming the variable.
func TestLockWaitBudget(t *testing.T) {
	tests := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{raw: "", want: 0},
		{raw: "0", want: 0},
		{raw: "0s", want: 0},
		{raw: "10", want: 10 * time.Second},
		{raw: "10s", want: 10 * time.Second},
		{raw: "500ms", want: 500 * time.Millisecond},
		{raw: "1h30m", want: 90 * time.Minute},
		{raw: "0.5", want: 500 * time.Millisecond},
		{raw: "-5", wantErr: true},
		{raw: "-5s", wantErr: true},
		{raw: "abc", wantErr: true},
		{raw: "10x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run("ST_LOCK_WAIT="+tt.raw, func(t *testing.T) {
			t.Setenv("ST_LOCK_WAIT", tt.raw)
			got, err := lockWaitBudget()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("lockWaitBudget(%q) = %v, want error", tt.raw, got)
				}
				if !strings.Contains(err.Error(), "ST_LOCK_WAIT") {
					t.Fatalf("error %q does not name ST_LOCK_WAIT", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("lockWaitBudget(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("lockWaitBudget(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// TestLockWaitAcquireRetries pins the retry loop: ErrLocked is retried at the
// fixed interval until the budget runs out; success mid-wait returns the
// release immediately.
func TestLockWaitAcquireRetries(t *testing.T) {
	calls := 0
	acquire := func() (func(), error) {
		calls++
		if calls < 3 {
			return nil, ErrLocked
		}
		return func() {}, nil
	}
	start := time.Now()
	release, err := lockWaitAcquire(acquire, time.Second)
	if err != nil {
		t.Fatalf("lockWaitAcquire: %v", err)
	}
	if release == nil {
		t.Fatal("release is nil")
	}
	if calls != 3 {
		t.Fatalf("acquire called %d times, want 3", calls)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("two retries should take >= 200ms, took %v", elapsed)
	}
}

// TestLockWaitAcquireBudgetExhausted pins the timeout contract: the error that
// comes back is still ErrLocked (exit 5 / "locked"), never a timeout variant.
func TestLockWaitAcquireBudgetExhausted(t *testing.T) {
	calls := 0
	start := time.Now()
	release, err := lockWaitAcquire(func() (func(), error) {
		calls++
		return nil, ErrLocked
	}, 250*time.Millisecond)
	if err == nil {
		release()
		t.Fatal("lockWaitAcquire succeeded with the lock permanently held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked unchanged on budget exhaustion", err)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("waited %v, want >= the ~200ms budget", elapsed)
	}
	if calls < 3 {
		t.Fatalf("acquire called %d times, want at least initial + 2 retries", calls)
	}
}

// TestLockWaitAcquireSkipsNonContention pins the two error classes that must
// NOT be retried: the abandoned reclaim guard (operator maintenance, not
// patience) and hard failures like permissions. Both return on the first call.
func TestLockWaitAcquireSkipsNonContention(t *testing.T) {
	for name, want := range map[string]error{
		"guard":         &abandonedReclaimGuardError{path: "/x/lock.reclaim"},
		"permission":    fmt.Errorf("open lock file: %w", syscall.EACCES),
		"wrapped-guard": fmt.Errorf("ctx: %w", &abandonedReclaimGuardError{path: "/y"}),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			start := time.Now()
			release, err := lockWaitAcquire(func() (func(), error) {
				calls++
				return nil, want
			}, 10*time.Second)
			if err == nil {
				release()
				t.Fatal("succeeded")
			}
			if calls != 1 {
				t.Fatalf("acquire called %d times, want 1 (no retry)", calls)
			}
			if elapsed := time.Since(start); elapsed >= lockWaitInterval {
				t.Fatalf("non-contention error waited %v; must return immediately", elapsed)
			}
			if err != want {
				t.Fatalf("err = %v, want the error passed through unchanged", err)
			}
		})
	}
}

// TestLockWaitsThroughRealContention exercises the full path against the real
// platform lock: a held Lock() is retried past the wait interval and acquired
// the moment the holder releases.
func TestLockWaitsThroughRealContention(t *testing.T) {
	initGitRepo(t)
	t.Setenv("ST_LOCK_WAIT", "2s")

	release, err := Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		release()
	}()

	release2, err := Lock()
	if err != nil {
		t.Fatalf("Lock with ST_LOCK_WAIT=2s failed while the holder released at 150ms: %v", err)
	}
	release2()
}

// TestLockWaitBudgetExhaustedOnRealLock pins the observable contract: a lock
// held past the budget still fails with ErrLocked after approximately the
// budget — never sooner, never a different error class.
func TestLockWaitBudgetExhaustedOnRealLock(t *testing.T) {
	initGitRepo(t)
	t.Setenv("ST_LOCK_WAIT", "300ms")

	release, err := Lock()
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()

	start := time.Now()
	release2, err := Lock()
	if err == nil {
		release2()
		t.Fatal("Lock succeeded while the lock was held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked (exit-5 contract)", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("waited %v, want ~300ms", elapsed)
	}
}

// TestLockInvalidWaitEnv pins the fail-fast: a malformed ST_LOCK_WAIT is a
// startup error naming the variable, before any lock attempt.
func TestLockInvalidWaitEnv(t *testing.T) {
	initGitRepo(t)
	t.Setenv("ST_LOCK_WAIT", "bogus")

	release, err := Lock()
	if err == nil {
		release()
		t.Fatal("Lock succeeded with ST_LOCK_WAIT=bogus")
	}
	if !strings.Contains(err.Error(), "ST_LOCK_WAIT") {
		t.Fatalf("err = %v, want it to name ST_LOCK_WAIT", err)
	}
}

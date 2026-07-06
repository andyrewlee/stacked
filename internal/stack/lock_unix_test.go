//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package stack

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockIsExclusive asserts a second Lock() in the same repository fails fast
// with the contention error while the first is held — the serialization mutating
// commands rely on to avoid corrupting state. flock-less platforms run a no-op
// Lock and are excluded by the build constraint.
func TestLockIsExclusive(t *testing.T) {
	initGitRepo(t)

	release, err := Lock()
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}

	if _, err := Lock(); err == nil {
		release()
		t.Fatal("second Lock succeeded while the lock was held, want a contention error")
	} else if !strings.Contains(err.Error(), "another st command is running") {
		release()
		t.Fatalf("second Lock error = %v, want the contention message", err)
	}

	release()

	// Acquirable again after release.
	release2, err := Lock()
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	release2()
}

func TestLockConcurrentAcquirers(t *testing.T) {
	initGitRepo(t)
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
					release, err = Lock()
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
					errCh <- "concurrent Lock holders observed"
				}
				time.Sleep(100 * time.Microsecond)
				if got := holders.Add(-1); got != 0 {
					errCh <- "Lock holder count did not return to zero"
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
}

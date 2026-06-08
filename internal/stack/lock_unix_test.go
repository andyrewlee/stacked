//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package stack

import (
	"strings"
	"testing"
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

//go:build windows

package stack

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLockCreateConflictRetryStatLoop pins the two exits of the access-denied
// stat probe: a path that exists classifies as contention immediately, and a
// path that never appears exhausts the lockFileAccessRetryAttempts budget and
// classifies as a real (non-contention) failure. Runs on the ci-windows leg.
func TestLockCreateConflictRetryStatLoop(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "present.lock")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !lockCreateConflictRetry(present, windowsAccessDenied) {
		t.Fatal("access-denied with an existing lock file should classify as contention")
	}

	missing := filepath.Join(dir, "missing.lock")
	start := time.Now()
	if lockCreateConflictRetry(missing, windowsAccessDenied) {
		t.Fatal("access-denied with a never-appearing lock file should not classify as contention")
	}
	// The negative exit must consume the retry budget (~1ms per attempt);
	// allow generous scheduling slack but catch a short-circuited loop.
	if elapsed := time.Since(start); elapsed < lockFileAccessRetryAttempts*time.Millisecond/2 {
		t.Fatalf("stat loop returned after %v; expected it to consume the %d-attempt budget", elapsed, lockFileAccessRetryAttempts)
	}

	// A sharing violation short-circuits as contention without the stat loop.
	if !lockCreateConflictRetry(missing, windowsSharingViolation) {
		t.Fatal("sharing violation should classify as contention regardless of the path")
	}
}

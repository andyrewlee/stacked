//go:build windows

package stack

import (
	"os"
	"time"
)

// retryableLockFileAccess gates the read/remove retry loop: on windows a
// transient access-denied or sharing violation (AV scanner, indexer) is worth
// retrying. The classification itself lives in lock_access.go.
func retryableLockFileAccess(err error) bool {
	return lockAccessDeniedErr(err) || lockSharingViolationErr(err)
}

// lockCreateConflictRetry decides whether an O_CREATE|O_EXCL failure is lock
// contention: a sharing violation always is; an access-denied counts only if
// the lock file is observed to exist within the retry budget (a persistent
// access-denied on a path that never appears is a real permission problem,
// not contention).
func lockCreateConflictRetry(path string, err error) bool {
	if lockSharingViolationErr(err) {
		return true
	}
	if !lockAccessDeniedErr(err) {
		return false
	}
	for attempt := 0; attempt < lockFileAccessRetryAttempts; attempt++ {
		if _, statErr := os.Stat(path); statErr == nil {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

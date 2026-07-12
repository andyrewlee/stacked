package stack

import (
	"errors"
	"os"
	"strings"
)

// The classifiers below are the contract for interpreting file-access errors
// on the off-flock lock path. They compile on every GOOS so the
// classification table is unit-tested on unix CI, even though the retry
// behavior that consumes them is windows-only; the raw-errno matching lives
// behind the errnoIs* hooks because plan9 has no syscall.Errno. Future
// Windows lock flakes should add a table row here, not another inline
// substring check.

// lockAccessDeniedErr reports whether err reads as a permission/access-denied
// failure: os.ErrPermission (EACCES/EPERM on unix, ERROR_ACCESS_DENIED wraps
// on windows), the raw windows errno, or the windows message fallback.
func lockAccessDeniedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) || errnoIsAccessDenied(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "access is denied")
}

// lockSharingViolationErr reports whether err reads as a windows sharing
// violation — another process holding the file open (AV scanner, indexer, or
// a racing st) — which is always transient contention, never a permission
// problem.
func lockSharingViolationErr(err error) bool {
	if err == nil {
		return false
	}
	if errnoIsSharingViolation(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sharing violation")
}

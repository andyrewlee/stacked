//go:build windows

package stack

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	windowsAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
	windowsSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
)

func retryableLockFileAccess(err error) bool {
	return windowsAccessDeniedErr(err) || windowsSharingViolationErr(err)
}

func lockCreateConflictRetry(path string, err error) bool {
	if windowsSharingViolationErr(err) {
		return true
	}
	if !windowsAccessDeniedErr(err) {
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

func windowsAccessDeniedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) ||
		errors.Is(err, windowsAccessDenied) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "access is denied")
}

func windowsSharingViolationErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, windowsSharingViolation) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sharing violation")
}

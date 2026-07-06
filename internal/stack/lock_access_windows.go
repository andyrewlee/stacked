//go:build windows

package stack

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

const (
	windowsAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
	windowsSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
)

func retryableLockFileAccess(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) ||
		errors.Is(err, windowsAccessDenied) ||
		errors.Is(err, windowsSharingViolation) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "sharing violation")
}

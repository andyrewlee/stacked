//go:build windows

package stack

import (
	"errors"
	"os"
	"syscall"
)

const (
	windowsAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
	windowsSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
)

func retryableLockFileAccess(err error) bool {
	return errors.Is(err, os.ErrPermission) ||
		errors.Is(err, windowsAccessDenied) ||
		errors.Is(err, windowsSharingViolation)
}

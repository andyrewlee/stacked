//go:build !plan9

package stack

import (
	"errors"
	"syscall"
)

const (
	windowsAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
	windowsSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
)

func errnoIsAccessDenied(err error) bool {
	return errors.Is(err, windowsAccessDenied)
}

func errnoIsSharingViolation(err error) bool {
	return errors.Is(err, windowsSharingViolation)
}

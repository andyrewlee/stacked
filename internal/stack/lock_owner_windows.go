//go:build windows

package stack

import (
	"errors"
	"os"
	"syscall"
)

func lockOwnerIsGone(content string) bool {
	pid, ok := lockOwnerPID(content)
	if !ok {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		// OpenProcess returns ERROR_INVALID_PARAMETER when no process has this
		// PID. Other errors, especially access denied, fail closed.
		return errors.Is(err, syscall.Errno(87))
	}
	defer func() { _ = p.Release() }()
	gone := false
	if err := p.WithHandle(func(handle uintptr) {
		status, err := syscall.WaitForSingleObject(syscall.Handle(handle), 0)
		gone = err == nil && status == syscall.WAIT_OBJECT_0
	}); err != nil {
		return false
	}
	return gone
}

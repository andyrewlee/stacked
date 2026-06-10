//go:build windows

package stack

import (
	"errors"
	"os"
	"strconv"
	"strings"
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

func lockOwnerPID(content string) (int, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	return pid, err == nil && pid > 0
}

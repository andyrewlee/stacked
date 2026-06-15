//go:build !windows && !plan9

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
		return false
	}
	defer func() { _ = p.Release() }()
	err = p.Signal(syscall.Signal(0))
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}

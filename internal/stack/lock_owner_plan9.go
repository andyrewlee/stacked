//go:build plan9

package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

func lockOwnerIsGone(content string) bool {
	pid, ok := lockOwnerPID(content)
	if !ok {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return errors.Is(err, os.ErrNotExist)
}

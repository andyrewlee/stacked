//go:build plan9

package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func lockOwnerIsGone(content string) bool {
	pid, ok := lockOwnerPID(content)
	if !ok {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return errors.Is(err, os.ErrNotExist)
}

func lockOwnerPID(content string) (int, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	return pid, err == nil && pid > 0
}

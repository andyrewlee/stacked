//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd

package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lock acquires an exclusive lock that serializes mutating stacked commands
// across concurrent processes in the same repository. On platforms without
// flock it is an O_CREATE|O_EXCL lock file holding the owner's pid and an
// RFC3339 timestamp; the returned release function removes it. Unlike flock,
// the OS does not clean up after a killed process, so a lock file older than
// lockStaleAfter is treated as abandoned and reclaimed.
func Lock() (func(), error) {
	dir, err := stackedDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create stacked dir: %w", err)
	}
	path := filepath.Join(dir, "lock.excl")
	// Two attempts: the second runs only after a stale lock was reclaimed.
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := f.WriteString(lockFileContent(os.Getpid(), time.Now())); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write lock file: %w", err)
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("close lock file: %w", err)
			}
			return func() {
				// Best-effort cleanup; a leftover file is reclaimed as stale.
				_ = os.Remove(path)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		content, _ := os.ReadFile(path)
		var mtime time.Time
		if info, statErr := os.Stat(path); statErr == nil {
			mtime = info.ModTime()
		}
		if attempt == 0 && lockIsStale(string(content), mtime, time.Now()) {
			_ = os.Remove(path)
			continue
		}
		return nil, errors.New("another st command is running in this repository")
	}
	return nil, errors.New("another st command is running in this repository")
}

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
// RFC3339 timestamp; the returned release function removes it only when the
// file still contains this process's ownership token. Unlike flock, the OS does
// not clean up after a killed process, so a later process may reclaim a lock
// only after proving the recorded owner is gone.
func Lock() (func(), error) {
	dir, err := stackedDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create stacked dir: %w", err)
	}
	path := filepath.Join(dir, "lock.excl")
	token := newLockToken()
	contents := lockFileContent(os.Getpid(), time.Now(), token)
	releaseGuard := func() {}
	defer func() { releaseGuard() }()
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := f.WriteString(contents); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write lock file: %w", err)
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("close lock file: %w", err)
			}
			releaseGuard()
			releaseGuard = func() {}
			return func() {
				// Best-effort cleanup; remove only the lock this process owns.
				_ = removeLockFileIfContent(path, contents)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		if attempt > 0 {
			return nil, errors.New("another st command is running in this repository")
		}
		guardRelease, ok := acquireReclaimGuard(dir)
		if !ok {
			return nil, errors.New("another st command is running in this repository")
		}
		releaseGuard = guardRelease
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		now := time.Now()
		if (!lockOwnerIsGone(string(existing)) && !ownedLockIsStale(string(existing), now) && !malformedLockIsAbandoned(path, string(existing), now)) ||
			!removeLockFileIfContent(path, string(existing)) {
			return nil, errors.New("another st command is running in this repository")
		}
	}
	return nil, errors.New("another st command is running in this repository")
}

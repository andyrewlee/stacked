//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquirePlatformLock takes the platform lock once — flock here, an exclusive
// lock file on platforms without flock (see lock_other.go). The public Lock
// (lock_wait.go) wraps it in the ST_LOCK_WAIT retry loop. The returned release
// function unlocks and closes the lock file; the lock is also released
// automatically if the process exits.
func acquirePlatformLock() (func(), error) {
	dir, err := stackedDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create stacked dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock stacked state: %w", err)
	}
	return func() {
		// Best-effort cleanup: unlock and close. Errors here are not actionable
		// (the OS releases the flock and fd on process exit regardless).
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

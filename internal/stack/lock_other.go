//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd

package stack

import (
	"fmt"
	"os"
)

// acquirePlatformLock takes the platform lock once — an O_CREATE|O_EXCL lock
// file holding the owner's pid and an RFC3339 timestamp on platforms without
// flock. The returned release function removes it only when the file still
// contains this process's ownership token. Unlike flock, the OS does not clean
// up after a killed process, so a later process may reclaim a lock only after
// proving the recorded owner is gone. The acquisition itself lives in
// acquireExclLock (lock_stale.go, build-tag-free) so the unix suite can test
// it; the public Lock (lock_wait.go) adds the ST_LOCK_WAIT retry loop.
func acquirePlatformLock() (func(), error) {
	dir, err := stackedDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create stacked dir: %w", err)
	}
	return acquireExclLock(dir)
}

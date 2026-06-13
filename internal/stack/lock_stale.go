package stack

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const malformedLockReclaimAfter = 10 * time.Minute

// ownedLockReclaimAfter bounds how long a well-formed lock whose recorded owner
// still resolves to a live process is honored. A single mutating command
// finishes in seconds, so a lock older than this was almost certainly left by a
// process that died (without running its release) whose pid has since been
// reused by an unrelated live process — which defeats the signal-0 liveness
// check and would otherwise make the lock permanently un-reclaimable.
const ownedLockReclaimAfter = time.Hour

// newLockToken returns an opaque per-acquisition token used to avoid removing
// another process's replacement lock during release.
func newLockToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

// lockFileContent renders an exclusive lock file's contents: the holder's pid,
// the RFC3339 acquisition time, and a per-acquisition token.
func lockFileContent(pid int, now time.Time, token string) string {
	return fmt.Sprintf("%d %s %s\n", pid, now.UTC().Format(time.RFC3339), token)
}

func removeLockFileIfContent(path, want string) bool {
	content, err := os.ReadFile(path)
	if err != nil || string(content) != want {
		return false
	}
	return os.Remove(path) == nil
}

func lockContentHasOwner(content string) bool {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return false
	}
	pid, err := strconv.Atoi(fields[0])
	return err == nil && pid > 0
}

func malformedLockIsAbandoned(path, content string, now time.Time) bool {
	if lockContentHasOwner(content) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) > malformedLockReclaimAfter
}

// lockContentAcquiredAt parses the RFC3339 acquisition time embedded as the
// second field of a lock file's contents. The boolean is false when the
// timestamp is absent or unparseable.
func lockContentAcquiredAt(content string) (time.Time, bool) {
	fields := strings.Fields(content)
	if len(fields) < 2 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ownedLockIsStale reports whether a well-formed (owned) lock is old enough to
// reclaim despite its pid currently resolving to a live process. This bounds
// the pid-reuse hazard the way malformedLockIsAbandoned bounds malformed files:
// without it, a dead holder whose pid was recycled to an unrelated live process
// would leave the lock un-reclaimable forever.
func ownedLockIsStale(content string, now time.Time) bool {
	if !lockContentHasOwner(content) {
		return false
	}
	acquired, ok := lockContentAcquiredAt(content)
	if !ok {
		return false
	}
	return now.Sub(acquired) > ownedLockReclaimAfter
}

func acquireReclaimGuard(dir string) (func(), bool) {
	path := filepath.Join(dir, "lock.reclaim")
	token := newLockToken()
	contents := lockFileContent(os.Getpid(), time.Now(), token)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := f.WriteString(contents); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, false
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return nil, false
			}
			return func() {
				_ = removeLockFileIfContent(path, contents)
			}, true
		}
		if attempt > 0 {
			return nil, false
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		now := time.Now()
		if (!lockOwnerIsGone(string(existing)) && !ownedLockIsStale(string(existing), now) && !malformedLockIsAbandoned(path, string(existing), now)) ||
			!removeLockFileIfContent(path, string(existing)) {
			return nil, false
		}
	}
	return nil, false
}

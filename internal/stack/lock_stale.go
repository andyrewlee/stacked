package stack

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	malformedLockReclaimAfter = 10 * time.Minute
	// lockFileAccessRetryAttempts bounds the 1ms-sleep retry loops around lock
	// file reads/removes and the create-conflict stat probe: ~100ms worst case,
	// sized for transient windows holds by AV scanners and indexers (bumped
	// from 10 after ci-windows flakes; a persistent failure past the budget is
	// treated as real, not contention).
	lockFileAccessRetryAttempts = 100
)

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
	ok, _ := removeLockFileIfContentErr(path, want)
	return ok
}

// removeLockFileIfContentErr removes path while it still holds exactly want.
// When the removal fails for a reason other than a content mismatch, the
// final read/remove error is returned so callers can distinguish a permission
// failure from ordinary contention.
func removeLockFileIfContentErr(path, want string) (bool, error) {
	var lastErr error
	for attempt := 0; attempt < lockFileAccessRetryAttempts; attempt++ {
		content, err := os.ReadFile(path)
		if err != nil {
			if retryableLockFileAccess(err) {
				lastErr = err
				time.Sleep(time.Millisecond)
				continue
			}
			return false, err
		}
		if string(content) != want {
			return false, nil
		}
		err = os.Remove(path)
		if err == nil {
			return true, nil
		}
		if !retryableLockFileAccess(err) {
			return false, err
		}
		lastErr = err
		time.Sleep(time.Millisecond)
	}
	return false, lastErr
}

func lockCreateConflict(path string, err error) bool {
	if errors.Is(err, os.ErrExist) {
		return true
	}
	return lockCreateConflictRetry(path, err)
}

// lockOwnerPID parses the holder pid recorded on a lock file's first line,
// reporting ok=false for a malformed or non-positive pid. It is the single
// parser shared by lockContentHasOwner and every platform's lockOwnerIsGone.
func lockOwnerPID(content string) (int, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	return pid, err == nil && pid > 0
}

func lockContentHasOwner(content string) bool {
	_, ok := lockOwnerPID(content)
	return ok
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

// acquireReclaimGuard takes the lock.reclaim guard file that serializes stale
// lock reclamation. On failure the returned func is nil; the error, when
// non-nil, is the underlying cause (used by acquireExclLock to tell a real
// permission problem from ordinary contention — a nil error means plain
// contention).
func acquireReclaimGuard(dir string) (func(), error) {
	path := filepath.Join(dir, "lock.reclaim")
	token := newLockToken()
	contents := lockFileContent(os.Getpid(), time.Now(), token)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := f.WriteString(contents); err != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, err
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() {
				_ = removeLockFileIfContent(path, contents)
			}, nil
		}
		if !lockCreateConflict(path, err) || attempt > 0 {
			return nil, err
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		if !lockOwnerIsGone(string(existing)) && !malformedLockIsAbandoned(path, string(existing), time.Now()) {
			return nil, nil
		}
		if removed, rmErr := removeLockFileIfContentErr(path, string(existing)); !removed {
			return nil, rmErr
		}
	}
	return nil, nil
}

// acquireExclLock takes an exclusive O_CREATE|O_EXCL lock file in dir — the
// serialization primitive for platforms without flock — and returns a release
// that removes the file only while it still holds this process's token. A held
// lock is reclaimed only after proving the recorded owner is gone (or a
// malformed lock is abandoned), guarded by lock.reclaim so two reclaimers cannot
// race. It carries no build tag so the composed path is exercised by the unix
// test suite, even though lock_other.go's Lock wrapper only ships off-flock.
func acquireExclLock(dir string) (func(), error) {
	busy := errors.New("another st command is running in this repository")
	path := filepath.Join(dir, "lock.excl")
	token := newLockToken()
	contents := lockFileContent(os.Getpid(), time.Now(), token)
	// The reclaim guard, once taken, is released however we leave the function.
	var guardRelease func()
	defer func() {
		if guardRelease != nil {
			guardRelease()
		}
	}()
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
			return func() {
				// Best-effort cleanup; remove only the lock this process owns.
				_ = removeLockFileIfContent(path, contents)
			}, nil
		}
		if !lockCreateConflict(path, err) {
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		if attempt > 0 {
			return nil, busy
		}
		gr, gerr := acquireReclaimGuard(dir)
		if gr == nil {
			// A guard failure is normally contention (another reclaimer, or a
			// live holder). But when the recorded owner is provably gone and the
			// failure classifies as access-denied, "wait for the other command"
			// is a lie — the lock is stale and this process cannot act on it.
			if gerr != nil && lockAccessDeniedErr(gerr) && lockOwnerGoneAt(path) {
				return nil, fmt.Errorf("cannot reclaim stale lock %s (owner is gone): %w — check directory permissions", path, gerr)
			}
			return nil, busy
		}
		guardRelease = gr
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		if !lockOwnerIsGone(string(existing)) && !malformedLockIsAbandoned(path, string(existing), time.Now()) {
			return nil, busy
		}
		if removed, rmErr := removeLockFileIfContentErr(path, string(existing)); !removed {
			// The owner is gone but the stale file cannot be removed: surface a
			// permission failure instead of pointing the user at a process that
			// does not exist. Anything else stays the busy sentinel (e.g. the
			// content changed because a racing reclaimer won).
			if rmErr != nil && lockAccessDeniedErr(rmErr) {
				return nil, fmt.Errorf("cannot reclaim stale lock %s (owner is gone): %w — check directory permissions", path, rmErr)
			}
			return nil, busy
		}
	}
	return nil, busy
}

// lockOwnerGoneAt reports whether the lock file at path records an owner that
// is provably gone, or is malformed and abandoned; read failures fail closed
// (treated as a possibly-live owner).
func lockOwnerGoneAt(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return lockOwnerIsGone(string(content)) || malformedLockIsAbandoned(path, string(content), time.Now())
}

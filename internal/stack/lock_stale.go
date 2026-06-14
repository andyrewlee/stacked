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

const malformedLockReclaimAfter = 10 * time.Minute

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
		if (!lockOwnerIsGone(string(existing)) && !malformedLockIsAbandoned(path, string(existing), time.Now())) ||
			!removeLockFileIfContent(path, string(existing)) {
			return nil, false
		}
	}
	return nil, false
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
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		if attempt > 0 {
			return nil, busy
		}
		gr, ok := acquireReclaimGuard(dir)
		if !ok {
			return nil, busy
		}
		guardRelease = gr
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		if (!lockOwnerIsGone(string(existing)) && !malformedLockIsAbandoned(path, string(existing), time.Now())) ||
			!removeLockFileIfContent(path, string(existing)) {
			return nil, busy
		}
	}
	return nil, busy
}

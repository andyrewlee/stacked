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

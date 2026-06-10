package stack

import (
	"testing"
	"time"
)

// TestLockIsStale pins the pure reclaim decision shared by the non-flock lock
// (the syscalls stay build-tagged; this runs on every platform).
func TestLockIsStale(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	old := now.Add(-lockStaleAfter - time.Minute)
	fresh := now.Add(-time.Minute)

	cases := []struct {
		name    string
		content string
		mtime   time.Time
		want    bool
	}{
		{"fresh recorded timestamp", lockFileContent(123, fresh), time.Time{}, false},
		{"old recorded timestamp", lockFileContent(123, old), time.Time{}, true},
		{"old timestamp beats fresh mtime", lockFileContent(123, old), fresh, true},
		{"fresh timestamp beats old mtime", lockFileContent(123, fresh), old, false},
		{"garbage content falls back to old mtime", "not a lock file", old, true},
		{"garbage content falls back to fresh mtime", "not a lock file", fresh, false},
		{"no timestamp at all keeps the lock", "", time.Time{}, false},
		{"exactly at the threshold keeps the lock", lockFileContent(123, now.Add(-lockStaleAfter)), time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lockIsStale(c.content, c.mtime, now); got != c.want {
				t.Fatalf("lockIsStale(%q, mtime=%v) = %v, want %v", c.content, c.mtime, got, c.want)
			}
		})
	}
}

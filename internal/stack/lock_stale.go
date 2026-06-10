package stack

import (
	"fmt"
	"strings"
	"time"
)

// lockStaleAfter is how old an exclusive lock file must be before another
// process may assume its holder died without releasing it and reclaim the
// lock. Deliberately conservative: no st operation should hold the lock for
// minutes, but a false reclaim could let two mutations interleave.
const lockStaleAfter = 10 * time.Minute

// lockFileContent renders an exclusive lock file's contents: the holder's pid
// and the RFC3339 acquisition time, "pid timestamp\n".
func lockFileContent(pid int, now time.Time) string {
	return fmt.Sprintf("%d %s\n", pid, now.UTC().Format(time.RFC3339))
}

// lockIsStale decides whether an existing exclusive lock file may be
// reclaimed: its recorded acquisition timestamp — falling back to the file
// mtime when the content does not parse — is older than lockStaleAfter. With
// no usable timestamp at all the lock is kept (never reclaim on a guess).
func lockIsStale(content string, mtime, now time.Time) bool {
	acquired := mtime
	if fields := strings.Fields(content); len(fields) >= 2 {
		if ts, err := time.Parse(time.RFC3339, fields[1]); err == nil {
			acquired = ts
		}
	}
	if acquired.IsZero() {
		return false
	}
	return now.Sub(acquired) > lockStaleAfter
}

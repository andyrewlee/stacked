package stack

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// lockWaitInterval is the fixed poll interval between lock attempts while an
// ST_LOCK_WAIT budget remains. A CLI lock does not need backoff.
const lockWaitInterval = 100 * time.Millisecond

// Lock acquires the repo's advisory lock, honoring ST_LOCK_WAIT: when the
// platform acquisition fails with ErrLocked it retries every lockWaitInterval
// until the budget set by the env var is exhausted, then returns the last
// ErrLocked unchanged (callers keep mapping it to exit 5 / "locked"). Errors
// that are not live contention — including ErrReclaimGuardAbandoned, which
// needs an operator — are returned without waiting.
func Lock() (func(), error) {
	budget, err := lockWaitBudget()
	if err != nil {
		return nil, err
	}
	return lockWaitAcquire(acquirePlatformLock, budget)
}

// lockWaitAcquire retries acquire on ErrLocked until budget is spent. The
// acquire func is a parameter so tests can drive the loop without real locks.
func lockWaitAcquire(acquire func() (func(), error), budget time.Duration) (func(), error) {
	release, err := acquire()
	for errors.Is(err, ErrLocked) && budget > 0 {
		time.Sleep(lockWaitInterval)
		budget -= lockWaitInterval
		release, err = acquire()
	}
	return release, err
}

// lockWaitBudget parses ST_LOCK_WAIT: a Go duration ("500ms", "10s", "1m") or
// bare seconds ("10" → 10s). Empty, "0", and zero-valued durations mean no
// waiting. Anything else — unparseable, negative — is a startup error naming
// the var rather than a silent ignore.
func lockWaitBudget() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("ST_LOCK_WAIT"))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		secs, ferr := strconv.ParseFloat(raw, 64)
		if ferr != nil {
			return 0, fmt.Errorf("invalid ST_LOCK_WAIT %q: expected a Go duration (500ms, 10s) or bare seconds (10)", raw)
		}
		d = time.Duration(secs * float64(time.Second))
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid ST_LOCK_WAIT %q: wait cannot be negative", raw)
	}
	return d, nil
}

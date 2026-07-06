//go:build !windows

package stack

func retryableLockFileAccess(error) bool {
	return false
}

func lockCreateConflictRetry(string, error) bool {
	return false
}

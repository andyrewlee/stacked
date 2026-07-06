//go:build !windows

package stack

func retryableLockFileAccess(error) bool {
	return false
}

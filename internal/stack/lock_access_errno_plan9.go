//go:build plan9

package stack

// plan9 has no syscall.Errno; its syscall errors are strings, which the
// message fallbacks in lock_access.go already cover.

func errnoIsAccessDenied(error) bool { return false }

func errnoIsSharingViolation(error) bool { return false }

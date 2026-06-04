//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd

package stack

// Lock is a no-op on platforms without flock support; mutating commands there
// run without cross-process serialization.
func Lock() (func(), error) {
	return func() {}, nil
}

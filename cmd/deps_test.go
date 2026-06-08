package cmd

import (
	"runtime/debug"
	"testing"
)

// TestNoModuleDependencies enforces the project's hardest invariant in the normal
// test run: the shipped tool stays standard-library only (zero go.mod requires).
// `make check-deps` guards go.mod/go.sum at the file level; this catches an actual
// linked-in dependency so a new import can never land green.
func TestNoModuleDependencies(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("build info unavailable")
	}
	if n := len(info.Deps); n != 0 {
		var names []string
		for _, d := range info.Deps {
			names = append(names, d.Path)
		}
		t.Errorf("module has %d dependencies, want 0 (standard library only): %v", n, names)
	}
}

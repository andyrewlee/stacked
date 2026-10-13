package git

import (
	"errors"
	"testing"
)

// TestParallelProbesPanicRepansOnCaller pins the exit-code contract: a panic
// inside a worker goroutine must not kill the process with Go's runtime exit
// 2 (the code st reserves for conflicts). The panic is captured per slot and
// re-raised on the calling goroutine — where the caller's recover renders
// the internal-error path — with the original payload intact.
func TestParallelProbesPanicRepansOnCaller(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = ParallelProbes(4, func(i int) error {
			if i == 2 {
				panic("probe kaboom")
			}
			return nil
		})
	}()
	if recovered == nil {
		t.Fatal("worker panic did not reach the calling goroutine")
	}
	if recovered.(string) != "probe kaboom" {
		t.Fatalf("recovered = %v, want the original panic payload", recovered)
	}
}

// The drain returns the lowest-indexed failure whatever its kind: an error at
// an earlier index beats a later panic, exactly as the serial loop would
// report the first error it hit.
func TestParallelProbesErrorPrecedesPanic(t *testing.T) {
	err := ParallelProbes(4, func(i int) error {
		if i == 1 {
			return errors.New("probe one failed")
		}
		if i == 3 {
			panic("probe three")
		}
		return nil
	})
	if err == nil || err.Error() != "probe one failed" {
		t.Fatalf("err = %v, want the lowest-indexed failure (index 1's error)", err)
	}
}

// The inverse ordering: a panic at index 0 outranks an error at index 1 —
// index order decides, not the kind of failure.
func TestParallelProbesPanicPrecedesError(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = ParallelProbes(4, func(i int) error {
			if i == 0 {
				panic("probe zero")
			}
			if i == 1 {
				return errors.New("probe one failed")
			}
			return nil
		})
	}()
	if recovered == nil || recovered.(string) != "probe zero" {
		t.Fatalf("recovered = %v, want the lowest-indexed failure (index 0's panic)", recovered)
	}
}

// The n == 1 fast path runs inline on the caller's goroutine — a panic there
// propagates directly, never through the capture machinery.
func TestParallelProbesSinglePanicsInline(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = ParallelProbes(1, func(i int) error {
			panic("inline boom")
		})
	}()
	if recovered == nil || recovered.(string) != "inline boom" {
		t.Fatalf("recovered = %v, want the inline panic", recovered)
	}
}

// A worker that panics mid-sweep leaves its unclaimed slots untouched — the
// slots before the panic still carry their errors, and the drain reports the
// panic first because it sits at the lower index.
func TestParallelProbesWorkerStopsAfterPanic(t *testing.T) {
	var recovered any
	var err error
	func() {
		defer func() { recovered = recover() }()
		err = ParallelProbes(6, func(i int) error {
			switch i {
			case 0, 1:
				return nil
			case 2:
				panic("probe two")
			default:
				return errors.New("later probe failed")
			}
		})
	}()
	if recovered == nil || recovered.(string) != "probe two" {
		t.Fatalf("recovered = %v, want the lower-indexed panic to win", recovered)
	}
	if err != nil {
		t.Fatalf("err = %v, want nil beside the re-panicked failure", err)
	}
}

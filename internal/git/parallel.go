package git

import (
	"sync"
	"sync/atomic"
)

// ParallelProbes runs fn(i) for i in [0, n) on a bounded set of goroutines —
// the fan-out for independent read-only git probes, where every fn is a
// subprocess spawn whose only cost is wall time. The bound keeps a wide fan
// (dozens of branches) from stampeding the process table; call-site ordering
// stays deterministic because fn must write only its own result slot and the
// returned error is the lowest-indexed non-nil one, the same choice a serial
// loop would make. Probes that feed a mid-loop decision are NOT candidates —
// this is for loops whose results are consumed after the fan completes.
func ParallelProbes(n int, fn func(i int) error) error {
	if n <= 1 {
		// A single probe gains nothing from the machinery — run it inline.
		if n == 1 {
			return fn(0)
		}
		return nil
	}
	errs := make([]error, n)
	var next atomic.Int64
	next.Store(-1)
	var wg sync.WaitGroup
	for w := 0; w < min(n, 8); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int(next.Add(1)); i < n; i = int(next.Add(1)) {
				errs[i] = fn(i)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

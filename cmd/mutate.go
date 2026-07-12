package cmd

// The single implementation of the locking/undo/save protocol every mutation
// runs through: lock -> load -> record undo -> op -> save -> finalize.

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

// acquireLock takes the repository stack lock; the caller must defer the
// returned release function. It serializes mutating commands across concurrent
// st processes (a no-op on platforms without flock).
func acquireLock() (func(), error) {
	return stack.Lock()
}

// lockAndLoad acquires the stack lock and then loads the state, so a mutating
// command holds the lock across its whole read-modify-write. The caller must
// defer the returned release function. stack.ErrNotInitialized is returned
// unchanged for callers to print.
func lockAndLoad() (*stack.State, func(), error) {
	release, err := acquireLock()
	if err != nil {
		return nil, nil, err
	}
	s, err := loadState()
	if err != nil {
		release()
		return nil, nil, err
	}
	return s, release, nil
}

// mutateState runs a stack-mutating op under the repo lock with the undo-snapshot
// protocol: it records an undo entry, runs op, and on success persists and
// finalizes the entry (trimming it, or dropping it when op was a no-op); on
// error it cleans up the tentative entry. It does NOT render — callers render
// from the (possibly mutated) state. This is the single implementation of the
// locking/undo/save protocol; mutate is the common case, and commands with a
// custom result shape (sync, repair) call it directly. The op closure may close
// over a Remote, so remote-dependent mutations fit the same protocol.
func mutateState(label string, asJSON bool, op func(stack.Env, *stack.State) error) error {
	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()
	env := stackEnv(s, asJSON)
	if err := s.RecordUndo(env.Git, label); err != nil {
		return err
	}
	undoEntry, _, _ := stack.PeekUndo()
	if err := op(env, s); err != nil {
		if cleanupErr := stack.CleanupUndoOnError(env.Git, s, err); cleanupErr != nil {
			return stack.AlsoFailed(err, "clean up undo entry", cleanupErr)
		}
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("saving stack state: %w", err)
	}
	if err := stack.FinalizeUndo(env.Git, s, undoEntry); err != nil {
		return fmt.Errorf("finalizing undo entry: %w", err)
	}
	return nil
}

// mutate runs an engine operation that returns an OpResult through mutateState
// and renders the result, so each command stays a thin adapter.
func mutate(label string, asJSON bool, op func(stack.Env, *stack.State) (*stack.OpResult, error)) error {
	var res *stack.OpResult
	if err := mutateState(label, asJSON, func(env stack.Env, s *stack.State) error {
		r, err := op(env, s)
		res = r
		return err
	}); err != nil {
		return err
	}
	return renderResult(res, asJSON)
}

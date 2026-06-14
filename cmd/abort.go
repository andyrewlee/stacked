package cmd

import (
	"errors"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "abort",
		Summary: "Abort an in-progress restack/rebase, restoring the prior state",
		Usage:   "st abort [--json]",
		Run:     runAbort,
	})
}

// runAbort aborts an in-progress rebase via the engine (stack.Abort). Branches
// that had already been restacked before the conflict keep their new positions;
// only the branch git was mid-rebase on is rolled back. It works even when
// stacked is not initialized in this repo.
func runAbort(args []string) error {
	var asJSON bool
	fs := newFlagSet("abort", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("abort", fs.Args()); err != nil {
		return err
	}

	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	// Tolerate an uninitialized repo: the rebase abort must still run, so load
	// state best-effort and pass nil when stacked was never initialized here.
	s, err := loadState()
	if err != nil {
		if !errors.Is(err, stack.ErrNotInitialized) {
			return err
		}
		s = nil
	}
	res, err := stack.Abort(stackEnv(s, asJSON), s)
	if err != nil {
		return err
	}
	if s != nil {
		if err := s.Save(); err != nil {
			return fmt.Errorf("saving stack state after abort: %w", err)
		}
	}

	payload := struct {
		Aborted bool   `json:"aborted"`
		Summary string `json:"summary"`
	}{true, res.Summary}
	return emit(asJSON, payload, func() {
		out("Aborted the in-progress rebase.\n")
		out("Branches that already restacked keep their new positions; the conflicted branch still needs a restack (run: st restack).\n")
	})
}

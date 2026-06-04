package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "abort",
		Summary: "Abort an in-progress restack/rebase, restoring the prior state",
		Usage:   "st abort",
		Run:     runAbort,
	})
}

// runAbort aborts an in-progress rebase (git rebase --abort). Branches that had
// already been restacked before the conflict keep their new positions; only the
// branch git was mid-rebase on is rolled back, and the stack metadata already
// reflects that it still needs a restack.
func runAbort(args []string) error {
	fs := flag.NewFlagSet("abort", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st abort [--json]") }
	if err := fs.Parse(args); err != nil {
		return err
	}

	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	inProgress, err := git.RebaseInProgress()
	if err != nil {
		return err
	}
	if !inProgress {
		return fmt.Errorf("no rebase in progress; nothing to abort")
	}
	if err := git.RebaseAbort(); err != nil {
		return fmt.Errorf("aborting rebase: %w", err)
	}

	payload := struct {
		Aborted bool   `json:"aborted"`
		Summary string `json:"summary"`
	}{true, "aborted the in-progress rebase"}
	return emit(asJSON, payload, func() {
		out("Aborted the in-progress rebase.\n")
		out("Branches that already restacked keep their new positions; the conflicted branch still needs a restack (run: st restack).\n")
	})
}

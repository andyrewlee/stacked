package cmd

import (
	"fmt"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "undo",
		Summary: "Undo the last stack-mutating command",
		Usage:   "st undo [--json]",
		Run:     runUndo,
	})
}

// runUndo reverts the most recent mutating command by handing its recorded
// snapshot to the engine (stack.Undo): the stack metadata is rolled back and
// every recorded branch is reset to its prior tip. It does not touch the
// working tree, so uncommitted changes are preserved.
func runUndo(args []string) error {
	var asJSON bool
	fs := newFlagSet("undo", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("undo", fs.Args()); err != nil {
		return err
	}

	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	if inProgress, err := git.RebaseInProgress(); err != nil {
		return err
	} else if inProgress {
		return fmt.Errorf("cannot undo while a rebase is in progress; run st abort or resolve conflicts and run st continue")
	}

	entry, ok, err := stack.PeekUndo()
	if err != nil {
		return err
	}
	if !ok {
		return emit(asJSON, struct {
			Undone bool `json:"undone"`
		}{false}, func() { out("nothing to undo\n") })
	}

	// The current state informs which branches the undone command created; when
	// it cannot be loaded the engine still reverts from the snapshot alone, and
	// the snapshot bytes are persisted directly.
	env := stack.Env{Git: gitShell}
	s, loadErr := stack.Load()
	if loadErr == nil {
		env.Save = s.Save
	} else {
		s = nil
		env.Save = func() error { return stack.RestoreState(entry.State) }
	}

	res, err := stack.Undo(env, s, entry)
	if err != nil {
		return err
	}
	if err := stack.DropUndo(); err != nil {
		return fmt.Errorf("dropping undo entry: %w", err)
	}

	restored := res.Restacked
	if restored == nil {
		restored = []string{}
	}
	payload := struct {
		Undone   bool     `json:"undone"`
		Label    string   `json:"label"`
		Restored []string `json:"restored"`
	}{true, entry.Label, restored}
	return emit(asJSON, payload, func() {
		out("undid: %s\n", entry.Label)
		if len(restored) > 0 {
			out("restored branches: %s\n", joinNames(restored))
		}
		out("note: your working tree was not modified; run `git status` to review.\n")
	})
}

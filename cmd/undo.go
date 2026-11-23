package cmd

import (
	"errors"
	"fmt"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "undo",
		Summary:    "Undo the last stack-mutating command",
		Usage:      "st undo [--force] [--json]",
		Run:        runUndo,
		NewFlagSet: undoFlagSet,
	})
}

type undoRunResult struct {
	Undone   bool     `json:"undone"`
	Label    string   `json:"label,omitempty"`
	Restored []string `json:"restored,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// runUndo reverts the most recent mutating command by handing its recorded
// snapshot to stack.Undo: stack metadata is rolled back and every recorded
// branch is restored to its prior tip without touching the working tree.
func runUndo(args []string) error {
	var o undoOpts
	fs := newUndoFlags(&o)
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
		return errors.New(stack.UndoRebaseGateMsg)
	}

	entry, ok, err := stack.PeekUndo()
	if err != nil {
		return err
	}
	if !ok {
		return emit(o.asJSON, undoRunResult{Undone: false}, func() {
			out("nothing to undo\n")
		})
	}

	s, loadErr := stack.Load()
	if loadErr != nil {
		if errors.Is(loadErr, stack.ErrStateTooNew) {
			return loadErr
		}
		s = nil
	}

	if err := stack.ValidateUndoState(entry.State); err != nil {
		return fmt.Errorf("parsing undo state: %w", err)
	}

	env := stack.Env{Git: newGitPort()}
	if s != nil {
		env.Save = s.Save
	} else {
		raw := entry.State
		env.Save = func() error { return stack.RestoreState(raw) }
	}

	res, err := stack.Undo(env, s, entry, o.force)
	if err != nil {
		return err
	}
	if err := stack.DropUndo(); err != nil {
		return fmt.Errorf("dropping undo entry: %w", err)
	}

	restored := append([]string(nil), res.Restacked...)
	sort.Strings(restored)
	if restored == nil {
		restored = []string{}
	}
	payload := undoRunResult{
		Undone:   true,
		Label:    entry.Label,
		Restored: restored,
		Notes:    res.Notes,
	}
	return emit(o.asJSON, payload, func() {
		out("undid: %s\n", sanitizeForTerminal(entry.Label))
		if len(restored) > 0 {
			out("restored branches: %s\n", joinTerminalNames(restored))
		}
		for _, n := range res.Notes {
			out("note: %s\n", sanitizeForTerminal(n))
		}
		out("note: your working tree was not modified; run `git status` to review.\n")
	})
}

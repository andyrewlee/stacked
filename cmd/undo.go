package cmd

import (
	"fmt"
	"os"

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
	asJSON, err := parsePlain("undo", args)
	if err != nil {
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
	if err := prepareUndoCurrentCreatedWorktree(entry); err != nil {
		return err
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
		out("undid: %s\n", sanitizeForTerminal(entry.Label))
		if len(restored) > 0 {
			out("restored branches: %s\n", joinTerminalNames(restored))
		}
		out("note: your working tree was not modified; run `git status` to review.\n")
	})
}

func prepareUndoCurrentCreatedWorktree(entry *stack.UndoEntry) error {
	cur, err := currentBranch()
	if err != nil {
		return nil
	}
	if !undoEntryCreatedBranch(entry, cur) {
		return nil
	}
	wts, err := worktrees()
	if err != nil {
		return err
	}
	owner, ok := stack.LinkedOwnerOf(wts, cur)
	if !ok {
		return nil
	}
	main, ok := stack.MainWorktree(wts)
	if !ok || main.Path == "" {
		return fmt.Errorf("cannot undo creation of current worktree branch %q: main worktree not found", cur)
	}
	dest := main.Path
	if entry.CurrentBranch != "" {
		if wt, ok := stack.LinkedOwnerOf(wts, entry.CurrentBranch); ok {
			dest = wt.Path
		}
	}
	if !shimActive() {
		return fmt.Errorf("cannot undo creation of current worktree branch %q from inside its worktree %q without the shell shim; run from the main worktree or run: cd %s && st undo", cur, owner.Path, main.Path)
	}
	if err := os.Chdir(main.Path); err != nil {
		return fmt.Errorf("leaving worktree %q before undo: %w", owner.Path, err)
	}
	writeCDDirective(dest)
	return nil
}

func undoEntryCreatedBranch(entry *stack.UndoEntry, name string) bool {
	if entry == nil || name == "" {
		return false
	}
	for _, created := range entry.CreatedBranches {
		if created == name {
			return true
		}
	}
	if entry.LocalBranches == nil {
		return false
	}
	for _, existed := range entry.LocalBranches {
		if existed == name {
			return false
		}
	}
	return true
}

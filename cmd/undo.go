package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "undo",
		Summary:    "Undo the last stack-mutating command",
		Usage:      "st undo [--list] [--json]",
		Run:        runUndo,
		NewFlagSet: undoFlagSet,
	})
}

// runUndo reverts the most recent mutating command by handing its recorded
// snapshot to the engine (stack.Undo): the stack metadata is rolled back and
// every recorded branch is reset to its prior tip. It does not touch the
// working tree, so uncommitted changes are preserved. With --list it only
// prints the journal — a pure read, so it runs without the repo lock (like
// the other read commands) and is allowed while a rebase is in progress.
func runUndo(args []string) error {
	var o undoOpts
	fs := newUndoFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("undo", fs.Args()); err != nil {
		return err
	}
	if o.list {
		return runUndoList(o.asJSON)
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
		return emit(o.asJSON, struct {
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
	cdAfterSuccess, err := prepareUndoCurrentCreatedWorktree(entry, s)
	if err != nil {
		return err
	}

	res, err := stack.Undo(env, s, entry)
	if err != nil {
		return err
	}
	if err := stack.DropUndo(); err != nil {
		return fmt.Errorf("dropping undo entry: %w", err)
	}
	if cdAfterSuccess != "" {
		writeCDDirective(cdAfterSuccess)
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
	return emit(o.asJSON, payload, func() {
		out("undid: %s\n", sanitizeForTerminal(entry.Label))
		if len(restored) > 0 {
			out("restored branches: %s\n", joinTerminalNames(restored))
		}
		out("note: your working tree was not modified; run `git status` to review.\n")
	})
}

func prepareUndoCurrentCreatedWorktree(entry *stack.UndoEntry, s *stack.State) (string, error) {
	cur, err := currentBranch()
	if err != nil {
		return "", nil
	}
	if !undoEntryCreatedWorktree(entry, cur) {
		return "", nil
	}
	wts, err := worktrees()
	if err != nil {
		return "", err
	}
	owner, ok := stack.LinkedOwnerOf(wts, cur)
	if !ok {
		return "", nil
	}
	main, ok := stack.MainWorktree(wts)
	if !ok || main.Path == "" {
		return "", fmt.Errorf("cannot undo creation of current worktree branch %q: main worktree not found", cur)
	}
	dest := main.Path
	if entry.CurrentBranch != "" {
		if wt, ok := stack.LinkedOwnerOf(wts, entry.CurrentBranch); ok {
			dest = wt.Path
		}
	}
	if !shimActive() {
		return "", fmt.Errorf("cannot undo creation of current worktree branch %q from inside its worktree %q without the shell shim; run from the main worktree or run: cd %s && st undo", cur, owner.Path, main.Path)
	}
	if err := os.Chdir(main.Path); err != nil {
		return "", fmt.Errorf("leaving worktree %q before undo: %w", owner.Path, err)
	}
	if inProgress, err := git.RebaseInProgress(); err != nil {
		return "", err
	} else if inProgress {
		return "", fmt.Errorf("cannot undo while a rebase is in progress in the main worktree %q; run st abort or resolve conflicts and run st continue there", main.Path)
	}
	return dest, nil
}

func undoEntryCreatedWorktree(entry *stack.UndoEntry, name string) bool {
	if entry == nil || name == "" {
		return false
	}
	return entry.CreatedWorktrees[name] != ""
}

// undoListEntry is the --list projection of a journal entry: the fields a user
// needs to decide whether undoing is safe — what the op was (label), which
// branches it created and undo would delete (createdBranches, with their
// createdWorktrees), where HEAD would land (currentBranch), and which tips
// would move back (refs). The state snapshot and the captured local-branch
// list are restore plumbing, not display data, so they stay out. index counts
// down from the newest entry: 1 is what a bare `st undo` reverts, so a future
// multi-step undo can reference positions the list already numbers.
type undoListEntry struct {
	Index            int               `json:"index"`
	Label            string            `json:"label"`
	CurrentBranch    string            `json:"currentBranch,omitempty"`
	CreatedBranches  []string          `json:"createdBranches,omitempty"`
	CreatedWorktrees map[string]string `json:"createdWorktrees,omitempty"`
	Refs             map[string]string `json:"refs"`
}

// runUndoList prints the undo journal newest-first without touching it. The
// empty journal matches bare undo's empty case: "nothing to undo" in text,
// {"entries": []} in JSON, exit 0 either way.
func runUndoList(asJSON bool) error {
	entries, err := stack.ListUndo()
	if err != nil {
		return err
	}
	payload := struct {
		Entries []undoListEntry `json:"entries"`
	}{Entries: []undoListEntry{}}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		refs := e.Refs
		if refs == nil {
			refs = map[string]string{}
		}
		payload.Entries = append(payload.Entries, undoListEntry{
			Index:            len(entries) - i,
			Label:            e.Label,
			CurrentBranch:    e.CurrentBranch,
			CreatedBranches:  e.CreatedBranches,
			CreatedWorktrees: e.CreatedWorktrees,
			Refs:             refs,
		})
	}
	return emit(asJSON, payload, func() {
		if len(payload.Entries) == 0 {
			out("nothing to undo\n")
			return
		}
		for _, e := range payload.Entries {
			line := fmt.Sprintf("%d: %s", e.Index, sanitizeForTerminal(e.Label))
			var bits []string
			if len(e.CreatedBranches) > 0 {
				bits = append(bits, "created "+joinTerminalNames(e.CreatedBranches))
			}
			if len(e.CreatedWorktrees) > 0 {
				names := make([]string, 0, len(e.CreatedWorktrees))
				for name := range e.CreatedWorktrees {
					names = append(names, name)
				}
				sort.Strings(names)
				bits = append(bits, "worktrees "+joinTerminalNames(names))
			}
			if e.CurrentBranch != "" {
				bits = append(bits, "on "+sanitizeForTerminal(e.CurrentBranch))
			}
			if len(bits) > 0 {
				line += " (" + strings.Join(bits, "; ") + ")"
			}
			out("%s\n", line)
		}
	})
}

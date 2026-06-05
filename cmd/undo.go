package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "undo",
		Summary: "Undo the last stack-mutating command",
		Usage:   "st undo",
		Run:     runUndo,
	})
}

// runUndo reverts the most recent mutating command by restoring the snapshot it
// recorded: the stack metadata is rolled back and every recorded branch is reset
// to its prior tip. It does not touch the working tree, so uncommitted changes
// are preserved.
func runUndo(args []string) error {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st undo [--json]") }
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

	var prev stack.State
	if err := json.Unmarshal(entry.State, &prev); err != nil {
		return fmt.Errorf("parsing undo state: %w", err)
	}
	checkoutAfterRestore := ""
	skipCheckoutRestore := false
	current, currentErr := stack.Load()
	if entry.LocalBranches != nil {
		candidates := map[string]bool{}
		if currentErr == nil {
			candidates[current.Trunk] = true
			for name := range current.Branches {
				candidates[name] = true
			}
		}
		for _, name := range entry.CreatedBranches {
			candidates[name] = true
		}
		var extra []string
		for name := range candidates {
			if branchCreatedByEntry(entry, name) && git.BranchExists(name) {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		for _, name := range extra {
			target := prev.Trunk
			if currentErr == nil {
				if b, ok := current.Get(name); ok && git.BranchExists(b.Parent) {
					target = b.Parent
				}
			}
			if cur, err := git.CurrentBranch(); err == nil && cur == name {
				if entry.Label == "rename" {
					checkoutAfterRestore = restoredRenameTarget(&prev, current, name)
				}
				if checkoutAfterRestore == "" && entry.Label == "rename" {
					checkoutAfterRestore = missingRestoredRef(entry)
				}
				if !git.BranchExists(target) {
					sha, ok := entry.Refs[target]
					if !ok {
						return fmt.Errorf("cannot restore checkout target %q before deleting %q", target, name)
					}
					if err := git.UpdateRef("refs/heads/"+target, sha); err != nil {
						return fmt.Errorf("restoring branch %q before deleting %q: %w", target, name, err)
					}
				}
				if err := git.Checkout(target); err != nil {
					if checkoutBlockedByLocalChanges(err) {
						head, revErr := git.RevParse("HEAD")
						if revErr != nil {
							return fmt.Errorf("resolving HEAD before deleting %q: %w", name, revErr)
						}
						if detachErr := git.CheckoutDetach(head); detachErr != nil {
							return fmt.Errorf("detaching HEAD before deleting %q: %w", name, detachErr)
						}
						skipCheckoutRestore = true
					} else {
						return fmt.Errorf("checking out %q before deleting %q: %w", target, name, err)
					}
				}
			}
			if err := git.DeleteBranch(name, true); err != nil {
				return fmt.Errorf("deleting branch %q created by undone command: %w", name, err)
			}
		}
	}

	if err := stack.RestoreState(entry.State); err != nil {
		return fmt.Errorf("restoring stack state: %w", err)
	}

	names := make([]string, 0, len(entry.Refs))
	for name := range entry.Refs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := git.UpdateRef("refs/heads/"+name, entry.Refs[name]); err != nil {
			return fmt.Errorf("restoring branch %q: %w", name, err)
		}
	}
	if !skipCheckoutRestore && checkoutAfterRestore == "" && entry.CurrentBranch != "" && git.BranchExists(entry.CurrentBranch) {
		checkoutAfterRestore = entry.CurrentBranch
	}
	if checkoutAfterRestore != "" {
		if err := git.Checkout(checkoutAfterRestore); err != nil {
			if !checkoutBlockedByLocalChanges(err) {
				return fmt.Errorf("checking out restored branch %q: %w", checkoutAfterRestore, err)
			}
		}
	}
	if err := stack.DropUndo(); err != nil {
		return fmt.Errorf("dropping undo entry: %w", err)
	}

	if names == nil {
		names = []string{}
	}
	payload := struct {
		Undone   bool     `json:"undone"`
		Label    string   `json:"label"`
		Restored []string `json:"restored"`
	}{true, entry.Label, names}
	return emit(asJSON, payload, func() {
		out("undid: %s\n", entry.Label)
		if len(names) > 0 {
			out("restored branches: %s\n", joinNames(names))
		}
		out("note: your working tree was not modified; run `git status` to review.\n")
	})
}

func restoredRenameTarget(prev, current *stack.State, deleted string) string {
	if current == nil {
		return ""
	}
	if current.Trunk == deleted && prev.Trunk != current.Trunk {
		return prev.Trunk
	}
	for name := range prev.Branches {
		if !current.IsTracked(name) {
			return name
		}
	}
	return ""
}

func missingRestoredRef(entry *stack.UndoEntry) string {
	var missing []string
	for name := range entry.Refs {
		if !git.BranchExists(name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) == 1 {
		return missing[0]
	}
	return ""
}

func branchCreatedByEntry(entry *stack.UndoEntry, name string) bool {
	for _, created := range entry.CreatedBranches {
		if created == name {
			return true
		}
	}
	for _, existed := range entry.LocalBranches {
		if existed == name {
			return false
		}
	}
	return true
}

func checkoutBlockedByLocalChanges(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "local changes") || strings.Contains(msg, "would be overwritten")
}

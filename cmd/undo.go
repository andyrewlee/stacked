package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"

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
	if err := fs.Parse(args); err != nil {
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
	if current, err := stack.Load(); err == nil && entry.LocalBranches != nil {
		existed := map[string]bool{}
		for _, name := range entry.LocalBranches {
			existed[name] = true
		}
		candidates := map[string]bool{current.Trunk: true}
		for name := range current.Branches {
			candidates[name] = true
		}
		var extra []string
		for name := range candidates {
			if !existed[name] && git.BranchExists(name) {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		for _, name := range extra {
			target := prev.Trunk
			if b, ok := current.Get(name); ok && git.BranchExists(b.Parent) {
				target = b.Parent
			}
			if cur, err := git.CurrentBranch(); err == nil && cur == name {
				if entry.Label == "rename" {
					checkoutAfterRestore = restoredRenameTarget(&prev, current, name)
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
					return fmt.Errorf("checking out %q before deleting %q: %w", target, name, err)
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
	if checkoutAfterRestore != "" {
		if err := git.Checkout(checkoutAfterRestore); err != nil {
			return fmt.Errorf("checking out restored branch %q: %w", checkoutAfterRestore, err)
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

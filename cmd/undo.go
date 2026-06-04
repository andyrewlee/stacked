package cmd

import (
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

	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	entry, ok, err := stack.PopUndo()
	if err != nil {
		return err
	}
	if !ok {
		return emit(asJSON, struct {
			Undone bool `json:"undone"`
		}{false}, func() { out("nothing to undo\n") })
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

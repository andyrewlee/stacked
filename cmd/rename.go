package cmd

import (
	"fmt"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "rename",
		Aliases: []string{"mv"},
		Summary: "Rename a branch and update the stack metadata",
		Usage:   "st rename [old] <new> [--json]",
		Run:     runRename,
	})
}

func runRename(args []string) error {
	var asJSON bool
	fs := newFlagSet("rename", &asJSON)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 1 || len(rest) > 2 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("rename requires a new name (and optionally the old name)")
	}
	oldName, newName := "", rest[0]
	if len(rest) == 2 {
		oldName, newName = rest[0], rest[1]
	}
	if err := git.CheckBranchName(newName); err != nil {
		return err
	}

	return mutate("rename", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Rename(env, s, oldName, newName)
	})
}

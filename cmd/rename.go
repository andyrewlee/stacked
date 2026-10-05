package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "rename",
		Aliases:    []string{"mv"},
		Summary:    "Rename a branch and update the stack metadata",
		Usage:      "st rename [old] <new> [--dry-run] [--json]",
		Run:        runRename,
		NewFlagSet: renameFlagSet,
		// rename's first positional is the branch to rename — trunk or tracked;
		// the second is the new name, which has no candidates.
		Completion: func(cc *completionCtx) []string {
			if cc.flagName != "" || len(cc.positionals) != 0 {
				return nil
			}
			return trackedAndTrunk(cc.s)
		},
	})
}

func runRename(args []string) error {
	var o renameOpts
	fs := newRenameFlags(&o)
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
	if o.dryRun {
		return preview(o.asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			return stack.RenamePlan(env, s, oldName, newName)
		})
	}

	return mutate("rename", o.asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Rename(env, s, oldName, newName)
	})
}

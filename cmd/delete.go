package cmd

import (
	"errors"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "delete",
		Aliases:    []string{"rm"},
		Summary:    "Delete a branch and re-parent its children",
		Usage:      "st delete <name> [-f|--force] [--dry-run] [--json]",
		Run:        runDelete,
		NewFlagSet: deleteFlagSet,
		// delete's positional is a tracked branch — the engine refuses the
		// trunk, and the current branch deletes legally (st checks the parent
		// out first), so every tracked name is a candidate.
		Completion: func(cc *completionCtx) []string {
			if cc.flagName != "" || len(cc.positionals) != 0 {
				return nil
			}
			return cc.s.BranchNames()
		},
	})
}

func runDelete(args []string) error {
	var o deleteOpts
	fs := newDeleteFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	asJSON, force, dryRun := o.asJSON, o.force, o.dryRun
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return errors.New("delete requires exactly one branch name")
	}
	name := rest[0]

	if dryRun {
		return preview(asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			return stack.DeletePlan(env, s, name, force)
		})
	}
	return mutate("delete", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Delete(env, s, name, force)
	})
}

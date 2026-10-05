package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "untrack",
		Summary:    "Stop tracking a branch (re-parents its children)",
		Usage:      "st untrack [name] [--dry-run] [--json]",
		Run:        runUntrack,
		NewFlagSet: untrackFlagSet,
		// untrack's optional positional is a tracked branch — the engine
		// refuses the trunk (bare untrack targets the current branch).
		Completion: func(cc *completionCtx) []string {
			if cc.flagName != "" || len(cc.positionals) != 0 {
				return nil
			}
			return cc.s.BranchNames()
		},
	})
}

func runUntrack(args []string) error {
	var o untrackOpts
	fs := newUntrackFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("untrack takes at most one branch name")
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	if o.dryRun {
		return preview(o.asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			return stack.UntrackPlan(env, s, name)
		})
	}

	return mutate("untrack", o.asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.UntrackBranch(env, s, name)
	})
}

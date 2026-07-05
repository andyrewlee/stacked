package cmd

import "stacked/internal/stack"

func init() {
	register(&Command{
		Name:       "squash",
		Summary:    "Squash all of the current branch's commits into one",
		Usage:      "st squash [-m <msg>] [--dry-run] [--json]",
		Run:        runSquash,
		NewFlagSet: squashFlagSet,
	})
}

func runSquash(args []string) error {
	var o squashOpts
	fs := newSquashFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("squash", fs.Args()); err != nil {
		return err
	}
	asJSON, message, dryRun := o.asJSON, o.message, o.dryRun

	if dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		res, err := stack.SquashPlan(stackEnv(s, asJSON), s, message)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}
	return mutate("squash", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Squash(env, s, message)
	})
}

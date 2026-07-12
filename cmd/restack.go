package cmd

import "github.com/andyrewlee/stacked/internal/stack"

func init() {
	register(&Command{
		Name:       "restack",
		Aliases:    []string{"r"},
		Summary:    "Rebase the current branch and everything above it onto their parents",
		Usage:      "st restack [--all] [--dry-run] [--json]",
		Run:        runRestack,
		NewFlagSet: restackFlagSet,
	})
}

func runRestack(args []string) error {
	var o restackOpts
	fs := newRestackFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("restack", fs.Args()); err != nil {
		return err
	}
	asJSON, dryRun, all := o.asJSON, o.dryRun, o.all

	if dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		plan := stack.RestackPlan
		if all {
			plan = stack.RestackAllPlan
		}
		res, err := plan(stackEnv(s, asJSON), s)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}
	if all {
		return mutate("restack", asJSON, stack.RestackAllOp)
	}
	return mutate("restack", asJSON, stack.Restack)
}

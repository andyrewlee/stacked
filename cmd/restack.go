package cmd

import "stacked/internal/stack"

func init() {
	register(&Command{
		Name:       "restack",
		Aliases:    []string{"r"},
		Summary:    "Rebase the current branch and everything above it onto their parents",
		Usage:      "st restack [--dry-run] [--json]",
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
	asJSON, dryRun := o.asJSON, o.dryRun

	if dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		res, err := stack.RestackPlan(stackEnv(s, asJSON), s)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}
	return mutate("restack", asJSON, stack.Restack)
}

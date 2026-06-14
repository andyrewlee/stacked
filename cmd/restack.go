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
	var asJSON bool
	fs := newFlagSet("restack", &asJSON)
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "show what would be restacked without changing anything")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("restack", fs.Args()); err != nil {
		return err
	}

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

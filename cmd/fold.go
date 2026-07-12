package cmd

import "github.com/andyrewlee/stacked/internal/stack"

func init() {
	register(&Command{
		Name:       "fold",
		Summary:    "Fold the current branch into its parent (parent absorbs its commits)",
		Usage:      "st fold [--dry-run] [--json]",
		Run:        runFold,
		NewFlagSet: foldFlagSet,
	})
}

func runFold(args []string) error {
	var o foldOpts
	fs := newFoldFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("fold", fs.Args()); err != nil {
		return err
	}
	asJSON, dryRun := o.asJSON, o.dryRun

	if dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		res, err := stack.FoldPlan(stackEnv(s, asJSON), s)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}
	return mutate("fold", asJSON, stack.Fold)
}

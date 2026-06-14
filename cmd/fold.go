package cmd

import "stacked/internal/stack"

func init() {
	register(&Command{
		Name:    "fold",
		Summary: "Fold the current branch into its parent (parent absorbs its commits)",
		Usage:   "st fold [--json]",
		Run:     runFold,
	})
}

func runFold(args []string) error {
	var asJSON bool
	fs := newFlagSet("fold", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("fold", fs.Args()); err != nil {
		return err
	}

	return mutate("fold", asJSON, stack.Fold)
}

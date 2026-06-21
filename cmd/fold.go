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
	asJSON, err := parsePlain("fold", args)
	if err != nil {
		return err
	}

	return mutate("fold", asJSON, stack.Fold)
}

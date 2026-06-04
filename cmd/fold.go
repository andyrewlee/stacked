package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "fold",
		Summary: "Fold the current branch into its parent (parent absorbs its commits)",
		Usage:   "st fold [--json]",
		Run:     runFold,
	})
}

func runFold(args []string) error {
	fs := flag.NewFlagSet("fold", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st fold [--json]") }
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return mutate("fold", asJSON, stack.Fold)
}

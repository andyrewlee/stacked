package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "restack",
		Aliases: []string{"r"},
		Summary: "Rebase the current branch and everything above it onto their parents",
		Usage:   "st restack [--dry-run] [--json]",
		Run:     runRestack,
	})
}

func runRestack(args []string) error {
	fs := flag.NewFlagSet("restack", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "show what would be restacked without changing anything")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st restack [--dry-run] [--json]") }
	if err := fs.Parse(args); err != nil {
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
		res, err := stack.RestackPlan(stackEnv(s), s)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}
	return mutate("restack", asJSON, stack.Restack)
}

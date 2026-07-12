package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "onto",
		Aliases:    []string{"move"},
		Summary:    "Move the current branch (and its upstack) onto a new parent",
		Usage:      "st onto <target> [--dry-run] [--json]",
		Run:        runOnto,
		NewFlagSet: ontoFlagSet,
	})
}

func runOnto(args []string) error {
	var o ontoOpts
	fs := newOntoFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("onto requires exactly one target branch")
	}
	asJSON, dryRun := o.asJSON, o.dryRun
	target := rest[0]

	if dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		res, err := stack.OntoPlan(stackEnv(s, asJSON), s, target)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}
	return mutate("onto", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Onto(env, s, target)
	})
}

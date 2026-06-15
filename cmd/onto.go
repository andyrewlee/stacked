package cmd

import (
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "onto",
		Aliases: []string{"move"},
		Summary: "Move the current branch (and its upstack) onto a new parent",
		Usage:   "st onto <target> [--json]",
		Run:     runOnto,
	})
}

func runOnto(args []string) error {
	var asJSON bool
	fs := newFlagSet("onto", &asJSON)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("onto requires exactly one target branch")
	}
	target := rest[0]

	return mutate("onto", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Onto(env, s, target)
	})
}

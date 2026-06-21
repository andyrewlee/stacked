package cmd

import (
	"errors"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "create",
		Aliases:    []string{"c"},
		Summary:    "Create a new branch stacked on the current branch",
		Usage:      "st create <name> [-m <msg>] [-a|--all] [--json]",
		Run:        runCreate,
		NewFlagSet: createFlagSet,
	})
}

func runCreate(args []string) error {
	var o createOpts
	fs := newCreateFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	asJSON, message, all := o.asJSON, o.message, o.all
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return errors.New("create requires exactly one branch name")
	}
	name := rest[0]
	if err := git.CheckBranchName(name); err != nil {
		return err
	}

	return mutate("create", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Create(env, s, name, message, all)
	})
}

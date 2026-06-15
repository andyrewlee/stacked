package cmd

import (
	"errors"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "delete",
		Aliases:    []string{"rm"},
		Summary:    "Delete a branch and re-parent its children",
		Usage:      "st delete <name> [-f|--force] [--json]",
		Run:        runDelete,
		NewFlagSet: deleteFlagSet,
	})
}

func runDelete(args []string) error {
	var o deleteOpts
	fs := newDeleteFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	asJSON, force := o.asJSON, o.force
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return errors.New("delete requires exactly one branch name")
	}
	name := rest[0]

	return mutate("delete", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Delete(env, s, name, force)
	})
}

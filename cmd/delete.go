package cmd

import (
	"errors"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "delete",
		Aliases: []string{"rm"},
		Summary: "Delete a branch and re-parent its children",
		Usage:   "st delete <name> [-f|--force] [--json]",
		Run:     runDelete,
	})
}

func runDelete(args []string) error {
	var asJSON bool
	fs := newFlagSet("delete", &asJSON)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageLine("delete"))
		fs.PrintDefaults()
	}
	var force bool
	fs.BoolVar(&force, "f", false, "force delete the branch even if not fully merged")
	fs.BoolVar(&force, "force", false, "force delete the branch even if not fully merged")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
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

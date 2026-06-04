package cmd

import (
	"errors"
	"flag"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "create",
		Aliases: []string{"c"},
		Summary: "Create a new branch stacked on the current branch",
		Usage:   "st create <name> [-m <msg>] [-a|--all] [--json]",
		Run:     runCreate,
	})
}

func runCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: st create <name> [-m <msg>] [-a|--all] [--json]")
		fs.PrintDefaults()
	}
	var message string
	fs.StringVar(&message, "m", "", "commit message for the new branch")
	fs.StringVar(&message, "message", "", "commit message for the new branch")
	var all bool
	fs.BoolVar(&all, "a", false, "stage all changes before committing")
	fs.BoolVar(&all, "all", false, "stage all changes before committing")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return errors.New("create requires exactly one branch name")
	}
	name := rest[0]

	return mutate("create", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Create(env, s, name, message, all)
	})
}

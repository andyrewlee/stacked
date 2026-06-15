package cmd

import (
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "modify",
		Aliases:    []string{"amend", "m"},
		Summary:    "Amend (or add) a commit on the current branch and restack everything above",
		Usage:      "st modify [-m <msg>] [-a|--all] [--commit] [--json]",
		Run:        runModify,
		NewFlagSet: modifyFlagSet,
	})
}

func runModify(args []string) error {
	var asJSON bool
	fs := newFlagSet("modify", &asJSON)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageLine("modify"))
		fs.PrintDefaults()
	}
	var message string
	fs.StringVar(&message, "m", "", "commit message")
	fs.StringVar(&message, "message", "", "commit message")
	// Default to staging all changes so a bare invocation (and the "amend" alias)
	// behaves like "stage all + amend".
	all := true
	fs.BoolVar(&all, "a", true, "stage all tracked changes before amending/committing")
	fs.BoolVar(&all, "all", true, "stage all tracked changes before amending/committing")
	var commit bool
	fs.BoolVar(&commit, "commit", false, "create a new commit instead of amending the tip")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("modify", fs.Args()); err != nil {
		return err
	}

	return mutate("modify", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Modify(env, s, message, all, commit)
	})
}

package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "modify",
		Aliases: []string{"amend", "m"},
		Summary: "Amend (or add) a commit on the current branch and restack everything above",
		Usage:   "st modify [-m <msg>] [-a|--all] [--commit] [--json]",
		Run:     runModify,
	})
}

func runModify(args []string) error {
	fs := flag.NewFlagSet("modify", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: st modify [-m <msg>] [-a|--all] [--commit] [--json]")
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
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("modify takes no positional arguments")
	}

	return mutate("modify", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Modify(env, s, message, all, commit)
	})
}

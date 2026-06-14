package cmd

import (
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "untrack",
		Summary: "Stop tracking a branch (re-parents its children)",
		Usage:   "st untrack [name] [--json]",
		Run:     runUntrack,
	})
}

func runUntrack(args []string) error {
	var asJSON bool
	fs := newFlagSet("untrack", &asJSON)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageLine("untrack"))
		fs.PrintDefaults()
	}
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("untrack takes at most one branch name")
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}

	return mutate("untrack", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.UntrackBranch(env, s, name)
	})
}

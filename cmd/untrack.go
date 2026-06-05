package cmd

import (
	"flag"
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
	fs := flag.NewFlagSet("untrack", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: st untrack [name] [--json]")
		fs.PrintDefaults()
	}
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
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

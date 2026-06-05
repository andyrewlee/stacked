package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "squash",
		Summary: "Squash all of the current branch's commits into one",
		Usage:   "st squash [-m <msg>] [--json]",
		Run:     runSquash,
	})
}

func runSquash(args []string) error {
	fs := flag.NewFlagSet("squash", flag.ContinueOnError)
	var message string
	fs.StringVar(&message, "m", "", "commit message for the squashed commit")
	fs.StringVar(&message, "message", "", "commit message for the squashed commit")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st squash [-m <msg>] [--json]") }
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("squash", fs.Args()); err != nil {
		return err
	}

	return mutate("squash", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Squash(env, s, message)
	})
}

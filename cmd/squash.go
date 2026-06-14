package cmd

import "stacked/internal/stack"

func init() {
	register(&Command{
		Name:    "squash",
		Summary: "Squash all of the current branch's commits into one",
		Usage:   "st squash [-m <msg>] [--json]",
		Run:     runSquash,
	})
}

func runSquash(args []string) error {
	var asJSON bool
	fs := newFlagSet("squash", &asJSON)
	var message string
	fs.StringVar(&message, "m", "", "commit message for the squashed commit")
	fs.StringVar(&message, "message", "", "commit message for the squashed commit")
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

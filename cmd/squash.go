package cmd

import "stacked/internal/stack"

func init() {
	register(&Command{
		Name:       "squash",
		Summary:    "Squash all of the current branch's commits into one",
		Usage:      "st squash [-m <msg>] [--json]",
		Run:        runSquash,
		NewFlagSet: squashFlagSet,
	})
}

func runSquash(args []string) error {
	var o squashOpts
	fs := newSquashFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("squash", fs.Args()); err != nil {
		return err
	}
	asJSON, message := o.asJSON, o.message

	return mutate("squash", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Squash(env, s, message)
	})
}

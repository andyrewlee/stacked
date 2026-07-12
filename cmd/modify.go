package cmd

import "github.com/andyrewlee/stacked/internal/stack"

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
	var o modifyOpts
	fs := newModifyFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("modify", fs.Args()); err != nil {
		return err
	}
	asJSON, message, all, commit := o.asJSON, o.message, o.all, o.commit

	return mutate("modify", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Modify(env, s, message, all, commit)
	})
}

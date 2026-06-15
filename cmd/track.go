package cmd

import (
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "track",
		Summary:    "Start tracking the current git branch in the stack",
		Usage:      "st track [--parent <branch>] [--json]",
		Run:        runTrack,
		NewFlagSet: trackFlagSet,
	})
}

func runTrack(args []string) error {
	var asJSON bool
	fs := newFlagSet("track", &asJSON)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageLine("track"))
		fs.PrintDefaults()
	}
	var parent string
	fs.StringVar(&parent, "parent", "", "parent branch (trunk or a tracked branch)")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("track", fs.Args()); err != nil {
		return err
	}

	return mutate("track", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.TrackBranch(env, s, parent)
	})
}

package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "track",
		Summary: "Start tracking the current git branch in the stack",
		Usage:   "st track [--parent <branch>] [--json]",
		Run:     runTrack,
	})
}

func runTrack(args []string) error {
	fs := flag.NewFlagSet("track", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: st track [--parent <branch>] [--json]")
		fs.PrintDefaults()
	}
	var parent string
	fs.StringVar(&parent, "parent", "", "parent branch (trunk or a tracked branch)")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
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

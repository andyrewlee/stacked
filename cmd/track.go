package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "track",
		Summary:    "Start tracking a git branch in the stack",
		Usage:      "st track [name] [--parent <branch>] [--json]",
		Run:        runTrack,
		NewFlagSet: trackFlagSet,
	})
}

func runTrack(args []string) error {
	var o trackOpts
	fs := newTrackFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("track takes at most one branch name")
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	asJSON, parent := o.asJSON, o.parent

	return mutate("track", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.TrackBranch(env, s, name, parent)
	})
}

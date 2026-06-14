package cmd

import "stacked/internal/stack"

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
	var o trackOpts
	fs := newTrackFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("track", fs.Args()); err != nil {
		return err
	}
	asJSON, parent := o.asJSON, o.parent

	return mutate("track", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.TrackBranch(env, s, parent)
	})
}

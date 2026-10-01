package cmd

import (
	"fmt"
	"sort"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "track",
		Summary:    "Start tracking a git branch in the stack",
		Usage:      "st track [name] [--parent <branch>] [--all] [--json]",
		Run:        runTrack,
		NewFlagSet: trackFlagSet,
		// track's positional offers local branches that are neither tracked nor
		// trunk (suppressed by --all, which takes no name); its --parent flag
		// value completes the tracked set instead.
		Completion: func(cc *completionCtx) []string {
			locals := cc.localTips()
			if locals == nil {
				return nil
			}
			if cc.flagName != "" {
				if cc.flagName == "parent" {
					return trackedAndTrunk(cc.s)
				}
				return nil
			}
			if len(cc.positionals) == 0 && !cc.seen["all"] {
				var names []string
				for name := range locals {
					if name != cc.s.Trunk && !cc.s.IsTracked(name) {
						names = append(names, name)
					}
				}
				sort.Strings(names)
				return names
			}
			return nil
		},
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
	if o.all && (len(rest) == 1 || o.parent != "") {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("--all takes no branch name or --parent")
	}
	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}
	asJSON, parent := o.asJSON, o.parent

	return mutate("track", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		if o.all {
			return stack.TrackAllBranches(env, s)
		}
		return stack.TrackBranch(env, s, name, parent)
	})
}

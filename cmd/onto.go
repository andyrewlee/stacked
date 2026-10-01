package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "onto",
		Aliases:    []string{"move"},
		Summary:    "Move the current branch (and its upstack) onto a new parent",
		Usage:      "st onto <target> [--dry-run] [--json]",
		Run:        runOnto,
		NewFlagSet: ontoFlagSet,
		// onto's target positional: moving a subtree onto itself or one of its
		// descendants is refused, so every other branch — trunk included — is a
		// legal target.
		Completion: func(cc *completionCtx) []string {
			cur := cc.current() // the subtree root being moved
			if cc.flagName != "" || len(cc.positionals) != 0 {
				return nil
			}
			excl := map[string]bool{cur: true}
			for _, d := range cc.s.Descendants(cur) {
				excl[d] = true
			}
			return minusNames(trackedAndTrunk(cc.s), excl)
		},
	})
}

func runOnto(args []string) error {
	var o ontoOpts
	fs := newOntoFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return fmt.Errorf("onto requires exactly one target branch")
	}
	asJSON, dryRun := o.asJSON, o.dryRun
	target := rest[0]

	if dryRun {
		return preview(asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			return stack.OntoPlan(env, s, target)
		})
	}
	return mutate("onto", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Onto(env, s, target)
	})
}

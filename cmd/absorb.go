package cmd

import (
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "absorb",
		Summary:    "Absorb staged hunks into the stack commits that own their lines",
		Usage:      "st absorb [--dry-run] [--json]",
		Run:        runAbsorb,
		NewFlagSet: absorbFlagSet,
	})
}

// runAbsorb drives absorb: --dry-run attributes the staged hunks with zero
// mutation; the bare form applies a single-target plan (amend the owning tip,
// cascade the descendants) through one mutateState call, so `st undo` reverts
// the amend and the cascade as one entry.
func runAbsorb(args []string) error {
	var o absorbOpts
	fs := newAbsorbFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("absorb", fs.Args()); err != nil {
		return err
	}
	var res *stack.AbsorbResult
	if o.dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		res, err = stack.AbsorbPlan(stackEnv(s, o.asJSON), s)
		if err != nil {
			return err
		}
		return emitAbsorb(o.asJSON, res)
	}
	if err := mutateState("absorb", o.asJSON, func(env stack.Env, s *stack.State) error {
		r, err := stack.Absorb(env, s)
		res = r
		return err
	}); err != nil {
		return err
	}
	return emitAbsorb(o.asJSON, res)
}

func emitAbsorb(asJSON bool, res *stack.AbsorbResult) error {
	return emit(asJSON, res, func() {
		out("%s\n", sanitizeForTerminal(res.Summary))
		for _, a := range res.Absorbed {
			out("  absorb %s:%s -> %s (%s)\n", sanitizeForTerminal(a.File), a.Lines, sanitizeForTerminal(a.Branch), sanitizeForTerminal(a.Commit))
		}
		for _, r := range res.Refused {
			out("  refuse %s:%s (%s)\n", sanitizeForTerminal(r.File), r.Lines, sanitizeForTerminal(r.Reason))
		}
		if len(res.Restacked) > 0 {
			out("  restacked: %s\n", joinTerminalNames(res.Restacked))
		}
		for _, n := range res.Notes {
			out("  note: %s\n", sanitizeForTerminal(n))
		}
	})
}

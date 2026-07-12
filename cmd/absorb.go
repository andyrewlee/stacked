package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "absorb",
		Summary:    "Attribute staged hunks to the stack commits that own their lines",
		Usage:      "st absorb --dry-run [--json]",
		Run:        runAbsorb,
		NewFlagSet: absorbFlagSet,
	})
}

// runAbsorb drives the absorb attribution. Slice 1 ships the dry-run mapping
// only: hunks are attributed and refusals reported with zero mutation.
func runAbsorb(args []string) error {
	var o absorbOpts
	fs := newAbsorbFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("absorb", fs.Args()); err != nil {
		return err
	}
	// Applying the hunks (rewriting the target commits) is the next absorb
	// slice; until it lands a bare `st absorb` points at the dry-run.
	if !o.dryRun {
		return fmt.Errorf("st absorb: applying hunks is not implemented yet; run: st absorb --dry-run")
	}
	s, err := loadState()
	if err != nil {
		return err
	}
	res, err := stack.AbsorbPlan(stackEnv(s, o.asJSON), s)
	if err != nil {
		return err
	}
	return emit(o.asJSON, res, func() {
		out("%s\n", sanitizeForTerminal(res.Summary))
		for _, a := range res.Absorbed {
			out("  absorb %s:%s -> %s (%s)\n", sanitizeForTerminal(a.File), a.Lines, sanitizeForTerminal(a.Branch), sanitizeForTerminal(a.Commit))
		}
		for _, r := range res.Refused {
			out("  refuse %s:%s (%s)\n", sanitizeForTerminal(r.File), r.Lines, sanitizeForTerminal(r.Reason))
		}
	})
}

package cmd

import (
	"fmt"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "doctor",
		Aliases:    []string{"validate"},
		Summary:    "Check stack state for inconsistencies and drift (--fix to repair)",
		Usage:      "st doctor [--fix] [--all] [--json]",
		Run:        runDoctor,
		NewFlagSet: doctorFlagSet,
	})
}

// runDoctor checks stack metadata against git and reports problems and drift
// warnings. With --fix it reconciles metadata inconsistencies automatically
// (replacing the old standalone `st repair` command).
func runDoctor(args []string) error {
	var o doctorOpts
	fs := newDoctorFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("doctor", fs.Args()); err != nil {
		return err
	}
	if o.fix {
		return runDoctorFix(o.asJSON)
	}

	s, err := loadState()
	if err != nil {
		return err
	}

	var problems, warnings []string

	tips, err := git.TipsFor(s.TipNames())
	if err != nil {
		return err
	}
	drift := s.DriftAgainst(tips)

	inRebase, err := git.RebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state: %w", err)
	}
	problemBranch := map[string]bool{}
	for _, p := range s.Inconsistencies(tips, inRebase) {
		problems = append(problems, formatProblem(p))
		problemBranch[p.Branch] = true
	}

	// Drift warnings scope to the current stack by default when on a tracked
	// branch, so unrelated stacks don't create noise; --all checks every stack.
	warnScope := map[string]bool(nil)
	if !o.all {
		if cur, cerr := currentBranch(); cerr == nil && s.IsTracked(cur) {
			warnScope = map[string]bool{}
			for _, name := range s.StackBranches(cur) {
				warnScope[name] = true
			}
		}
	}

	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if warnScope != nil && !warnScope[name] {
			continue
		}
		if !problemBranch[name] && drift[name] {
			warnings = append(warnings, fmt.Sprintf("%s needs restack (run: st restack)", name))
		}
	}

	if problems == nil {
		problems = []string{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	payload := struct {
		OK       bool     `json:"ok"`
		Tracked  int      `json:"tracked"`
		Problems []string `json:"problems"`
		Warnings []string `json:"warnings"`
	}{OK: len(problems) == 0, Tracked: len(names), Problems: problems, Warnings: warnings}

	if err := emit(o.asJSON, payload, func() {
		if len(problems) == 0 && len(warnings) == 0 {
			out("ok: %d tracked branch(es), no problems found\n", len(names))
			return
		}
		if len(problems) > 0 {
			out("problems:\n")
			for _, p := range problems {
				out("  - %s\n", sanitizeForTerminal(p))
			}
		}
		if len(warnings) > 0 {
			out("warnings:\n")
			for _, w := range warnings {
				out("  - %s\n", sanitizeForTerminal(w))
			}
		}
	}); err != nil {
		return err
	}

	if len(problems) > 0 {
		return fmt.Errorf("doctor found %d problem(s)", len(problems))
	}
	return nil
}

func runDoctorFix(asJSON bool) error {
	var fixes []string
	if err := mutateState("repair", asJSON, func(env stack.Env, s *stack.State) error {
		res, err := stack.Repair(env, s)
		if err != nil {
			return err
		}
		fixes = res.Notes
		return nil
	}); err != nil {
		return err
	}

	if fixes == nil {
		fixes = []string{}
	}
	payload := struct {
		Repaired bool     `json:"repaired"`
		Fixes    []string `json:"fixes"`
	}{len(fixes) > 0, fixes}
	return emit(asJSON, payload, func() {
		if len(fixes) == 0 {
			out("nothing to repair; the stack is consistent with the repository\n")
			return
		}
		out("repaired:\n")
		for _, f := range fixes {
			out("  - %s\n", sanitizeForTerminal(f))
		}
		out("run `st restack` to rebase any re-parented branches onto their new parents.\n")
	})
}

func formatProblem(p stack.Problem) string {
	switch p.Kind {
	case stack.TrunkMissing:
		return fmt.Sprintf("trunk branch %q does not exist", p.Branch)
	case stack.BranchMissing:
		return fmt.Sprintf("%s is tracked but its git branch is missing (run: st doctor --fix)", p.Branch)
	case stack.ParentUntracked:
		return fmt.Sprintf("%s has parent %q which is not the trunk or a tracked branch (run: st doctor --fix)", p.Branch, p.Detail)
	case stack.ParentMissing:
		return fmt.Sprintf("%s has parent %q whose git branch is missing (run: st doctor --fix)", p.Branch, p.Detail)
	case stack.ParentCycle:
		return fmt.Sprintf("%s is part of a parent cycle: %s (run: st doctor --fix)", p.Branch, p.Detail)
	case stack.StalePendingReparent:
		return fmt.Sprintf("%s has a recorded pending reparent onto %q but no rebase is in progress (run: st doctor --fix)", p.Branch, p.Detail)
	case stack.StalePendingContinuation:
		return fmt.Sprintf("%s has a recorded pending continuation but no rebase is in progress (run: st doctor --fix)", p.Branch)
	}
	return ""
}

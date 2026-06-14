package cmd

import (
	"fmt"
	"sort"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "validate",
		Aliases: []string{"doctor"},
		Summary: "Check the stack state for drift or inconsistencies",
		Usage:   "st validate [--json]",
		Run:     runValidate,
	})
}

// runValidate checks the recorded stack state against the actual git repository
// and reports problems (a missing trunk, tracked branches whose git branch is
// gone, parents that are neither the trunk nor a tracked branch, and parent
// cycles) as well as warnings (branches that have drifted and need a restack).
// It exits non-zero when any problem is found so it is usable in scripts.
func runValidate(args []string) error {
	var asJSON bool
	fs := newFlagSet("validate", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("validate", fs.Args()); err != nil {
		return err
	}

	s, err := loadState()
	if err != nil {
		return err
	}

	var problems, warnings []string

	// One for-each-ref read answers every branch-exists and drift question.
	tips, err := git.Tips()
	if err != nil {
		return err
	}
	drift := s.DriftAgainst(tips)

	// The engine is the single source of truth for what an inconsistent stack is
	// (the same classifier st repair fixes); this command just renders it.
	problemBranch := map[string]bool{}
	for _, p := range s.Inconsistencies(tips) {
		problems = append(problems, formatProblem(p))
		problemBranch[p.Branch] = true
	}

	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)

	// A branch warns about drift only when it is otherwise consistent (no missing
	// branch, a valid parent, no cycle) — the same gate as before.
	for _, name := range names {
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

	if err := emit(asJSON, payload, func() {
		if len(problems) == 0 && len(warnings) == 0 {
			out("ok: %d tracked branch(es), no problems found\n", len(names))
			return
		}
		if len(problems) > 0 {
			out("problems:\n")
			for _, p := range problems {
				out("  - %s\n", p)
			}
		}
		if len(warnings) > 0 {
			out("warnings:\n")
			for _, w := range warnings {
				out("  - %s\n", w)
			}
		}
	}); err != nil {
		return err
	}

	if len(problems) > 0 {
		return fmt.Errorf("validate found %d problem(s)", len(problems))
	}
	return nil
}

// formatProblem renders an engine Problem as validate's human-readable line.
func formatProblem(p stack.Problem) string {
	switch p.Kind {
	case stack.TrunkMissing:
		return fmt.Sprintf("trunk branch %q does not exist", p.Branch)
	case stack.BranchMissing:
		return fmt.Sprintf("%s is tracked but its git branch is missing (run: st untrack %s)", p.Branch, p.Branch)
	case stack.ParentUntracked:
		return fmt.Sprintf("%s has parent %q which is not the trunk or a tracked branch", p.Branch, p.Detail)
	case stack.ParentMissing:
		return fmt.Sprintf("%s has parent %q whose git branch is missing", p.Branch, p.Detail)
	case stack.ParentCycle:
		return fmt.Sprintf("%s is part of a parent cycle: %s", p.Branch, p.Detail)
	}
	return ""
}

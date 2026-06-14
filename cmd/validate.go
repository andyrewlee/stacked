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

	if _, ok := tips[s.Trunk]; !ok {
		problems = append(problems, fmt.Sprintf("trunk branch %q does not exist", s.Trunk))
	}

	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		b, _ := s.Get(name)

		_, branchOK := tips[name]
		if !branchOK {
			problems = append(problems, fmt.Sprintf("%s is tracked but its git branch is missing (run: st untrack %s)", name, name))
		}

		parentOK := true
		_, parentExists := tips[b.Parent]
		switch {
		case b.Parent == s.Trunk:
			// fine
		case !s.IsTracked(b.Parent):
			parentOK = false
			problems = append(problems, fmt.Sprintf("%s has parent %q which is not the trunk or a tracked branch", name, b.Parent))
		case !parentExists:
			parentOK = false
			problems = append(problems, fmt.Sprintf("%s has parent %q whose git branch is missing", name, b.Parent))
		}

		if path := stack.CyclePath(s, name); path != "" {
			problems = append(problems, fmt.Sprintf("%s is part of a parent cycle: %s", name, path))
			continue // drift would be unreliable on a cycle
		}

		if branchOK && parentOK && drift[name] {
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

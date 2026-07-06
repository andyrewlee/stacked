package cmd

import (
	"errors"
	"fmt"
	"sort"

	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "checkout",
		Aliases: []string{"co"},
		Summary: "Check out a tracked branch (lists branches if no name)",
		Usage:   "st checkout [name] [--json]",
		Run:     runCheckout,
	})
}

// runCheckout checks out a tracked branch (or the trunk) by name, or, when no
// name is given, lists the trunk and all tracked branches.
func runCheckout(args []string) error {
	var asJSON bool
	fs := newFlagSet("checkout", &asJSON)
	if err := parseArgs(fs, args); err != nil {
		return err
	}

	s, err := loadState()
	if err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) > 1 {
		return errors.New("checkout takes at most one branch name")
	}

	if len(rest) == 1 {
		name := rest[0]
		if name != s.Trunk && !s.IsTracked(name) {
			return fmt.Errorf("%q is not a tracked branch", name)
		}
		dest, err := teleportCheckout(name)
		if err != nil {
			return err
		}
		// A teleport without the shim does NOT move the parent shell, so report it
		// as not-switched and tell the user how to get there; with the shim (or an
		// in-place checkout) the move really happened.
		teleportedNoShim := dest != "" && !shimActive()
		payload := struct {
			Branch   string `json:"branch"`
			Switched bool   `json:"switched"`
			Worktree string `json:"worktree,omitempty"`
		}{name, !teleportedNoShim, dest}
		return emit(asJSON, payload, func() {
			safeName := sanitizeForTerminal(name)
			safeDest := sanitizeForTerminal(dest)
			switch {
			case teleportedNoShim:
				out("%s\n", sanitizeForTerminal(teleportHint(name, dest)))
			case dest != "":
				out("switched to %s (worktree: %s)\n", safeName, safeDest)
			default:
				out("switched to %s\n", safeName)
			}
		})
	}

	return listBranches(s, asJSON)
}

// listBranches renders the trunk plus every tracked branch, marking the current
// one with "*" in text mode.
func listBranches(s *stack.State, asJSON bool) error {
	cur, err := currentBranch()
	if err != nil {
		return fmt.Errorf("determining current branch: %w", err)
	}

	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)

	payload := struct {
		Trunk    string   `json:"trunk"`
		Current  string   `json:"current"`
		Branches []string `json:"branches"`
	}{s.Trunk, cur, names}

	return emit(asJSON, payload, func() {
		for _, name := range append([]string{s.Trunk}, names...) {
			marker := " "
			if name == cur {
				marker = "*"
			}
			out("%s %s\n", marker, sanitizeForTerminal(name))
		}
	})
}

package cmd

import (
	"errors"
	"flag"
	"fmt"
	"sort"

	"stacked/internal/git"
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
	fs := flag.NewFlagSet("checkout", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st checkout [name] [--json]") }
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
		if err := git.Checkout(name); err != nil {
			return fmt.Errorf("checking out %q: %w", name, err)
		}
		payload := struct {
			Branch   string `json:"branch"`
			Switched bool   `json:"switched"`
		}{name, true}
		return emit(asJSON, payload, func() { out("Switched to %s\n", name) })
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
			out("%s %s\n", marker, name)
		}
	})
}

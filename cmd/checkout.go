package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "checkout",
		Aliases: []string{"co"},
		Summary: "Check out a tracked branch (lists branches if no name)",
		Usage:   "st checkout [name] [--json]",
		Run:     runCheckout,
		Completion: func(cc *completionCtx) []string {
			if cc.flagName != "" || len(cc.positionals) != 0 {
				return nil
			}
			return trackedAndTrunk(cc.s)
		},
	})
}

// checkoutBranch checks out name in the current worktree, refusing clearly when
// another worktree already owns name.
func checkoutBranch(name string) error {
	wts, err := worktrees()
	if err == nil && stack.IsMultiWorktree(wts) {
		if owner, ok := stack.OwnerOf(wts, name); ok {
			if root, rerr := git.RepoRoot(); rerr == nil && !samePath(owner.Path, root) {
				return fmt.Errorf("branch %q is checked out in another worktree %q", name, owner.Path)
			}
		}
	}
	return checkoutAndReset(name)
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// runCheckout checks out a tracked branch (or the trunk) by name, or, when no
// name is given, lists the trunk and all tracked branches.
func runCheckout(args []string) error {
	var asJSON bool
	fs := newFlagSet("checkout", &asJSON)
	if err := parseArgs(fs, args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) > 1 {
		return errors.New("checkout takes at most one branch name")
	}

	if len(rest) == 0 {
		s, err := loadState()
		if err != nil {
			return err
		}
		return listBranches(s, asJSON)
	}

	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()

	name := rest[0]
	if name != s.Trunk && !s.IsTracked(name) {
		return stack.ErrNotTracked(name)
	}
	if err := checkoutBranch(name); err != nil {
		return err
	}
	payload := struct {
		Branch   string `json:"branch"`
		Switched bool   `json:"switched"`
	}{name, true}
	return emit(asJSON, payload, func() {
		out("switched to %s\n", sanitizeForTerminal(name))
	})
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

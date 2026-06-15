package cmd

import (
	"fmt"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "down",
		Aliases: []string{"d"},
		Summary: "Move down the stack toward trunk",
		Usage:   "st down [n] [--json]",
		Run:     runDown,
	})
}

// runDown moves the current branch selection down the stack toward the trunk.
// It accepts an optional positional integer n (default 1) and walks parent links
// up to n times, stopping early if it reaches the trunk, then checks out the
// resulting branch.
func runDown(args []string) error {
	var asJSON bool
	fs := newFlagSet("down", &asJSON)
	n, err := parseCount(fs, args, "down")
	if err != nil {
		return err
	}

	s, cur, err := loadStateAndCurrent()
	if err != nil {
		return err
	}

	if cur == s.Trunk {
		return navEmit(asJSON, s.Trunk, fmt.Sprintf("already at trunk: %s", s.Trunk))
	}

	for i := 0; i < n; i++ {
		b, ok := s.Get(cur)
		if !ok {
			return fmt.Errorf("branch %q is not tracked by stacked", cur)
		}
		cur = b.Parent
		if cur == s.Trunk {
			break
		}
	}

	if err := git.Checkout(cur); err != nil {
		return fmt.Errorf("checking out %q: %w", cur, err)
	}

	return navEmit(asJSON, cur, fmt.Sprintf("switched to %s", cur))
}

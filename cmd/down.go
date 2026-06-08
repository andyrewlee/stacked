package cmd

import (
	"flag"
	"fmt"
	"strconv"

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
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: st down [n] [--json]")
	}
	if err := parseArgs(fs, args); err != nil {
		return err
	}

	n := 1
	if rest := fs.Args(); len(rest) > 1 {
		return fmt.Errorf("down takes at most one step count, got %q", rest[1])
	} else if len(rest) > 0 {
		v, err := strconv.Atoi(rest[0])
		if err != nil {
			return fmt.Errorf("invalid step count %q: %w", rest[0], err)
		}
		if v < 1 {
			return fmt.Errorf("step count must be at least 1, got %d", v)
		}
		n = v
	}

	s, err := loadState()
	if err != nil {
		return err
	}

	cur, err := currentBranch()
	if err != nil {
		return err
	}

	if cur == s.Trunk {
		return navEmit(asJSON, s.Trunk, fmt.Sprintf("already at trunk %q", s.Trunk))
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

	return navEmit(asJSON, cur, fmt.Sprintf("switched to %q", cur))
}

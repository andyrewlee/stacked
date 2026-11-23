package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
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

func runDown(args []string) error {
	var asJSON bool
	fs := newFlagSet("down", &asJSON)
	n, err := parseCount(fs, args, "down")
	if err != nil {
		return err
	}

	s, cur, release, err := lockAndLoadCurrent()
	if err != nil {
		return err
	}
	defer release()

	if cur == s.Trunk {
		return navEmit(asJSON, s.Trunk, fmt.Sprintf("already at trunk (%s)", s.Trunk))
	}

	for i := 0; i < n; i++ {
		b, ok := s.Get(cur)
		if !ok {
			return stack.ErrNotTracked(cur)
		}
		cur = b.Parent
		if cur == s.Trunk {
			break
		}
	}

	if err := checkoutBranch(cur); err != nil {
		return err
	}
	return navEmitText(asJSON, cur,
		fmt.Sprintf("switched to %s", cur),
		fmt.Sprintf("switched to %s", sanitizeForTerminal(cur)))
}

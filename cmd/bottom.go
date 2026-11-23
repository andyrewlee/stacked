package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "bottom",
		Aliases: []string{"b"},
		Summary: "Jump to the bottom branch of the current stack (just above trunk)",
		Usage:   "st bottom [--json]",
		Run:     runBottom,
	})
}

func runBottom(args []string) error {
	asJSON, err := parsePlain("bottom", args)
	if err != nil {
		return err
	}

	s, cur, release, err := lockAndLoadCurrent()
	if err != nil {
		return err
	}
	defer release()

	if cur == s.Trunk {
		return navEmit(asJSON, s.Trunk, "at trunk")
	}
	if !s.IsTracked(cur) {
		return stack.ErrNotTracked(cur)
	}

	b := s.BottomOf(cur)
	if b == cur {
		return navEmit(asJSON, b, fmt.Sprintf("already at bottom (%s)", b))
	}

	if err := checkoutBranch(b); err != nil {
		return err
	}
	return navEmitText(asJSON, b,
		fmt.Sprintf("switched to bottom: %s", b),
		fmt.Sprintf("switched to bottom: %s", sanitizeForTerminal(b)))
}

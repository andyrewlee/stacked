package cmd

import (
	"fmt"
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

// runBottom checks out the bottom branch of the current stack (the tracked
// branch whose parent is the trunk). If the current branch is the trunk, there
// is nothing below it and a notice is printed instead.
func runBottom(args []string) error {
	asJSON, err := parsePlain("bottom", args)
	if err != nil {
		return err
	}

	s, cur, err := loadStateAndCurrent()
	if err != nil {
		return err
	}

	if cur == s.Trunk {
		return navEmit(asJSON, s.Trunk, "at trunk")
	}
	if !s.IsTracked(cur) {
		return fmt.Errorf("branch %q is not tracked by stacked", cur)
	}

	b := s.BottomOf(cur)
	if b == cur {
		return navEmit(asJSON, b, alreadyAtSummary("already at bottom", b))
	}

	dest, err := teleportCheckout(b)
	if err != nil {
		return err
	}
	return navEmit(asJSON, b, navSummary("switched to bottom:", b, dest))
}

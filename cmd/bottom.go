package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "bottom",
		Aliases: []string{"b"},
		Summary: "Jump to the bottom branch of the current stack (just above trunk)",
		Usage:   "st bottom",
		Run:     runBottom,
	})
}

// runBottom checks out the bottom branch of the current stack (the tracked
// branch whose parent is the trunk). If the current branch is the trunk, there
// is nothing below it and a notice is printed instead.
func runBottom(args []string) error {
	fs := flag.NewFlagSet("bottom", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() {
		out("usage: st bottom [--json]\n")
	}
	if err := parseFlagSet(fs, args); err != nil {
		return err
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
		return navEmit(asJSON, s.Trunk, "at trunk")
	}
	if !s.IsTracked(cur) {
		return fmt.Errorf("branch %q is not tracked by stacked", cur)
	}

	b := s.BottomOf(cur)
	if b == cur {
		return navEmit(asJSON, b, fmt.Sprintf("already at bottom: %s", b))
	}

	if err := git.Checkout(b); err != nil {
		return err
	}
	return navEmit(asJSON, b, fmt.Sprintf("switched to bottom: %s", b))
}

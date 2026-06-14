package cmd

import (
	"fmt"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "top",
		Aliases: []string{"t"},
		Summary: "Jump to the top of the current stack",
		Usage:   "st top [--json]",
		Run:     runTop,
	})
}

// runTop walks upward from the current branch following single children until it
// reaches the leaf of the stack, then checks that leaf out. A branch point (more
// than one child) stops the walk and directs the user to st checkout.
func runTop(args []string) error {
	var asJSON bool
	fs := newFlagSet("top", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("top", fs.Args()); err != nil {
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
	if cur != s.Trunk && !s.IsTracked(cur) {
		return fmt.Errorf("branch %q is not tracked by stacked", cur)
	}

	leaf := cur
	for {
		children := s.Children(leaf)
		switch len(children) {
		case 0:
			if leaf == cur {
				return navEmit(asJSON, cur, fmt.Sprintf("already at the top of the stack: %s", cur))
			}
			if err := git.Checkout(leaf); err != nil {
				return fmt.Errorf("checking out %s: %w", leaf, err)
			}
			return navEmit(asJSON, leaf, fmt.Sprintf("moved to top of stack: %s", leaf))
		case 1:
			leaf = children[0].Name
		default:
			return fmt.Errorf("%s is a branch point with %d children; use st checkout to pick one", leaf, len(children))
		}
	}
}

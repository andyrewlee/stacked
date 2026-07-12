package cmd

import (
	"fmt"
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
	asJSON, err := parsePlain("top", args)
	if err != nil {
		return err
	}

	s, cur, err := loadStateAndCurrent()
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
				return navEmit(asJSON, cur, alreadyAtSummary("already at the top of the stack", cur))
			}
			dest, err := teleportCheckout(leaf)
			if err != nil {
				return err
			}
			return navEmit(asJSON, leaf, navSummary("moved to top of stack:", leaf, dest))
		case 1:
			leaf = children[0].Name
		default:
			return fmt.Errorf("%s is a branch point with %d children; use st checkout to pick one", leaf, len(children))
		}
	}
}

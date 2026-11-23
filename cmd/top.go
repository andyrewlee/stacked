package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
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

func runTop(args []string) error {
	asJSON, err := parsePlain("top", args)
	if err != nil {
		return err
	}

	s, cur, release, err := lockAndLoadCurrent()
	if err != nil {
		return err
	}
	defer release()
	if cur != s.Trunk && !s.IsTracked(cur) {
		return stack.ErrNotTracked(cur)
	}

	leaf := cur
	seen := map[string]bool{cur: true}
	for {
		children := s.Children(leaf)
		switch len(children) {
		case 0:
			if leaf == cur {
				return navEmit(asJSON, cur, fmt.Sprintf("already at the top of the stack (%s)", cur))
			}
			if err := checkoutBranch(leaf); err != nil {
				return err
			}
			return navEmitText(asJSON, leaf,
				fmt.Sprintf("moved to top of stack: %s", leaf),
				fmt.Sprintf("moved to top of stack: %s", sanitizeForTerminal(leaf)))
		case 1:
			next := children[0].Name
			if seen[next] {
				return fmt.Errorf("stack metadata contains a parent cycle at %q; run `st doctor --fix` to repair", next)
			}
			seen[next] = true
			leaf = next
		default:
			return fmt.Errorf("%s is a branch point with %d children; use st checkout to pick one", leaf, len(children))
		}
	}
}

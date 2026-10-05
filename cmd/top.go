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

// runTop walks upward from the current branch following single children until it
// reaches the leaf of the stack, then checks that leaf out. A branch point (more
// than one child) stops the walk and directs the user to st checkout.
func runTop(args []string) error {
	asJSON, err := parsePlain("top", args)
	if err != nil {
		return err
	}

	// Navigation moves HEAD, so it takes the same repository lock mutations
	// take: a concurrent cascade or a second navigator must not interleave
	// with this move.
	s, cur, release, err := lockAndLoadCurrent()
	if err != nil {
		return err
	}
	defer release()
	if cur != s.Trunk && !s.IsTracked(cur) {
		return stack.ErrNotTracked(cur)
	}

	// The walk is unbounded over corrupted metadata: guard the cycle the
	// same way the stack.go topology helpers do — every other reader of
	// Children carries a seen set for exactly this case.
	leaf := cur
	seen := map[string]bool{cur: true}
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
			return navEmitText(asJSON, leaf, navSummary("moved to top of stack:", leaf, dest), navSummaryForTerminal("moved to top of stack:", leaf, dest))
		case 1:
			next := children[0].Name
			if seen[next] {
				return fmt.Errorf("stack metadata contains a parent cycle at %q; run `st validate` to inspect and `st repair` to fix", next)
			}
			seen[next] = true
			leaf = next
		default:
			return fmt.Errorf("%s is a branch point with %d children; use st checkout to pick one", leaf, len(children))
		}
	}
}

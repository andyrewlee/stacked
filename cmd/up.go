package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "up",
		Aliases: []string{"u"},
		Summary: "Move up the stack to a child branch",
		Usage:   "st up [n] [--json]",
		Run:     runUp,
	})
}

func runUp(args []string) error {
	var asJSON bool
	fs := newFlagSet("up", &asJSON)
	n, err := parseCount(fs, args, "up")
	if err != nil {
		return err
	}

	state, cur, release, err := lockAndLoadCurrent()
	if err != nil {
		return err
	}
	defer release()
	if cur != state.Trunk && !state.IsTracked(cur) {
		return stack.ErrNotTracked(cur)
	}

	start := cur
	checkout := func(name string) error {
		if name == start {
			return nil
		}
		return checkoutBranch(name)
	}

	for i := 0; i < n; i++ {
		children := state.Children(cur)
		switch len(children) {
		case 0:
			if err := checkout(cur); err != nil {
				return err
			}
			if cur == start {
				return navEmit(asJSON, cur, "already at the top of the stack")
			}
			return navEmitText(asJSON, cur,
				fmt.Sprintf("switched to %s (top of stack)", cur),
				fmt.Sprintf("switched to %s (top of stack)", sanitizeForTerminal(cur)))
		case 1:
			cur = children[0].Name
		default:
			if err := checkout(cur); err != nil {
				return err
			}
			names := make([]string, 0, len(children))
			for _, c := range children {
				names = append(names, c.Name)
			}
			return emit(asJSON, struct {
				Branch   string   `json:"branch"`
				Summary  string   `json:"summary"`
				Children []string `json:"children"`
			}{cur, "branch point: pick a child with st checkout", names}, func() {
				out("%s\n", branchPointSummaryForTerminal(cur))
				for _, name := range names {
					out("  %s\n", sanitizeForTerminal(name))
				}
			})
		}
	}

	if err := checkout(cur); err != nil {
		return err
	}
	return navEmitText(asJSON, cur,
		fmt.Sprintf("switched to %s", cur),
		fmt.Sprintf("switched to %s", sanitizeForTerminal(cur)))
}

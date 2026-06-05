package cmd

import (
	"flag"
	"fmt"
	"strconv"

	"stacked/internal/git"
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

// runUp climbs n levels up the stack from the current branch, following child
// links, and checks out the resulting branch. It stops at a branch point with
// multiple children.
func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st up [n] [--json]") }
	if err := parseArgs(fs, args); err != nil {
		return err
	}

	n := 1
	if rest := fs.Args(); len(rest) > 1 {
		return fmt.Errorf("up takes at most one level count, got %q", rest[1])
	} else if len(rest) > 0 {
		parsed, err := strconv.Atoi(rest[0])
		if err != nil {
			return fmt.Errorf("invalid level count %q: %w", rest[0], err)
		}
		if parsed < 1 {
			return fmt.Errorf("level count must be a positive integer, got %d", parsed)
		}
		n = parsed
	}

	state, err := loadState()
	if err != nil {
		return err
	}
	cur, err := currentBranch()
	if err != nil {
		return err
	}
	if cur != state.Trunk && !state.IsTracked(cur) {
		return fmt.Errorf("branch %q is not tracked by stacked", cur)
	}

	start := cur
	checkout := func(name string) error {
		if name == start {
			return nil
		}
		if err := git.Checkout(name); err != nil {
			return fmt.Errorf("checking out %q: %w", name, err)
		}
		return nil
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
			return navEmit(asJSON, cur, fmt.Sprintf("switched to %s (top of stack)", cur))
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
				out("multiple children of %s; pick one and run \"st checkout <name>\":\n", cur)
				for _, name := range names {
					out("  %s\n", name)
				}
			})
		}
	}

	if err := checkout(cur); err != nil {
		return err
	}
	return navEmit(asJSON, cur, fmt.Sprintf("switched to %s", cur))
}

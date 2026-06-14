package cmd

import (
	"fmt"
	"sort"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "repair",
		Summary: "Reconcile the stack metadata with the repository (fix drift)",
		Usage:   "st repair [--json]",
		Run:     runRepair,
	})
}

// runRepair fixes the inconsistencies that `st validate` reports: it untracks
// branches whose git branch was deleted outside st (re-parenting their children),
// re-parents branches whose parent is no longer valid onto the trunk, and breaks
// parent cycles by re-parenting onto the trunk. Re-parented branches may then
// need `st restack`.
func runRepair(args []string) error {
	var asJSON bool
	fs := newFlagSet("repair", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("repair", fs.Args()); err != nil {
		return err
	}

	var fixes []string
	if err := mutateState("repair", asJSON, func(_ stack.Env, s *stack.State) error {
		// One for-each-ref read answers every branch-exists and tip question for
		// the whole forest, instead of a show-ref/rev-parse spawn per branch.
		tips, err := git.Tips()
		if err != nil {
			return err
		}
		trunkTip, ok := tips[s.Trunk]
		if !ok {
			return fmt.Errorf("trunk branch %q does not exist; cannot repair", s.Trunk)
		}

		names := make([]string, 0, len(s.Branches))
		for name := range s.Branches {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			b, ok := s.Get(name)
			if !ok {
				continue // removed during an earlier fix
			}

			// Branch deleted outside st: untrack it, re-parenting children.
			// Not stack.RemoveBranch: a child with no recorded base gets a
			// repaired ParentSHA here, rather than keeping it verbatim.
			if _, exists := tips[name]; !exists {
				newParent, psha := b.Parent, trunkTip
				if _, parentExists := tips[newParent]; newParent != s.Trunk && !parentExists {
					newParent = s.Trunk
				} else if sha, ok := tips[newParent]; ok {
					psha = sha
				}
				for _, child := range s.Children(name) {
					child.Parent = newParent
					if child.ParentSHA == "" {
						child.ParentSHA = psha
					}
				}
				s.Untrack(name)
				fixes = append(fixes, fmt.Sprintf("untracked missing branch %s (children re-parented onto %s)", name, newParent))
				continue
			}

			// Parent no longer valid: re-parent onto the trunk.
			if _, parentExists := tips[b.Parent]; b.Parent != s.Trunk && (!s.IsTracked(b.Parent) || !parentExists) {
				b.Parent = s.Trunk
				b.ParentSHA = repairedParentSHA(s.Trunk, name, trunkTip)
				fixes = append(fixes, fmt.Sprintf("re-parented %s onto trunk (its parent was invalid)", name))
				continue
			}

			// Cycle: break it by re-parenting onto the trunk.
			if cyclePath(s, name) != "" {
				b.Parent = s.Trunk
				b.ParentSHA = repairedParentSHA(s.Trunk, name, trunkTip)
				fixes = append(fixes, fmt.Sprintf("broke a parent cycle at %s (re-parented onto trunk)", name))
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if fixes == nil {
		fixes = []string{}
	}
	payload := struct {
		Repaired bool     `json:"repaired"`
		Fixes    []string `json:"fixes"`
	}{len(fixes) > 0, fixes}
	return emit(asJSON, payload, func() {
		if len(fixes) == 0 {
			out("nothing to repair; the stack is consistent with the repository\n")
			return
		}
		out("repaired:\n")
		for _, f := range fixes {
			out("  - %s\n", f)
		}
		out("run `st restack` to rebase any re-parented branches onto their new parents.\n")
	})
}

func repairedParentSHA(trunk, branch, fallback string) string {
	if sha, err := git.MergeBase(trunk, branch); err == nil {
		return sha
	}
	return fallback
}

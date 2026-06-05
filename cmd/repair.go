package cmd

import (
	"flag"
	"fmt"
	"sort"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "repair",
		Summary: "Reconcile the stack metadata with the repository (fix drift)",
		Usage:   "st repair",
		Run:     runRepair,
	})
}

// runRepair fixes the inconsistencies that `st validate` reports: it untracks
// branches whose git branch was deleted outside st (re-parenting their children),
// re-parents branches whose parent is no longer valid onto the trunk, and breaks
// parent cycles by re-parenting onto the trunk. Re-parented branches may then
// need `st restack`.
func runRepair(args []string) error {
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "usage: st repair [--json]") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectArgs("repair", fs.Args()); err != nil {
		return err
	}

	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()

	if !git.BranchExists(s.Trunk) {
		return fmt.Errorf("trunk branch %q does not exist; cannot repair", s.Trunk)
	}

	trunkTip, err := git.RevParse(s.Trunk)
	if err != nil {
		return err
	}

	if err := s.RecordUndo("repair"); err != nil {
		return err
	}

	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)

	var fixes []string
	for _, name := range names {
		b, ok := s.Get(name)
		if !ok {
			continue // removed during an earlier fix
		}

		// Branch deleted outside st: untrack it, re-parenting children.
		if !git.BranchExists(name) {
			newParent, psha := b.Parent, trunkTip
			if newParent != s.Trunk && !git.BranchExists(newParent) {
				newParent = s.Trunk
			} else if sha, err := git.RevParse(newParent); err == nil {
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
		if b.Parent != s.Trunk && (!s.IsTracked(b.Parent) || !git.BranchExists(b.Parent)) {
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

	if len(fixes) > 0 {
		if err := s.Save(); err != nil {
			return fmt.Errorf("saving stack state: %w", err)
		}
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

package stack

import (
	"fmt"
	"sort"
	"strings"
)

// Repair reconciles the stack metadata with the repository, fixing the
// inconsistencies st validate reports: it untracks branches whose git branch was
// deleted outside st (re-parenting their children), re-parents branches whose
// parent is no longer valid onto the trunk, and breaks parent cycles by
// re-parenting onto the trunk. Re-parented branches may then need a restack. The
// human-readable fixes are returned in OpResult.Notes. As a pure engine
// operation over the Git port it is visible to the invariant model, unlike the
// old cmd-only closure.
func Repair(env Env, s *State) (*OpResult, error) {
	g := env.Git
	// One for-each-ref read answers every branch-exists and tip question for the
	// whole forest, instead of a show-ref/rev-parse spawn per branch.
	tips, err := g.Tips()
	if err != nil {
		return nil, err
	}
	trunkTip, ok := tips[s.Trunk]
	if !ok {
		return nil, fmt.Errorf("trunk branch %q does not exist; cannot repair", s.Trunk)
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

		// Branch deleted outside st: untrack it, re-parenting children. Not
		// RemoveBranch: a child with no recorded base gets a repaired ParentSHA
		// here, rather than keeping it verbatim.
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
			b.ParentSHA = repairedParentSHA(g, s.Trunk, name, trunkTip)
			fixes = append(fixes, fmt.Sprintf("re-parented %s onto trunk (its parent was invalid)", name))
			continue
		}

		// Cycle: break it by re-parenting onto the trunk.
		if CyclePath(s, name) != "" {
			b.Parent = s.Trunk
			b.ParentSHA = repairedParentSHA(g, s.Trunk, name, trunkTip)
			fixes = append(fixes, fmt.Sprintf("broke a parent cycle at %s (re-parented onto trunk)", name))
		}
	}
	return &OpResult{Summary: "repair complete", Notes: fixes}, nil
}

// repairedParentSHA returns the merge-base of trunk and branch (the correct new
// recorded base after re-parenting onto trunk), or the trunk tip when no merge
// base exists.
func repairedParentSHA(g Git, trunk, branch, fallback string) string {
	if sha, err := g.MergeBase(trunk, branch); err == nil {
		return sha
	}
	return fallback
}

// CyclePath walks the parent chain from name and returns a human-readable path
// (e.g. "a -> b -> a") if a cycle is reached before the trunk, or "" if the
// chain is sound or ends at an untracked parent (reported separately). It is the
// single cycle detector shared by validate and Repair.
func CyclePath(s *State, name string) string {
	seen := map[string]bool{name: true}
	path := []string{name}
	cur := name
	for {
		b, ok := s.Get(cur)
		if !ok || b.Parent == s.Trunk {
			return ""
		}
		path = append(path, b.Parent)
		if seen[b.Parent] {
			return strings.Join(path, " -> ")
		}
		seen[b.Parent] = true
		cur = b.Parent
	}
}

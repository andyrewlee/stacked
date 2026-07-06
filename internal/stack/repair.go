package stack

import (
	"fmt"
	"strings"
)

// ProblemKind classifies a single inconsistency between the recorded stack state
// and the repository. ParentUntracked and ParentMissing are kept distinct because
// validate reports them with different messages, even though Repair fixes both
// the same way (re-parent onto the trunk).
type ProblemKind int

const (
	TrunkMissing    ProblemKind = iota // the trunk branch's git ref is gone
	BranchMissing                      // a tracked branch's git ref is gone
	ParentUntracked                    // parent is neither the trunk nor a tracked branch
	ParentMissing                      // parent is tracked but its git ref is gone
	ParentCycle                        // the parent chain loops before reaching the trunk
)

// Problem is one inconsistency found by Inconsistencies. Detail carries the
// parent name (for the parent kinds) or the human-readable cycle path.
type Problem struct {
	Kind   ProblemKind
	Branch string
	Detail string
}

// branchProblems returns the inconsistencies of a single tracked branch against
// tips, in validate's reporting order: missing branch, invalid parent, cycle. It
// reads the current state, so a caller that mutates between branches (Repair)
// sees the updates; the first element is therefore the highest-priority problem.
func (s *State) branchProblems(tips map[string]string, name string) []Problem {
	b, ok := s.Get(name)
	if !ok {
		return nil
	}
	var ps []Problem
	if _, branchOK := tips[name]; !branchOK {
		ps = append(ps, Problem{Kind: BranchMissing, Branch: name})
	}
	if b.Parent != s.Trunk {
		if !s.IsTracked(b.Parent) {
			ps = append(ps, Problem{Kind: ParentUntracked, Branch: name, Detail: b.Parent})
		} else if _, parentExists := tips[b.Parent]; !parentExists {
			ps = append(ps, Problem{Kind: ParentMissing, Branch: name, Detail: b.Parent})
		}
	}
	if path := CyclePath(s, name); path != "" {
		ps = append(ps, Problem{Kind: ParentCycle, Branch: name, Detail: path})
	}
	return ps
}

// Inconsistencies returns every inconsistency between the recorded state and the
// repository, computed from one Tips() map: a missing trunk first, then each
// tracked branch in sorted order (missing branch, invalid parent, cycle). It is
// the single definition of an inconsistent stack — validate renders every
// Problem and Repair fixes them, so the two cannot drift apart.
func (s *State) Inconsistencies(tips map[string]string) []Problem {
	var ps []Problem
	if _, ok := tips[s.Trunk]; !ok {
		ps = append(ps, Problem{Kind: TrunkMissing, Branch: s.Trunk})
	}
	for _, name := range sortedBranchNames(s) {
		ps = append(ps, s.branchProblems(tips, name)...)
	}
	return ps
}

// Repair reconciles the stack metadata with the repository, fixing the
// inconsistencies Inconsistencies/validate report: it untracks branches whose
// git branch was deleted outside st (re-parenting their children), re-parents
// branches whose parent is no longer valid onto the trunk, and breaks parent
// cycles by re-parenting onto the trunk. Re-parented branches may then need a
// restack. The human-readable fixes are returned in OpResult.Notes. As a pure
// engine operation over the Git port it is visible to the invariant model.
func Repair(env Env, s *State) (*OpResult, error) {
	g := env.Git
	// Repair is a recovery path: tracked names may already be corrupt enough
	// that they cannot be safely passed back to git. Read all branch tips and
	// classify invalid tracked names as missing so repair can remove them.
	tips, err := g.Tips()
	if err != nil {
		return nil, err
	}
	trunkTip, ok := tips[s.Trunk]
	if !ok {
		return nil, fmt.Errorf("trunk branch %q does not exist; cannot repair", s.Trunk)
	}

	var fixes []string
	for _, name := range sortedBranchNames(s) {
		b, ok := s.Get(name)
		if !ok {
			continue // removed during an earlier fix
		}
		// branchProblems reads current state, so a fix applied to an earlier
		// branch (untracking a missing branch and re-parenting its children) is
		// reflected here. The highest-priority problem wins — the exclusivity the
		// repair loop always had.
		probs := s.branchProblems(tips, name)
		if len(probs) == 0 {
			continue
		}
		switch probs[0].Kind {
		case BranchMissing:
			// Not RemoveBranch: a child with no recorded base gets a repaired
			// ParentSHA here, rather than keeping it verbatim.
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
		case ParentUntracked, ParentMissing:
			b.Parent = s.Trunk
			b.ParentSHA = repairedParentSHA(g, s.Trunk, name, trunkTip)
			fixes = append(fixes, fmt.Sprintf("re-parented %s onto trunk (its parent was invalid)", name))
		case ParentCycle:
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

// ops_track.go — metadata ops that change which branches are tracked:
// TrackBranch/TrackAllBranches (with parent inference and adoption ordering),
// UntrackBranch, and Rename.
package stack

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
)

// TrackBranch starts tracking name — the current branch when name is empty.
// parent is used when non-empty (it must be the trunk or a tracked branch);
// otherwise it is inferred from the commit graph as the closest tracked
// ancestor, exactly as the current-branch path does. HEAD never moves.
func TrackBranch(env Env, s *State, name, parent string) (*OpResult, error) {
	g := env.Git
	if name == "" {
		cur, err := g.CurrentBranch()
		if err != nil {
			return nil, err
		}
		name = cur
	}
	if name == s.Trunk {
		return nil, fmt.Errorf("cannot track the trunk branch %q", name)
	}
	if s.IsTracked(name) {
		return nil, fmt.Errorf("branch %q is already tracked", name)
	}
	if !g.BranchExists(name) {
		return nil, fmt.Errorf("branch %q does not exist", name)
	}
	if parent != "" {
		if parent == name {
			return nil, errors.New("a branch cannot be its own parent")
		}
		if parent != s.Trunk && !s.IsTracked(parent) {
			return nil, fmt.Errorf("parent %q is not the trunk or a tracked branch", parent)
		}
	} else {
		var err error
		parent, err = inferParent(g, s, name)
		if err != nil {
			return nil, err
		}
	}
	if err := trackOne(env, s, name, parent); err != nil {
		return nil, err
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Tracking %s (parent: %s)", name, parent), Branch: name}, nil
}

// trackOne records name under parent — the shared tail of TrackBranch and
// TrackAllBranches: merge-base the pair and update the forest. The caller
// persists via env.save() (once per command, not per branch).
func trackOne(env Env, s *State, name, parent string) error {
	parentSHA, err := env.Git.MergeBase(branchTipRef(parent), branchTipRef(name))
	if err != nil {
		return fmt.Errorf("computing merge base of %q and %q: %w", parent, name, err)
	}
	s.Track(name, parent, parentSHA)
	return nil
}

// trackAllPlan is the read-only core TrackAllBranches and TrackAllPlan share:
// enumerate the untracked local branches, infer each one's parent (skipping
// orphans with a note), and refuse cyclic proposals. It returns the proposed
// parent map plus the untracked count so callers can pick the right
// nothing-to-adopt message — and it never mutates the state or saves.
func trackAllPlan(g Git, s *State) (parents map[string]string, notes []string, untracked int, err error) {
	tips, err := g.Tips()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("list local branches: %w", err)
	}
	candidates := []string{s.Trunk}
	var names []string
	for name := range tips {
		if name == s.Trunk {
			continue
		}
		candidates = append(candidates, name)
		if !s.IsTracked(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, nil, 0, nil
	}

	// The trunk's merged set is loop-invariant (the op holds the lock and
	// never fetches) — compute it once instead of per candidate.
	mergedIntoTrunk, err := g.MergedInto(branchTipRef(s.Trunk))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("list branches merged into %q: %w", s.Trunk, err)
	}
	// Each name's inference is an independent set of read-only probes — fan it
	// out and resolve the results in sorted-name order so the parent map, the
	// orphan notes, and the first error match a serial pass exactly.
	picks := make([]string, len(names))
	if err := git.ParallelProbes(len(names), func(i int) error {
		p, err := inferParentAmongMerged(g, s.Trunk, mergedIntoTrunk, names[i], candidates)
		picks[i] = p
		return err
	}); err != nil {
		return nil, nil, 0, err
	}
	parents = make(map[string]string, len(names))
	for i, name := range names {
		parent := picks[i]
		if parent == s.Trunk {
			// The trunk fallback means nothing else is an ancestor — if the
			// branch also shares no history with the trunk (an orphan like
			// gh-pages) there is no base to record; skip it rather than fail
			// the whole adoption.
			if _, err := g.MergeBase(branchTipRef(s.Trunk), branchTipRef(name)); err != nil {
				notes = append(notes, fmt.Sprintf("skipped %s: no common commit history with %s", name, s.Trunk))
				continue
			}
		}
		parents[name] = parent
	}
	if len(parents) == 0 {
		return nil, notes, len(names), nil
	}
	adoptable := make([]string, 0, len(parents))
	for name := range parents {
		adoptable = append(adoptable, name)
	}
	sort.Strings(adoptable)
	if cyclic := adoptionCycles(adoptable, parents); len(cyclic) > 0 {
		return nil, nil, 0, fmt.Errorf("cannot infer parents for %v (their proposals form a cycle): track them one at a time with --parent", cyclic)
	}
	return parents, notes, len(names), nil
}

// TrackAllPlan previews a bulk adopt: the inferred parent map (Tracked) plus
// per-branch skip notes, with nothing recorded — the dry-run half of
// TrackAllBranches, identical apart from the apply.
func TrackAllPlan(env Env, s *State) (*OpResult, error) {
	parents, notes, untracked, err := trackAllPlan(env.Git, s)
	if err != nil {
		return nil, err
	}
	res := &OpResult{DryRun: true, Tracked: parents, Notes: notes}
	switch {
	case untracked == 0:
		res.Summary = "Nothing to adopt: every local branch is already tracked"
	case len(parents) == 0:
		res.Summary = "Nothing to adopt: no adoptable untracked branches"
	default:
		res.Summary = fmt.Sprintf("Would track %d branch(es)", len(parents))
	}
	return res, nil
}

// TrackAllBranches adopts every untracked local branch in one operation,
// inferring each branch's parent from the full local-branch set — so an
// existing a→b→c stack comes in as a proper chain rather than three
// trunk-parented orphans. The proposed parent map is checked for cycles
// (possible on odd DAGs) before anything is recorded: a cyclic proposal fails
// the whole command naming the cycle members, which need explicit --parent
// calls. Branches are applied parents-first; one env.save() persists the
// whole batch so a single undo entry covers it.
func TrackAllBranches(env Env, s *State) (*OpResult, error) {
	parents, notes, untracked, err := trackAllPlan(env.Git, s)
	if err != nil {
		return nil, err
	}
	if untracked == 0 {
		return &OpResult{Summary: "Nothing to adopt: every local branch is already tracked"}, nil
	}
	if len(parents) == 0 {
		return &OpResult{Summary: "Nothing to adopt: no adoptable untracked branches", Notes: notes}, nil
	}
	adoptable := make([]string, 0, len(parents))
	for name := range parents {
		adoptable = append(adoptable, name)
	}
	sort.Strings(adoptable)
	for _, name := range adoptionOrder(adoptable, parents) {
		if err := trackOne(env, s, name, parents[name]); err != nil {
			return nil, err
		}
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	return &OpResult{
		Summary: fmt.Sprintf("Tracked %d branch(es)", len(parents)),
		Tracked: parents,
		Notes:   notes,
	}, nil
}

// adoptionCycles returns the sorted members of any cycle in the proposed
// parent map, restricted to edges between untracked branches (parents that are
// the trunk or already tracked are roots). Each node has at most one outgoing
// edge, so a walk that revisits a node on the same path has found a cycle.
func adoptionCycles(untracked []string, parents map[string]string) []string {
	cyclic := map[string]bool{}
	for _, n := range untracked {
		seen := map[string]int{}
		var path []string
		for cur := n; ; {
			p, open := parents[cur]
			if !open {
				break // reached the trunk or an already-tracked parent
			}
			if idx, ok := seen[cur]; ok {
				for _, m := range path[idx:] {
					cyclic[m] = true
				}
				break
			}
			if cyclic[cur] {
				break // this path only leads into a cycle; it is not one
			}
			seen[cur] = len(path)
			path = append(path, cur)
			cur = p
		}
	}
	var names []string
	for n := range cyclic {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// adoptionOrder emits untracked branches parents-before-children. Call only
// after adoptionCycles reports none: every chain then bottoms out at the
// trunk or an already-tracked (or already-emitted) parent.
func adoptionOrder(untracked []string, parents map[string]string) []string {
	emitted := make(map[string]bool, len(untracked))
	var order []string
	for _, n := range untracked {
		var chain []string
		for cur := n; !emitted[cur]; {
			chain = append(chain, cur)
			p := parents[cur]
			if _, pUntracked := parents[p]; emitted[p] || !pUntracked {
				break
			}
			cur = p
		}
		for i := len(chain) - 1; i >= 0; i-- {
			order = append(order, chain[i])
			emitted[chain[i]] = true
		}
	}
	return order
}

// inferParent picks the closest tracked ancestor (or the trunk) of name — the
// branch being adopted, current or not.
func inferParent(g Git, s *State, name string) (string, error) {
	candidates := []string{s.Trunk}
	for n := range s.Branches {
		if n != name {
			candidates = append(candidates, n)
		}
	}
	return inferParentAmong(g, s.Trunk, name, candidates)
}

// inferParentAmong picks the closest ancestor of name among candidates (the
// trunk is the implicit fallback). The two per-candidate ancestry questions
// ("is c an ancestor of name?" and "is c merged into trunk?") are answered by
// two bounded `for-each-ref --merged` scans: tip(c) is an ancestor of name iff
// c ∈ MergedInto(name), and is merged into the trunk iff c ∈ MergedInto(trunk).
// A branch missing from the merged set (e.g. deleted) simply fails the ancestor
// test — it cannot be a parent. Only the closest-ancestor tie-break still
// spawns, and just for the few candidates that are actual ancestors of name.
// name, trunk, and candidates are branch NAMES — this function qualifies them
// to refs/heads/ itself, so callers can never hand it a shadowable bare name.
func inferParentAmong(g Git, trunk, name string, candidates []string) (string, error) {
	mergedIntoName, err := g.MergedInto(branchTipRef(name))
	if err != nil {
		return "", fmt.Errorf("list branches merged into %q: %w", name, err)
	}
	mergedIntoTrunk, err := g.MergedInto(branchTipRef(trunk))
	if err != nil {
		return "", fmt.Errorf("list branches merged into %q: %w", trunk, err)
	}
	return inferParentPick(g, trunk, name, mergedIntoTrunk, mergedIntoName, candidates)
}

// inferParentAmongMerged is inferParentAmong with the trunk's merged set
// already computed — TrackAllBranches hoists the loop-invariant probe out of
// its per-candidate loop.
func inferParentAmongMerged(g Git, trunk string, mergedIntoTrunk map[string]bool, name string, candidates []string) (string, error) {
	mergedIntoName, err := g.MergedInto(branchTipRef(name))
	if err != nil {
		return "", fmt.Errorf("list branches merged into %q: %w", name, err)
	}
	return inferParentPick(g, trunk, name, mergedIntoTrunk, mergedIntoName, candidates)
}

// inferParentPick selects the closest ancestor of name from candidates given
// the two precomputed merged sets — the pure half both probe paths share.
func inferParentPick(g Git, trunk, name string, mergedIntoTrunk, mergedIntoName map[string]bool, candidates []string) (string, error) {
	best := trunk
	// Iterate in a fixed order so the choice between incomparable ancestors (two
	// candidates where neither is an ancestor of the other, e.g. across a
	// merge) is deterministic rather than dependent on map iteration order. The
	// sort works on a copy — callers share the candidate slice across names,
	// and trackAllPlan now runs those inferences concurrently.
	sorted := slices.Clone(candidates)
	sort.Strings(sorted)
	for _, c := range sorted {
		if c == name || c == best {
			continue
		}
		if !mergedIntoName[c] {
			continue // not an ancestor of name (or the git branch is gone)
		}
		if mergedIntoTrunk[c] {
			continue // already merged into the trunk
		}
		if best != trunk {
			bestIsAncestor, err := g.IsAncestor(branchTipRef(best), branchTipRef(c))
			if err != nil {
				return "", fmt.Errorf("check whether %q is an ancestor of %q: %w", best, c, err)
			}
			if !bestIsAncestor {
				continue
			}
		}
		best = c
	}
	return best, nil
}

// UntrackBranch stops tracking name (the current branch when name is empty),
// re-parenting its children onto its parent. The git branch is not deleted.
func UntrackBranch(env Env, s *State, name string) (*OpResult, error) {
	g := env.Git
	if name == "" {
		cur, err := g.CurrentBranch()
		if err != nil {
			return nil, err
		}
		name = cur
	}
	if name == s.Trunk {
		return nil, fmt.Errorf("cannot untrack the trunk %q", name)
	}
	b, err := s.tracked(name)
	if err != nil {
		return nil, err
	}
	children := s.Children(name)
	mergedIntoParent := false
	if len(children) > 0 {
		mergedIntoParent, err = g.IsAncestor(branchTipRef(name), branchTipRef(b.Parent))
		if err != nil {
			if g.BranchExists(name) {
				return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, b.Parent, err)
			}
			mergedIntoParent = false
		}
	}
	// Not RemoveBranch: an untracked branch's commits remain part of the
	// children's history, so the children's ParentSHA must move to the
	// untracked branch's base (unless it merged), not stay where it was.
	for _, child := range children {
		parentSHA := b.ParentSHA
		if mergedIntoParent {
			parentSHA = child.ParentSHA
		}
		s.Track(child.Name, b.Parent, parentSHA)
	}
	s.Untrack(name)
	if err := env.save(); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Untracked %s (re-parented children onto %s)", name, b.Parent), Branch: name}, nil
}

// Rename renames oldName (the current branch when empty) to newName with git and
// updates the metadata: the branch record, the trunk name if applicable, and
// every child's parent pointer.
func Rename(env Env, s *State, oldName, newName string) (*OpResult, error) {
	g := env.Git
	if oldName == "" {
		cur, err := g.CurrentBranch()
		if err != nil {
			return nil, err
		}
		oldName = cur
	}
	if oldName == newName {
		return nil, errors.New("new name is the same as the old name")
	}
	if g.BranchExists(newName) {
		return nil, fmt.Errorf("branch %q already exists", newName)
	}
	isTrunk := oldName == s.Trunk
	if !isTrunk && !s.IsTracked(oldName) {
		return nil, fmt.Errorf("%q is not the trunk or a tracked branch", oldName)
	}
	if err := g.RenameBranch(oldName, newName); err != nil {
		return nil, fmt.Errorf("renaming branch: %w", err)
	}
	if isTrunk {
		s.Trunk = newName
	} else {
		b := s.Branches[oldName]
		b.Name = newName
		delete(s.Branches, oldName)
		s.Branches[newName] = b
	}
	for _, child := range s.Branches {
		if child.Parent == oldName {
			child.Parent = newName
		}
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Renamed %s -> %s", oldName, newName), Branch: newName}, nil
}

package stack

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// OpResult is the typed outcome of a stack-mutating operation. The CLI renders
// it as text or JSON; the engine never prints.
type OpResult struct {
	Summary   string   `json:"summary"`
	Branch    string   `json:"branch,omitempty"`
	Restacked []string `json:"restacked,omitempty"`
	Deleted   []string `json:"deleted,omitempty"`
	Notes     []string `json:"notes,omitempty"`
	// DryRun marks a preview: the Restacked/Deleted lists are what *would* happen,
	// nothing was changed.
	DryRun bool `json:"dryRun,omitempty"`
}

// ErrDirty is returned when an operation needs a clean working tree but the tree
// has uncommitted changes. ErrConflict is returned when a rebase stops on a
// conflict and the user must resolve it and run `st continue`. Both are sentinels
// the CLI maps to dedicated exit codes.
var (
	ErrDirty    = errors.New("working tree is dirty; commit or stash first")
	ErrConflict = errors.New("rebase conflict — resolve the conflicts, stage them with git add, then run: st continue")
)

// requireClean returns ErrDirty when the working tree has uncommitted changes.
func requireClean(g Git) error {
	clean, err := g.IsClean()
	if err != nil {
		return fmt.Errorf("checking working tree: %w", err)
	}
	if !clean {
		return ErrDirty
	}
	return nil
}

// restoreHEAD checks out target (falling back to fallback when target is gone),
// returning HEAD to where the user started after an operation moved it.
func restoreHEAD(env Env, target, fallback string) error {
	g := env.Git
	if !g.BranchExists(target) {
		target = fallback
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return fmt.Errorf("determine current branch: %w", err)
	}
	if cur == target {
		return nil
	}
	if err := g.Checkout(target); err != nil {
		return fmt.Errorf("restore branch %q: %w", target, err)
	}
	return nil
}

// Create makes a new branch stacked on the current branch and tracks it. With
// all it stages everything first; with a message it commits the staged changes.
func Create(env Env, s *State, name, message string, all bool) (*OpResult, error) {
	g := env.Git
	if g.BranchExists(name) {
		return nil, fmt.Errorf("branch %q already exists", name)
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	parentSHA, err := g.RevParse(cur)
	if err != nil {
		return nil, fmt.Errorf("resolving parent %q: %w", cur, err)
	}
	if all {
		if err := g.Add(); err != nil {
			return nil, fmt.Errorf("staging changes: %w", err)
		}
	}
	staged, err := g.HasStagedChanges()
	if err != nil {
		return nil, fmt.Errorf("checking staged changes: %w", err)
	}
	if err := g.CreateBranch(name); err != nil {
		return nil, fmt.Errorf("creating branch %q: %w", name, err)
	}
	if message != "" {
		if err := g.Commit(message, all); err != nil {
			return nil, fmt.Errorf("committing on %q: %w", name, err)
		}
	} else if staged {
		return nil, errors.New("staged changes present; provide a commit message with -m")
	}
	s.Track(name, cur, parentSHA)
	if err := env.save(); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Created %s on top of %s", name, cur), Branch: name}, nil
}

// Modify amends (or, with commit, adds) a commit on the current branch and
// restacks its descendants onto the new tip.
func Modify(env Env, s *State, message string, all, commit bool) (*OpResult, error) {
	g := env.Git
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if cur == s.Trunk {
		return nil, fmt.Errorf("refusing to modify the trunk branch %q", cur)
	}
	if all {
		if err := g.Add(); err != nil {
			return nil, fmt.Errorf("staging changes on %q: %w", cur, err)
		}
	}

	var action string
	switch {
	case commit:
		if message == "" {
			return nil, errors.New("--commit requires a commit message (-m <msg>)")
		}
		if err := g.Commit(message, all); err != nil {
			return nil, fmt.Errorf("committing on %q: %w", cur, err)
		}
		action = "Committed on " + cur
	case message != "":
		if err := g.AmendMessage(message, all); err != nil {
			return nil, fmt.Errorf("amending %q: %w", cur, err)
		}
		action = "Amended " + cur + " with new message"
	default:
		if err := g.AmendNoEdit(all); err != nil {
			return nil, fmt.Errorf("amending %q: %w", cur, err)
		}
		action = "Amended " + cur
	}

	// On a conflict the rebase is left in progress for `st continue`; do not
	// restore HEAD in that case.
	rebased, err := s.RestackUpstack(env, cur)
	if err != nil {
		return nil, fmt.Errorf("restacking upstack of %q: %w", cur, err)
	}
	if err := restoreHEAD(env, cur, s.Trunk); err != nil {
		return nil, err
	}
	return &OpResult{Summary: action, Branch: cur, Restacked: rebased}, nil
}

// Restack rebases the current branch and its upstack onto their parents. From
// the trunk it restacks every tracked branch. Requires a clean working tree.
func Restack(env Env, s *State) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}

	var rebased []string
	if start != s.Trunk {
		needs, err := s.NeedsRestack(g, start)
		if err != nil {
			return nil, err
		}
		if err := s.RestackBranch(env, start); err != nil {
			return nil, err
		}
		if needs {
			rebased = append(rebased, start)
		}
	}
	up, err := s.RestackUpstack(env, start)
	if err != nil {
		return nil, err
	}
	rebased = append(rebased, up...)

	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}
	if len(rebased) == 0 {
		return &OpResult{Summary: "everything up to date"}, nil
	}
	return &OpResult{Summary: "restacked", Restacked: rebased}, nil
}

// Fold folds the current branch into its parent: the parent advances to include
// the branch's commits, the branch is deleted, and its children are re-parented
// onto the parent.
func Fold(env Env, s *State) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	b, ok := s.Get(cur)
	if !ok {
		return nil, fmt.Errorf("branch %q is not tracked", cur)
	}
	parent := b.Parent
	if parent == s.Trunk {
		return nil, fmt.Errorf("cannot fold %q into the trunk %q", cur, parent)
	}
	needs, err := s.NeedsRestack(g, cur)
	if err != nil {
		return nil, err
	}
	if needs {
		return nil, fmt.Errorf("%q needs restack before folding (run: st restack)", cur)
	}

	curTip, err := g.RevParse(cur)
	if err != nil {
		return nil, err
	}
	if err := g.ForceBranch(parent, curTip); err != nil {
		return nil, fmt.Errorf("advancing %q to %q: %w", parent, cur, err)
	}
	for _, child := range s.Children(cur) {
		child.Parent = parent
	}
	s.Untrack(cur)
	if err := g.Checkout(parent); err != nil {
		return nil, fmt.Errorf("checking out %q: %w", parent, err)
	}
	if err := g.DeleteBranch(cur, true); err != nil {
		return nil, fmt.Errorf("deleting %q: %w", cur, err)
	}
	if err := env.save(); err != nil {
		return nil, err
	}

	rebased, err := s.RestackUpstack(env, parent)
	if err != nil {
		return nil, err
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	if err := restoreHEAD(env, parent, s.Trunk); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Folded %s into %s", cur, parent), Branch: parent, Restacked: rebased}, nil
}

// Squash collapses every commit on the current branch (since its parent) into
// one, then restacks its descendants. With an empty message the squashed
// message is composed from the existing commit subjects.
func Squash(env Env, s *State, message string) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	b, ok := s.Get(cur)
	if !ok {
		return nil, fmt.Errorf("branch %q is not tracked", cur)
	}
	needs, err := s.NeedsRestack(g, cur)
	if err != nil {
		return nil, err
	}
	if needs {
		return nil, fmt.Errorf("%q needs restack before squashing (run: st restack)", cur)
	}

	base := b.ParentSHA
	subjects, err := g.CommitSubjects(base, cur)
	if err != nil {
		return nil, err
	}
	if len(subjects) <= 1 {
		return &OpResult{Summary: fmt.Sprintf("%s already has a single commit; nothing to squash", cur), Branch: cur}, nil
	}
	if message == "" {
		message = subjects[len(subjects)-1]
		var body []string
		for i := len(subjects) - 2; i >= 0; i-- {
			body = append(body, "- "+subjects[i])
		}
		if len(body) > 0 {
			message += "\n\n" + strings.Join(body, "\n")
		}
	}

	if err := g.ResetSoft(base); err != nil {
		return nil, fmt.Errorf("resetting %q to base: %w", cur, err)
	}
	if err := g.Commit(message, false); err != nil {
		return nil, fmt.Errorf("creating squashed commit on %q: %w", cur, err)
	}

	rebased, err := s.RestackUpstack(env, cur)
	if err != nil {
		return nil, err
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	if err := restoreHEAD(env, cur, s.Trunk); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Squashed %d commits on %s into one", len(subjects), cur), Branch: cur, Restacked: rebased}, nil
}

// Onto re-parents the current branch onto target and rebases it (and its
// descendants) there. target must be the trunk or a tracked branch and may not
// be the branch itself or one of its descendants.
func Onto(env Env, s *State, target string) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	b, ok := s.Get(cur)
	if !ok {
		return nil, fmt.Errorf("branch %q is not tracked", cur)
	}
	if target == cur {
		return nil, fmt.Errorf("cannot move %q onto itself", cur)
	}
	if target != s.Trunk && !s.IsTracked(target) {
		return nil, fmt.Errorf("target %q is not the trunk or a tracked branch", target)
	}
	for _, d := range s.Descendants(cur) {
		if d == target {
			return nil, fmt.Errorf("cannot move %q onto its own descendant %q", cur, target)
		}
	}
	if b.Parent == target {
		return &OpResult{Summary: fmt.Sprintf("%s is already stacked on %s", cur, target), Branch: cur}, nil
	}

	oldBase := b.ParentSHA
	newParentTip, err := g.RevParse(target)
	if err != nil {
		return nil, err
	}
	// Record the new parent before rebasing so a conflict + `st continue` records
	// the correct base for cur.
	b.Parent = target
	b.ParentSHA = newParentTip
	if err := env.save(); err != nil {
		return nil, err
	}
	if err := g.RebaseOnto(newParentTip, oldBase, cur); err != nil {
		return nil, fmt.Errorf("moving %q onto %q: %w", cur, target, ErrConflict)
	}

	rebased, err := s.RestackUpstack(env, cur)
	if err != nil {
		return nil, err
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	if err := restoreHEAD(env, cur, s.Trunk); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Moved %s onto %s", cur, target), Branch: cur, Restacked: rebased}, nil
}

// Delete removes a tracked branch, re-parents its children onto the deleted
// branch's parent, and restacks them so the deleted branch's commits are dropped
// from their history.
func Delete(env Env, s *State, name string, force bool) (*OpResult, error) {
	g := env.Git
	if name == s.Trunk {
		return nil, fmt.Errorf("cannot delete the trunk branch %q", name)
	}
	b, ok := s.Get(name)
	if !ok {
		return nil, fmt.Errorf("branch %q is not tracked", name)
	}
	parent := b.Parent

	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if start == name {
		if err := g.Checkout(parent); err != nil {
			return nil, fmt.Errorf("checking out parent %q: %w", parent, err)
		}
		start = parent
	}

	// Re-parent children onto the deleted branch's parent, PRESERVING each
	// child's ParentSHA so the follow-up restack drops the deleted commits.
	children := s.Children(name)
	formerChildren := make([]string, 0, len(children))
	for _, child := range children {
		child.Parent = parent
		formerChildren = append(formerChildren, child.Name)
	}
	s.Untrack(name)
	if err := g.DeleteBranch(name, force); err != nil {
		return nil, fmt.Errorf("deleting branch %q: %w", name, err)
	}
	if err := env.save(); err != nil {
		return nil, err
	}

	for _, child := range formerChildren {
		if err := s.RestackBranch(env, child); err != nil {
			return nil, err
		}
		if _, err := s.RestackUpstack(env, child); err != nil {
			return nil, err
		}
	}
	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}

	res := &OpResult{Summary: fmt.Sprintf("Deleted %s", name), Deleted: []string{name}}
	if len(formerChildren) > 0 {
		res.Summary = fmt.Sprintf("Deleted %s; re-parented %d branch(es) onto %s", name, len(formerChildren), parent)
	}
	return res, nil
}

// Sync fetches and fast-forwards the trunk via the remote port, prunes branches
// already merged into the trunk, restacks every remaining stack onto the updated
// trunk, and restores the caller's branch. With noDelete, merged branches are
// kept. Requires a clean working tree.
func Sync(env Env, r Remote, s *State, remote string, noDelete bool) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	orig, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}

	ffResult := "skipped (no remote)"
	if r.Exists(remote) {
		if err := r.Fetch(remote); err != nil {
			return nil, fmt.Errorf("fetch %q: %w", remote, err)
		}
		ffResult, err = r.FastForward(s.Trunk, remote)
		if err != nil {
			return nil, err
		}
	}

	var deleted []string
	if !noDelete {
		if deleted, err = PruneMerged(env, s); err != nil {
			return nil, err
		}
	}
	// Persist the prune before restacking so a conflict cannot leave the metadata
	// referencing already-deleted branches.
	if err := env.save(); err != nil {
		return nil, err
	}

	rebased, err := RestackAll(env, s)
	if err != nil {
		return nil, err
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	if err := restoreHEAD(env, orig, s.Trunk); err != nil {
		return nil, err
	}

	return &OpResult{
		Summary:   "sync complete",
		Deleted:   deleted,
		Restacked: rebased,
		Notes:     []string{"trunk: " + ffResult},
	}, nil
}

// SyncPlan previews what a sync would do: which merged branches it would prune
// and which branches it would restack. It does not fetch, fast-forward, or
// mutate anything.
func SyncPlan(env Env, s *State, noDelete bool) (*OpResult, error) {
	g := env.Git
	deleted := map[string]bool{}
	var deletedList []string
	if !noDelete {
		for _, name := range sortedBranchNames(s) {
			merged, err := g.IsAncestor(name, s.Trunk)
			if err != nil {
				return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, s.Trunk, err)
			}
			if merged {
				deleted[name] = true
				deletedList = append(deletedList, name)
			}
		}
	}
	full, err := s.restackPlan(g, s.Trunk)
	if err != nil {
		return nil, err
	}
	var plan []string
	for _, name := range full {
		if !deleted[name] {
			plan = append(plan, name)
		}
	}
	return &OpResult{Summary: "sync (dry run)", Deleted: deletedList, Restacked: plan, DryRun: true}, nil
}

// Continue resumes a restack interrupted by a conflict: it completes the
// in-progress rebase, records the rebased branch's new base, and restacks the
// rest of the stack.
func Continue(env Env, s *State) (*OpResult, error) {
	g := env.Git
	inProgress, err := g.RebaseInProgress()
	if err != nil {
		return nil, err
	}
	if !inProgress {
		return nil, errors.New("no rebase in progress; nothing to continue")
	}
	conflicted, err := g.RebaseHeadName()
	if err != nil {
		return nil, err
	}
	if err := g.RebaseContinue(); err != nil {
		return nil, fmt.Errorf("rebase did not complete: %w", ErrConflict)
	}

	// The just-finished branch now sits on its parent's current tip.
	if conflicted != "" {
		if b, ok := s.Get(conflicted); ok {
			tip, err := g.RevParse(b.Parent)
			if err != nil {
				return nil, fmt.Errorf("resolve parent %q: %w", b.Parent, err)
			}
			b.ParentSHA = tip
			if err := env.save(); err != nil {
				return nil, err
			}
		}
	}

	rebased, err := RestackAll(env, s)
	if err != nil {
		return nil, err
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	if conflicted != "" {
		if err := restoreHEAD(env, conflicted, s.Trunk); err != nil {
			return nil, err
		}
	}

	res := &OpResult{Summary: "continued restack", Restacked: rebased}
	if conflicted != "" {
		res.Notes = []string{"completed: " + conflicted}
	}
	return res, nil
}

// RestackAll restacks every stack rooted on the trunk, parents before children,
// reading live tips at each step. Used by sync and continue.
func RestackAll(env Env, s *State) ([]string, error) {
	var rebased []string
	for _, root := range s.Children(s.Trunk) {
		needs, err := s.NeedsRestack(env.Git, root.Name)
		if err != nil {
			return rebased, err
		}
		if err := s.RestackBranch(env, root.Name); err != nil {
			return rebased, err
		}
		if needs {
			rebased = append(rebased, root.Name)
		}
		more, err := s.RestackUpstack(env, root.Name)
		if err != nil {
			return rebased, err
		}
		rebased = append(rebased, more...)
	}
	return rebased, nil
}

// PruneMerged deletes tracked branches whose commits are already contained in
// the trunk, re-parenting each deleted branch's children onto its parent. It
// returns the deleted branch names in sorted order. The caller persists.
func PruneMerged(env Env, s *State) ([]string, error) {
	g := env.Git
	trunk := s.Trunk
	var deleted []string
	for _, name := range sortedBranchNames(s) {
		b, ok := s.Get(name)
		if !ok {
			continue
		}
		merged, err := g.IsAncestor(name, trunk)
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, trunk, err)
		}
		if !merged {
			continue
		}
		for _, child := range s.Children(name) {
			child.Parent = b.Parent
		}
		s.Untrack(name)
		if err := g.DeleteBranch(name, true); err != nil {
			return deleted, fmt.Errorf("delete merged branch %q: %w", name, err)
		}
		deleted = append(deleted, name)
	}
	return deleted, nil
}

// TrackBranch starts tracking the current branch. parent is used when non-empty
// (it must be the trunk or a tracked branch); otherwise it is inferred from the
// commit graph as the closest tracked ancestor.
func TrackBranch(env Env, s *State, parent string) (*OpResult, error) {
	g := env.Git
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if cur == s.Trunk {
		return nil, fmt.Errorf("cannot track the trunk branch %q", cur)
	}
	if s.IsTracked(cur) {
		return nil, fmt.Errorf("branch %q is already tracked", cur)
	}
	if parent != "" {
		if parent == cur {
			return nil, errors.New("a branch cannot be its own parent")
		}
		if parent != s.Trunk && !s.IsTracked(parent) {
			return nil, fmt.Errorf("parent %q is not the trunk or a tracked branch", parent)
		}
	} else {
		var err error
		parent, err = inferParent(g, s, cur)
		if err != nil {
			return nil, err
		}
	}
	parentSHA, err := g.MergeBase(parent, cur)
	if err != nil {
		return nil, fmt.Errorf("computing merge base of %q and %q: %w", parent, cur, err)
	}
	s.Track(cur, parent, parentSHA)
	if err := env.save(); err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Tracking %s (parent: %s)", cur, parent), Branch: cur}, nil
}

// inferParent picks the closest tracked ancestor (or the trunk) of cur.
func inferParent(g Git, s *State, cur string) (string, error) {
	best := s.Trunk
	candidates := []string{s.Trunk}
	for name := range s.Branches {
		if name != cur {
			candidates = append(candidates, name)
		}
	}
	for _, c := range candidates {
		if c == cur || c == best {
			continue
		}
		ancestor, err := g.IsAncestor(c, cur)
		if err != nil {
			return "", fmt.Errorf("check whether %q is an ancestor of %q: %w", c, cur, err)
		}
		if !ancestor {
			continue
		}
		bestIsAncestor, err := g.IsAncestor(best, c)
		if err != nil {
			return "", fmt.Errorf("check whether %q is an ancestor of %q: %w", best, c, err)
		}
		if bestIsAncestor {
			best = c
		}
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
	b, ok := s.Get(name)
	if !ok {
		return nil, fmt.Errorf("branch %q is not tracked", name)
	}
	for _, child := range s.Children(name) {
		sha, err := g.RevParse(b.Parent)
		if err != nil {
			return nil, fmt.Errorf("resolving new parent %q for %q: %w", b.Parent, child.Name, err)
		}
		s.Track(child.Name, b.Parent, sha)
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

// sortedBranchNames returns all tracked branch names in deterministic order.
func sortedBranchNames(s *State) []string {
	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

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

// ConflictError reports a rebase that stopped on a conflict, naming the branch
// being rebased and the parent it was moving onto. It Unwraps to ErrConflict, so
// errors.Is(err, ErrConflict) — and the exit-2 / "conflict" mappings — still
// hold, while errors.As lets the CLI surface Branch and Onto as structured JSON
// fields instead of leaving them buried in the message prose.
type ConflictError struct {
	Action string // the verb, e.g. "rebasing" or "moving"
	Branch string // the branch whose rebase stopped
	Onto   string // the parent it was being rebased onto
}

func (e *ConflictError) Error() string {
	// Onto is empty only on the rare re-stall of an untracked branch; drop the
	// "onto …" clause then rather than render an empty quoted parent.
	if e.Onto == "" {
		return fmt.Sprintf("%s %q: %s", e.Action, e.Branch, ErrConflict.Error())
	}
	return fmt.Sprintf("%s %q onto %q: %s", e.Action, e.Branch, e.Onto, ErrConflict.Error())
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// AlsoFailed joins an operation error with the error of a follow-up
// recovery/rollback step that also failed, keeping both matchable with
// errors.Is/errors.As: "<primary>; additionally failed to <what>: <secondary>".
func AlsoFailed(primary error, what string, secondary error) error {
	return fmt.Errorf("%w; additionally failed to %s: %w", primary, what, secondary)
}

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

// tracked returns the tracked branch named name, or the canonical
// "branch %q is not tracked" error. It is the single source of that message for
// the operations that act on an existing tracked branch.
func (s *State) tracked(name string) (*Branch, error) {
	b, ok := s.Get(name)
	if !ok {
		return nil, fmt.Errorf("branch %q is not tracked", name)
	}
	return b, nil
}

// currentTracked returns the checked-out branch and its tracked metadata, or an
// error when HEAD is not on a tracked branch — the shared preamble of the
// operations that rewrite the current branch (fold, squash, onto).
func currentTracked(g Git, s *State) (string, *Branch, error) {
	cur, err := g.CurrentBranch()
	if err != nil {
		return "", nil, err
	}
	b, err := s.tracked(cur)
	if err != nil {
		return "", nil, err
	}
	return cur, b, nil
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

func restoreHEADAfterNonConflict(env Env, target, fallback string, err error) error {
	if errors.Is(err, ErrConflict) {
		return err
	}
	if restoreErr := restoreHEAD(env, target, fallback); restoreErr != nil {
		return AlsoFailed(err, fmt.Sprintf("restore %q", target), restoreErr)
	}
	return err
}

type upstackResult struct {
	restacked []string
	notes     []string
}

// finishUpstack is the common tail of the operations that rewrite a branch's
// commits (fold, squash, onto): it restacks anchor's descendants, persists, and
// restores HEAD to the original branch (or the trunk). On a non-conflict restack
// error it restores HEAD before returning; on a conflict it leaves the rebase in
// progress for `st continue`.
func finishUpstack(env Env, s *State, anchor string) (upstackResult, error) {
	rebased, err := s.RestackUpstack(env, anchor)
	if err != nil {
		return upstackResult{}, restoreHEADAfterNonConflict(env, anchor, s.Trunk, err)
	}
	notes := skippedWorktreeNotes(s)
	if err := env.save(); err != nil {
		return upstackResult{}, err
	}
	if err := restoreHEAD(env, anchor, s.Trunk); err != nil {
		return upstackResult{}, err
	}
	return upstackResult{restacked: rebased, notes: notes}, nil
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
	if cur != s.Trunk && !s.IsTracked(cur) {
		return nil, fmt.Errorf("current branch %q is not the trunk or a tracked branch", cur)
	}
	if all && message == "" {
		return nil, errors.New("-a requires a commit message (-m <msg>)")
	}
	parentSHA, err := g.RevParse(branchTipRef(cur))
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
	if message == "" && staged {
		return nil, errors.New("staged changes present; provide a commit message with -m")
	}
	if message != "" && !staged {
		return nil, errors.New("no staged changes to commit; stage changes or pass -a")
	}
	if err := g.CreateBranch(name); err != nil {
		return nil, fmt.Errorf("creating branch %q: %w", name, err)
	}
	if message != "" {
		if err := g.Commit(message, all); err != nil {
			err = fmt.Errorf("committing on %q: %w", name, err)
			if checkoutErr := g.Checkout(cur); checkoutErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", cur), checkoutErr)
			}
			if deleteErr := g.DeleteBranch(name, true); deleteErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("delete new branch %q", name), deleteErr)
			}
			return nil, err
		}
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
	if !s.IsTracked(cur) {
		return nil, fmt.Errorf("branch %q is not tracked", cur)
	}
	if all {
		if err := g.Add(); err != nil {
			return nil, fmt.Errorf("staging changes on %q: %w", cur, err)
		}
	}
	if len(s.Descendants(cur)) > 0 {
		unstaged, err := g.HasUnstagedChanges()
		if err != nil {
			return nil, fmt.Errorf("checking unstaged changes: %w", err)
		}
		if unstaged {
			return nil, ErrDirty
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

	// Restack the upstack and restore HEAD through the shared epilogue (which
	// leaves a conflict's rebase in progress for `st continue`), the same tail
	// Fold/Squash/Onto use.
	upstack, err := finishUpstack(env, s, cur)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: action, Branch: cur, Restacked: upstack.restacked, Notes: upstack.notes}, nil
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
		did, err := s.RestackBranch(env, start)
		if err != nil {
			return nil, err
		}
		if did {
			rebased = append(rebased, start)
		}
	}
	up, err := s.RestackUpstack(env, start)
	if err != nil {
		err = restoreHEADAfterNonConflict(env, start, s.Trunk, err)
		return nil, err
	}
	rebased = append(rebased, up...)

	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}
	notes := skippedWorktreeNotes(s)
	if len(rebased) == 0 && len(notes) == 0 {
		return &OpResult{Summary: "everything up to date"}, nil
	}
	return &OpResult{Summary: "restacked", Restacked: rebased, Notes: notes}, nil
}

// skippedWorktreeNotes drains the branches a restack skipped because their owning
// worktree was dirty, turning each into a human-readable note. Empty when the
// cascade skipped nothing (the common, single-tree case).
func skippedWorktreeNotes(s *State) []string {
	skipped := s.SkippedWorktrees()
	if len(skipped) == 0 {
		return nil
	}
	notes := make([]string, 0, len(skipped))
	for _, name := range skipped {
		notes = append(notes, fmt.Sprintf("skipped %s: its worktree is dirty (commit/stash there, then re-run)", name))
	}
	return notes
}

// Fold folds the current branch into its parent: the parent advances to include
// the branch's commits, the branch is deleted, and its children are re-parented
// onto the parent.
func Fold(env Env, s *State) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, b, err := currentTracked(g, s)
	if err != nil {
		return nil, err
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
	// Fold deletes cur; if it lives in another worktree git would refuse, so tear
	// that (clean) worktree down first. A dirty owner errors before any git
	// mutation, so nothing changes. (Normally cur is checked out HERE and this is
	// a no-op; the guard covers the case where fold runs against an owned-elsewhere
	// branch.)
	if err := s.releaseOwnedWorktree(env, cur); err != nil {
		return nil, err
	}

	curTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, err
	}
	// Capture the parent's tip first so the git side can be rolled back if a later
	// step fails. Advancing the parent ref and only then failing to check out or
	// delete would leave the parent silently holding cur's commits while the
	// persisted metadata still listed cur as a separate branch — a repo/state
	// disagreement a naive retry would compound. The metadata is therefore moved
	// (re-parent children, untrack cur) and saved only after every git mutation
	// has committed.
	parentTip, err := g.RevParse(branchTipRef(parent))
	if err != nil {
		return nil, err
	}
	if err := g.ForceBranch(parent, curTip); err != nil {
		return nil, fmt.Errorf("advancing %q to %q: %w", parent, cur, err)
	}
	if err := g.Checkout(parent); err != nil {
		err = fmt.Errorf("checking out %q: %w", parent, err)
		if rollbackErr := g.UpdateRef(branchTipRef(parent), parentTip); rollbackErr != nil {
			return nil, AlsoFailed(err, fmt.Sprintf("roll back %q", parent), rollbackErr)
		}
		return nil, err
	}
	if err := g.DeleteBranch(cur, true); err != nil {
		if rollbackErr := g.UpdateRef(branchTipRef(parent), parentTip); rollbackErr != nil {
			return nil, AlsoFailed(fmt.Errorf("deleting %q: %w", cur, err), fmt.Sprintf("roll back %q", parent), rollbackErr)
		}
		if restoreErr := g.Checkout(cur); restoreErr != nil {
			return nil, AlsoFailed(fmt.Errorf("deleting %q: %w", cur, err), fmt.Sprintf("restore %q after rolling back %q", cur, parent), restoreErr)
		}
		return nil, fmt.Errorf("deleting %q: %w", cur, err)
	}
	s.RemoveBranch(cur)
	if err := env.save(); err != nil {
		return nil, err
	}

	upstack, err := finishUpstack(env, s, parent)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Folded %s into %s", cur, parent), Branch: parent, Restacked: upstack.restacked, Notes: upstack.notes}, nil
}

// Squash collapses every commit on the current branch (since its parent) into
// one, then restacks its descendants. With an empty message the squashed
// message is composed from the existing commit subjects.
func Squash(env Env, s *State, message string) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, b, err := currentTracked(g, s)
	if err != nil {
		return nil, err
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
	origTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, fmt.Errorf("resolving %q before squash: %w", cur, err)
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
		err = fmt.Errorf("creating squashed commit on %q: %w", cur, err)
		if restoreErr := g.ResetSoft(origTip); restoreErr != nil {
			return nil, AlsoFailed(err, fmt.Sprintf("restore %q to %s", cur, origTip), restoreErr)
		}
		return nil, err
	}

	upstack, err := finishUpstack(env, s, cur)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Squashed %d commits on %s into one", len(subjects), cur), Branch: cur, Restacked: upstack.restacked, Notes: upstack.notes}, nil
}

// Onto re-parents the current branch onto target and rebases it (and its
// descendants) there. target must be the trunk or a tracked branch and may not
// be the branch itself or one of its descendants.
func Onto(env Env, s *State, target string) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	cur, b, err := currentTracked(g, s)
	if err != nil {
		return nil, err
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
	newParentTip, err := g.RevParse(branchTipRef(target))
	if err != nil {
		return nil, err
	}
	if rebaseErr := g.RebaseOnto(newParentTip, oldBase, cur); rebaseErr != nil {
		paused, outErr := rebaseFailure(g, rebaseErr, "moving", cur, target)
		if paused {
			s.PendingReparent = &PendingReparent{Branch: cur, Parent: target, ParentSHA: newParentTip}
			if saveErr := env.save(); saveErr != nil {
				// The reparent intent could not be persisted, so a later `st
				// continue` (a fresh process loading from disk) would recover
				// against the OLD parent and silently mis-parent cur. Abort the
				// paused rebase so git and metadata both return to the pre-Onto
				// state instead of diverging, and drop the in-memory pending entry
				// to match the unpersisted disk.
				s.PendingReparent = nil
				if abortErr := g.RebaseAbort(); abortErr != nil {
					// Neither persisting nor aborting worked: the rebase is still
					// paused, so keep ErrConflict (recovery via st continue/abort)
					// and surface both failures.
					return nil, AlsoFailed(AlsoFailed(&ConflictError{Action: "moving", Branch: cur, Onto: target}, "record pending reparent", saveErr), "abort the in-progress rebase", abortErr)
				}
				// The rebase was aborted and nothing changed, so do not wrap
				// ErrConflict — there is nothing to continue.
				return nil, fmt.Errorf("moving %q onto %q: recording the reparent failed, so the rebase was aborted: %w", cur, target, saveErr)
			}
			return nil, &ConflictError{Action: "moving", Branch: cur, Onto: target}
		}
		return nil, outErr
	}
	b.Parent = target
	b.ParentSHA = newParentTip
	if s.PendingReparent != nil && s.PendingReparent.Branch == cur {
		s.PendingReparent = nil
	}
	if err := env.save(); err != nil {
		return nil, err
	}

	upstack, err := finishUpstack(env, s, cur)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: fmt.Sprintf("Moved %s onto %s", cur, target), Branch: cur, Restacked: upstack.restacked, Notes: upstack.notes}, nil
}

// Delete removes a tracked branch, re-parents its children onto the deleted
// branch's parent, and restacks them so the deleted branch's commits are dropped
// from their history.
func Delete(env Env, s *State, name string, force bool) (*OpResult, error) {
	g := env.Git
	if name == s.Trunk {
		return nil, fmt.Errorf("cannot delete the trunk branch %q", name)
	}
	b, err := s.tracked(name)
	if err != nil {
		return nil, err
	}
	if err := requireClean(g); err != nil {
		return nil, err
	}
	// If name lives in another worktree, git refuses to delete it; tear that
	// (clean) worktree down first. A dirty owner errors out and nothing changes.
	if err := s.releaseOwnedWorktree(env, name); err != nil {
		return nil, err
	}
	parent := b.Parent

	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if !force {
		mergedIntoParent, err := g.IsAncestor(name, parent)
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, parent, err)
		}
		if !mergedIntoParent {
			return nil, fmt.Errorf("branch %q is not merged into its stack parent %q (use --force to delete anyway)", name, parent)
		}
	}
	if start == name {
		if err := g.Checkout(parent); err != nil {
			return nil, fmt.Errorf("checking out parent %q: %w", parent, err)
		}
		start = parent
	}

	if err := g.DeleteBranch(name, true); err != nil {
		err = fmt.Errorf("deleting branch %q: %w", name, err)
		if restoreErr := restoreHEAD(env, start, s.Trunk); restoreErr != nil {
			return nil, AlsoFailed(err, fmt.Sprintf("restore %q", start), restoreErr)
		}
		return nil, err
	}
	formerChildren := s.RemoveBranch(name)
	if err := env.save(); err != nil {
		return nil, err
	}

	var restacked []string
	for _, child := range formerChildren {
		did, err := s.RestackBranch(env, child)
		if err != nil {
			return nil, restoreHEADAfterNonConflict(env, start, s.Trunk, err)
		}
		if did {
			restacked = append(restacked, child)
		}
		more, err := s.RestackUpstack(env, child)
		if err != nil {
			return nil, restoreHEADAfterNonConflict(env, start, s.Trunk, err)
		}
		restacked = append(restacked, more...)
	}
	notes := skippedWorktreeNotes(s)
	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}

	res := &OpResult{Summary: fmt.Sprintf("Deleted %s", name), Deleted: []string{name}, Restacked: restacked, Notes: notes}
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
			if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
			}
			return nil, err
		}
	}

	var deleted []string
	if !noDelete {
		if err := g.Checkout(s.Trunk); err != nil {
			return nil, fmt.Errorf("checkout trunk %q before pruning: %w", s.Trunk, err)
		}
		if deleted, err = PruneMerged(env, s); err != nil {
			if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
			}
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
		// Leaves a conflict's rebase in progress for `st continue`; restores HEAD
		// otherwise. Same guard the other mutations use.
		return nil, restoreHEADAfterNonConflict(env, orig, s.Trunk, err)
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
		Notes:     append([]string{"trunk: " + ffResult}, skippedWorktreeNotes(s)...),
	}, nil
}

// SyncPlan previews what a sync would do: which merged branches it would prune
// and which branches it would restack. It does not fetch, fast-forward, or
// mutate anything.
func SyncPlan(env Env, s *State, noDelete bool) (*OpResult, error) {
	return SyncPlanAgainst(env, s, noDelete, branchTipRef(s.Trunk))
}

// SyncPlanAgainst previews sync against the supplied trunk ref, used by the CLI
// dry-run path after fetching the selected remote's trunk.
func SyncPlanAgainst(env Env, s *State, noDelete bool, trunkRef string) (*OpResult, error) {
	g := env.Git
	// The real Sync requires a clean tree (engine.go Sync), so the preview must
	// too — otherwise it reports branches it "would restack" that the real
	// command will refuse to touch, returning exit 0 instead of the dirty-tree
	// exit code.
	if err := requireClean(g); err != nil {
		return nil, err
	}
	planState := cloneState(s)
	deleted := map[string]bool{}
	var deletedList []string
	if !noDelete {
		for _, name := range sortedBranchNames(planState) {
			merged, err := g.IsAncestor(name, trunkRef)
			if err != nil {
				return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, trunkRef, err)
			}
			if merged {
				planState.RemoveBranch(name)
				deleted[name] = true
				deletedList = append(deletedList, name)
			}
		}
	}
	trunkTip, err := g.RevParse(trunkRef)
	if err != nil {
		return nil, err
	}
	tips, err := g.Tips()
	if err != nil {
		return nil, err
	}
	full := planState.restackPlanFromTips(tips, trunkTip, planState.Trunk)
	var plan []string
	for _, name := range full {
		if !deleted[name] {
			plan = append(plan, name)
		}
	}
	return &OpResult{Summary: "sync (dry run)", Deleted: deletedList, Restacked: plan, DryRun: true}, nil
}

func cloneState(s *State) *State {
	cp := &State{Trunk: s.Trunk, Branches: make(map[string]*Branch, len(s.Branches))}
	for name, branch := range s.Branches {
		b := *branch
		cp.Branches[name] = &b
	}
	if s.PendingReparent != nil {
		p := *s.PendingReparent
		cp.PendingReparent = &p
	}
	return cp
}

// Abort rolls back an in-progress restack/rebase (git rebase --abort) and clears
// any pending reparent it was promoting — the engine counterpart of Continue.
// Branches restacked before the conflict keep their new positions; only the
// branch git was mid-rebase on is rolled back. s may be nil when stacked is not
// initialized in the repo, in which case the rebase is still aborted.
func Abort(env Env, s *State) (*OpResult, error) {
	g := env.Git
	inProgress, err := g.RebaseInProgress()
	if err != nil {
		return nil, err
	}
	if !inProgress {
		return nil, errors.New("no rebase in progress; nothing to abort")
	}
	// Like Continue, fall back to the lone pending reparent when git's head-name
	// file can't be read.
	conflicted, _ := g.RebaseHeadName()
	if conflicted == "" && s != nil && s.PendingReparent != nil {
		conflicted = s.PendingReparent.Branch
	}
	if err := g.RebaseAbort(); err != nil {
		return nil, fmt.Errorf("aborting rebase: %w", err)
	}
	if s != nil && s.PendingReparent != nil && (conflicted == "" || s.PendingReparent.Branch == conflicted) {
		s.PendingReparent = nil
	}
	return &OpResult{Summary: "aborted the in-progress rebase"}, nil
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
	// A paused `onto` rebase is unambiguous even when git's head-name file can't
	// be read (RebaseHeadName returns ""): there is exactly one pending reparent.
	// Fall back to it so the reparent is still promoted and HEAD restored, rather
	// than silently leaving cur mis-parented. This mirrors `st abort`, which
	// already treats an empty head-name plus a pending reparent as that branch.
	if conflicted == "" && s.PendingReparent != nil {
		conflicted = s.PendingReparent.Branch
	}
	if err := g.RebaseContinue(); err != nil {
		// Surface the branch the rebase re-stalled on as structured fields, like
		// the other conflict paths (RestackBranch/Onto), so a `st continue --json`
		// that re-stalls carries branch/onto instead of only the prose message.
		if conflicted != "" {
			onto := ""
			if pending := s.PendingReparent; pending != nil && pending.Branch == conflicted {
				onto = pending.Parent // an Onto-originated reparent: report the intended target, not the old parent
			} else if b, ok := s.Get(conflicted); ok {
				onto = b.Parent
			}
			return nil, &ConflictError{Action: "continuing", Branch: conflicted, Onto: onto}
		}
		return nil, fmt.Errorf("rebase did not complete: %w", ErrConflict)
	}

	// The just-finished branch now sits on its parent's current tip.
	if conflicted != "" {
		if pending := s.PendingReparent; pending != nil && pending.Branch == conflicted {
			if b, ok := s.Get(conflicted); ok {
				b.Parent = pending.Parent
				b.ParentSHA = pending.ParentSHA
			}
			s.PendingReparent = nil
			if err := env.save(); err != nil {
				return nil, err
			}
		} else if b, ok := s.Get(conflicted); ok {
			tip, err := g.RevParse(branchTipRef(b.Parent))
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
		if conflicted != "" {
			err = restoreHEADAfterNonConflict(env, conflicted, s.Trunk, err)
		}
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
// reading live tips at each step. Used by sync and continue. The whole forest is
// exactly the trunk's upstack (every tracked branch descends from the trunk), so
// it delegates to the one canonical restack path — RestackUpstack — which
// Descendants(trunk) walks in the same sorted, parents-first order.
func RestackAll(env Env, s *State) ([]string, error) {
	return s.RestackUpstack(env, s.Trunk)
}

// PruneMerged deletes tracked branches whose commits are already contained in
// the trunk, re-parenting each deleted branch's children onto its parent. It
// returns the deleted branch names in sorted order. The caller persists.
func PruneMerged(env Env, s *State) ([]string, error) {
	g := env.Git
	trunk := s.Trunk
	var deleted []string
	for _, name := range sortedBranchNames(s) {
		if _, ok := s.Get(name); !ok {
			continue
		}
		merged, err := g.IsAncestor(name, trunk)
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, trunk, err)
		}
		if !merged {
			continue
		}
		// A merged branch living in another worktree can't be deleted by git until
		// its worktree is gone; tear a clean one down first. A dirty owner errors
		// (the user must commit/stash or `st worktree rm` it), leaving everything
		// pruned so far intact.
		if err := s.releaseOwnedWorktree(env, name); err != nil {
			return deleted, err
		}
		if err := g.DeleteBranch(name, true); err != nil {
			return deleted, fmt.Errorf("delete merged branch %q: %w", name, err)
		}
		s.RemoveBranch(name)
		deleted = append(deleted, name)
		if err := env.save(); err != nil {
			return deleted, fmt.Errorf("save state after pruning %q: %w", name, err)
		}
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

// inferParent picks the closest tracked ancestor (or the trunk) of cur. The two
// per-candidate ancestry questions ("is c an ancestor of cur?" and "is c merged
// into trunk?") are answered from precomputed reachability sets — one rev-list
// for cur and one for the trunk — instead of a `merge-base --is-ancestor` spawn
// per candidate. Only the closest-ancestor tie-break still spawns, and just for
// the few candidates that are actual ancestors of cur.
func inferParent(g Git, s *State, cur string) (string, error) {
	curAncestors, err := g.AncestorSet(cur)
	if err != nil {
		return "", fmt.Errorf("list ancestors of %q: %w", cur, err)
	}
	trunkAncestors, err := g.AncestorSet(s.Trunk)
	if err != nil {
		return "", fmt.Errorf("list ancestors of %q: %w", s.Trunk, err)
	}
	tips, err := g.Tips()
	if err != nil {
		return "", fmt.Errorf("read branch tips: %w", err)
	}

	best := s.Trunk
	candidates := []string{s.Trunk}
	for name := range s.Branches {
		if name != cur {
			candidates = append(candidates, name)
		}
	}
	// Iterate in a fixed order so the choice between incomparable ancestors (two
	// tracked branches where neither is an ancestor of the other, e.g. across a
	// merge) is deterministic rather than dependent on map iteration order.
	sort.Strings(candidates)
	for _, c := range candidates {
		if c == cur || c == best {
			continue
		}
		tip, ok := tips[c]
		if !ok {
			continue // candidate's git branch is gone; it cannot be a parent
		}
		if !curAncestors[tip] {
			continue // not an ancestor of cur
		}
		if trunkAncestors[tip] {
			continue // already merged into the trunk
		}
		if best != s.Trunk {
			bestIsAncestor, err := g.IsAncestor(best, c)
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
		mergedIntoParent, err = g.IsAncestor(name, b.Parent)
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

// sortedBranchNames returns all tracked branch names in deterministic order.
func sortedBranchNames(s *State) []string {
	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

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
	// Tracked maps each branch a bulk adopt (`st track --all`) recorded to the
	// parent it was inferred under; unset for every other command.
	Tracked map[string]string `json:"tracked,omitempty"`
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

// RequireNoPausedRebase refuses a stack mutation while a rebase is in progress:
// state edits mid-rebase can orphan the PendingReparent record or reorder the
// stack under the paused rebase (e.g. renaming the branch being rebased). st
// continue/st abort bypass this on purpose — they exist to resolve the pause.
func RequireNoPausedRebase(g Git) error {
	inProgress, err := g.RebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state: %w", err)
	}
	if inProgress {
		return errors.New("a rebase is in progress; resolve it with `st continue` or `st abort` first")
	}
	return nil
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
	// A CurrentBranch failure (detached HEAD — sync parks there before pruning)
	// only skips the already-on-target shortcut; the checkout below either
	// restores the target or surfaces the real problem.
	if cur, err := g.CurrentBranch(); err == nil && cur == target {
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
	rebased, err := s.restackUpstack(env, anchor)
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

// CreateInWorktreePrep creates and tracks a new branch at the current branch's
// tip without switching the current worktree to it. The command layer can then
// materialize a dedicated linked worktree for the new branch.
func CreateInWorktreePrep(env Env, s *State, name string) (*OpResult, error) {
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
	parentSHA, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, fmt.Errorf("resolving parent %q: %w", cur, err)
	}
	if err := g.CreateBranchAt(name, parentSHA); err != nil {
		return nil, fmt.Errorf("creating branch %q: %w", name, err)
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
		did, err := s.restackBranch(env, start)
		if err != nil {
			return nil, err
		}
		if did {
			rebased = append(rebased, start)
		}
	}
	up, err := s.restackUpstack(env, start)
	if err != nil {
		err = restoreHEADAfterNonConflict(env, start, s.Trunk, err)
		return nil, err
	}
	rebased = append(rebased, up...)
	return restackEpilogue(env, s, start, rebased)
}

// restackEpilogue is the shared tail of Restack and RestackAllOp: restore HEAD
// to the branch the caller started on, turn any dirty-worktree skips into
// notes, and build the OpResult ("everything up to date" when nothing moved
// and nothing was skipped, else "restacked").
func restackEpilogue(env Env, s *State, start string, rebased []string) (*OpResult, error) {
	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}
	notes := append(skippedWorktreeNotes(s), unreachableNotes(s)...)
	if len(rebased) == 0 && len(notes) == 0 {
		return &OpResult{Summary: "everything up to date"}, nil
	}
	return &OpResult{Summary: "restacked", Restacked: rebased, Notes: notes}, nil
}

// RestackAllOp restacks every tracked branch (parents before children),
// regardless of the current branch — the whole forest is the trunk's upstack,
// so unlike Restack it never rebases the current branch specially (the walk
// covers it) and the current branch may even be untracked. Requires a clean
// tree; restores HEAD afterwards.
func RestackAllOp(env Env, s *State) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	rebased, err := restackAll(env, s)
	if err != nil {
		return nil, restoreHEADAfterNonConflict(env, start, s.Trunk, err)
	}
	return restackEpilogue(env, s, start, rebased)
}

// skippedWorktreeNotes drains the branches a restack skipped because their owning
// worktree was dirty or mid-rebase, turning each into a human-readable note.
// Empty when the cascade skipped nothing (the common, single-tree case).
func skippedWorktreeNotes(s *State) []string {
	skipped, rebase := s.drainSkippedWorktrees()
	return skippedWorktreeNotesFrom(skipped, rebase)
}

// unreachableNotes turns the tracked branches no trunk forest walk can reach
// — cycle members, dangling parents — into a repair-pointing warning. st
// validate and st repair already classify both kinds (ParentUntracked,
// ParentMissing, ParentCycle), so the advice actually resolves the condition.
func unreachableNotes(s *State) []string {
	un := s.UnreachableBranches()
	if len(un) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("tracked branches unreachable from trunk %q (cycle or dangling parent): %s — run `st repair`", s.Trunk, joinComma(un))}
}

func skippedWorktreeNotesFrom(skipped []string, rebase map[string]bool) []string {
	if len(skipped) == 0 {
		return nil
	}
	notes := make([]string, 0, len(skipped))
	for _, name := range skipped {
		notes = append(notes, skippedWorktreeNote(name, rebase[name]))
	}
	return notes
}

func skippedWorktreeNote(name string, rebase bool) string {
	if rebase {
		return fmt.Sprintf("skipped %s: a rebase is already in progress in its worktree (finish or abort it there, then re-run)", name)
	}
	return fmt.Sprintf("skipped %s: its worktree is dirty (commit/stash there, then re-run)", name)
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
	if owner, elsewhere, err := s.ownerElsewhere(g, parent); err != nil {
		return nil, err
	} else if elsewhere {
		return nil, fmt.Errorf("cannot fold into %q because it is checked out in another worktree %q", parent, owner.Path)
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
	parent := b.Parent

	if !force {
		mergedIntoParent, err := g.IsAncestor(branchTipRef(name), branchTipRef(parent))
		if err != nil {
			return nil, fmt.Errorf("check whether %q is merged into %q: %w", name, parent, err)
		}
		if !mergedIntoParent {
			return nil, fmt.Errorf("branch %q is not merged into its stack parent %q (use --force to delete anyway)", name, parent)
		}
	}
	// If name lives in another worktree, git refuses to delete it; tear that
	// (clean) worktree down first. A dirty owner errors out and nothing changes.
	if err := s.releaseOwnedWorktree(env, name); err != nil {
		return nil, err
	}
	start, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if start == name {
		if owner, elsewhere, err := s.ownerElsewhere(g, parent); err != nil {
			return nil, err
		} else if elsewhere {
			return nil, fmt.Errorf("cannot delete current branch %q because its parent %q is checked out in another worktree %q", name, parent, owner.Path)
		}
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

	// One pass over one shared tips map (the walk order — each child, then its
	// descendants — matches DeletePlan's appendRestackPlans exactly), instead
	// of a fresh full ref scan per re-parented child.
	restacked, err := s.restackForest(env, formerChildren)
	if err != nil {
		return nil, restoreHEADAfterNonConflict(env, start, s.Trunk, err)
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
// kept. With noFetch the remote is untouched — no fetch, no fast-forward — and
// the prune basis is the existing remote-tracking ref when one exists, else the
// local trunk. Requires a clean working tree.
func Sync(env Env, r Remote, s *State, remote string, noDelete, noFetch bool) (*OpResult, error) {
	g := env.Git
	if err := requireClean(g); err != nil {
		return nil, err
	}
	orig, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}
	// Resolve where the trunk is checked out ONCE: sync's trunk operations run
	// in the trunk's own worktree, so `st sync` works from a linked worktree
	// (where git refuses to check out the trunk a second time).
	trunkHere := orig == s.Trunk
	trunkOwnerDir := ""
	if !trunkHere {
		wts, wtErr := g.Worktrees()
		if wtErr != nil {
			return nil, wtErr
		}
		if owner, ok := OwnerOf(wts, s.Trunk); ok {
			trunkOwnerDir = owner.Path
		}
	}

	// The prune basis is the local trunk — after a fast-forward it is the
	// remote tip. Under --no-fetch nothing moves the local trunk, so the
	// already-fetched remote-tracking ref is the fresher basis when it exists.
	// Always qualified: a bare "main" resolves through gitrevisions order where
	// a tag named main would shadow the branch.
	ffResult := "skipped (no remote)"
	trunkRef := branchTipRef(s.Trunk)
	switch {
	case noFetch:
		ffResult = "skipped (--no-fetch)"
		if r.Exists(remote) {
			remoteRef := "refs/remotes/" + remote + "/" + s.Trunk
			if _, err := g.RevParse(remoteRef); err == nil {
				trunkRef = remoteRef
			}
		}
	case r.Exists(remote):
		if err := r.Fetch(remote); err != nil {
			return nil, fmt.Errorf("fetch %q: %w", remote, err)
		}
		ffResult, err = r.FastForward(s.Trunk, remote, trunkOwnerDir, trunkHere)
		if err != nil {
			if restoreErr := restoreHEAD(env, orig, s.Trunk); restoreErr != nil {
				return nil, AlsoFailed(err, fmt.Sprintf("restore %q", orig), restoreErr)
			}
			return nil, err
		}
	}

	var deleted []string
	if !noDelete {
		// HEAD must not sit on a prunable branch. Checking out the trunk does
		// that; from a linked worktree (trunk owned elsewhere) git refuses the
		// checkout, so park HEAD detached instead — any branch, including orig,
		// can then be pruned.
		if trunkOwnerDir != "" {
			head, revErr := g.RevParse("HEAD")
			if revErr != nil {
				return nil, fmt.Errorf("resolving HEAD before pruning: %w", revErr)
			}
			if err := g.CheckoutDetach(head); err != nil {
				return nil, fmt.Errorf("detaching HEAD before pruning: %w", err)
			}
		} else if err := g.Checkout(s.Trunk); err != nil {
			return nil, fmt.Errorf("checkout trunk %q before pruning: %w", s.Trunk, err)
		}
		if deleted, err = PruneMergedAgainst(env, s, trunkRef); err != nil {
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

	rebased, err := restackAll(env, s)
	if err != nil {
		// Leaves a conflict's rebase in progress for `st continue`; restores HEAD
		// otherwise. Same guard the other mutations use.
		return nil, restoreHEADAfterNonConflict(env, orig, s.Trunk, err)
	}
	if err := env.save(); err != nil {
		return nil, err
	}
	notes := []string{"trunk: " + ffResult}
	if !g.BranchExists(orig) && trunkOwnerDir != "" {
		// orig was pruned and restoreHEAD's trunk fallback cannot be checked out
		// here (it lives in another worktree): stay where the cascade left HEAD
		// rather than fail or hijack that worktree's branch. That is NOT always
		// detached — an in-place rebase of a surviving branch re-attaches HEAD to
		// that branch — so report where HEAD actually landed.
		if cur, curErr := g.CurrentBranch(); curErr == nil && cur != "" {
			notes = append(notes, "HEAD is on "+cur+"; trunk is checked out in "+trunkOwnerDir)
		} else {
			notes = append(notes, "HEAD left detached; trunk is checked out in "+trunkOwnerDir)
		}
	} else if err := restoreHEAD(env, orig, s.Trunk); err != nil {
		return nil, err
	}

	return &OpResult{
		Summary:   "sync complete",
		Deleted:   deleted,
		Restacked: rebased,
		Notes:     append(notes, append(skippedWorktreeNotes(s), unreachableNotes(s)...)...),
	}, nil
}

// SyncPlanAgainst previews what a sync would do against the supplied trunk
// ref — which merged branches it would prune and which branches it would
// restack — without fetching, fast-forwarding, or mutating anything. Used by
// the CLI dry-run path after fetching the selected remote's trunk.
func SyncPlanAgainst(env Env, s *State, noDelete bool, trunkRef string) (*OpResult, error) {
	g := env.Git
	// The real Sync requires a clean tree (engine.go Sync), so the preview must
	// too — otherwise it reports branches it "would restack" that the real
	// command will refuse to touch, returning exit 0 instead of the dirty-tree
	// exit code.
	if err := requireClean(g); err != nil {
		return nil, err
	}
	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
	}
	if err := requireStateTips(s, tips); err != nil {
		return nil, err
	}
	if trunkRef != s.Trunk && trunkRef != branchTipRef(s.Trunk) {
		trunkTip, err := g.RevParse(trunkRef)
		if err != nil {
			return nil, fmt.Errorf("resolve trunk ref %q: %w", trunkRef, err)
		}
		tips[s.Trunk] = trunkTip
	}
	planState := cloneState(s)
	deleted := map[string]bool{}
	var deletedList []string
	if !noDelete {
		candidates, err := pruneTargets(env, s, trunkRef)
		if err != nil {
			return nil, err
		}
		for _, name := range candidates {
			planState.RemoveBranch(name)
			deleted[name] = true
			deletedList = append(deletedList, name)
		}
	}
	preview, err := restackPlanAgainstWithWorktrees(env, planState, planState.Trunk, tips)
	if err != nil {
		return nil, err
	}
	var plan []string
	for _, name := range preview.restacked {
		if !deleted[name] {
			plan = append(plan, name)
		}
	}
	return &OpResult{Summary: "sync (dry run)", Deleted: deletedList, Restacked: plan, Notes: preview.notes(), DryRun: true}, nil
}

func cloneState(s *State) *State {
	cp := &State{Version: s.Version, Trunk: s.Trunk, Branches: make(map[string]*Branch, len(s.Branches))}
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
	// The adoption is verified against the paused rebase's recorded target: a
	// stale record (its rebase finished or aborted outside st) or a foreign
	// rebase has a different onto, and must not be promoted.
	if conflicted == "" && s.PendingReparent != nil {
		if onto, err := g.RebaseOntoSHA(); err == nil && onto == s.PendingReparent.ParentSHA {
			conflicted = s.PendingReparent.Branch
		}
	}

	// Capture the target the paused rebase is actually replaying onto BEFORE
	// RebaseContinue removes the worktree-local metadata. Only an ordinary
	// tracked branch needs it: a pending reparent already persists its target
	// (pending.ParentSHA), and an untracked conflicted branch has no ParentSHA
	// to stamp. If the parent ref moved while the rebase was paused, the live
	// tip is NOT what the rebase incorporated — recording it would suppress
	// the follow-up restack that picks up the move.
	ontoSHA := ""
	isPendingReparent := s.PendingReparent != nil && s.PendingReparent.Branch == conflicted
	if conflicted != "" && !isPendingReparent {
		if _, ok := s.Get(conflicted); ok {
			if ontoSHA, err = g.RebaseOntoSHA(); err != nil {
				return nil, fmt.Errorf("reading the paused rebase's target before continuing %q: %w", conflicted, err)
			}
		}
	}

	if err := g.RebaseContinue(); err != nil {
		// Surface the branch the rebase re-stalled on as structured fields, like
		// the other conflict paths (restackBranch/Onto), so a `st continue --json`
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
	var foreignNote string
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
			// Record the target the rebase actually replayed onto — even when it
			// is not the recorded parent's tip-line (a rebase st did not start).
			// Stamping the truth is what lets NeedsRestack see the divergence and
			// the cascade below recover the branch onto its parent; withholding
			// the stamp would leave the branch sitting on the foreign target
			// while reporting clean. The warning tells the user this was not an
			// st-initiated rebase.
			b.ParentSHA = ontoSHA
			if !rebaseTargetIsParentBase(g, b, ontoSHA) {
				foreignNote = fmt.Sprintf("continued a rebase on %s that was not headed for recorded parent %s; recorded the actual target as its base and restacked (run `st repair` if the stack needs reconciling)", conflicted, b.Parent)
			}
			if err := env.save(); err != nil {
				return nil, err
			}
		}
	}

	rebased, err := restackAll(env, s)
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
	if foreignNote != "" {
		res.Notes = append(res.Notes, foreignNote)
	}
	return res, nil
}

// rebaseTargetIsParentBase reports whether ontoSHA — the target the just-finished
// rebase recorded — is the tip of b's recorded parent or an ancestor of it (the
// ancestor case covers the parent moving forward while the rebase was paused).
// When it is not, the rebase was not st-initiated and Continue warns while still
// stamping the real target so the cascade can recover the branch.
func rebaseTargetIsParentBase(g Git, b *Branch, ontoSHA string) bool {
	if ontoSHA == "" {
		return false
	}
	tip, err := g.RevParse(branchTipRef(b.Parent))
	if err != nil {
		return false // parent ref unreadable: cannot prove the target was ours
	}
	if tip == ontoSHA {
		return true
	}
	anc, err := g.IsAncestor(ontoSHA, tip)
	return err == nil && anc
}

// restackAll restacks every stack rooted on the trunk, parents before children,
// reading live tips at each step. Used by sync and continue. The whole forest is
// exactly the trunk's upstack (every tracked branch descends from the trunk), so
// it delegates to the one canonical restack path — restackUpstack — which
// Descendants(trunk) walks in the same sorted, parents-first order.
func restackAll(env Env, s *State) ([]string, error) {
	return s.restackUpstack(env, s.Trunk)
}

// PruneMerged deletes tracked branches whose commits or content are already
// contained in the local trunk. It returns the deleted branch names in sorted
// order. The caller persists.
func PruneMerged(env Env, s *State) ([]string, error) {
	return PruneMergedAgainst(env, s, branchTipRef(s.Trunk))
}

// PruneMergedAgainst is PruneMerged against an arbitrary basis ref — the local
// trunk, or a fetched remote-tracking ref (sync --no-fetch).
func PruneMergedAgainst(env Env, s *State, trunkRef string) ([]string, error) {
	candidates, err := pruneTargets(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	return applyPrune(env, s, candidates)
}

// Prune is the standalone `st prune`: delete every tracked branch already
// merged into trunkRef (local trunk or an explicit remote-tracking ref). Unlike
// Sync it never fetches, fast-forwards, moves HEAD, or restacks — so when HEAD
// is on a prunable branch it refuses rather than auto-checkout: sync owns the
// move. No clean-tree requirement: branch deletion touches no worktree content.
func Prune(env Env, s *State, trunkRef string) (*OpResult, error) {
	names, err := pruneMergedNames(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	if err := refusePruneCurrent(env, names); err != nil {
		return nil, err
	}
	candidates, err := gatePruneCandidates(env, s, names)
	if err != nil {
		return nil, err
	}
	deleted, err := applyPrune(env, s, candidates)
	if err != nil {
		return nil, err
	}
	return &OpResult{
		Summary: fmt.Sprintf("Pruned %d merged branch(es)", len(deleted)),
		Deleted: deleted,
	}, nil
}

// PrunePlan previews Prune against trunkRef — the same merged enumeration,
// current-branch refusal, and worktree-release gate, with nothing deleted.
func PrunePlan(env Env, s *State, trunkRef string) (*OpResult, error) {
	names, err := pruneMergedNames(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	if err := refusePruneCurrent(env, names); err != nil {
		return nil, err
	}
	candidates, err := gatePruneCandidates(env, s, names)
	if err != nil {
		return nil, err
	}
	return &OpResult{Summary: "prune (dry run)", Deleted: candidates, DryRun: true}, nil
}

// refusePruneCurrent refuses a standalone prune that would delete the branch
// HEAD is on — Prune never moves HEAD, so the user must switch away first (or
// run st sync, which owns the move). A detached HEAD reports "detached HEAD"
// from CurrentBranch — not a branch name — so the check degrades to "no branch
// to protect".
func refusePruneCurrent(env Env, names []string) error {
	cur, _ := env.Git.CurrentBranch()
	for _, name := range names {
		if name == cur {
			return fmt.Errorf("current branch %q would be pruned; check out another branch or run st sync", cur)
		}
	}
	return nil
}

// gatePruneCandidates applies the worktree-release check to each merged name —
// the same check applyPrune's releaseOwnedWorktree performs — so a preview and
// an apply share identical eligibility, and a dirty owner fails BEFORE any
// branch is deleted instead of mid-loop.
func gatePruneCandidates(env Env, s *State, names []string) ([]string, error) {
	var candidates []string
	for _, name := range names {
		if _, err := s.ownedWorktreeReleaseTarget(env, name); err != nil {
			return nil, err
		}
		candidates = append(candidates, name)
	}
	return candidates, nil
}

// pruneTargets returns the deletable merged set in sorted order for the sync
// path (HEAD already moved off any prunable branch): pruneMergedNames plus the
// release gate.
func pruneTargets(env Env, s *State, trunkRef string) ([]string, error) {
	names, err := pruneMergedNames(env, s, trunkRef)
	if err != nil {
		return nil, err
	}
	return gatePruneCandidates(env, s, names)
}

// pruneMergedNames returns the tracked branches already merged into trunkRef,
// in sorted order — tips sanity check plus the merged enumeration. Shared by
// every prune preview and apply path.
func pruneMergedNames(env Env, s *State, trunkRef string) ([]string, error) {
	g := env.Git
	names := sortedBranchNames(s)
	tips, err := g.TipsFor(names)
	if err != nil {
		return nil, fmt.Errorf("read tracked branch tips: %w", err)
	}
	for _, name := range names {
		if _, ok := tips[name]; !ok {
			return nil, fmt.Errorf("tracked branch %q does not exist", name)
		}
	}
	merged, err := mergedBranches(g, s, trunkRef)
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, name := range names {
		if merged[name] {
			candidates = append(candidates, name)
		}
	}
	return candidates, nil
}

// applyPrune deletes each candidate: releases its owned worktree (a dirty one
// errors, leaving everything pruned so far intact), drops the git branch,
// untracks it, and checkpoints. Callers pass the gatePruneCandidates result.
func applyPrune(env Env, s *State, candidates []string) ([]string, error) {
	g := env.Git
	var deleted []string
	for _, name := range candidates {
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

// mergedBranches returns the set of tracked branches trunkRef already
// contains — the ones a sync prune may delete without losing anything. Two
// detection layers: ancestry-merged tips, answered for every local branch by
// one batched MergedInto scan; and content-equivalent branches — squash-merged
// or fully cherry-picked, where the branch's whole diff relative to its merge
// base is already in trunkRef's tree even though its tip is no ancestor —
// answered per unmerged branch by ChangesContainedIn's exact tree comparison
// (never a patch-id heuristic, so a branch carrying unique content is never
// flagged). trunkRef may be the local trunk or a fetched remote-tracking ref;
// a bare name is qualified so a tag can never shadow the trunk branch.
func mergedBranches(g Git, s *State, trunkRef string) (map[string]bool, error) {
	if !strings.HasPrefix(trunkRef, "refs/") {
		trunkRef = branchTipRef(trunkRef)
	}
	merged, err := g.MergedInto(trunkRef)
	if err != nil {
		return nil, fmt.Errorf("list branches merged into %q: %w", trunkRef, err)
	}
	for _, name := range sortedBranchNames(s) {
		if merged[name] {
			continue
		}
		contained, err := g.ChangesContainedIn(trunkRef, branchTipRef(name))
		if err != nil {
			return nil, fmt.Errorf("check whether %q's changes are contained in %q: %w", name, trunkRef, err)
		}
		if contained {
			merged[name] = true
		}
	}
	return merged, nil
}

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

// TrackAllBranches adopts every untracked local branch in one operation,
// inferring each branch's parent from the full local-branch set — so an
// existing a→b→c stack comes in as a proper chain rather than three
// trunk-parented orphans. The proposed parent map is checked for cycles
// (possible on odd DAGs) before anything is recorded: a cyclic proposal fails
// the whole command naming the cycle members, which need explicit --parent
// calls. Branches are applied parents-first; one env.save() persists the
// whole batch so a single undo entry covers it.
func TrackAllBranches(env Env, s *State) (*OpResult, error) {
	g := env.Git
	tips, err := g.Tips()
	if err != nil {
		return nil, fmt.Errorf("list local branches: %w", err)
	}
	candidates := []string{s.Trunk}
	var untracked []string
	for name := range tips {
		if name == s.Trunk {
			continue
		}
		candidates = append(candidates, name)
		if !s.IsTracked(name) {
			untracked = append(untracked, name)
		}
	}
	sort.Strings(untracked)
	if len(untracked) == 0 {
		return &OpResult{Summary: "Nothing to adopt: every local branch is already tracked"}, nil
	}

	// The trunk's merged set is loop-invariant (the op holds the lock and
	// never fetches) — compute it once instead of per candidate.
	mergedIntoTrunk, err := g.MergedInto(branchTipRef(s.Trunk))
	if err != nil {
		return nil, fmt.Errorf("list branches merged into %q: %w", s.Trunk, err)
	}
	parents := make(map[string]string, len(untracked))
	var notes []string
	for _, name := range untracked {
		parent, err := inferParentAmongMerged(g, s.Trunk, mergedIntoTrunk, name, candidates)
		if err != nil {
			return nil, err
		}
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
		return &OpResult{Summary: "Nothing to adopt: no adoptable untracked branches", Notes: notes}, nil
	}
	adoptable := make([]string, 0, len(parents))
	for name := range parents {
		adoptable = append(adoptable, name)
	}
	sort.Strings(adoptable)
	if cyclic := adoptionCycles(adoptable, parents); len(cyclic) > 0 {
		return nil, fmt.Errorf("cannot infer parents for %v (their proposals form a cycle): track them one at a time with --parent", cyclic)
	}
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
	// merge) is deterministic rather than dependent on map iteration order.
	sort.Strings(candidates)
	for _, c := range candidates {
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

// sortedBranchNames returns all tracked branch names in deterministic order.
func sortedBranchNames(s *State) []string {
	names := make([]string, 0, len(s.Branches))
	for name := range s.Branches {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

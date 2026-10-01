// ops_onto.go — the conflicted-rebase family: Onto reparents a subtree;
// Continue and Abort resolve or roll back a paused rebase. All three share
// PendingReparent semantics.
package stack

import (
	"errors"
	"fmt"
)

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

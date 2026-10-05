// ops_lifecycle.go — branch birth and commit ops: Create (plus its
// --worktree prep variant) and Modify/amend.
package stack

import (
	"errors"
	"fmt"
)

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
		return nil, fmt.Errorf("cannot modify the trunk branch %q", cur)
	}
	if !s.IsTracked(cur) {
		return nil, ErrNotTracked(cur)
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

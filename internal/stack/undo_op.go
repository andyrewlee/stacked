package stack

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Undo reverts the mutation recorded in entry: branches the undone command
// created are deleted (moving HEAD out of the way first when needed), the
// state is rolled back to the snapshot, every recorded ref is restored, and
// the branch that was checked out at capture time is checked out again when
// possible. s is the currently-loaded state (nil when it could not be loaded);
// on success it is replaced in place with the snapshot state and persisted via
// env.save(). The working tree is never modified. The journal entry itself is
// not dropped — that is the caller's job after a successful undo.
func Undo(env Env, s *State, entry *UndoEntry) (*OpResult, error) {
	g := env.Git
	var prev State
	if err := json.Unmarshal(entry.State, &prev); err != nil {
		return nil, fmt.Errorf("parsing undo state: %w", err)
	}

	skipCheckoutRestore := false
	if entry.LocalBranches != nil {
		// Branches created by the undone command must be deleted. Candidates are
		// every branch the current state knows about plus the ones the entry
		// recorded as created; a candidate counts as created when it was not in
		// the entry's local-branch list.
		candidates := map[string]bool{}
		if s != nil {
			candidates[s.Trunk] = true
			for name := range s.Branches {
				candidates[name] = true
			}
		}
		for _, name := range entry.CreatedBranches {
			candidates[name] = true
		}
		var extra []string
		for name := range candidates {
			if branchCreatedByEntry(entry, name) && g.BranchExists(name) {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		for _, name := range extra {
			// Always look for a live linked worktree owning the branch, even when
			// the journal never recorded one (e.g. the branch was created plainly
			// and its worktree materialized later with `st worktree`): the branch
			// is being deleted either way, and git refuses to delete a branch
			// checked out in a linked worktree.
			recorded := entry.CreatedWorktrees[name] // may be ""
			if err := removeCreatedWorktree(env, name, recorded); err != nil {
				return nil, fmt.Errorf("removing worktree for branch %q created by undone command: %w", name, err)
			}
			target := prev.Trunk
			if s != nil {
				if b, ok := s.Get(name); ok && g.BranchExists(b.Parent) {
					target = b.Parent
				}
			}
			if cur, err := g.CurrentBranch(); err == nil && cur == name {
				// HEAD is on a branch we are about to delete; move it to target (the
				// parent, or trunk) so the branch can be removed. The final landing
				// branch is restored below from entry.CurrentBranch (the branch that
				// was checked out when the command ran) — for a current-branch rename
				// that is the restored old name, so no command-label coupling is
				// needed here.
				if !g.BranchExists(target) {
					sha, ok := entry.Refs[target]
					if !ok {
						return nil, fmt.Errorf("cannot restore checkout target %q before deleting %q", target, name)
					}
					if err := g.UpdateRef(branchTipRef(target), sha); err != nil {
						return nil, fmt.Errorf("restoring branch %q before deleting %q: %w", target, name, err)
					}
				}
				if err := g.Checkout(target); err != nil {
					if !checkoutBlockedByLocalChanges(err) && !checkoutBlockedByOtherWorktree(err) {
						return nil, fmt.Errorf("checking out %q before deleting %q: %w", target, name, err)
					}
					// Local changes or a target branch checked out in another
					// worktree block the checkout: park HEAD on a detached commit so
					// the branch can still be deleted without touching the working
					// tree.
					head, revErr := g.RevParse("HEAD")
					if revErr != nil {
						return nil, fmt.Errorf("resolving HEAD before deleting %q: %w", name, revErr)
					}
					if detachErr := g.CheckoutDetach(head); detachErr != nil {
						return nil, fmt.Errorf("detaching HEAD before deleting %q: %w", name, detachErr)
					}
					skipCheckoutRestore = true
				}
			}
			if err := g.DeleteBranch(name, true); err != nil {
				return nil, fmt.Errorf("deleting branch %q created by undone command: %w", name, err)
			}
		}
	}

	if s != nil {
		*s = prev
		if s.Branches == nil {
			s.Branches = make(map[string]*Branch)
		}
	}
	if err := env.save(); err != nil {
		return nil, fmt.Errorf("restoring stack state: %w", err)
	}

	names := make([]string, 0, len(entry.Refs))
	for name := range entry.Refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := g.UpdateRef(branchTipRef(name), entry.Refs[name]); err != nil {
			return nil, fmt.Errorf("restoring branch %q: %w", name, err)
		}
	}

	if !skipCheckoutRestore && entry.CurrentBranch != "" && g.BranchExists(entry.CurrentBranch) {
		if err := g.Checkout(entry.CurrentBranch); err != nil {
			// Local changes blocking the final checkout are tolerated: the refs are
			// already restored and HEAD simply stays where it is. A branch that is
			// already checked out in another worktree is likewise restored; this
			// process just cannot check it out in place.
			if !checkoutBlockedByLocalChanges(err) && !checkoutBlockedByOtherWorktree(err) {
				return nil, fmt.Errorf("checking out restored branch %q: %w", entry.CurrentBranch, err)
			}
		}
	}

	return &OpResult{
		Summary:   "undid: " + entry.Label,
		Restacked: names,
		Notes:     []string{entry.Label},
	}, nil
}

func removeCreatedWorktree(env Env, branch, path string) error {
	wts, err := env.Git.Worktrees()
	if err != nil {
		return err
	}
	owner, ok := LinkedOwnerOf(wts, branch)
	if !ok {
		return nil
	}
	// An empty path means the journal recorded no worktree for the branch —
	// there is nothing to cross-check, and a clean worktree for a branch being
	// deleted has no independent value. A MISMATCHED recorded path, by
	// contrast, signals journal/topology disagreement and must refuse.
	if path != "" && !sameWorktreePath(owner.Path, path) {
		return fmt.Errorf("branch is checked out in worktree %q, but undo recorded created worktree %q; not removing an unexpected worktree", owner.Path, path)
	}
	clean, err := env.Git.IsCleanIn(owner.Path)
	if err != nil {
		return fmt.Errorf("checking worktree %q for %q: %w", owner.Path, branch, err)
	}
	if !clean {
		return fmt.Errorf("branch %q has uncommitted changes in its worktree %q; commit/stash there or run `st worktree rm %s` first", branch, owner.Path, branch)
	}
	if err := env.Git.WorktreeRemove(owner.Path, false); err != nil {
		return fmt.Errorf("removing worktree %q for %q: %w", owner.Path, branch, err)
	}
	return nil
}

func sameWorktreePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func checkoutBlockedByLocalChanges(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "local changes") || strings.Contains(msg, "would be overwritten")
}

func checkoutBlockedByOtherWorktree(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already checked out") ||
		strings.Contains(msg, "used by worktree") ||
		strings.Contains(msg, "checked out at")
}

// branchCreatedByEntry reports whether name was created by the command the
// entry snapshots: it is listed in CreatedBranches, or absent from the
// local-branch list captured before the command ran.
func branchCreatedByEntry(entry *UndoEntry, name string) bool {
	for _, created := range entry.CreatedBranches {
		if created == name {
			return true
		}
	}
	for _, existed := range entry.LocalBranches {
		if existed == name {
			return false
		}
	}
	return true
}

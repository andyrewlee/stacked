package stack

import (
	"encoding/json"
	"fmt"
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

	checkoutAfterRestore := ""
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
			target := prev.Trunk
			if s != nil {
				if b, ok := s.Get(name); ok && g.BranchExists(b.Parent) {
					target = b.Parent
				}
			}
			if cur, err := g.CurrentBranch(); err == nil && cur == name {
				// The rename target is still derived from the entry label; the
				// follow-up is to derive it from the state diff instead.
				if entry.Label == "rename" {
					checkoutAfterRestore = restoredRenameTarget(&prev, s, name)
					if checkoutAfterRestore == "" {
						checkoutAfterRestore = missingRestoredRef(g, entry)
					}
				}
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
					if !checkoutBlockedByLocalChanges(err) {
						return nil, fmt.Errorf("checking out %q before deleting %q: %w", target, name, err)
					}
					// Local changes block the checkout: park HEAD on a detached
					// commit so the branch can still be deleted without touching
					// the working tree.
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

	if !skipCheckoutRestore && checkoutAfterRestore == "" && entry.CurrentBranch != "" && g.BranchExists(entry.CurrentBranch) {
		checkoutAfterRestore = entry.CurrentBranch
	}
	if checkoutAfterRestore != "" {
		if err := g.Checkout(checkoutAfterRestore); err != nil {
			// Local changes blocking the final checkout are tolerated: the refs
			// are already restored and HEAD simply stays where it is.
			if !checkoutBlockedByLocalChanges(err) {
				return nil, fmt.Errorf("checking out restored branch %q: %w", checkoutAfterRestore, err)
			}
		}
	}

	return &OpResult{
		Summary:   "undid: " + entry.Label,
		Restacked: names,
		Notes:     []string{entry.Label},
	}, nil
}

func checkoutBlockedByLocalChanges(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "local changes") || strings.Contains(msg, "would be overwritten")
}

// restoredRenameTarget picks the branch to check out after undoing a rename:
// the snapshot name that the current state no longer tracks (or the snapshot
// trunk when the trunk itself was renamed).
func restoredRenameTarget(prev, current *State, deleted string) string {
	if current == nil {
		return ""
	}
	if current.Trunk == deleted && prev.Trunk != current.Trunk {
		return prev.Trunk
	}
	for name := range prev.Branches {
		if !current.IsTracked(name) {
			return name
		}
	}
	return ""
}

// missingRestoredRef returns the single recorded ref whose branch is currently
// missing, or "" when there is not exactly one.
func missingRestoredRef(g Git, entry *UndoEntry) string {
	var missing []string
	for name := range entry.Refs {
		if !g.BranchExists(name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) == 1 {
		return missing[0]
	}
	return ""
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

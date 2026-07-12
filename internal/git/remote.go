package git

import (
	"fmt"
)

// RemoteShell implements the stack engine's Remote port against the real git
// binary.
type RemoteShell struct{}

func (RemoteShell) Exists(name string) bool { return RemoteExists(name) }
func (RemoteShell) Fetch(name string) error { return Fetch(name) }

// FastForward fast-forwards the local trunk to <remote>/<trunk>, returning a
// short description. The engine resolves where the trunk is checked out and
// passes the decision down, so the operation runs in the trunk's own worktree:
//
//	trunk checked out | action
//	------------------|-------------------------------------------------------
//	here              | checkout (no-op) + `merge --ff-only` in cwd
//	elsewhere         | require a clean owner worktree, then
//	                  | `git -C <ownerDir> merge --ff-only` there
//	nowhere           | guarded ref-only update (never a forced non-ff move)
//
// Whether there is anything to advance is decided with plumbing (IsAncestor),
// never by parsing git's localized merge output.
func (RemoteShell) FastForward(trunk, remote, ownerDir string, checkedOutHere bool) (string, error) {
	if checkedOutHere {
		if err := Checkout(trunk); err != nil {
			return "", fmt.Errorf("checkout trunk %q: %w", trunk, err)
		}
	}
	localTrunk := "refs/heads/" + trunk
	upstream := "refs/remotes/" + remote + "/" + trunk
	upToDate, err := IsAncestor(upstream, localTrunk)
	if err != nil {
		return "", fmt.Errorf("compare %q with %q: %w", trunk, upstream, err)
	}
	if upToDate {
		return fmt.Sprintf("%s already up to date", trunk), nil
	}
	switch {
	case checkedOutHere:
		if _, err := Run("merge", "--ff-only", upstream); err != nil {
			return "", fmt.Errorf("fast-forward %q to %q: %w", trunk, upstream, err)
		}
	case ownerDir != "":
		clean, err := IsCleanIn(ownerDir)
		if err != nil {
			return "", fmt.Errorf("check trunk worktree %q: %w", ownerDir, err)
		}
		if !clean {
			return "", fmt.Errorf("trunk %q has uncommitted changes in its worktree %s; commit or stash there before syncing", trunk, ownerDir)
		}
		if err := MergeFFOnlyIn(ownerDir, upstream); err != nil {
			return "", fmt.Errorf("fast-forward %q to %q: %w", trunk, upstream, err)
		}
	default:
		// The trunk is checked out nowhere: no working tree to update, so move
		// the ref directly — but only after proving it is a true fast-forward.
		ff, err := IsAncestor(localTrunk, upstream)
		if err != nil {
			return "", fmt.Errorf("compare %q with %q: %w", trunk, upstream, err)
		}
		if !ff {
			return "", fmt.Errorf("fast-forward %q to %q: local trunk has diverged from the upstream", trunk, upstream)
		}
		sha, err := RevParse(upstream)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", upstream, err)
		}
		if err := UpdateRef(localTrunk, sha); err != nil {
			return "", fmt.Errorf("fast-forward %q to %q: %w", trunk, upstream, err)
		}
	}
	return fmt.Sprintf("%s fast-forwarded to %s", trunk, upstream), nil
}

package git

import (
	"fmt"
	"strings"
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
	localTrunk := LocalBranchNameRef(trunk)
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

// Fetch updates remote-tracking refs for the named remote.
func Fetch(remote string) error {
	if err := validRefArg("remote", remote); err != nil {
		return err
	}
	_, err := Run("fetch", remote)
	return err
}

// PushStatus is the remote's confirmed outcome for one pushed ref, taken from
// the push's own porcelain status records — never inferred from process exit.
type PushStatus int

const (
	// PushUnconfirmed means no usable status record arrived for the ref.
	PushUnconfirmed PushStatus = iota
	// PushUpdated means the remote moved the ref (new, fast-forward, or
	// forced update).
	PushUpdated
	// PushUpToDate means the remote already had the requested tip.
	PushUpToDate
	// PushRejected means the remote explicitly refused the ref update.
	PushRejected
)

// PushResult reports the confirmed per-ref outcome of a single push
// invocation. Status holds one entry per requested branch; entries that stay
// PushUnconfirmed must never be reported as pushed or failed — the transport
// error (if any) explains why.
type PushResult struct {
	Status map[string]PushStatus
}

// PushBranches pushes the given branches to remote in a single git invocation
// and records upstreams (-u) for each branch. The returned PushResult carries
// every per-ref status the remote confirmed; on a partial rejection the
// process error and the confirmed records both come back so callers can
// report exactly which refs landed without pushing again. A push that exits
// zero but reports no status for a requested ref is itself an error — an
// unrecorded ref is never assumed pushed.
func PushBranches(remote string, branches []string, force bool) (*PushResult, error) {
	if err := validRefArg("remote", remote); err != nil {
		return nil, err
	}
	for _, branch := range branches {
		if err := validRefArg("branch", branch); err != nil {
			return nil, err
		}
	}
	res := &PushResult{Status: make(map[string]PushStatus, len(branches))}
	for _, branch := range branches {
		res.Status[branch] = PushUnconfirmed
	}
	if len(branches) == 0 {
		return res, nil
	}

	args := []string{"push", "--porcelain", "-u"}
	if force {
		args = append(args, "--force-with-lease")
	}
	args = append(args, remote)
	for _, branch := range branches {
		refspec := LocalBranchNameRef(branch) + ":" + LocalBranchNameRef(branch)
		args = append(args, refspec)
	}
	out, err := run(args...)
	parsePushPorcelain(out, branches, res)
	if err != nil {
		return res, err
	}
	for _, branch := range branches {
		if res.Status[branch] == PushUnconfirmed {
			return res, fmt.Errorf("git push: no per-ref status reported for %q", branch)
		}
	}
	return res, nil
}

// parsePushPorcelain folds `git push --porcelain` status records into res.
// Each record is "<flag>\t<src>:<dst>\t<summary>" — the flag can be a literal
// space, so lines are never trimmed. Only records whose dst is a requested
// refs/heads/<branch> are honored: unrelated refs, non-record chatter (the
// "To <url>" header, "set up to track" lines, "Done") and malformed lines
// never fabricate outcomes. A duplicate record simply rewrites the status —
// the remote's final word stands.
func parsePushPorcelain(out string, branches []string, res *PushResult) {
	want := make(map[string]string, len(branches))
	for _, b := range branches {
		want[LocalBranchNameRef(b)] = b
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 || line[1] != '\t' {
			continue
		}
		refs, _, ok := strings.Cut(line[2:], "\t")
		if !ok {
			continue
		}
		_, dst, ok := strings.Cut(refs, ":")
		if !ok || dst == "" {
			continue
		}
		branch, ok := want[dst]
		if !ok {
			continue
		}
		switch line[0] {
		case ' ', '*', '+', '-':
			res.Status[branch] = PushUpdated
		case '=':
			res.Status[branch] = PushUpToDate
		case '!':
			res.Status[branch] = PushRejected
		}
	}
}

// RemoteExists reports whether a remote with the given name is configured. A
// flag-like name is simply "not configured": it is rejected before exec so git
// can never parse it as an option.
func RemoteExists(name string) bool {
	if err := validRefArg("remote", name); err != nil {
		return false
	}
	return ok("remote", "get-url", name)
}

// RemoteURL returns the configured fetch URL of the named remote.
func RemoteURL(remote string) (string, error) {
	if err := validRefArg("remote", remote); err != nil {
		return "", err
	}
	return Run("remote", "get-url", remote)
}

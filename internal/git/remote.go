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

// PublishedState classifies a branch's live local tip against its
// remote-tracking ref (refs/remotes/<remote>/<branch>) as it stands locally.
// It describes the last fetch/push — never the server's current state.
type PublishedState string

const (
	// PublishedCurrent means the tracking ref equals the live tip.
	PublishedCurrent PublishedState = "current"
	// PublishedStale means the tracking ref is an ancestor of the live tip —
	// the branch is ahead of what the remote last saw; submit needed.
	PublishedStale PublishedState = "stale"
	// PublishedDiverged means the tracking ref exists but is not an ancestor
	// of the live tip — the published history was rewritten locally.
	PublishedDiverged PublishedState = "diverged"
	// PublishedMissing means no tracking ref exists under the remote — never
	// pushed, or the ref was pruned.
	PublishedMissing PublishedState = "missing"
	// PublishedUnknown means the comparison could not be made — a missing
	// local ref or a failed probe.
	PublishedUnknown PublishedState = "unknown"
)

// RemoteTrackingTips lists refs/remotes/<remote>/<branch> → SHA in one
// for-each-ref invocation, keyed by the branch part. The map is the remote's
// tracking-ref cache — what the last fetch or push recorded, not live server
// state.
func RemoteTrackingTips(remote string) (map[string]string, error) {
	if err := validRefArg("remote", remote); err != nil {
		return nil, err
	}
	prefix := "refs/remotes/" + remote + "/"
	out, err := Run("for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		return nil, err
	}
	tips := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		tips[strings.TrimPrefix(ref, prefix)] = sha
	}
	return tips, nil
}

// PublishedStates compares each named branch's live local tip to its
// remote-tracking ref under remote, per the PublishedState contract. The
// reads are two batched probes (one for-each-ref, one TipsFor) plus a bounded
// fan-out of merge-base --is-ancestor only for branches whose tracking ref
// differs from the tip — no network access, ever.
func PublishedStates(remote string, branches []string) (map[string]PublishedState, error) {
	states := make(map[string]PublishedState, len(branches))
	if len(branches) == 0 {
		return states, nil
	}
	tracking, err := RemoteTrackingTips(remote)
	if err != nil {
		return nil, err
	}
	tips, err := TipsFor(branches)
	if err != nil {
		return nil, err
	}
	// Equality and presence classify most branches in-process; only the
	// unequal survivors need the ancestry probe.
	checkIdx := []int{}
	ancestors := []bool{}
	ancestorOK := []bool{}
	for i, name := range branches {
		tip := tips[name]
		tracked, trackedOK := tracking[name]
		switch {
		case tip == "":
			states[name] = PublishedUnknown
		case !trackedOK:
			states[name] = PublishedMissing
		case tracked == tip:
			states[name] = PublishedCurrent
		default:
			checkIdx = append(checkIdx, i)
			ancestors = append(ancestors, false)
			ancestorOK = append(ancestorOK, false)
		}
	}
	_ = ParallelProbes(len(checkIdx), func(i int) error {
		anc, err := IsAncestor(tracking[branches[checkIdx[i]]], tips[branches[checkIdx[i]]])
		if err != nil {
			return nil // a failed ancestry probe is per-branch unknown, not fatal
		}
		ancestors[i] = anc
		ancestorOK[i] = true
		return nil
	})
	for i, bi := range checkIdx {
		switch {
		case !ancestorOK[i]:
			states[branches[bi]] = PublishedUnknown
		case ancestors[i]:
			states[branches[bi]] = PublishedStale
		default:
			states[branches[bi]] = PublishedDiverged
		}
	}
	return states, nil
}

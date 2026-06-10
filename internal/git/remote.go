package git

import (
	"fmt"
)

// RemoteShell implements the stack engine's Remote port against the real git
// binary.
type RemoteShell struct{}

func (RemoteShell) Exists(name string) bool { return RemoteExists(name) }
func (RemoteShell) Fetch(name string) error { return Fetch(name) }

// FastForward checks out the trunk and fast-forwards it to <remote>/<trunk>,
// returning a short description. Whether there is anything to advance is
// decided with plumbing (IsAncestor), never by parsing git's localized merge
// output.
func (RemoteShell) FastForward(trunk, remote string) (string, error) {
	if err := Checkout(trunk); err != nil {
		return "", fmt.Errorf("checkout trunk %q: %w", trunk, err)
	}
	localTrunk := "refs/heads/" + trunk
	upstream := "refs/remotes/" + remote + "/" + trunk
	upstreamSHA, err := RevParse(upstream)
	if err != nil {
		return "", fmt.Errorf("resolve upstream %q: %w", upstream, err)
	}
	upToDate, err := IsAncestor(upstreamSHA, localTrunk)
	if err != nil {
		return "", fmt.Errorf("compare %q with %q: %w", trunk, upstream, err)
	}
	if upToDate {
		return fmt.Sprintf("%s already up to date", trunk), nil
	}
	if _, err := Run("merge", "--ff-only", upstream); err != nil {
		return "", fmt.Errorf("fast-forward %q to %q: %w", trunk, upstream, err)
	}
	return fmt.Sprintf("%s fast-forwarded to %s", trunk, upstream), nil
}

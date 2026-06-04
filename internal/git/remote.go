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

// FastForward checks out the trunk and fast-forwards it to <remote>/<trunk>,
// returning a short description. An "already up to date" merge is tolerated.
func (RemoteShell) FastForward(trunk, remote string) (string, error) {
	if err := Checkout(trunk); err != nil {
		return "", fmt.Errorf("checkout trunk %q: %w", trunk, err)
	}
	localTrunk := "refs/heads/" + trunk
	upstream := "refs/remotes/" + remote + "/" + trunk
	before, err := RevParse(localTrunk)
	if err != nil {
		return "", fmt.Errorf("resolve trunk %q: %w", trunk, err)
	}
	out, err := Run("merge", "--ff-only", upstream)
	if err != nil {
		if isAlreadyUpToDate(out, err) {
			return fmt.Sprintf("%s already up to date", trunk), nil
		}
		return "", fmt.Errorf("fast-forward %q to %q: %w", trunk, upstream, err)
	}
	after, err := RevParse(localTrunk)
	if err != nil {
		return "", fmt.Errorf("resolve trunk %q: %w", trunk, err)
	}
	if before == after {
		return fmt.Sprintf("%s already up to date", trunk), nil
	}
	return fmt.Sprintf("%s fast-forwarded to %s", trunk, upstream), nil
}

// isAlreadyUpToDate reports whether a failed ff-only merge simply had nothing to do.
func isAlreadyUpToDate(out string, err error) bool {
	hay := strings.ToLower(out + " " + err.Error())
	return strings.Contains(hay, "already up to date") || strings.Contains(hay, "up-to-date")
}

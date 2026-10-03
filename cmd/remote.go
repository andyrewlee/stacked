package cmd

import (
	"flag"
	"fmt"
)

// Shared remote-name policy for the commands that read a remote's tracking
// state — sync, prune, submit, open. Centralized here so the existence check,
// the explicitness check, and the tracking-ref computation each live once.

// explicitRemote reports whether --remote was set on fs by the caller rather
// than left at its default: fs.Visit visits only flags that were SET, so an
// absent visit means the default "origin" (or whatever NewFlagSet seeded).
func explicitRemote(fs *flag.FlagSet) bool {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "remote" {
			explicit = true
		}
	})
	return explicit
}

// requireRemote fails when name does not resolve to a configured remote. It is
// the unconditional policy for submit/open (a remote is mandatory there) and
// the named-remote policy for sync/prune, where a missing DEFAULT remote is
// fine — a repo can sync/prune locally — but one the user named must exist.
func requireRemote(p *cachedPort, name string) error {
	if !p.remoteExists(name) {
		return fmt.Errorf("remote %q does not exist", name)
	}
	return nil
}

// resolveTrunkRef picks the ref a sync dry-run or an explicit-remote prune
// measures "merged" against: the named remote's tracking ref for the trunk
// when it resolves locally (already fetched), else the local trunk branch.
// remoteResolved reports which arm answered so a caller that REQUIRES the
// remote ref (prune) can reject the fallback with its own error. It shares
// the caller's port so an earlier remoteExists check is not re-probed.
func resolveTrunkRef(p *cachedPort, remote, trunk string) (ref string, remoteResolved bool) {
	if p.remoteExists(remote) {
		remoteRef := "refs/remotes/" + remote + "/" + trunk
		if _, err := p.RevParse(remoteRef); err == nil {
			return remoteRef, true
		}
	}
	return "refs/heads/" + trunk, false
}

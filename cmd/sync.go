package cmd

import (
	"flag"
	"fmt"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "sync",
		Aliases:    []string{"s"},
		Summary:    "Fetch trunk, fast-forward it, restack everything, and prune merged branches",
		Usage:      "st sync [--no-delete] [--no-fetch] [--remote <name>] [--dry-run] [--json]",
		Run:        runSync,
		NewFlagSet: syncFlagSet,
	})
}

func runSync(args []string) error {
	var o syncOpts
	fs := newSyncFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("sync", fs.Args()); err != nil {
		return err
	}
	asJSON, noDelete, noFetch, remote, dryRun := o.asJSON, o.noDelete, o.noFetch, o.remote, o.dryRun

	// If the user explicitly named a remote that does not exist, fail loudly like
	// `st submit` does instead of silently treating it as "no remote" and
	// reporting success. A missing DEFAULT origin is still allowed: a repo with no
	// remotes syncs locally (the fast-forward is skipped, prune+restack still run).
	remoteExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "remote" {
			remoteExplicit = true
		}
	})
	p := newGitPort()
	if remoteExplicit && !p.remoteExists(remote) {
		return fmt.Errorf("remote %q does not exist", remote)
	}

	if dryRun {
		return preview(asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			// --no-fetch previews against the local trunk — the same basis the
			// apply path uses — so the preview cannot predict a remote-only
			// prune the real run will not do. An ordinary dry run still uses
			// the already-fetched remote tip when one exists.
			basis := "refs/heads/" + s.Trunk
			if !noFetch {
				basis = resolveTrunkRef(p, remote, s.Trunk)
			}
			return stack.SyncPlanAgainst(env, s, noDelete, basis)
		})
	}

	return mutate("sync", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Sync(env, git.RemoteShell{}, s, remote, noDelete, noFetch)
	})
}

// resolveTrunkRef picks the ref a dry-run sync or an explicit-remote prune
// measures "merged" against: the named remote's tracking ref for the trunk
// when it exists (already fetched), else the local trunk branch. It shares
// the caller's port so an earlier remoteExists check is not re-probed.
func resolveTrunkRef(p *cachedPort, remote, trunk string) string {
	trunkRef := "refs/heads/" + trunk
	if p.remoteExists(remote) {
		remoteRef := "refs/remotes/" + remote + "/" + trunk
		if _, err := git.RevParse(remoteRef); err == nil {
			trunkRef = remoteRef
		}
	}
	return trunkRef
}

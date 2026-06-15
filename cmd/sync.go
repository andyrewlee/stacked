package cmd

import (
	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "sync",
		Aliases:    []string{"s"},
		Summary:    "Fetch trunk, fast-forward it, restack everything, and prune merged branches",
		Usage:      "st sync [--no-delete] [--remote <name>] [--dry-run] [--json]",
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
	asJSON, noDelete, remote, dryRun := o.asJSON, o.noDelete, o.remote, o.dryRun

	if dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		trunkRef := "refs/heads/" + s.Trunk
		if git.RemoteExists(remote) {
			remoteRef := "refs/remotes/" + remote + "/" + s.Trunk
			if _, err := git.RevParse(remoteRef); err == nil {
				trunkRef = remoteRef
			}
		}
		res, err := stack.SyncPlanAgainst(stackEnv(s, asJSON), s, noDelete, trunkRef)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}

	return mutate("sync", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Sync(env, git.RemoteShell{}, s, remote, noDelete)
	})
}

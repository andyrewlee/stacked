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
	var asJSON bool
	fs := newFlagSet("sync", &asJSON)
	var noDelete bool
	fs.BoolVar(&noDelete, "no-delete", false, "do not delete merged branches")
	remote := "origin"
	fs.StringVar(&remote, "remote", "origin", "remote to fetch and fast-forward from")
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "show what would be pruned/restacked without changing anything")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("sync", fs.Args()); err != nil {
		return err
	}

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

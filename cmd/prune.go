package cmd

import (
	"flag"
	"fmt"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "prune",
		Summary:    "Delete tracked branches already merged into the trunk (no fetch, no restack)",
		Usage:      "st prune [--remote <name>] [--dry-run] [--json]",
		Run:        runPrune,
		NewFlagSet: pruneFlagSet,
	})
}

// runPrune is sync's prune step as a standalone command: it touches no remote
// and never moves HEAD, so it refuses when the current branch itself would be
// pruned. By default "merged" is measured against the local trunk; --remote r
// measures against refs/remotes/r/<trunk> as it exists locally (no fetch).
func runPrune(args []string) error {
	var o pruneOpts
	fs := newPruneFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("prune", fs.Args()); err != nil {
		return err
	}
	asJSON, remote, dryRun := o.asJSON, o.remote, o.dryRun

	s, err := loadState()
	if err != nil {
		return err
	}

	trunkRef := "refs/heads/" + s.Trunk
	remoteExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "remote" {
			remoteExplicit = true
		}
	})
	if remoteExplicit {
		if !newGitPort().remoteExists(remote) {
			return fmt.Errorf("remote %q does not exist", remote)
		}
		remoteRef := "refs/remotes/" + remote + "/" + s.Trunk
		if _, err := git.RevParse(remoteRef); err != nil {
			return fmt.Errorf("remote %q has no tracking ref for trunk %q — fetch it first (or omit --remote)", remote, s.Trunk)
		}
		trunkRef = remoteRef
	}

	if dryRun {
		return previewState(asJSON, s, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			return stack.PrunePlan(env, s, trunkRef)
		})
	}
	return mutate("prune", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Prune(env, s, trunkRef)
	})
}

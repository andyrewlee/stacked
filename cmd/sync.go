package cmd

import (
	"flag"
	"fmt"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "sync",
		Aliases: []string{"s"},
		Summary: "Fetch trunk, fast-forward it, restack everything, and prune merged branches",
		Usage:   "st sync [--no-delete] [--remote <name>] [--json]",
		Run:     runSync,
	})
}

func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	var noDelete bool
	fs.BoolVar(&noDelete, "no-delete", false, "do not delete merged branches")
	remote := "origin"
	fs.StringVar(&remote, "remote", "origin", "remote to fetch and fast-forward from")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "show what would be pruned/restacked without changing anything")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: st sync [--no-delete] [--remote <name>] [--dry-run] [--json]")
	}
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("sync takes no positional arguments, got %q", rest[0])
	}

	if dryRun {
		s, err := loadState()
		if err != nil {
			return err
		}
		trunkRef := "refs/heads/" + s.Trunk
		remoteShell := git.RemoteShell{}
		if remoteShell.Exists(remote) {
			if err := remoteShell.Fetch(remote); err != nil {
				return fmt.Errorf("fetch %q: %w", remote, err)
			}
			trunkRef = "refs/remotes/" + remote + "/" + s.Trunk
		}
		res, err := stack.SyncPlanAgainst(stackEnv(s, asJSON), s, noDelete, trunkRef)
		if err != nil {
			return err
		}
		return renderResult(res, asJSON)
	}

	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()
	if err := s.RecordUndo("sync"); err != nil {
		return err
	}
	undoEntry, _, _ := stack.PeekUndo()

	res, err := stack.Sync(stackEnv(s, asJSON), git.RemoteShell{}, s, remote, noDelete)
	if err != nil {
		if cleanupErr := cleanupNoopUndoOnError(s, err); cleanupErr != nil {
			return fmt.Errorf("%w; additionally failed to clean up undo entry: %v", err, cleanupErr)
		}
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("saving stack state: %w", err)
	}
	if err := finalizeUndoOnSuccess(s, undoEntry); err != nil {
		return fmt.Errorf("finalizing undo entry: %w", err)
	}
	return renderResult(res, asJSON)
}

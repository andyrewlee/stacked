package cmd

import (
	"errors"
	"fmt"
	"strings"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "init",
		Summary:    "Initialize stacked stack tracking in this repo",
		Usage:      "st init [--trunk <name>] [--json]",
		Run:        runInit,
		NewFlagSet: initFlagSet,
	})
}

// runInit initializes stacked stack tracking in the current repository. It picks
// the trunk branch from the --trunk flag, falling back to the remote default
// branch, the current branch, and finally "main".
func runInit(args []string) error {
	var o initOpts
	fs := newInitFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("init", fs.Args()); err != nil {
		return err
	}
	asJSON, trunk := o.asJSON, o.trunk

	// Ensure we are inside a git repository.
	if _, err := git.RepoRoot(); err != nil {
		return fmt.Errorf("not inside a git repository: %w", err)
	}

	// If already initialized, report the existing trunk rather than erroring out
	// with a low-level message.
	if existing, err := stack.Load(); err == nil {
		return emit(asJSON, initResult{Trunk: existing.Trunk, AlreadyInitialized: true}, func() {
			out("stacked already initialized (trunk: %s)\n", sanitizeForTerminal(existing.Trunk))
		})
	} else if !errors.Is(err, stack.ErrNotInitialized) {
		return err
	}

	if trunk == "" {
		trunk = detectTrunk()
	}
	if !git.BranchExists(trunk) {
		return fmt.Errorf("trunk branch %q does not exist", trunk)
	}

	if _, err := stack.Init(trunk); err != nil {
		return fmt.Errorf("initializing stacked: %w", err)
	}

	return emit(asJSON, initResult{Trunk: trunk, Initialized: true}, func() {
		out("initialized stacked (trunk: %s)\n", sanitizeForTerminal(trunk))
		out("next: st create <name>\n")
	})
}

// initResult is the single JSON shape both init outcomes emit, so an agent
// unmarshals one struct and reads the booleans rather than probing for which
// key is present: a fresh init sets initialized, an already-initialized repo
// sets alreadyInitialized.
type initResult struct {
	Trunk              string `json:"trunk"`
	Initialized        bool   `json:"initialized"`
	AlreadyInitialized bool   `json:"alreadyInitialized"`
}

// detectTrunk determines the trunk branch name. It prefers the remote default
// branch (origin/HEAD), then the current branch, and finally "main".
func detectTrunk() string {
	if ref, err := git.Run("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(ref), "origin/"); name != "" {
			return name
		}
	}
	if cur, err := git.CurrentBranch(); err == nil {
		if cur = strings.TrimSpace(cur); cur != "" {
			return cur
		}
	}
	return "main"
}

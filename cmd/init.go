package cmd

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "init",
		Summary: "Initialize stacked stack tracking in this repo",
		Usage:   "st init [--trunk <name>]",
		Run:     runInit,
	})
}

// runInit initializes stacked stack tracking in the current repository. It picks
// the trunk branch from the --trunk flag, falling back to the remote default
// branch, the current branch, and finally "main".
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	var trunk string
	fs.StringVar(&trunk, "trunk", "", "name of the trunk branch (default: detected)")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: st init [--trunk <name>] [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Ensure we are inside a git repository.
	if _, err := git.RepoRoot(); err != nil {
		return fmt.Errorf("not inside a git repository: %w", err)
	}

	// If already initialized, report the existing trunk rather than erroring out
	// with a low-level message.
	if existing, err := stack.Load(); err == nil {
		return emit(asJSON, struct {
			Trunk              string `json:"trunk"`
			AlreadyInitialized bool   `json:"alreadyInitialized"`
		}{existing.Trunk, true}, func() {
			out("stacked already initialized (trunk: %s)\n", existing.Trunk)
		})
	} else if !errors.Is(err, stack.ErrNotInitialized) {
		return err
	}

	if trunk == "" {
		trunk = detectTrunk()
	}

	if _, err := stack.Init(trunk); err != nil {
		return fmt.Errorf("initializing stacked: %w", err)
	}

	return emit(asJSON, struct {
		Trunk       string `json:"trunk"`
		Initialized bool   `json:"initialized"`
	}{trunk, true}, func() {
		out("initialized stacked (trunk: %s)\n", trunk)
		out("next: st create <name>\n")
	})
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

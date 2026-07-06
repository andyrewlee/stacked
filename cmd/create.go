package cmd

import (
	"errors"
	"fmt"

	"stacked/internal/git"
	"stacked/internal/stack"
)

const createWorktreeCommitFlagErr = "--worktree cannot be combined with -m/-a; create the worktree first, then commit inside it"

func init() {
	register(&Command{
		Name:       "create",
		Aliases:    []string{"c"},
		Summary:    "Create a new branch stacked on the current branch",
		Usage:      "st create <name> [-m <msg>] [-a|--all] [--worktree] [--json]",
		Run:        runCreate,
		NewFlagSet: createFlagSet,
	})
}

func runCreate(args []string) error {
	var o createOpts
	fs := newCreateFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	asJSON, message, all := o.asJSON, o.message, o.all
	rest := fs.Args()
	if len(rest) != 1 {
		usageUnlessJSON(fs, args)
		return errors.New("create requires exactly one branch name")
	}
	name := rest[0]
	if err := git.CheckBranchName(name); err != nil {
		return err
	}
	if o.worktree {
		if message != "" || all {
			return errors.New(createWorktreeCommitFlagErr)
		}
		return runCreateWorktree(name, asJSON)
	}

	return mutate("create", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
		return stack.Create(env, s, name, message, all)
	})
}

func runCreateWorktree(name string, asJSON bool) error {
	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()

	env := stackEnv(s, asJSON)
	if err := s.RecordUndo(env.Git, "create"); err != nil {
		return err
	}
	undoEntry, _, _ := stack.PeekUndo()

	prep, err := stack.CreateInWorktreePrep(env, s, name)
	if err != nil {
		if cleanupErr := stack.CleanupUndoOnError(env.Git, s, err); cleanupErr != nil {
			return stack.AlsoFailed(err, "clean up undo entry", cleanupErr)
		}
		return err
	}
	b, ok := s.Get(name)
	if !ok {
		err := fmt.Errorf("branch %q was created but is not tracked", name)
		if cleanupErr := stack.CleanupUndoOnError(env.Git, s, err); cleanupErr != nil {
			return stack.AlsoFailed(err, "clean up undo entry", cleanupErr)
		}
		return err
	}
	created, err := materializeWorktree(name)
	if err != nil {
		err = fmt.Errorf("branch %q created and tracked, but its worktree failed: %w (retry with: st worktree %s)", name, err, name)
		if cleanupErr := stack.CleanupUndoOnError(env.Git, s, err); cleanupErr != nil {
			return stack.AlsoFailed(err, "clean up undo entry", cleanupErr)
		}
		return err
	}
	if err := stack.SetLastUndoCreatedWorktrees(map[string]string{name: created.Path}); err != nil {
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("saving stack state: %w", err)
	}
	if err := stack.FinalizeUndo(env.Git, s, undoEntry); err != nil {
		return fmt.Errorf("finalizing undo entry: %w", err)
	}
	writeCDDirective(created.Path)

	payload := struct {
		Branch   string   `json:"branch"`
		Parent   string   `json:"parent"`
		Worktree string   `json:"worktree"`
		Copied   []string `json:"copied,omitempty"`
		Switched bool     `json:"switched"`
		Summary  string   `json:"summary"`
	}{name, b.Parent, created.Path, created.Copied, shimActive(), prep.Summary}
	return emit(asJSON, payload, func() {
		out("%s\n", sanitizeForTerminal(prep.Summary))
		safeName := sanitizeForTerminal(name)
		safePath := sanitizeForTerminal(created.Path)
		if !shimActive() {
			out("%s\n", teleportHintForTerminal(name, created.Path))
			return
		}
		out("switched to %s (worktree: %s)\n", safeName, safePath)
	})
}

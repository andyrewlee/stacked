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

// runCreateWorktree drives create --worktree through the shared mutation
// protocol (lock -> undo snapshot -> op -> save -> finalize), capturing its
// custom payload via closure variables the way sync does. The created-worktree
// annotation runs inside the op closure, i.e. before the protocol saves and
// finalizes — the annotation must already be on the tentative journal entry
// when the finalize step runs.
func runCreateWorktree(name string, asJSON bool) error {
	var prep *stack.OpResult
	var parent string
	var created materializedWorktree
	if err := mutateState("create", asJSON, func(env stack.Env, s *stack.State) error {
		p, err := stack.CreateInWorktreePrep(env, s, name)
		if err != nil {
			return err
		}
		prep = p
		b, ok := s.Get(name)
		if !ok {
			return fmt.Errorf("branch %q was created but is not tracked", name)
		}
		parent = b.Parent
		c, err := materializeWorktree(name)
		if err != nil {
			return fmt.Errorf("branch %q created and tracked, but its worktree failed: %w (retry with: st worktree %s)", name, err, name)
		}
		created = c
		return stack.SetLastUndoCreatedWorktrees(map[string]string{name: created.Path})
	}); err != nil {
		return err
	}
	writeCDDirective(created.Path)

	payload := struct {
		Branch   string   `json:"branch"`
		Parent   string   `json:"parent"`
		Worktree string   `json:"worktree"`
		Copied   []string `json:"copied,omitempty"`
		Switched bool     `json:"switched"`
		Summary  string   `json:"summary"`
	}{name, parent, created.Path, created.Copied, shimActive(), prep.Summary}
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

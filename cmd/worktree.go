package cmd

import (
	"fmt"
	"sort"

	"stacked/internal/git"
	"stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:    "worktree",
		Aliases: []string{"wt"},
		Summary: "Materialize, list, or remove a branch's own worktree",
		Usage:   "st worktree <branch> | ls | rm <branch> [--json]",
		Run:     runWorktree,
	})
}

// runWorktree manages the worktrees that let a branch run on its own:
//
//	st worktree <branch>   materialize a worktree for an existing tracked branch
//	st worktree ls         list every worktree
//	st worktree rm <name>  remove a branch's worktree
func runWorktree(args []string) error {
	var asJSON bool
	fs := newFlagSet("worktree", &asJSON)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("worktree requires a branch name (or: ls, rm <branch>)")
	}

	switch rest[0] {
	case "ls", "list":
		if len(rest) != 1 {
			return fmt.Errorf("worktree ls takes no arguments")
		}
		return worktreeList(asJSON)
	case "rm", "remove":
		if len(rest) != 2 {
			return fmt.Errorf("worktree rm requires exactly one branch name")
		}
		return worktreeRemove(rest[1], asJSON)
	default:
		if len(rest) != 1 {
			return fmt.Errorf("worktree takes exactly one branch name")
		}
		return worktreeAdd(rest[0], asJSON)
	}
}

// repoIdentifier returns the stable repository key used as the <repo-key>
// segment of a generated worktree path, plus the repo root it resolved on the
// way (so callers need not spawn `rev-parse --show-toplevel` again).
func repoIdentifier() (key, root string, err error) {
	root, err = git.RepoRoot()
	if err != nil {
		return "", "", err
	}
	commonDir, err := git.GitCommonDir()
	if err != nil {
		return "", "", err
	}
	key, err = stack.StableRepoKey(stack.StableRepoBase(root, commonDir), commonDir)
	if err != nil {
		return "", "", err
	}
	return key, root, nil
}

// worktreeAdd materializes a worktree for an existing tracked branch at the
// canonical ~/.stacked/worktrees/<repo-key>/<encoded-branch> path, then copies
// any .worktreeinclude matches into it.
func worktreeAdd(branch string, asJSON bool) error {
	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	s, err := loadState()
	if err != nil {
		return err
	}
	if branch != s.Trunk && !s.IsTracked(branch) {
		return fmt.Errorf("%q is not a tracked branch", branch)
	}

	created, err := materializeWorktree(branch)
	if err != nil {
		return err
	}
	return emitWorktree(asJSON, branch, created.Path, created.Copied, created.Summary)
}

type materializedWorktree struct {
	Path    string
	Copied  []string
	Summary string
}

func materializeWorktree(branch string) (materializedWorktree, error) {
	wts, err := worktrees()
	if err != nil {
		return materializedWorktree{}, err
	}
	if wt, ok := stack.LinkedOwnerOf(wts, branch); ok {
		// Already materialized as its own linked worktree: report the existing path
		// idempotently rather than failing, so the command is safe to re-run.
		return materializedWorktree{Path: wt.Path, Summary: "worktree already exists"}, nil
	}
	// If branch is the one checked out in the MAIN worktree, git cannot give it a
	// second checkout, so there is no separate worktree to create. Say so plainly
	// instead of pointing the user at the main tree as if it were a dedicated
	// worktree (or leaking git's "already used by worktree" error).
	if main, ok := stack.MainWorktree(wts); ok && main.Branch == branch {
		return materializedWorktree{}, fmt.Errorf("%q is checked out in the main worktree (%s); switch it to another branch there first to give %q its own worktree", branch, main.Path, branch)
	}

	repo, root, err := repoIdentifier()
	if err != nil {
		return materializedWorktree{}, err
	}
	path, err := stack.WorktreePath(repo, branch)
	if err != nil {
		return materializedWorktree{}, err
	}
	addErr := git.WorktreeAdd(path, branch)
	// Invalidate the per-process worktree cache even on error: a failed add can
	// still have changed registration state, and a spare re-list is cheap.
	resetWorktreeCache()
	if addErr != nil {
		return materializedWorktree{}, fmt.Errorf("creating worktree for %q: %w", branch, addErr)
	}

	copied, err := copyWorktreeIncludes(root, path)
	if err != nil {
		copyErr := fmt.Errorf("copying .worktreeinclude into %q: %w", path, err)
		removeErr := git.WorktreeRemove(path, true)
		resetWorktreeCache()
		if removeErr != nil {
			return materializedWorktree{}, stack.AlsoFailed(copyErr, fmt.Sprintf("remove failed worktree %q", path), removeErr)
		}
		return materializedWorktree{}, copyErr
	}

	return materializedWorktree{Path: path, Copied: copied, Summary: "created worktree"}, nil
}

// worktreeRemove removes the worktree owning branch.
func worktreeRemove(branch string, asJSON bool) error {
	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	wts, err := worktrees()
	if err != nil {
		return err
	}
	wt, ok := stack.LinkedOwnerOf(wts, branch)
	if !ok {
		// The branch has no separate worktree. Distinguish the case where it is the
		// branch checked out in the main worktree (which is NOT a removable linked
		// worktree — git refuses with a raw "is a main working tree" fatal) from the
		// case where it has no worktree at all, so the user gets a clear message
		// either way.
		if main, mok := stack.MainWorktree(wts); mok && main.Branch == branch {
			return fmt.Errorf("%q is checked out in the main worktree, not a separate one; nothing to remove", branch)
		}
		return fmt.Errorf("%q has no worktree", branch)
	}
	removeErr := git.WorktreeRemove(wt.Path, false)
	resetWorktreeCache()
	if removeErr != nil {
		return fmt.Errorf("removing worktree for %q: %w", branch, removeErr)
	}
	payload := struct {
		Branch  string `json:"branch"`
		Removed string `json:"removed"`
	}{branch, wt.Path}
	return emit(asJSON, payload, func() {
		out("removed worktree %s (%s)\n", sanitizeForTerminal(branch), sanitizeForTerminal(wt.Path))
	})
}

// worktreeListEntry is the JSON contract of one `st worktree ls` row. It
// mirrors git.Worktree field-for-field on purpose and is the public shape
// documented in docs/AGENT.md; adding a field to git.Worktree must not leak here
// without a deliberate contract change.
type worktreeListEntry struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
	Bare     bool   `json:"bare,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
}

// worktreeList renders every worktree linked to the repository.
func worktreeList(asJSON bool) error {
	wts, err := worktrees()
	if err != nil {
		return err
	}
	wts = append([]git.Worktree(nil), wts...)
	sort.Slice(wts, func(i, j int) bool { return wts[i].Path < wts[j].Path })
	entries := make([]worktreeListEntry, 0, len(wts))
	for _, wt := range wts {
		entries = append(entries, worktreeListEntry{
			Path:     wt.Path,
			Branch:   wt.Branch,
			Head:     wt.Head,
			Bare:     wt.Bare,
			Detached: wt.Detached,
			Locked:   wt.Locked,
		})
	}
	return emit(asJSON, entries, func() {
		for _, wt := range wts {
			label := wt.Branch
			switch {
			case wt.Bare:
				label = "(bare)"
			case label == "":
				label = "(detached)"
			}
			out("%s\t%s\n", sanitizeForTerminal(label), sanitizeForTerminal(wt.Path))
		}
	})
}

// emitWorktree renders the result of a create/already-exists worktree action.
func emitWorktree(asJSON bool, branch, path string, copied []string, summary string) error {
	payload := struct {
		Branch  string   `json:"branch"`
		Path    string   `json:"path"`
		Copied  []string `json:"copied,omitempty"`
		Summary string   `json:"summary"`
	}{branch, path, copied, summary}
	return emit(asJSON, payload, func() {
		out("%s: %s -> %s\n", summary, sanitizeForTerminal(branch), sanitizeForTerminal(path))
		if len(copied) > 0 {
			safeCopied := make([]string, 0, len(copied))
			for _, name := range copied {
				safeCopied = append(safeCopied, sanitizeForTerminal(name))
			}
			out("copied: %s\n", joinNames(safeCopied))
		}
	})
}

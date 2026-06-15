package cmd

import (
	"encoding/json"
	"errors"
	"strings"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "status",
		Aliases: []string{"stat"},
		Summary: "Show current branch, its parent/children, and restack state",
		Usage:   "st status [--json]",
		Run:     runStatus,
	})
}

// runStatus summarizes the current branch's position in the stack: its role
// (trunk/tracked/untracked), parent, children, whether it needs a restack, and
// whether the working tree is clean. With --json it emits the same data as JSON.
func runStatus(args []string) error {
	var asJSON bool
	fs := newFlagSet("status", &asJSON)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("status", fs.Args()); err != nil {
		return err
	}

	s, err := loadState()
	if err != nil {
		return err
	}

	// A paused restack leaves HEAD detached; detect it before reading the current
	// branch so status can still report where the rebase stopped (and the
	// conflicted files) instead of failing with "detached HEAD".
	var rebaseInProgress bool
	var rebaseBranch string
	var conflictedFiles []string
	if inProgress, err := git.RebaseInProgress(); err != nil {
		return err
	} else if inProgress {
		rebaseInProgress = true
		if rebaseBranch, _ = git.RebaseHeadName(); rebaseBranch == "" && s.PendingReparent != nil {
			rebaseBranch = s.PendingReparent.Branch
		}
		if conflictedFiles, err = git.UnmergedFiles(); err != nil {
			return err
		}
	}

	cur, err := currentBranch()
	if err != nil {
		// A detached HEAD is expected mid-rebase; report the rebase state with no
		// current branch rather than failing.
		if !rebaseInProgress || !errors.Is(err, git.ErrDetachedHEAD) {
			return err
		}
		cur = ""
	}

	role := "untracked"
	switch {
	case cur == s.Trunk:
		role = "trunk"
	case s.IsTracked(cur):
		role = "tracked"
	}

	parent := ""
	if b, ok := s.Get(cur); ok {
		parent = b.Parent
	}

	children := []string{}
	for _, c := range s.Children(cur) {
		children = append(children, c.Name)
	}

	var needs *bool
	if s.IsTracked(cur) {
		n, err := s.NeedsRestack(gitShell, cur)
		if err != nil {
			return err
		}
		needs = &n
	}

	clean, err := git.IsClean()
	if err != nil {
		return err
	}

	if asJSON {
		payload := struct {
			Branch           string   `json:"branch"`
			Trunk            string   `json:"trunk"`
			Role             string   `json:"role"`
			Parent           string   `json:"parent,omitempty"`
			Children         []string `json:"children"`
			NeedsRestack     *bool    `json:"needsRestack,omitempty"`
			WorktreeClean    bool     `json:"worktreeClean"`
			RebaseInProgress bool     `json:"rebaseInProgress,omitempty"`
			RebaseBranch     string   `json:"rebaseBranch,omitempty"`
			ConflictedFiles  []string `json:"conflictedFiles,omitempty"`
		}{cur, s.Trunk, role, parent, children, needs, clean, rebaseInProgress, rebaseBranch, conflictedFiles}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		out("%s\n", data)
		return nil
	}

	out("branch:   %s\n", cur)
	switch role {
	case "trunk":
		out("status:   trunk\n")
	case "tracked":
		out("status:   tracked\n")
	default:
		out("status:   untracked (run: st create or st track)\n")
	}

	switch {
	case parent != "":
		out("parent:   %s\n", parent)
	case cur == s.Trunk:
		out("parent:   (none)\n")
	default:
		out("parent:   (untracked)\n")
	}

	if len(children) == 0 {
		out("children: (none)\n")
	} else {
		out("children: %s\n", strings.Join(children, ", "))
	}

	switch {
	case needs == nil:
		out("restack:  n/a\n")
	case *needs:
		out("restack:  needed (run: st restack)\n")
	default:
		out("restack:  up to date\n")
	}

	if clean {
		out("worktree: clean\n")
	} else {
		out("worktree: dirty\n")
	}
	if rebaseInProgress {
		out("rebase:   in progress on %s (run: st continue / st abort)\n", rebaseBranch)
	}
	return nil
}

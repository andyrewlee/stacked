package cmd

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "status",
		Aliases:    []string{"stat"},
		Summary:    "Show current branch, its parent/children, commits, and restack state (--pr for GitHub PR)",
		Usage:      "st status [--pr] [--json]",
		Run:        runStatus,
		NewFlagSet: statusFlagSet,
	})
}

func runStatus(args []string) error {
	var o statusOpts
	fs := newStatusFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("status", fs.Args()); err != nil {
		return err
	}
	asJSON := o.asJSON

	s, err := loadState()
	if err != nil {
		return err
	}

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
	var commits []git.CommitInfo
	if b, ok := s.Get(cur); ok {
		parent = b.Parent
		if b.ParentSHA != "" {
			if list, cerr := git.CommitList(b.ParentSHA, cur); cerr == nil {
				commits = list
			}
		}
	}

	children := []string{}
	for _, c := range s.Children(cur) {
		children = append(children, c.Name)
	}

	p := newGitPort()

	var needs *bool
	if s.IsTracked(cur) {
		n, err := s.NeedsRestack(p, cur)
		if err != nil {
			return err
		}
		needs = &n
	}

	clean, err := git.IsClean()
	if err != nil {
		return err
	}

	tip := ""
	if cur != "" {
		tips, err := git.TipsFor([]string{cur})
		if err != nil {
			return err
		}
		tip = tips[cur]
	}

	published := ""
	if cur != "" && p.remoteExists("origin") {
		states, err := git.PublishedStates("origin", []string{cur})
		if err != nil {
			return err
		}
		published = string(states[cur])
	}

	trunkBehind := trunkBehindCount(p, s.Trunk)

	var prInfo *prStatus
	if o.pr {
		openPRs, err := loadOpenPRsForDisplay()
		if err != nil {
			return err
		}
		if cur != "" {
			if pr, ok := openPRs[cur]; ok {
				prInfo = makePRStatus(&pr, parent)
			}
		}
	}

	worktreePath := ""
	if wts, err := worktrees(); err != nil {
		return err
	} else if stack.IsMultiWorktree(wts) {
		if wt, ok := stack.OwnerOf(wts, cur); ok {
			worktreePath = wt.Path
		}
	}

	if asJSON {
		payload := struct {
			Branch           string           `json:"branch"`
			Trunk            string           `json:"trunk"`
			TrunkBehind      int              `json:"trunkBehind,omitempty"`
			Role             string           `json:"role"`
			Parent           string           `json:"parent,omitempty"`
			Tip              string           `json:"tip,omitempty"`
			Published        string           `json:"published,omitempty"`
			Children         []string         `json:"children"`
			Commits          []git.CommitInfo `json:"commits,omitempty"`
			NeedsRestack     *bool            `json:"needsRestack,omitempty"`
			WorktreeClean    bool             `json:"worktreeClean"`
			RebaseInProgress bool             `json:"rebaseInProgress,omitempty"`
			RebaseBranch     string           `json:"rebaseBranch,omitempty"`
			ConflictedFiles  []string         `json:"conflictedFiles,omitempty"`
			Worktree         string           `json:"worktree,omitempty"`
			PR               *prStatus        `json:"pr,omitempty"`
		}{cur, s.Trunk, trunkBehind, role, parent, tip, published, children, commits, needs, clean, rebaseInProgress, rebaseBranch, conflictedFiles, worktreePath, prInfo}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		out("%s\n", data)
		return nil
	}

	out("branch:   %s\n", sanitizeForTerminal(cur))
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
		out("parent:   %s\n", sanitizeForTerminal(parent))
	case cur == s.Trunk:
		out("parent:   (none)\n")
	default:
		out("parent:   (untracked)\n")
	}

	if len(children) == 0 {
		out("children: (none)\n")
	} else {
		safeChildren := make([]string, 0, len(children))
		for _, child := range children {
			safeChildren = append(safeChildren, sanitizeForTerminal(child))
		}
		out("children: %s\n", strings.Join(safeChildren, ", "))
	}

	switch {
	case needs == nil:
		out("restack:  n/a\n")
	case *needs:
		out("restack:  needed (run: st restack)\n")
	default:
		out("restack:  up to date\n")
	}

	if trunkBehind > 0 {
		out("trunk:    %d commit(s) behind origin/%s (run: st sync)\n", trunkBehind, sanitizeForTerminal(s.Trunk))
	}

	if clean {
		out("worktree: clean\n")
	} else {
		out("worktree: dirty\n")
	}
	if worktreePath != "" {
		out("worktree path: %s\n", sanitizeForTerminal(worktreePath))
	}
	if prInfo != nil {
		out("pr:       %s %s\n", formatPRTag(prInfo), sanitizeForTerminal(prInfo.URL))
	}
	if rebaseInProgress {
		out("rebase:   in progress on %s (run: st continue / st abort)\n", sanitizeForTerminal(rebaseBranch))
	}
	return nil
}

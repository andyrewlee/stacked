package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "commits",
		Summary:    "List the commits in a branch's recorded stack range (base..tip)",
		Usage:      "st commits [<branch>] [--json]",
		Run:        runCommits,
		NewFlagSet: commitsFlagSet,
		// commits' one positional is a tracked branch name — same policy as
		// delete: every tracked name is a candidate.
		Completion: func(cc *completionCtx) []string {
			if cc.flagName != "" || len(cc.positionals) != 0 {
				return nil
			}
			return cc.s.BranchNames()
		},
	})
}

// runCommits lists the commits between a branch's recorded base (parentSHA)
// and its live tip, newest first — read-only: no lock, no journal. The base is
// the stack model's recorded parentSHA, not a recomputed merge-base, so the
// answer stays the model's claim; drift shows up via needsRestack/validate.
func runCommits(args []string) error {
	var o commitsOpts
	fs := newCommitsFlags(&o)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return fmt.Errorf("commits takes at most one branch, got %q", rest[1])
	}
	branch := ""
	if len(rest) == 1 {
		branch = rest[0]
	}

	s, err := loadState()
	if err != nil {
		return err
	}
	res, err := stack.CommitsPlan(stackEnv(s, o.asJSON), s, branch)
	if err != nil {
		return err
	}
	return emit(o.asJSON, res, func() {
		for _, c := range res.Commits {
			out("%s %s\n", c.SHA, sanitizeForTerminal(c.Subject))
		}
	})
}

package cmd

import (
	"flag"
	"fmt"
	"strings"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "submit",
		Aliases: []string{"ss"},
		Summary: "Push every branch in the current stack to the remote (no PRs — login-free)",
		Usage:   "st submit [--remote <name>] [--dry-run]",
		Run:     runSubmit,
	})
}

// runSubmit pushes every branch on the current stack — from the bottom branch
// (just above trunk) up to and including the currently checked-out branch — to
// the configured remote using --force-with-lease. stacked is login-free and does
// not talk to any host API, so it never opens pull requests; the user can create
// PRs on their host afterwards. With --dry-run no branches are pushed and the
// planned pushes are printed instead.
func runSubmit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)

	var remote string
	fs.StringVar(&remote, "remote", "origin", "remote to push to")

	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "print what would be pushed without pushing")

	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "output the result as JSON")

	fs.Usage = func() {
		out("usage: st submit [--remote <name>] [--dry-run] [--json]\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	state, err := loadState()
	if err != nil {
		return err
	}

	if !git.RemoteExists(remote) {
		return fmt.Errorf("remote %q does not exist", remote)
	}

	cur, err := currentBranch()
	if err != nil {
		return err
	}

	if cur == state.Trunk {
		return emit(asJSON, struct {
			Remote  string   `json:"remote"`
			Pushed  []string `json:"pushed"`
			Summary string   `json:"summary"`
		}{remote, []string{}, "at trunk; nothing to submit"}, func() {
			out("at trunk; nothing to submit\n")
		})
	}
	if !state.IsTracked(cur) {
		return fmt.Errorf("current branch %q is not tracked by stacked", cur)
	}

	// Build the ordered list of branches on the path bottom..current. Ancestors
	// gives the parents nearest-first up to and including the trunk; the tracked
	// branches among them, reversed, form the bottom-up prefix, and the current
	// branch is the top of the path.
	var stackBranches []string
	ancestors := state.Ancestors(cur)
	for i := len(ancestors) - 1; i >= 0; i-- {
		name := ancestors[i]
		if name == state.Trunk {
			continue
		}
		stackBranches = append(stackBranches, name)
	}
	stackBranches = append(stackBranches, cur)

	for _, name := range stackBranches {
		if !dryRun {
			if err := git.Push(name, true); err != nil {
				return fmt.Errorf("pushing %q: %w", name, err)
			}
		}
		if !asJSON {
			if dryRun {
				out("would push %s\n", name)
			} else {
				out("pushed %s\n", name)
			}
		}
	}
	pushed := stackBranches

	// stacked never opens PRs (it is login-free). Print the repository's web URL
	// so the user can open pull requests on their host by hand.
	repoURL := ""
	if raw, err := git.RemoteURL(remote); err == nil {
		repoURL, _ = remoteToHTTPS(raw)
	}

	payload := struct {
		Remote  string   `json:"remote"`
		DryRun  bool     `json:"dryRun"`
		Pushed  []string `json:"pushed"`
		RepoURL string   `json:"repoURL,omitempty"`
	}{remote, dryRun, pushed, repoURL}
	return emit(asJSON, payload, func() {
		if dryRun {
			out("\ndry run: %d branch(es) would be pushed to %s:\n", len(pushed), remote)
		} else {
			out("\nsubmitted %d branch(es) to %s:\n", len(pushed), remote)
		}
		for _, name := range pushed {
			out("  %s\n", name)
		}
		if repoURL != "" {
			out("\nopen pull requests on your host: %s\n", repoURL)
		}
	})
}

// remoteToHTTPS converts a git remote URL (https, ssh, or scp-like) into its
// https web URL and the host. It returns ("", "") for URLs it does not recognize.
func remoteToHTTPS(raw string) (webURL, host string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	switch {
	case strings.HasPrefix(raw, "git@"):
		rest := strings.TrimPrefix(raw, "git@") // host:owner/repo
		if i := strings.Index(rest, ":"); i >= 0 {
			host = rest[:i]
			return "https://" + host + "/" + rest[i+1:], host
		}
	case strings.HasPrefix(raw, "ssh://"):
		rest := strings.TrimPrefix(strings.TrimPrefix(raw, "ssh://"), "git@")
		if i := strings.Index(rest, "/"); i >= 0 {
			host = rest[:i]
			return "https://" + rest, host
		}
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "http://"):
		noScheme := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
		if i := strings.Index(noScheme, "/"); i >= 0 {
			host = noScheme[:i]
		}
		return raw, host
	}
	return "", ""
}

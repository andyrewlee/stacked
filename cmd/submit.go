package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"stacked/internal/git"
)

func init() {
	register(&Command{
		Name:    "submit",
		Aliases: []string{"ss"},
		Summary: "Push every branch in the current stack to the remote (no PRs — login-free)",
		Usage:   "st submit [--remote <name>] [--dry-run] [--json]",
		Run:     runSubmit,
	})
}

// submitResult is the single JSON shape every submit outcome emits, so an
// agent unmarshals one struct regardless of whether anything was pushed.
type submitResult struct {
	Remote  string   `json:"remote"`
	DryRun  bool     `json:"dryRun"`
	Pushed  []string `json:"pushed"`
	RepoURL string   `json:"repoURL,omitempty"`
	Summary string   `json:"summary,omitempty"`
	// Failed names the branch whose push failed; set only on a partial failure,
	// alongside the branches that were pushed before it (in Pushed).
	Failed string `json:"failed,omitempty"`
}

// runSubmit pushes every branch on the current stack — from the bottom branch
// (just above trunk) up to and including the currently checked-out branch — to
// the configured remote using --force-with-lease. stacked is login-free and does
// not talk to any host API, so it never opens pull requests; the user can create
// PRs on their host afterwards. With --dry-run no branches are pushed and the
// planned pushes are printed instead.
func runSubmit(args []string) error {
	var asJSON bool
	fs := newFlagSet("submit", &asJSON)

	var remote string
	fs.StringVar(&remote, "remote", "origin", "remote to push to")

	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "print what would be pushed without pushing")
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("submit", fs.Args()); err != nil {
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
		payload := submitResult{Remote: remote, DryRun: dryRun, Pushed: []string{}, Summary: "at trunk; nothing to submit"}
		return emit(asJSON, payload, func() {
			out("at trunk; nothing to submit\n")
		})
	}
	if !state.IsTracked(cur) {
		return fmt.Errorf("branch %q is not tracked by stacked", cur)
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

	pushed := []string{}
	for _, name := range stackBranches {
		if dryRun {
			pushed = append(pushed, name)
			if !asJSON {
				out("would push %s\n", name)
			}
			continue
		}
		if err := git.PushRemote(remote, name, true); err != nil {
			// Earlier branches were already pushed to the remote. In --json mode
			// the per-branch lines are suppressed, so emit a partial result (the
			// branches pushed so far plus the one that failed) before returning,
			// so a machine consumer can see the partial state — the non-zero exit
			// and error envelope on stderr still signal the failure.
			if asJSON {
				_ = emit(true, submitResult{Remote: remote, Pushed: pushed, Failed: name}, func() {})
			}
			return fmt.Errorf("pushing %q (pushed %d of %d): %w", name, len(pushed), len(stackBranches), err)
		}
		pushed = append(pushed, name)
		if !asJSON {
			out("pushed %s\n", name)
		}
	}

	// stacked never opens PRs (it is login-free). Print the repository's web URL
	// so the user can open pull requests on their host by hand.
	repoURL := ""
	if raw, err := git.RemoteURL(remote); err == nil {
		repoURL, _ = remoteToHTTPS(raw)
	}

	payload := submitResult{Remote: remote, DryRun: dryRun, Pushed: pushed, RepoURL: repoURL}
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
			path := sanitizeRemotePath(rest[i+1:])
			return "https://" + host + "/" + path, host
		}
	case strings.HasPrefix(raw, "ssh://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Path == "" {
			return "", ""
		}
		host = u.Hostname()
		if host == "" {
			return "", ""
		}
		displayHost := host
		if strings.Contains(displayHost, ":") {
			displayHost = "[" + displayHost + "]"
		}
		if port := u.Port(); port != "" {
			displayHost += ":" + port
		}
		return "https://" + displayHost + strings.TrimSuffix(u.EscapedPath(), ".git"), host
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "http://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", ""
		}
		u.User = nil
		if port := u.Port(); port != "" {
			host = u.Hostname()
			if host != "" && strings.Contains(host, ":") {
				u.Host = "[" + host + "]:" + port
			} else if host != "" {
				u.Host = host + ":" + port
			}
		}
		u.Path = strings.TrimSuffix(u.Path, ".git")
		u.RawQuery = ""
		u.Fragment = ""
		return u.String(), u.Host
	}
	return "", ""
}

func sanitizeRemotePath(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	return strings.TrimSuffix(path, ".git")
}

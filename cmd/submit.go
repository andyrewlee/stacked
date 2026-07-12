package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "submit",
		Aliases:    []string{"ss"},
		Summary:    "Push every branch in the current stack to the remote (no PRs — login-free)",
		Usage:      "st submit [--remote <name>] [--dry-run] [--json]",
		Run:        runSubmit,
		NewFlagSet: submitFlagSet,
	})
}

// submitResult is the single JSON shape every submit outcome emits, so an
// agent unmarshals one struct regardless of whether anything was pushed.
type submitResult struct {
	Remote  string   `json:"remote"`
	DryRun  bool     `json:"dryRun"`
	Pushed  []string `json:"pushed"`
	RepoURL string   `json:"repoURL,omitempty"`
	PRHints []prHint `json:"prHints,omitempty"`
	Summary string   `json:"summary,omitempty"`
	// Failed names the branch whose push failed; set only on a partial failure,
	// alongside the branches that were pushed before it (in Pushed).
	Failed string `json:"failed,omitempty"`
}

// prHint describes how to open one stacked PR: head's PR targets base. The
// compare URL is string formatting only; submit remains login-free and offline.
type prHint struct {
	Head       string `json:"head"`
	Base       string `json:"base"`
	CompareURL string `json:"compareURL,omitempty"`
}

// runSubmit pushes every branch on the current stack — from the bottom branch
// (just above trunk) up to and including the currently checked-out branch — to
// the configured remote using --force-with-lease. stacked is login-free and does
// not talk to any host API, so it never opens pull requests; the user can create
// PRs on their host afterwards. With --dry-run no branches are pushed and the
// planned pushes are printed instead.
func runSubmit(args []string) error {
	var o submitOpts
	fs := newSubmitFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("submit", fs.Args()); err != nil {
		return err
	}
	asJSON, remote, dryRun := o.asJSON, o.remote, o.dryRun

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
	if dryRun {
		for _, name := range stackBranches {
			pushed = append(pushed, name)
			if !asJSON {
				out("would push %s\n", sanitizeForTerminal(name))
			}
		}
	} else {
		if err := git.PushBranches(remote, stackBranches, true); err != nil {
			pushed, err = pushSubmitBranchesIndividually(remote, stackBranches, asJSON)
			if err != nil {
				return err
			}
		} else {
			pushed = append(pushed, stackBranches...)
			if !asJSON {
				for _, name := range pushed {
					out("pushed %s\n", sanitizeForTerminal(name))
				}
			}
		}
	}

	// stacked never opens PRs (it is login-free). Print the repository's web URL
	// so the user can open pull requests on their host by hand.
	repoURL := ""
	host := ""
	if raw, err := git.RemoteURL(remote); err == nil {
		repoURL, host = remoteToHTTPS(raw)
	}
	prHints := buildPRHints(state, stackBranches, repoURL, host)

	payload := submitResult{Remote: remote, DryRun: dryRun, Pushed: pushed, RepoURL: repoURL, PRHints: prHints}
	return emit(asJSON, payload, func() {
		if dryRun {
			out("\ndry run: %d branch(es) would be pushed to %s:\n", len(pushed), sanitizeForTerminal(remote))
		} else {
			out("\nsubmitted %d branch(es) to %s:\n", len(pushed), sanitizeForTerminal(remote))
		}
		for _, name := range pushed {
			out("  %s\n", sanitizeForTerminal(name))
		}
		if repoURL != "" {
			out("\nopen pull requests on your host: %s\n", sanitizeForTerminal(repoURL))
			for _, hint := range prHints {
				if hint.CompareURL == "" {
					continue
				}
				out("  %s -> %s  %s\n", sanitizeForTerminal(hint.Head), sanitizeForTerminal(hint.Base), sanitizeForTerminal(hint.CompareURL))
			}
		}
	})
}

func pushSubmitBranchesIndividually(remote string, branches []string, asJSON bool) ([]string, error) {
	pushed := []string{}
	for _, name := range branches {
		if err := git.PushRemote(remote, name, true); err != nil {
			// Earlier branches were already pushed to the remote. In --json mode
			// the per-branch lines are suppressed, so emit a partial result (the
			// branches pushed so far plus the one that failed) before returning,
			// so a machine consumer can see the partial state — the non-zero exit
			// and error envelope on stderr still signal the failure.
			if asJSON {
				_ = emit(true, submitResult{Remote: remote, Pushed: pushed, Failed: name}, func() {})
			}
			return pushed, fmt.Errorf("pushing %q (pushed %d of %d): %w", name, len(pushed), len(branches), err)
		}
		pushed = append(pushed, name)
		if !asJSON {
			out("pushed %s\n", sanitizeForTerminal(name))
		}
	}
	return pushed, nil
}

func buildPRHints(state *stack.State, branches []string, repoURL, host string) []prHint {
	hints := make([]prHint, 0, len(branches))
	for _, head := range branches {
		branch, ok := state.Get(head)
		if !ok {
			continue
		}
		hint := prHint{
			Head: head,
			Base: branch.Parent,
		}
		if repoURL != "" {
			hint.CompareURL = prCompareURL(repoURL, host, hint.Base, hint.Head)
		}
		hints = append(hints, hint)
	}
	return hints
}

func prCompareURL(repoURL, host, base, head string) string {
	base = url.PathEscape(base)
	head = url.PathEscape(head)
	switch strings.ToLower(host) {
	case "github.com":
		return repoURL + "/compare/" + base + "..." + head
	case "gitlab.com":
		return repoURL + "/-/compare/" + base + "..." + head
	default:
		return ""
	}
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

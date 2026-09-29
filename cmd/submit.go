package cmd

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "submit",
		Aliases:    []string{"ss"},
		Summary:    "Push the stack's branches to the remote (no PRs — login-free); --all pushes the whole forest",
		Usage:      "st submit [--all] [--remote <name>] [--dry-run] [--json]",
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
	// Failed names the first branch (in stack order) the remote confirmed
	// rejected; set only on a partial failure, alongside every branch
	// confirmed pushed (in Pushed). A ref whose outcome is unconfirmed is
	// never reported as pushed or failed.
	Failed string `json:"failed,omitempty"`
}

// prHint describes how to open one stacked PR: head's PR targets base. The
// compare URL is string formatting only; submit remains login-free and offline.
type prHint struct {
	Head       string `json:"head"`
	Base       string `json:"base"`
	CompareURL string `json:"compareURL,omitempty"`
}

// runSubmit pushes branches to the configured remote using --force-with-lease:
// with --all, every tracked branch in the forest in topological order; without
// it, every branch on the current stack — from the bottom branch (just above
// trunk) up to and including the currently checked-out branch. stacked is
// login-free and does not talk to any host API, so it never opens pull
// requests; the user can create PRs on their host afterwards. With --dry-run no
// branches are pushed and the planned pushes are printed instead.
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

	var stackBranches []string
	if o.all {
		// The whole forest is exactly the trunk's descendants, in the same
		// sorted parents-first order restack --all uses. A tracked branch
		// missing from that walk means corrupt state (a cycle or a dangling
		// parent) — name it rather than silently skip it.
		stackBranches = state.Descendants(state.Trunk)
		if len(stackBranches) != len(state.Branches) {
			reached := make(map[string]bool, len(stackBranches))
			for _, name := range stackBranches {
				reached[name] = true
			}
			var missing []string
			for name := range state.Branches {
				if !reached[name] {
					missing = append(missing, name)
				}
			}
			sort.Strings(missing)
			return fmt.Errorf("tracked branches %v do not descend from trunk %q (run st repair)", missing, state.Trunk)
		}
		if len(stackBranches) == 0 {
			payload := submitResult{Remote: remote, DryRun: dryRun, Pushed: []string{}, Summary: "nothing tracked"}
			return emit(asJSON, payload, func() {
				out("nothing tracked\n")
			})
		}
	} else {
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

		// Build the ordered list of branches on the path bottom..current.
		// Ancestors gives the parents nearest-first up to and including the
		// trunk; the tracked branches among them, reversed, form the bottom-up
		// prefix, and the current branch is the top of the path.
		ancestors := state.Ancestors(cur)
		for i := len(ancestors) - 1; i >= 0; i-- {
			name := ancestors[i]
			if name == state.Trunk {
				continue
			}
			stackBranches = append(stackBranches, name)
		}
		stackBranches = append(stackBranches, cur)
	}

	pushed := []string{}
	if dryRun {
		for _, name := range stackBranches {
			pushed = append(pushed, name)
			if !asJSON {
				out("would push %s\n", sanitizeForTerminal(name))
			}
		}
	} else {
		pushRes, pushErr := git.PushBranches(remote, stackBranches, true)
		// Argument-validation failures return a nil result: nothing was
		// attempted, so there is no per-ref status to report — surface the
		// error instead of dereferencing the empty result.
		if pushRes == nil {
			return fmt.Errorf("push to %q: %w", remote, pushErr)
		}
		// Report only what the remote confirmed, in stack order: a batch push
		// can land A and C while rejecting B, so pushed is not a prefix.
		for _, name := range stackBranches {
			switch pushRes.Status[name] {
			case git.PushUpdated, git.PushUpToDate:
				pushed = append(pushed, name)
			}
		}
		var failed string
		for _, name := range stackBranches {
			if pushRes.Status[name] == git.PushRejected {
				failed = name
				break
			}
		}
		if !asJSON {
			for _, name := range pushed {
				out("pushed %s\n", sanitizeForTerminal(name))
			}
		}
		if pushErr != nil || failed != "" {
			// Emit the confirmed partial result on stdout before returning the
			// error — a machine consumer sees exactly what landed; the
			// non-zero exit and stderr envelope still signal the failure.
			if asJSON {
				_ = emit(true, submitResult{Remote: remote, Pushed: pushed, Failed: failed}, func() {})
			}
			if failed != "" {
				if pushErr == nil {
					return fmt.Errorf("pushing %q: the remote rejected the ref", failed)
				}
				return fmt.Errorf("pushing %q (pushed %d of %d): %w", failed, len(pushed), len(stackBranches), pushErr)
			}
			return fmt.Errorf("push to %q did not confirm every ref outcome (%d of %d pushed): %w",
				remote, len(pushed), len(stackBranches), pushErr)
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
	switch forgeKind(host) {
	case forgeGitHub:
		return repoURL + "/compare/" + base + "..." + head
	case forgeGitLab:
		return repoURL + "/-/compare/" + base + "..." + head
	default:
		return ""
	}
}

type forge int

const (
	forgeUnknown forge = iota
	forgeGitHub
	forgeGitLab
)

// forgeKind classifies a host by its dot-separated labels so both the public
// hosts (github.com, gitlab.com) and self-hosted variants (github.example.com,
// gitlab.internal.example.com — GitHub Enterprise and self-managed GitLab use
// the identical /compare/ and /-/compare/ URL shapes) are recognized. Label
// matching avoids substring false positives: mygitlab.example.com has no
// "gitlab" label and stays unknown, keeping the honest "" fallback for forges
// whose URL shape is not certain.
func forgeKind(host string) forge {
	for _, label := range strings.Split(strings.ToLower(host), ".") {
		switch label {
		case "github":
			return forgeGitHub
		case "gitlab":
			return forgeGitLab
		}
	}
	return forgeUnknown
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

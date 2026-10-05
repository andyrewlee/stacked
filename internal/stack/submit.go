package stack

// submit.go — the decision half of `st submit`: which branches push, in what
// order, and how to render the repository's web URL for hand-opened PRs. The
// push itself (remote argv, ref-status confirmation) stays in cmd — this file
// holds only the State+probe decisions, so they test against fakeGit.

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// SubmitPlanResult is the ordered result of planning a submit.
type SubmitPlanResult struct {
	// Branches is the push list, bottom-first (the order a stack must land).
	// Empty when Reason explains why nothing would be pushed.
	Branches []string
	// Reason is the human-readable "nothing to do" line ("at trunk", "nothing
	// tracked") when Branches is empty.
	Reason string
}

// SubmitPlan computes a submit's push order: with all, the whole forest; else
// the current stack's path bottom..current. The push itself stays in cmd.
func SubmitPlan(env Env, s *State, all bool) (*SubmitPlanResult, error) {
	if all {
		return submitAll(env, s)
	}
	return submitCurrent(env, s)
}

// submitAll computes the whole-forest push order (--all): the trunk's
// descendants in the same sorted parents-first order restack --all uses. A
// tracked branch missing from that walk means corrupt state (a cycle or a
// dangling parent) — it is named rather than silently skipped.
func submitAll(env Env, s *State) (*SubmitPlanResult, error) {
	branches := s.Descendants(s.Trunk)
	if len(branches) != len(s.Branches) {
		reached := make(map[string]bool, len(branches))
		for _, name := range branches {
			reached[name] = true
		}
		var missing []string
		for name := range s.Branches {
			if !reached[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("tracked branches %v do not descend from trunk %q (run st repair)", missing, s.Trunk)
	}
	if len(branches) == 0 {
		return &SubmitPlanResult{Branches: branches, Reason: "nothing tracked"}, nil
	}
	return &SubmitPlanResult{Branches: branches}, nil
}

// SubmitForCurrent computes the current-stack push order (no --all): every
// branch on the path from the bottom branch (just above trunk) up to and
// including the currently checked-out branch.
func submitCurrent(env Env, s *State) (*SubmitPlanResult, error) {
	cur, err := env.Git.CurrentBranch()
	if err != nil {
		return nil, err
	}
	if cur == s.Trunk {
		return &SubmitPlanResult{Reason: "at trunk; nothing to submit"}, nil
	}
	if !s.IsTracked(cur) {
		return nil, ErrNotTracked(cur)
	}

	// Ancestors gives the parents nearest-first up to and including the trunk;
	// the tracked branches among them, reversed, form the bottom-up prefix, and
	// the current branch is the top of the path.
	var branches []string
	ancestors := s.Ancestors(cur)
	for i := len(ancestors) - 1; i >= 0; i-- {
		name := ancestors[i]
		if name == s.Trunk {
			continue
		}
		branches = append(branches, name)
	}
	branches = append(branches, cur)
	return &SubmitPlanResult{Branches: branches}, nil
}

// PRHint describes how to open one stacked PR: head's PR targets base. The
// compare URL is string formatting only; submit remains login-free and
// offline.
type PRHint struct {
	Head       string `json:"head"`
	Base       string `json:"base"`
	CompareURL string `json:"compareURL,omitempty"`
}

// PRHintsFor derives one hint per pushed branch from the recorded parents, in
// push order. compareURL is present for known forge URL shapes.
func PRHintsFor(s *State, branches []string, repoURL, host string) []PRHint {
	hints := make([]PRHint, 0, len(branches))
	for _, head := range branches {
		branch, ok := s.Get(head)
		if !ok {
			continue
		}
		hint := PRHint{
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

// RemoteToHTTPS converts a git remote URL (https, ssh, or scp-like) into its
// https web URL and the host. It returns ("", "") for URLs it does not
// recognize.
func RemoteToHTTPS(raw string) (webURL, host string) {
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

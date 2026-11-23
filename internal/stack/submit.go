package stack

// submit.go — the decision half of `st submit`: which branches push, in what
// order, and GitHub remote/URL classification.

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// SubmitPlanResult is the ordered result of planning a submit.
type SubmitPlanResult struct {
	Branches []string
	Reason   string
}

// SubmitScope selects which branches `st submit` pushes.
type SubmitScope int

const (
	// SubmitStack pushes the entire current stack (BottomOf(cur) plus all its
	// descendants in topological order), excluding unrelated sibling stacks.
	SubmitStack SubmitScope = iota
	// SubmitUpto pushes only the bottom..current path (omitting upstack
	// descendants of the current branch).
	SubmitUpto
	// SubmitAll pushes every tracked branch in the forest in topological order.
	SubmitAll
)

// SubmitPlan computes a submit's push order according to scope.
func SubmitPlan(env Env, s *State, scope SubmitScope) (*SubmitPlanResult, error) {
	switch scope {
	case SubmitAll:
		return submitAll(env, s)
	case SubmitUpto:
		return submitUpto(env, s)
	default:
		return submitStack(env, s)
	}
}

func submitStack(env Env, s *State) (*SubmitPlanResult, error) {
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
	return &SubmitPlanResult{Branches: s.StackBranches(cur)}, nil
}

func submitAll(_ Env, s *State) (*SubmitPlanResult, error) {
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
		return nil, fmt.Errorf("tracked branches %v do not descend from trunk %q (run st doctor --fix)", missing, s.Trunk)
	}
	if len(branches) == 0 {
		return &SubmitPlanResult{Branches: branches, Reason: "nothing tracked"}, nil
	}
	return &SubmitPlanResult{Branches: branches}, nil
}

func submitUpto(env Env, s *State) (*SubmitPlanResult, error) {
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

// PRHint describes how to open one stacked PR: head's PR targets base.
type PRHint struct {
	Head       string `json:"head"`
	Base       string `json:"base"`
	CompareURL string `json:"compareURL,omitempty"`
}

// PRHintsFor derives one hint per pushed branch from the recorded parents, in
// push order. CompareURL is populated for GitHub hosts.
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
			switch {
			case IsGitHubHost(host):
				hint.CompareURL = repoURL + "/compare/" + url.PathEscape(hint.Base) + "..." + url.PathEscape(hint.Head)
			case hostHasLabel(host, "gitlab"):
				hint.CompareURL = repoURL + "/-/compare/" + url.PathEscape(hint.Base) + "..." + url.PathEscape(hint.Head)
			}
		}
		hints = append(hints, hint)
	}
	return hints
}

// IsGitHubHost reports whether host names github.com or a GitHub Enterprise
// host (any dot-separated label equals "github").
func IsGitHubHost(host string) bool {
	return hostHasLabel(host, "github")
}

func hostHasLabel(host, want string) bool {
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	for _, label := range strings.Split(strings.ToLower(host), ".") {
		if label == want {
			return true
		}
	}
	return false
}

// IsGitHubRemote reports whether raw is a git remote URL pointing at GitHub.
func IsGitHubRemote(raw string) bool {
	_, host := RemoteToHTTPS(raw)
	return IsGitHubHost(host)
}

// RemoteToHTTPS converts a git remote URL (https, ssh, or scp-like) into its
// https web URL and the host. It returns ("", "") for URLs it does not
// recognize.
func RemoteToHTTPS(raw string) (webURL, host string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	switch {
	case strings.HasPrefix(raw, "git@"):
		rest := strings.TrimPrefix(raw, "git@")
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

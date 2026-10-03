package cmd

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "open",
		Summary:    "Open the branch's PR compare URL in a browser (no push, no host API)",
		Usage:      "st open [--all] [--remote <name>] [--dry-run] [--json]",
		Run:        runOpen,
		NewFlagSet: openFlagSet,
	})
}

// openResult is the JSON payload: the same PR-hint list submit prints, since
// --json never spawns — an agent decides what to do with the URLs.
type openResult struct {
	Remote  string         `json:"remote"`
	DryRun  bool           `json:"dryRun"`
	RepoURL string         `json:"repoURL,omitempty"`
	PRHints []stack.PRHint `json:"prHints,omitempty"`
	Summary string         `json:"summary,omitempty"`
}

// openFunc spawns the platform's URL opener; tests swap it to capture URLs
// without launching anything.
var openFunc = openInBrowser

// openInBrowser launches the platform's URL opener on url — the only non-git
// process st spawns. The URL passes as a single argv element: no shell ever
// interprets it.
func openInBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s not found — install it or open the URL manually", cmd.Args[0])
		}
		return err
	}
	return nil
}

// runOpen opens the PR compare URLs submit prints — the current branch's, or
// every tracked branch's under --all — without pushing anything or calling a
// host API. It is read-only: no lock, no undo entry, no ref moves.
func runOpen(args []string) error {
	var o openOpts
	fs := newOpenFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("open", fs.Args()); err != nil {
		return err
	}

	state, err := loadState()
	if err != nil {
		return err
	}
	p := newGitPort()
	if err := requireRemote(p, o.remote); err != nil {
		return err
	}
	repoURL, host := "", ""
	if raw, err := p.remoteURL(o.remote); err == nil {
		repoURL, host = stack.RemoteToHTTPS(raw)
	}

	branches, reason, err := openBranches(stackEnv(state, o.asJSON), state, o.all)
	if err != nil {
		return err
	}
	hints := stack.PRHintsFor(state, branches, repoURL, host)
	var urls []stack.PRHint
	for _, h := range hints {
		if h.CompareURL != "" {
			urls = append(urls, h)
		}
	}
	// Compare-URL presence is repo-wide: a recognized forge gives every hint a
	// URL, an unrecognized one leaves them all empty — so urls is either the
	// whole list or there is nothing openable.
	if reason == "" && len(urls) == 0 {
		reason = fmt.Sprintf("nothing to open: no compare URLs for remote %q (unrecognized host)", o.remote)
	}

	payload := openResult{Remote: o.remote, DryRun: o.dryRun, RepoURL: repoURL, PRHints: hints, Summary: reason}
	if o.asJSON {
		return emit(true, payload, func() {})
	}
	if reason != "" {
		out("%s\n", reason)
		return nil
	}
	if o.dryRun {
		for _, h := range urls {
			out("would open %s -> %s  %s\n",
				sanitizeForTerminal(h.Head), sanitizeForTerminal(h.Base), sanitizeForTerminal(h.CompareURL))
		}
		return nil
	}
	for _, h := range urls {
		if err := openFunc(h.CompareURL); err != nil {
			return fmt.Errorf("opening %s: %w", sanitizeForTerminal(h.CompareURL), err)
		}
		out("opened %s -> %s  %s\n",
			sanitizeForTerminal(h.Head), sanitizeForTerminal(h.Base), sanitizeForTerminal(h.CompareURL))
	}
	return nil
}

// openBranches resolves the URL set: --all reuses submit's whole-forest order;
// bare open is just the current branch.
func openBranches(env stack.Env, state *stack.State, all bool) (branches []string, reason string, err error) {
	if all {
		plan, err := stack.SubmitPlan(env, state, true)
		if err != nil {
			return nil, "", err
		}
		return plan.Branches, plan.Reason, nil
	}
	cur, err := currentBranch()
	if err != nil {
		return nil, "", err
	}
	switch {
	case cur == state.Trunk:
		return nil, "at trunk; nothing to open", nil
	case !state.IsTracked(cur):
		return nil, "", fmt.Errorf("branch %q is not tracked by stacked", cur)
	}
	return []string{cur}, "", nil
}

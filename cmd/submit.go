package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/andyrewlee/stacked/internal/gh"
	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "submit",
		Aliases:    []string{"ss"},
		Summary:    "Push the current stack and create/retarget/update GitHub PRs (--no-pr for push-only)",
		Usage:      "st submit [--draft] [--no-pr] [--title <t>] [--body <b>] [--all] [--upto] [--remote <name>] [--dry-run] [--json]",
		Run:        runSubmit,
		NewFlagSet: submitFlagSet,
	})
}

// submitResult is the single JSON shape every submit outcome emits.
type submitResult struct {
	Remote    string                        `json:"remote"`
	DryRun    bool                          `json:"dryRun"`
	Pushed    []string                      `json:"pushed"`
	PRs       []stack.PROutcome             `json:"prs,omitempty"`
	RepoURL   string                        `json:"repoURL,omitempty"`
	PRHints   []stack.PRHint                `json:"prHints,omitempty"`
	Summary   string                        `json:"summary,omitempty"`
	Notes     []string                      `json:"notes,omitempty"`
	Published map[string]git.PublishedState `json:"published,omitempty"`
	Failed    string                        `json:"failed,omitempty"`
}

type submitExecOpts struct {
	remote string
	scope  stack.SubmitScope
	draft  bool
	noPR   bool
	title  string
	body   string
	dryRun bool
	asJSON bool
}

func githubSyncEnabled(remoteURL string, noPR bool) bool {
	if noPR {
		return false
	}
	if os.Getenv("ST_GH_FORCE") == "1" {
		return gh.Available()
	}
	return stack.IsGitHubRemote(remoteURL) && gh.Available()
}

func runSubmit(args []string) error {
	var o submitOpts
	fs := newSubmitFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("submit", fs.Args()); err != nil {
		return err
	}
	if o.all && o.upto {
		return errors.New("--all and --upto cannot be combined")
	}
	if o.noPR && (o.draft || o.title != "" || o.body != "") {
		return errors.New("--no-pr cannot be combined with --draft, --title, or --body")
	}
	scope := stack.SubmitStack
	if o.all {
		scope = stack.SubmitAll
	} else if o.upto {
		scope = stack.SubmitUpto
	}

	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	state, err := loadState()
	if err != nil {
		return err
	}

	p := newGitPort()
	payload, err := executeSubmit(state, p, submitExecOpts{
		remote: o.remote,
		scope:  scope,
		draft:  o.draft,
		noPR:   o.noPR,
		title:  o.title,
		body:   o.body,
		dryRun: o.dryRun,
		asJSON: o.asJSON,
	})
	if err != nil {
		return err
	}

	if len(payload.Pushed) == 0 && payload.Summary != "" {
		return emit(o.asJSON, payload, func() {
			out("%s\n", payload.Summary)
		})
	}
	return emit(o.asJSON, payload, func() {
		renderSubmitText(payload)
	})
}

func executeSubmit(state *stack.State, p *cachedPort, opts submitExecOpts) (*submitResult, error) {
	if err := requireRemote(p, opts.remote); err != nil {
		return nil, err
	}

	env := stackEnv(state, opts.asJSON)
	plan, err := stack.SubmitPlan(env, state, opts.scope)
	if err != nil {
		return nil, err
	}
	stackBranches := plan.Branches
	if len(stackBranches) == 0 {
		return &submitResult{Remote: opts.remote, DryRun: opts.dryRun, Pushed: []string{}, Summary: plan.Reason}, nil
	}

	rawURL, _ := p.remoteURL(opts.remote)
	repoURL, host := stack.RemoteToHTTPS(rawURL)
	useGH := githubSyncEnabled(rawURL, opts.noPR)
	wantsPRFlags := opts.draft || opts.title != "" || opts.body != ""
	if wantsPRFlags && !useGH {
		if !gh.Available() {
			return nil, errors.New("--draft/--title/--body require the GitHub CLI (gh) on PATH")
		}
		return nil, fmt.Errorf("remote %q (%s) is not a GitHub remote; cannot manage GitHub PRs", opts.remote, rawURL)
	}

	var notes []string
	if !opts.noPR && stack.IsGitHubRemote(rawURL) && !gh.Available() {
		notes = append(notes, "gh CLI not found on PATH; pushed branches only (install gh for automatic PR sync)")
	}

	var ghClient gh.Shell
	var openByHead map[string]gh.PR
	var retargeted map[string]bool
	if useGH {
		openPRs, err := ghClient.ListOpenPRs()
		if err != nil {
			return nil, fmt.Errorf("listing open GitHub PRs: %w", err)
		}
		openByHead = stack.OpenPRsByHead(openPRs)
		// Retarget open PR bases BEFORE pushing so GitHub never auto-closes
		// child PRs when parent branches are deleted or rewritten.
		retargeted, err = stack.RetargetStackPRs(ghClient, state, stackBranches, openByHead, opts.dryRun)
		if err != nil {
			return nil, err
		}
	}

	pushed := []string{}
	created := map[string]bool{}
	if opts.dryRun {
		pushed = append(pushed, stackBranches...)
	} else {
		pushRes, pushErr := git.PushBranches(opts.remote, stackBranches, true)
		if pushRes == nil {
			return nil, fmt.Errorf("push to %q: %w", opts.remote, pushErr)
		}
		for _, name := range stackBranches {
			switch pushRes.Status[name] {
			case git.PushCreated:
				pushed = append(pushed, name)
				created[name] = true
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
		if pushErr != nil || failed != "" {
			if opts.asJSON {
				_ = emit(true, submitResult{Remote: opts.remote, Pushed: pushed, Failed: failed}, func() {})
			}
			if failed != "" {
				if pushErr == nil {
					return nil, fmt.Errorf("pushing %q: the remote rejected the ref", failed)
				}
				return nil, fmt.Errorf("pushing %q (pushed %d of %d): %w", failed, len(pushed), len(stackBranches), pushErr)
			}
			return nil, fmt.Errorf("push to %q did not confirm every ref outcome (%d of %d pushed): %w",
				opts.remote, len(pushed), len(stackBranches), pushErr)
		}
	}

	var prOutcomes []stack.PROutcome
	if useGH {
		cur, _ := env.Git.CurrentBranch()
		prOutcomes, err = stack.SyncStackPRs(ghClient, env.Git, state, pushed, openByHead, retargeted, stack.SubmitPROpts{
			Draft:        opts.draft,
			Title:        opts.title,
			Body:         opts.body,
			TargetBranch: cur,
			DryRun:       opts.dryRun,
		})
		if err != nil {
			return nil, err
		}
	}

	prHints := stack.PRHintsFor(state, stackBranches, repoURL, host)
	var published map[string]git.PublishedState
	if opts.dryRun {
		if published, err = git.PublishedStates(opts.remote, stackBranches); err != nil {
			return nil, err
		}
		for _, name := range stackBranches {
			if published[name] == git.PublishedMissing {
				created[name] = true
			}
		}
	}

	// Keep compare-URL hints in text output only when GitHub PR sync did not
	// already create/manage PRs directly.
	if useGH {
		created = nil
	}

	res := &submitResult{
		Remote:    opts.remote,
		DryRun:    opts.dryRun,
		Pushed:    pushed,
		PRs:       prOutcomes,
		RepoURL:   repoURL,
		PRHints:   prHints,
		Notes:     notes,
		Published: published,
	}
	if !opts.asJSON && len(created) > 0 && repoURL != "" && !useGH {
		// Store compare hints via PRHints + renderSubmitText handles display.
		res.Notes = append(res.Notes, fmt.Sprintf("open pull requests on your host: %s", repoURL))
		for _, hint := range prHints {
			if created[hint.Head] && hint.CompareURL != "" {
				res.Notes = append(res.Notes, fmt.Sprintf("  %s -> %s  %s", hint.Head, hint.Base, hint.CompareURL))
			}
		}
	}
	return res, nil
}

func renderSubmitText(res *submitResult) {
	if res.DryRun {
		for _, name := range res.Pushed {
			out("would push %s\n", sanitizeForTerminal(name))
		}
		out("\ndry run: %d branch(es) would be pushed to %s:\n", len(res.Pushed), sanitizeForTerminal(res.Remote))
	} else {
		for _, name := range res.Pushed {
			out("pushed %s\n", sanitizeForTerminal(name))
		}
		out("\nsubmitted %d branch(es) to %s:\n", len(res.Pushed), sanitizeForTerminal(res.Remote))
	}
	for _, name := range res.Pushed {
		out("  %s\n", sanitizeForTerminal(name))
	}
	if len(res.PRs) > 0 {
		out("\npull requests:\n")
		for _, pr := range res.PRs {
			num := ""
			if pr.Number > 0 {
				num = fmt.Sprintf("#%d ", pr.Number)
			}
			line := fmt.Sprintf("  %s%s -> %s (%s)", num, sanitizeForTerminal(pr.Branch), sanitizeForTerminal(pr.Base), sanitizeForTerminal(pr.Action))
			if pr.URL != "" {
				line += "  " + sanitizeForTerminal(pr.URL)
			}
			out("%s\n", line)
		}
	}
	for _, n := range res.Notes {
		out("%s\n", sanitizeForTerminal(n))
	}
}

package cmd

import (
	"fmt"

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
	Remote  string         `json:"remote"`
	DryRun  bool           `json:"dryRun"`
	Pushed  []string       `json:"pushed"`
	RepoURL string         `json:"repoURL,omitempty"`
	PRHints []stack.PRHint `json:"prHints,omitempty"`
	Summary string         `json:"summary,omitempty"`
	// Published maps each would-push branch to its state against the remote's
	// local tracking ref — current/stale/diverged/missing/unknown, reflecting
	// the last fetch or push, never live server state. Emitted only for
	// --dry-run, where "is a submit needed" is the decision being made; a real
	// push moves the tracking refs, so its Pushed/Failed records are the truth.
	Published map[string]git.PublishedState `json:"published,omitempty"`
	// Failed names the first branch (in stack order) the remote confirmed
	// rejected; set only on a partial failure, alongside every branch
	// confirmed pushed (in Pushed). A ref whose outcome is unconfirmed is
	// never reported as pushed or failed.
	Failed string `json:"failed,omitempty"`
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

	// Serialize with every other stack op: submit resolves which commits to
	// push from stack state + branch tips, and a concurrent sync/delete/undo
	// mid-flight would publish a stale tip or fail on a vanished branch. The
	// push itself moves no local refs, so no undo entry — just the lock.
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
	if err := requireRemote(p, remote); err != nil {
		return err
	}

	plan, err := stack.SubmitPlan(stackEnv(state, asJSON), state, o.all)
	if err != nil {
		return err
	}
	stackBranches := plan.Branches
	if len(stackBranches) == 0 {
		payload := submitResult{Remote: remote, DryRun: dryRun, Pushed: []string{}, Summary: plan.Reason}
		return emit(asJSON, payload, func() {
			out("%s\n", plan.Reason)
		})
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
	if raw, err := p.remoteURL(remote); err == nil {
		repoURL, host = stack.RemoteToHTTPS(raw)
	}
	prHints := stack.PRHintsFor(state, stackBranches, repoURL, host)

	var published map[string]git.PublishedState
	if dryRun {
		if published, err = git.PublishedStates(remote, stackBranches); err != nil {
			return err
		}
	}

	payload := submitResult{Remote: remote, DryRun: dryRun, Pushed: pushed, RepoURL: repoURL, PRHints: prHints, Published: published}
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

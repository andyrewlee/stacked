package cmd

import (
	"fmt"

	"github.com/andyrewlee/stacked/internal/gh"
	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "sync",
		Aliases:    []string{"s"},
		Summary:    "Fetch trunk, fast-forward it, prune merged branches, and restack (--submit to push & update PRs)",
		Usage:      "st sync [-s|--submit] [--all] [--no-delete] [--no-fetch] [--remote <name>] [--dry-run] [--json]",
		Run:        runSync,
		NewFlagSet: syncFlagSet,
	})
}

type syncSubmitResult struct {
	Summary    string            `json:"summary"`
	Branch     string            `json:"branch,omitempty"`
	Restacked  []string          `json:"restacked,omitempty"`
	Deleted    []string          `json:"deleted,omitempty"`
	Reparented []stack.Reparent  `json:"reparented,omitempty"`
	Notes      []string          `json:"notes,omitempty"`
	Pushed     []string          `json:"pushed,omitempty"`
	PRs        []stack.PROutcome `json:"prs,omitempty"`
}

func runSync(args []string) error {
	var o syncOpts
	fs := newSyncFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	if err := rejectArgs("sync", fs.Args()); err != nil {
		return err
	}
	asJSON, all, noDelete, noFetch, remote, dryRun := o.asJSON, o.all, o.noDelete, o.noFetch, o.remote, o.dryRun

	p := newGitPort()
	if explicitRemote(fs) || o.submit {
		if err := requireRemote(p, remote); err != nil {
			return err
		}
	}

	// Best-effort GitHub merged-PR lookup so squash/rebase-merged PRs whose
	// patches diverged from trunk are still pruned accurately.
	fetchGitHubMerged := func(s *stack.State) (map[string]bool, string) {
		if noFetch || noDelete {
			return nil, ""
		}
		rawURL, err := p.remoteURL(remote)
		if err != nil || !githubSyncEnabled(rawURL, false) {
			return nil, ""
		}
		merged, err := stack.MergedGitHubBranches(gh.Shell{}, s.BranchNames())
		if err != nil {
			return nil, fmt.Sprintf("github merged-PR check skipped: %v", err)
		}
		return merged, ""
	}

	if dryRun {
		return preview(asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			basis := git.LocalBranchNameRef(s.Trunk)
			if !noFetch {
				basis, _ = resolveTrunkRef(p, remote, s.Trunk)
			}
			extraMerged, note := fetchGitHubMerged(s)
			res, err := stack.SyncPlanAgainstWithMerged(env, s, noDelete, all, basis, extraMerged)
			if err != nil {
				return nil, err
			}
			if note != "" {
				res.Notes = append(res.Notes, note)
			}
			return res, nil
		})
	}

	if !o.submit {
		return mutate("sync", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
			extraMerged, note := fetchGitHubMerged(s)
			res, err := stack.SyncWithOptions(env, git.RemoteShell{}, s, stack.SyncOptions{
				Remote:      remote,
				NoDelete:    noDelete,
				NoFetch:     noFetch,
				All:         all,
				ExtraMerged: extraMerged,
			})
			if err != nil {
				return nil, err
			}
			if note != "" {
				res.Notes = append(res.Notes, note)
			}
			return res, nil
		})
	}

	var syncRes *stack.OpResult
	if err := mutateState("sync", asJSON, func(env stack.Env, s *stack.State) error {
		extraMerged, note := fetchGitHubMerged(s)
		r, err := stack.SyncWithOptions(env, git.RemoteShell{}, s, stack.SyncOptions{
			Remote:      remote,
			NoDelete:    noDelete,
			NoFetch:     noFetch,
			All:         all,
			ExtraMerged: extraMerged,
		})
		if err != nil {
			return err
		}
		if note != "" {
			r.Notes = append(r.Notes, note)
		}
		syncRes = r
		return nil
	}); err != nil {
		return err
	}

	// Reload state under lock and submit the surviving stack (retarget PR bases,
	// force-push restacked branches, refresh stack blocks).
	s, release, err := lockAndLoad()
	if err != nil {
		return err
	}
	defer release()

	scope := stack.SubmitStack
	if all {
		scope = stack.SubmitAll
	}
	submitRes, err := executeSubmit(s, newGitPort(), submitExecOpts{
		remote: remote,
		scope:  scope,
		asJSON: asJSON,
	})
	if err != nil {
		return err
	}

	combined := syncSubmitResult{
		Summary:    syncRes.Summary,
		Branch:     syncRes.Branch,
		Restacked:  syncRes.Restacked,
		Deleted:    syncRes.Deleted,
		Reparented: syncRes.Reparented,
		Notes:      append(append([]string(nil), syncRes.Notes...), submitRes.Notes...),
		Pushed:     submitRes.Pushed,
		PRs:        submitRes.PRs,
	}
	return emit(asJSON, combined, func() {
		_ = renderResult(syncRes, false)
		if len(submitRes.Pushed) > 0 {
			out("\n")
			renderSubmitText(submitRes)
		}
	})
}

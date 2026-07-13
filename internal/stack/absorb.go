package stack

import (
	"errors"
	"fmt"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
)

// AbsorbResult is the outcome of an absorb attribution pass. Absorbed lists
// the hunks mapped to a target commit; Refused lists per-hunk data explaining
// why a hunk could not be absorbed (a refusal is NOT a command failure — the
// CLI exits 0 and reports them, matching the repo's skip-loudly posture).
// DryRun marks an attribution-only pass.
type AbsorbResult struct {
	Summary   string         `json:"summary"`
	Absorbed  []AbsorbedHunk `json:"absorbed"`
	Refused   []RefusedHunk  `json:"refused"`
	Restacked []string       `json:"restacked,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	DryRun    bool           `json:"dryRun,omitempty"`
}

// AbsorbedHunk is one staged hunk attributed to the stack commit that owns
// every one of its pre-image lines.
type AbsorbedHunk struct {
	File   string `json:"file"`
	Lines  string `json:"lines"` // pre-image range, e.g. "2" or "3-4"
	Branch string `json:"branch"`
	Commit string `json:"commit"`

	// hunk is the source tuple, kept for per-target patch reassembly
	// (unexported: never marshaled).
	hunk git.Hunk
}

// RefusedHunk is one staged hunk absorb will not touch, with the reason.
type RefusedHunk struct {
	File   string `json:"file"`
	Lines  string `json:"lines"`
	Reason string `json:"reason"`
}

// requireNoUnstaged is absorb's working-tree guard: absorb's INPUT is the
// staged content, so unlike requireClean it permits a staged index but
// refuses unstaged changes (they would make the later apply — the absorb v1
// slice 2 — ambiguous).
func requireNoUnstaged(g Git) error {
	unstaged, err := g.HasUnstagedChanges()
	if err != nil {
		return fmt.Errorf("checking unstaged changes: %w", err)
	}
	if unstaged {
		return fmt.Errorf("unstaged changes present; stage them (git add) or discard before absorb")
	}
	return nil
}

// AbsorbPlan attributes every staged hunk to the stack commit that owns its
// pre-image lines, with zero mutation: reads only. The v1 decision table
// (from the absorb design spike) refuses everything ambiguous — multi-commit
// hunks, lines owned by trunk/history, pure additions, and targets that are
// not a tracked branch's tip.
func AbsorbPlan(env Env, s *State) (*AbsorbResult, error) {
	res, _, err := absorbPlan(env, s)
	return res, err
}

// absorbPlan is AbsorbPlan plus the resolved current branch, so Absorb can
// reuse it without a second CurrentBranch read.
func absorbPlan(env Env, s *State) (*AbsorbResult, string, error) {
	g := env.Git
	cur, _, err := currentTracked(g, s)
	if err != nil {
		return nil, "", err
	}
	if err := requireNoUnstaged(g); err != nil {
		return nil, "", err
	}
	hunks, unsupported, err := g.DiffCachedHunks()
	if err != nil {
		return nil, "", fmt.Errorf("reading staged hunks: %w", err)
	}
	res := &AbsorbResult{Absorbed: []AbsorbedHunk{}, Refused: []RefusedHunk{}, DryRun: true}
	// Every staged section the parser could not classify as text hunks is a
	// refusal — the zero-refusal apply gate must cover the WHOLE staged diff,
	// because the apply replays the full patch, not just the hunks.
	for _, u := range unsupported {
		res.Refused = append(res.Refused, RefusedHunk{File: u.File, Lines: "-", Reason: u.Reason + "; absorb v1 handles plain text hunks only"})
	}
	if len(hunks) == 0 && len(res.Refused) == 0 {
		res.Summary = "nothing to absorb"
		return res, cur, nil
	}

	// Stack set = commits reachable from the current branch but not from the
	// trunk — one bounded `git rev-list trunk..cur` (CommitRange). The old
	// two-AncestorSet subtraction walked the ENTIRE trunk history for the
	// same set.
	stackSet, err := g.CommitRange(branchTipRef(s.Trunk), branchTipRef(cur))
	if err != nil {
		return nil, "", fmt.Errorf("walk %s..%s: %w", s.Trunk, cur, err)
	}

	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, "", fmt.Errorf("read branch tips: %w", err)
	}
	tipToBranch := make(map[string]string, len(tips))
	for name, tip := range tips {
		if s.IsTracked(name) {
			tipToBranch[tip] = name
		}
	}

	blameByFile := map[string]map[int]string{}
	for _, h := range hunks {
		lines := hunkLines(h)
		if h.OldN == 0 {
			// The design spike's prototype showed nearest-context attribution
			// of a pure addition silently targets the trunk; refuse in v1.
			res.Refused = append(res.Refused, RefusedHunk{File: h.File, Lines: lines, Reason: "pure addition; use st modify"})
			continue
		}
		blame, ok := blameByFile[h.File]
		if !ok {
			blame, err = g.BlamePorcelain(h.File, "HEAD")
			if err != nil {
				return nil, "", fmt.Errorf("blame %q: %w", h.File, err)
			}
			blameByFile[h.File] = blame
		}
		owners := map[string]bool{}
		missing := false
		outside := false
		for line := h.OldStart; line <= h.OldStart+h.OldN-1; line++ {
			sha, ok := blame[line]
			if !ok {
				missing = true
				break
			}
			if !stackSet[sha] {
				outside = true
				break
			}
			owners[sha] = true
		}
		switch {
		case missing:
			res.Refused = append(res.Refused, RefusedHunk{File: h.File, Lines: lines, Reason: "cannot attribute (untracked, renamed, or binary file)"})
		case outside:
			res.Refused = append(res.Refused, RefusedHunk{File: h.File, Lines: lines, Reason: "touches lines owned by trunk or history below the stack"})
		case len(owners) > 1:
			names := make([]string, 0, len(owners))
			for sha := range owners {
				names = append(names, shortSHA(sha))
			}
			sort.Strings(names)
			res.Refused = append(res.Refused, RefusedHunk{File: h.File, Lines: lines, Reason: fmt.Sprintf("spans %d stack commits (%s)", len(names), joinComma(names))})
		default:
			var target string
			for sha := range owners {
				target = sha
			}
			branch, isTip := tipToBranch[target]
			if !isTip {
				res.Refused = append(res.Refused, RefusedHunk{File: h.File, Lines: lines, Reason: "target is not a branch tip; squash the branch or absorb manually"})
				continue
			}
			res.Absorbed = append(res.Absorbed, AbsorbedHunk{File: h.File, Lines: lines, Branch: branch, Commit: target, hunk: h})
		}
	}
	res.Summary = fmt.Sprintf("would absorb %d hunk(s); refused %d", len(res.Absorbed), len(res.Refused))
	return res, cur, nil
}

// Absorb applies the attribution plan when every staged hunk attributed to
// a tracked tip and nothing was refused. Each target branch's tip is amended
// with ONLY its own hunks via AmendTipWithPatch — a temp-index amend that
// touches no worktree, so the user's edits are safely in commits before any
// destructive step — then ONE upstack cascade from the lowest amended target
// restacks everything above it and HEAD returns to the starting branch. All
// targets lie on the current branch's ancestor path (the stack set is
// trunk..cur), so the lowest target's upstack covers every other target. Any
// refusal, or a dirty owner worktree for any target, returns the plan as
// data, unapplied — never an error; one undo entry reverts all amends plus
// the cascade.
func Absorb(env Env, s *State) (*AbsorbResult, error) {
	g := env.Git
	plan, cur, err := absorbPlan(env, s)
	if err != nil {
		return nil, err
	}
	if len(plan.Absorbed) == 0 && len(plan.Refused) == 0 {
		return plan, nil // nothing staged
	}
	targets := targetsOf(s, plan)
	if len(plan.Refused) > 0 || len(targets) == 0 {
		plan.Summary = "not applied: absorb refuses to apply a plan with refusals; " + plan.Summary
		return plan, nil
	}

	// Pre-flight EVERY target's owner worktree before any mutation: absorb is
	// all-or-nothing, so one dirty owner blocks the whole plan (a partial
	// apply would fracture the one-undo-entry story).
	ownerDirs := map[string]string{}
	for _, target := range targets {
		if target == cur {
			continue
		}
		owner, elsewhere, err := s.ownerElsewhere(g, target)
		if err != nil {
			return nil, err
		}
		if elsewhere {
			clean, err := g.IsCleanIn(owner.Path)
			if err != nil {
				return nil, fmt.Errorf("checking worktree %s: %w", owner.Path, err)
			}
			if !clean {
				plan.Summary = "not applied: a target's worktree is dirty; " + plan.Summary
				plan.Notes = append(plan.Notes, fmt.Sprintf("branch %q is checked out in %s with uncommitted changes; commit or stash there first", target, owner.Path))
				return plan, nil
			}
			ownerDirs[target] = owner.Path
		}
	}

	// Amend ancestors first (deterministic; the amends are independent — each
	// reads only its own tip tree, and targets' hunks are line-disjoint by the
	// multi-owner refusal). If amend k fails, amends 1..k-1 persist and the
	// undo entry reverts them.
	hunksByTarget := map[string][]git.Hunk{}
	for _, a := range plan.Absorbed {
		hunksByTarget[a.Branch] = append(hunksByTarget[a.Branch], a.hunk)
	}
	newTips := make(map[string]string, len(targets))
	for _, target := range targets {
		patch, err := g.DiffCachedPatchFor(hunksByTarget[target])
		if err != nil {
			return nil, fmt.Errorf("assembling %q's patch: %w", target, err)
		}
		newTip, err := g.AmendTipWithPatch(target, patch)
		if err != nil {
			// The temp-index apply is the pre-flight check: THIS target is
			// untouched; earlier amends persist under the undo entry.
			return nil, fmt.Errorf("absorb into %q: %w", target, err)
		}
		newTips[target] = newTip
	}
	// From here every staged edit is committed at its target's tip; the
	// resets below only drop copies.
	for i := range plan.Absorbed {
		plan.Absorbed[i].Commit = newTips[plan.Absorbed[i].Branch]
	}
	for target, dir := range ownerDirs {
		if err := g.ResetHardIn(dir, "HEAD"); err != nil {
			return nil, fmt.Errorf("syncing worktree %s to the amended %q: %w", dir, target, err)
		}
	}
	soleTargetIsCur := len(targets) == 1 && targets[0] == cur
	if !soleTargetIsCur {
		// Drop the staged copies from this worktree so the cascade can
		// rebase; the edits now live in their targets' tips and the restack
		// re-delivers them. (When cur is the ONLY target its index
		// self-resolves against the amended HEAD — no reset.)
		if err := g.ResetHardIn("", "HEAD"); err != nil {
			return nil, fmt.Errorf("dropping the absorbed staged copies: %w", err)
		}
	}

	plan.DryRun = false
	lowest := targets[0]
	rebased, err := s.RestackUpstack(env, lowest)
	if err != nil {
		err = restoreHEADAfterNonConflict(env, cur, s.Trunk, err)
		// On a hard (non-conflict) cascade failure the staged copies are
		// already gone from this worktree but the edits are committed in the
		// targets' tips — say so, or they silently "vanish" from where the
		// user was working. A conflict needs no hint: the paused rebase +
		// st continue is the documented path and re-delivers them itself.
		soleTargetIsCur := len(targets) == 1 && targets[0] == cur
		if !errors.Is(err, ErrConflict) && !soleTargetIsCur {
			err = fmt.Errorf("%w; your staged changes are safely committed in %s — run: st restack (or st undo to revert the absorb)", err, joinComma(targets))
		}
		return nil, err
	}
	plan.Restacked = rebased
	// The cascade rebases every target above the lowest, so the amend-time
	// commits recorded above are stale for them — report each hunk's commit
	// as its branch's live post-cascade tip.
	finalTips, err := g.TipsFor(targets)
	if err != nil {
		return nil, fmt.Errorf("re-reading amended tips: %w", err)
	}
	for i := range plan.Absorbed {
		if tip, ok := finalTips[plan.Absorbed[i].Branch]; ok {
			plan.Absorbed[i].Commit = tip
		}
	}
	plan.Notes = append(plan.Notes, skippedWorktreeNotes(s)...)
	if err := env.save(); err != nil {
		return nil, err
	}
	if err := restoreHEAD(env, cur, s.Trunk); err != nil {
		return nil, err
	}
	plan.Summary = fmt.Sprintf("absorbed %d hunk(s) into %s; restacked %d branch(es)", len(plan.Absorbed), joinComma(targets), len(rebased))
	return plan, nil
}

// targetsOf returns the distinct target branches of a zero-refusal plan,
// sorted ancestors-first by stack depth (every target lies on the current
// branch's ancestor path, so depth is a total order here).
func targetsOf(s *State, plan *AbsorbResult) []string {
	seen := map[string]bool{}
	var targets []string
	for _, a := range plan.Absorbed {
		if !seen[a.Branch] {
			seen[a.Branch] = true
			targets = append(targets, a.Branch)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		di, dj := len(s.Ancestors(targets[i])), len(s.Ancestors(targets[j]))
		if di != dj {
			return di < dj
		}
		return targets[i] < targets[j]
	})
	return targets
}

// hunkLines renders a hunk's pre-image range for humans: "2" or "3-4"; a pure
// addition anchors at the insertion point.
func hunkLines(h git.Hunk) string {
	if h.OldN <= 1 {
		return fmt.Sprintf("%d", h.OldStart)
	}
	return fmt.Sprintf("%d-%d", h.OldStart, h.OldStart+h.OldN-1)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

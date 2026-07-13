package stack

import (
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
	g := env.Git
	cur, _, err := currentTracked(g, s)
	if err != nil {
		return nil, err
	}
	if err := requireNoUnstaged(g); err != nil {
		return nil, err
	}
	hunks, unsupported, err := g.DiffCachedHunks()
	if err != nil {
		return nil, fmt.Errorf("reading staged hunks: %w", err)
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
		return res, nil
	}

	// Stack set = commits reachable from the current branch but not from the
	// trunk — exactly `git rev-list trunk..HEAD`, from the existing port.
	curTip, err := g.RevParse(branchTipRef(cur))
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", cur, err)
	}
	trunkTip, err := g.RevParse(branchTipRef(s.Trunk))
	if err != nil {
		return nil, fmt.Errorf("resolve trunk %q: %w", s.Trunk, err)
	}
	curSet, err := g.AncestorSet(curTip)
	if err != nil {
		return nil, fmt.Errorf("walk %q: %w", cur, err)
	}
	trunkSet, err := g.AncestorSet(trunkTip)
	if err != nil {
		return nil, fmt.Errorf("walk trunk %q: %w", s.Trunk, err)
	}
	stackSet := make(map[string]bool, len(curSet))
	for sha := range curSet {
		if !trunkSet[sha] {
			stackSet[sha] = true
		}
	}

	tips, err := g.TipsFor(stateTipNames(s))
	if err != nil {
		return nil, fmt.Errorf("read branch tips: %w", err)
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
				return nil, fmt.Errorf("blame %q: %w", h.File, err)
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
			res.Absorbed = append(res.Absorbed, AbsorbedHunk{File: h.File, Lines: lines, Branch: branch, Commit: target})
		}
	}
	res.Summary = fmt.Sprintf("would absorb %d hunk(s); refused %d", len(res.Absorbed), len(res.Refused))
	return res, nil
}

// Absorb applies the attribution plan when it names exactly one target branch
// with zero refusals (the v1 apply slice). The staged patch is committed into
// the target's tip via AmendTipWithPatch — a temp-index amend that touches no
// worktree, so the user's edit is safely in a commit before any destructive
// step — then ONE upstack cascade restacks the descendants and HEAD returns to
// the starting branch. Anything wider (multiple targets, any refusal, a dirty
// owner worktree) returns the plan as data, unapplied — never an error.
func Absorb(env Env, s *State) (*AbsorbResult, error) {
	g := env.Git
	plan, err := AbsorbPlan(env, s)
	if err != nil {
		return nil, err
	}
	if len(plan.Absorbed) == 0 && len(plan.Refused) == 0 {
		return plan, nil // nothing staged
	}
	target, ok := singleTarget(plan)
	if !ok {
		plan.Summary = "not applied: absorb v1 handles a single target with no refusals; " + plan.Summary
		return plan, nil
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return nil, err
	}

	// Resolve where the target lives. A dirty owner worktree is skipped loudly
	// (its uncommitted work is never clobbered by the post-amend sync).
	ownerDir := ""
	if target != cur {
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
				plan.Summary = "not applied: the target's worktree is dirty; " + plan.Summary
				plan.Notes = append(plan.Notes, fmt.Sprintf("branch %q is checked out in %s with uncommitted changes; commit or stash there first", target, owner.Path))
				return plan, nil
			}
			ownerDir = owner.Path
		}
	}

	patch, err := g.DiffCachedPatch()
	if err != nil {
		return nil, fmt.Errorf("capturing the staged patch: %w", err)
	}
	newTip, err := g.AmendTipWithPatch(target, patch)
	if err != nil {
		// Nothing was mutated: the temp-index apply is the pre-flight check.
		return nil, fmt.Errorf("absorb: %w", err)
	}
	// From here the staged content is committed at target's tip; the resets
	// below only drop copies of it.
	for i := range plan.Absorbed {
		plan.Absorbed[i].Commit = newTip
	}
	if ownerDir != "" {
		if err := g.ResetHardIn(ownerDir, "HEAD"); err != nil {
			return nil, fmt.Errorf("syncing worktree %s to the amended %q: %w", ownerDir, target, err)
		}
	}
	if target != cur {
		// Drop the staged copy from this worktree so the cascade can rebase;
		// the edit now lives in target's tip and the restack re-delivers it.
		if err := g.ResetHardIn("", "HEAD"); err != nil {
			return nil, fmt.Errorf("dropping the absorbed staged copy: %w", err)
		}
	}

	plan.DryRun = false
	rebased, err := s.RestackUpstack(env, target)
	if err != nil {
		return nil, restoreHEADAfterNonConflict(env, cur, s.Trunk, err)
	}
	plan.Restacked = rebased
	plan.Notes = append(plan.Notes, skippedWorktreeNotes(s)...)
	if err := env.save(); err != nil {
		return nil, err
	}
	if err := restoreHEAD(env, cur, s.Trunk); err != nil {
		return nil, err
	}
	plan.Summary = fmt.Sprintf("absorbed %d hunk(s) into %s; restacked %d branch(es)", len(plan.Absorbed), target, len(rebased))
	return plan, nil
}

// singleTarget reports the one branch every absorbed hunk targets, false when
// the plan has any refusal or more than one distinct target branch.
func singleTarget(plan *AbsorbResult) (string, bool) {
	if len(plan.Refused) > 0 || len(plan.Absorbed) == 0 {
		return "", false
	}
	target := plan.Absorbed[0].Branch
	for _, a := range plan.Absorbed[1:] {
		if a.Branch != target {
			return "", false
		}
	}
	return target, true
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

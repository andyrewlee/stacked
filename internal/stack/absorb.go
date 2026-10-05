package stack

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
)

// AbsorbResult is the outcome of an absorb attribution pass. Absorbed lists
// the hunks mapped to a target commit; Refused lists per-hunk data explaining
// why a hunk could not be absorbed (a refusal is NOT a command failure — the
// CLI exits 0 and reports them, matching the repo's skip-loudly posture).
// DryRun marks an attribution-only pass.
type AbsorbResult struct {
	Summary   string         `json:"summary"`
	Absorbed  []absorbedHunk `json:"absorbed"`
	Refused   []refusedHunk  `json:"refused"`
	Restacked []string       `json:"restacked,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	DryRun    bool           `json:"dryRun,omitempty"`
}

// absorbedHunk is one staged hunk attributed to the stack commit that owns
// every one of its pre-image lines.
type absorbedHunk struct {
	File   string `json:"file"`
	Lines  string `json:"lines"` // pre-image range, e.g. "2" or "3-4"
	Branch string `json:"branch"`
	Commit string `json:"commit"`

	// hunk is the source tuple, kept for per-target patch reassembly
	// (unexported: never marshaled).
	hunk git.Hunk
}

// refusedHunk is one staged hunk absorb will not touch, with the reason.
type refusedHunk struct {
	File   string `json:"file"`
	Lines  string `json:"lines"`
	Reason string `json:"reason"`
}

// requireNoUnstaged is absorb's working-tree guard: absorb's INPUT is the
// staged content, so unlike requireClean it permits a staged index but
// refuses unstaged changes (they would make the later apply ambiguous).
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
// pre-image lines, with zero mutation: reads only. The decision table
// (from the absorb design spike) refuses everything ambiguous — multi-commit
// hunks, lines owned by trunk/history, pure additions, targets that are not
// the tip of a tracked branch on the current stack's path, and hunks whose
// blame provenance does not map each old line to the SAME line number and
// path at the owning commit (shifted or renamed coordinates would make the
// -U0 apply land at the wrong position in the ancestor's tree — with
// repeated text, silently).
func AbsorbPlan(env Env, s *State) (*AbsorbResult, error) {
	res, _, err := absorbPlan(env, s)
	if err != nil {
		return nil, err
	}
	if len(res.Absorbed) > 0 && len(res.Refused) == 0 {
		res.Notes = append(res.Notes, "once applied, `st undo` restores the branch refs and state — not the staged working-tree copies; the edits live on in the amended tip commit(s) reported on apply")
	}
	return res, nil
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
	res := &AbsorbResult{Absorbed: []absorbedHunk{}, Refused: []refusedHunk{}, DryRun: true}
	// Every staged section the parser could not classify as text hunks is a
	// refusal — the zero-refusal apply gate must cover the WHOLE staged diff,
	// because the apply replays the full patch, not just the hunks.
	for _, u := range unsupported {
		res.Refused = append(res.Refused, refusedHunk{File: u.File, Lines: "-", Reason: u.Reason + "; absorb handles plain text hunks only"})
	}
	if len(hunks) == 0 && len(res.Refused) == 0 {
		res.Summary = "nothing to absorb"
		return res, cur, nil
	}

	// Stack set = commits reachable from the current branch but not from the
	// trunk — one bounded `git rev-list trunk..cur` (CommitRange), not two
	// unbounded history walks subtracted afterward.
	stackSet, err := g.CommitRange(branchTipRef(s.Trunk), branchTipRef(cur))
	if err != nil {
		return nil, "", fmt.Errorf("walk %s..%s: %w", s.Trunk, cur, err)
	}

	tips, err := g.TipsFor(s.TipNames())
	if err != nil {
		return nil, "", fmt.Errorf("read branch tips: %w", err)
	}
	// The owning commit maps to a branch only when it is the tip of a tracked
	// branch ON THE CURRENT STACK'S PATH (cur plus its ancestors): absorb never
	// writes into non-tip stack commits, and never into a different stack — a
	// tracked branch off the path can share the owning SHA (a side stack, a
	// sibling pointing at a mid-stack commit), but amending ITS tip would land
	// the hunk in the other stack while this worktree's staged copy is still
	// consumed. tipToBranchAll keeps the best tracked candidate per tip so the
	// refusal can name the off-path owner. When several branches share one
	// tip, the LOWEST on-path one wins — its restack covers every deeper
	// sharer — with the name as the deterministic tie-break, so map iteration
	// order never decides attribution.
	onPath := map[string]bool{cur: true}
	for _, name := range s.Ancestors(cur) {
		onPath[name] = true
	}
	tipToBranch := make(map[string]string, len(tips))
	tipToBranchAll := make(map[string]string, len(tips))
	for name, tip := range tips {
		if !s.IsTracked(name) {
			continue
		}
		if lowerTipBranch(s, name, tipToBranchAll[tip]) {
			tipToBranchAll[tip] = name
		}
		if onPath[name] && lowerTipBranch(s, name, tipToBranch[tip]) {
			tipToBranch[tip] = name
		}
	}

	// Blame is memoized per FILE for the life of one plan: every hunk in the
	// same file shares a single `git blame --porcelain` spawn because the
	// answer cannot change while the plan runs — the staged diff, HEAD, and
	// the index are frozen reads here (the apply half of st absorb runs under
	// the advisory lock, and this pass performs no mutation). Keyed on file
	// alone only because every lookup is at HEAD; if a future caller blames
	// other revs or sub-ranges, key on (file, rev, range).
	blameByFile := map[string]map[int]git.BlameLine{}
	for _, h := range hunks {
		lines := hunkLines(h)
		if h.OldN == 0 {
			// The design spike's prototype showed nearest-context attribution
			// of a pure addition silently targets the trunk; refuse it.
			res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: "pure addition; use st modify"})
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
		// Beyond ownership, every old line's provenance must carry IDENTITY
		// coordinates: the apply lands a -U0 patch on the owning commit's
		// tree at the HEAD line numbers, so a line whose OriginalLine or
		// path differs — shifted by a descendant's insert/delete, renamed
		// since the owner, or missing/malformed metadata — would apply at
		// the wrong spot (repeated text makes a wrong-position apply succeed
		// silently). Identity is also what makes a hunk's coordinates
		// contiguous: OriginalLine == final line for every line leaves no
		// room for gaps.
		owners := map[string]bool{}
		missing := false
		malformed := false
		renamed := ""
		var shiftFrom, shiftTo int
		outside := false
		for line := h.OldStart; line <= h.OldStart+h.OldN-1; line++ {
			bl, ok := blame[line]
			if !ok {
				missing = true
				break
			}
			switch {
			case bl.Path == "" || bl.FinalLine != line:
				malformed = true
			case !stackSet[bl.Commit]:
				outside = true
			case bl.Path != h.File:
				renamed = bl.Path
			case bl.OriginalLine != line:
				shiftFrom, shiftTo = bl.OriginalLine, line
			default:
				owners[bl.Commit] = true
				continue
			}
			break
		}
		switch {
		case missing:
			res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: "cannot attribute (untracked, renamed, or binary file)"})
		case malformed:
			res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: "cannot attribute (blame metadata missing or malformed)"})
		case outside:
			res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: "touches lines owned by trunk or history below the stack"})
		case renamed != "":
			res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: fmt.Sprintf("line's path at the owning commit was %q (the file was renamed since); absorb refuses historical paths", renamed)})
		case shiftTo != 0:
			res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: fmt.Sprintf("line %d was line %d at the owning commit (a descendant shifted it); absorb refuses shifted coordinates", shiftTo, shiftFrom)})
		case len(owners) > 1:
			names := make([]string, 0, len(owners))
			for sha := range owners {
				names = append(names, shortSHA(sha))
			}
			sort.Strings(names)
			res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: fmt.Sprintf("spans %d stack commits (%s)", len(names), joinComma(names))})
		default:
			var target string
			for sha := range owners {
				target = sha
			}
			branch, isTip := tipToBranch[target]
			if !isTip {
				reason := "target is not a branch tip; squash the branch or absorb manually"
				if offPath, ok := tipToBranchAll[target]; ok {
					reason = fmt.Sprintf("line is owned by a commit that tips %q, which is not on the current stack's path", offPath)
				}
				res.Refused = append(res.Refused, refusedHunk{File: h.File, Lines: lines, Reason: reason})
				continue
			}
			res.Absorbed = append(res.Absorbed, absorbedHunk{File: h.File, Lines: lines, Branch: branch, Commit: target, hunk: h})
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
// targets lie on the current branch's ancestor path (attribution is
// restricted to it), so the lowest target's upstack covers every other
// target. Any refusal, or a dirty owner worktree for any target, returns the
// plan as data, unapplied — never an error; one undo entry reverts all
// amends plus the cascade.
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
	// apply would fracture the one-undo-entry story). cur needs no probe —
	// it is checked out here by definition.
	var foreignTargets []string
	for _, target := range targets {
		if target != cur {
			foreignTargets = append(foreignTargets, target)
		}
	}
	ownerDirs := map[string]string{}
	if len(foreignTargets) > 0 {
		// ONE worktree snapshot answers every target's ownership check:
		// ownership cannot change mid-apply in a way a single pre-flight must
		// react to (the advisory lock serializes st mutations, and a racing
		// `git worktree add` behind the lock races a per-target read just the
		// same) — ownerElsewhere would spawn `git worktree list` per target
		// for the same answer. IsCleanIn stays per-target below: each owner
		// dir is a DIFFERENT worktree whose dirtiness cannot be shared.
		wts, err := g.Worktrees()
		if err != nil {
			return nil, err
		}
		var ownerPaths []string
		owners := map[string]git.Worktree{}
		for _, target := range foreignTargets {
			owner, elsewhere := ownerElsewhereFrom(wts, target, cur)
			if !elsewhere {
				continue
			}
			ownerPaths = append(ownerPaths, owner.Path)
			owners[target] = owner
		}
		// One parallel batch answers every target's cleanliness check instead
		// of a serial `git -C` spawn per foreign owner.
		verdicts := probeWorktrees(g, ownerPaths, false)
		for _, target := range foreignTargets {
			owner, ok := owners[target]
			if !ok {
				continue
			}
			v := verdicts[owner.Path]
			if v.cleanErr != nil {
				return nil, fmt.Errorf("checking worktree %s: %w", owner.Path, v.cleanErr)
			}
			if !v.clean {
				plan.Summary = "not applied: a target's worktree is dirty; " + plan.Summary
				plan.Notes = append(plan.Notes, fmt.Sprintf("branch %q is checked out in %s with uncommitted changes; commit or stash there first", target, owner.Path))
				return plan, nil
			}
			ownerDirs[target] = owner.Path
		}
	}

	// Amend ancestors first (deterministic; the amends are independent — each
	// reads only its own tip tree, and targets' hunks are line-disjoint by the
	// multi-owner refusal). The staged diff itself is captured once for all
	// targets, each lands a DIFFERENT hunk set on a DIFFERENT ref, and the
	// side-effect-free builds (temp index + write-tree + commit-tree — the
	// temp-index apply is the pre-flight check) fan out while the ref moves
	// and their recovery checkpoints stay serial in landed order: checkpoint
	// k still names only amends 1..k, and a CAS land failure leaves amends
	// 1..k-1 persisted for the undo entry to revert.
	hunksByTarget := map[string][]git.Hunk{}
	for _, a := range plan.Absorbed {
		hunksByTarget[a.Branch] = append(hunksByTarget[a.Branch], a.hunk)
	}
	patches, err := g.DiffCachedPatchesFor(hunksByTarget)
	if err != nil {
		return nil, fmt.Errorf("assembling staged patches: %w", err)
	}
	type builtTip struct{ newTip, oldTip string }
	built := make([]builtTip, len(targets))
	if err := git.ParallelProbes(len(targets), func(i int) error {
		target := targets[i]
		newTip, oldTip, err := g.BuildAmendedTip(target, patches[target])
		if err != nil {
			return fmt.Errorf("absorb into %q: %w", target, err)
		}
		built[i] = builtTip{newTip: newTip, oldTip: oldTip}
		return nil
	}); err != nil {
		// A build failure lands nothing — a stronger nothing-mutated
		// boundary than the serial interleave had (there, earlier targets
		// could already have landed when a later build failed).
		return nil, err
	}
	newTips := make(map[string]string, len(targets))
	for i, target := range targets {
		if err := g.LandAmendedTip(target, built[i].oldTip, built[i].newTip); err != nil {
			return nil, fmt.Errorf("absorb into %q: %w", target, err)
		}
		newTips[target] = built[i].newTip
		// Checkpoint the cumulative recovery map BEFORE the next land or any
		// reset below: a later failure — or an abort→undo — can still name the
		// commits now holding the staged edits. The map is cloned because the
		// callback may retain it; later lands must not rewrite an earlier
		// snapshot. A checkpoint failure stops the op here, and the error
		// still reports the SHAs that landed so they are recoverable even
		// when the journal write was not.
		if err := env.absorbCheckpoint(maps.Clone(newTips)); err != nil {
			return nil, fmt.Errorf("recording absorb recovery commits: %w; %s", err, absorbedCommitsNote(newTips))
		}
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
	rebased, err := s.restackUpstack(env, lowest)
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
	if len(plan.Absorbed) > 0 {
		commits := map[string]string{}
		for _, a := range plan.Absorbed {
			commits[a.Branch] = a.Commit
		}
		// Refresh the durable recovery map with the post-cascade tips before
		// the epilogue save and HEAD restore: the cascade rewrote every
		// target above the lowest, so the amend-time checkpoint is stale
		// for them. A failed refresh keeps the earlier journal annotation —
		// the error still reports the newer known commits.
		if err := env.absorbCheckpoint(commits); err != nil {
			return nil, fmt.Errorf("recording absorb recovery commits: %w; %s", err, absorbedCommitsNote(commits))
		}
		plan.Notes = append(plan.Notes, absorbedCommitsNote(commits))
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

// lowerTipBranch reports whether name is a better absorb target for a shared
// tip than the current pick ("" = none yet): the branch nearer the trunk wins —
// its upstack restack covers every deeper sharer, so amending it keeps the
// whole run consistent — with the branch name as the deterministic tie-break.
func lowerTipBranch(s *State, name, current string) bool {
	if current == "" {
		return true
	}
	if dn, dc := len(s.Ancestors(name)), len(s.Ancestors(current)); dn != dc {
		return dn < dc
	}
	return name < current
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
	return strings.Join(parts, ", ")
}

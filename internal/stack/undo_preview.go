package stack

import "github.com/andyrewlee/stacked/internal/git"

// --- undo preview (st undo --dry-run) ---------------------------------------
//
// UndoPreview renders the same computation Undo executes — planUndo
// (undo_plan.go) is the single source for gate ordering, created-resource
// discovery, worktree verdicts, and checkout prediction, so the preview
// cannot drift from the apply it mirrors. It calls ONLY read methods of the
// Git port; the undo_op_test decorator fails on any mutating call.

// zeroSHA stands in for a missing live tip in a preview's From field: the ref
// undo would restore does not exist, so there is no tip to read. (The same
// all-zeros object id git uses for a deleted ref.)

const zeroSHA = "0000000000000000000000000000000000000000"

// UndoRestorePreview is one WouldRestore entry: the ref move a real undo makes
// for Branch (live tip From → recorded To). CommitsLostFromRef is the count of
// commits reachable from From but not To (per-ref only — other refs may still
// reach them); it is the string "unknown" when either object is missing.
type UndoRestorePreview struct {
	Branch             string `json:"branch"`
	From               string `json:"from"`
	To                 string `json:"to"`
	CommitsLostFromRef any    `json:"commitsLostFromRef"`
}

// UndoDeletePreview is one WouldDelete entry: a branch the undone command
// created. Worktree is the byte-exact path of the live worktree owning it when
// one exists — discovered by the shared undo plan, even when the journal
// recorded none. WorktreeDirty reports that owner's observed cleanliness;
// IsCurrentWorktree marks the branch this process's worktree has checked out
// (deleting it deletes the caller's cwd).
type UndoDeletePreview struct {
	Branch            string `json:"branch"`
	Worktree          string `json:"worktree,omitempty"`
	WorktreeDirty     bool   `json:"worktreeDirty"`
	IsCurrentWorktree bool   `json:"isCurrentWorktree"`
}

// undoPreviewObserved reports what the preview read, so consumers can see
// staleness instead of trusting a reservation the preview never made.
type undoPreviewObserved struct {
	EntryIndex int                `json:"entryIndex"`
	Tips       map[string]*string `json:"tips"`
}

// UndoPreviewResult is the `st undo --dry-run` payload. Blockers lists — in
// the real undo's gate order — everything a real run would refuse on; it is
// always non-nil and empty means "would proceed". Everything else is intent:
// WouldRestore/WouldDelete are sorted by branch. Notes carries advisory
// warnings a real undo would also print (e.g. the commits an undone absorb
// leaves unreachable) — advisory, never a refusal.
//
// WouldCheckout is the branch a real undo ends on when everything goes to
// plan — it is nil when the recorded branch would not exist after the undo
// (e.g. the entry created it) or when WouldDetach is set. WouldDetach marks
// the doomed-current-branch path whose intermediate checkout a real undo
// predicts will fail (target already checked out in another worktree, or
// local changes in the caller's own worktree): the real op parks HEAD
// detached and skips the recorded-branch restore rather than refusing.
type UndoPreviewResult struct {
	DryRun        bool                 `json:"dryRun"`
	Label         string               `json:"label,omitempty"`
	WouldRestore  []UndoRestorePreview `json:"wouldRestore,omitempty"`
	WouldDelete   []UndoDeletePreview  `json:"wouldDelete,omitempty"`
	WouldCheckout *string              `json:"wouldCheckout"`
	WouldDetach   bool                 `json:"wouldDetach,omitempty"`
	JournalDrop   bool                 `json:"journalDrop,omitempty"`
	Observed      *undoPreviewObserved `json:"observed,omitempty"`
	Blockers      []string             `json:"blockers"`
	Notes         []string             `json:"notes,omitempty"`
}

// UndoPreview computes what undoing entry would do — the same gates as Undo,
// read-only: no ref moves, no checkouts, no worktree removals, no writes. s is
// the currently-loaded state (nil when it could not be decoded, mirroring
// Undo's degrade path); canTeleport is whether the shell shim could move the
// caller out of a doomed worktree (without it, that case blocks a real undo).
// journalIndex is the entry's 1-based position in `st undo --list` numbering
// (1 = the entry a bare undo reverts); it is reported back in
// Observed.EntryIndex. probes optionally supplies the step-invariant readings
// a multi-step preview shares — pass nil to read them fresh.
//
// The plan's gates run in the real undo's order: a paused rebase, then the
// schema barriers (current state, then the snapshot — short circuits,
// nothing else computed), then the journal self-check, the drift/paused
// refusals, the cwd-inside-created-worktree case (cmd's
// prepareUndoCurrentCreatedWorktree), and per created branch the
// recorded-vs-live worktree mismatch and dirty-owner checks.
func UndoPreview(env Env, s *State, entry *UndoEntry, canTeleport bool, journalIndex int, probes *UndoProbes) (*UndoPreviewResult, error) {
	g := env.Git
	res := &UndoPreviewResult{
		DryRun:   true,
		Label:    entry.Label,
		Blockers: []string{},
	}

	p, err := planUndo(env, s, entry, false, canTeleport, journalIndex, probes)
	if err != nil {
		return nil, err
	}
	if p.short {
		res.Blockers = append(res.Blockers, p.fatalCode)
		return res, nil
	}
	res.JournalDrop = true
	tips := map[string]*string{}
	liveSet := p.liveSet

	// The preflight refusals expand to per-ref blocker rows in plan order.
	for _, r := range p.refusals {
		res.Blockers = append(res.Blockers, r.codes...)
	}
	if p.tele != "" {
		res.Blockers = append(res.Blockers, "cwd_inside_created_worktree:"+p.tele)
	}
	if p.noPostRef {
		res.Notes = append(res.Notes, "the journal entry has no post-operation tips on record; branch refs would restore unconditionally")
	}

	// WouldDelete rows plus their per-branch blockers, in the plan's sorted
	// order — the order a real undo would process them.
	for _, d := range p.doomed {
		res.WouldDelete = append(res.WouldDelete, UndoDeletePreview{
			Branch:            d.name,
			Worktree:          d.owner,
			WorktreeDirty:     d.dirty,
			IsCurrentWorktree: d.isCurrent,
		})
		if d.mismatch {
			res.Blockers = append(res.Blockers, "recorded_worktree_mismatch:"+d.name)
		}
		if d.dirty {
			res.Blockers = append(res.Blockers, "worktree_dirty:"+d.name)
		}
		if tip, ok := liveSet.tip(g, d.name); ok {
			t := tip
			tips[d.name] = &t
		} else {
			tips[d.name] = nil
		}
		if d.isCurrent {
			if !d.targetLive && !d.targetRec {
				res.Blockers = append(res.Blockers, "missing_restore_target:"+d.name)
			} else if d.detachPred {
				res.WouldDetach = true
			}
		}
	}

	// WouldRestore: every recorded ref, sorted by branch. `commitsLostFromRef`
	// is rev-list to..from — the commits that stop being reachable from THAT
	// ref; a missing live ref or recorded object degrades it to "unknown"
	// (never a blocker — undo restores the ref by name). The rev-list spawns
	// are independent per ref, so they fan out in one bounded batch — each
	// slot holds the count or the "unknown" degrade the serial loop assigned.
	type restoreProbe struct {
		live    string
		liveOK  bool
		lostCnt int // -1 = "unknown" (missing live ref, missing object, or rev-list error)
	}
	slots := make([]restoreProbe, len(p.restores))
	_ = git.ParallelProbes(len(p.restores), func(i int) error {
		name := p.restores[i]
		var pr restoreProbe
		pr.live, pr.liveOK = liveSet.tip(g, name)
		pr.lostCnt = -1
		if pr.liveOK {
			if lost, err := g.CommitRange(entry.Refs[name], pr.live); err == nil {
				pr.lostCnt = len(lost)
			}
		}
		slots[i] = pr
		return nil
	})
	for i, name := range p.restores {
		pr := slots[i]
		r := UndoRestorePreview{Branch: name, To: entry.Refs[name]}
		if !pr.liveOK {
			r.From = zeroSHA
			r.CommitsLostFromRef = "unknown"
			tips[name] = nil
		} else {
			r.From = pr.live
			t := pr.live
			tips[name] = &t
			if pr.lostCnt < 0 {
				r.CommitsLostFromRef = "unknown"
			} else {
				r.CommitsLostFromRef = pr.lostCnt
			}
		}
		res.WouldRestore = append(res.WouldRestore, r)
	}

	res.WouldCheckout = p.checkout
	// The same advisory a real undo prints for absorb entries — name the
	// amended commits before the user orphaning them has to be discovered.
	if len(entry.AbsorbedCommits) > 0 {
		res.Notes = append(res.Notes, absorbedCommitsNote(entry.AbsorbedCommits))
	}
	res.Observed = &undoPreviewObserved{EntryIndex: journalIndex, Tips: tips}
	return res, nil
}

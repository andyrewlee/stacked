package stack

import (
	"errors"
	"fmt"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
)

// --- undo preview (st undo --dry-run) ---------------------------------------
//
// TWIN SITE: UndoPreview models Undo (undo_op.go) and must change with it —
// a new created-resource kind, gate, or schema barrier lands in both files in
// the same commit, or the preview lies. It calls ONLY read methods of the Git
// port; the undo_op_test decorator fails on any mutating call.

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
// one exists — discovered like removeCreatedWorktree, even when the journal
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
type UndoPreviewResult struct {
	DryRun        bool                 `json:"dryRun"`
	Label         string               `json:"label,omitempty"`
	WouldRestore  []UndoRestorePreview `json:"wouldRestore,omitempty"`
	WouldDelete   []UndoDeletePreview  `json:"wouldDelete,omitempty"`
	WouldCheckout *string              `json:"wouldCheckout"`
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
//
// Gate order mirrors runUndo + Undo: a paused rebase, then the schema
// barriers (current state, then the snapshot via ValidateUndoState — short
// circuits, nothing else computed), then the cwd-inside-created-worktree case
// (cmd's prepareUndoCurrentCreatedWorktree), then per created branch the
// recorded-vs-live worktree mismatch and dirty-owner checks.
func UndoPreview(env Env, s *State, entry *UndoEntry, canTeleport bool) (*UndoPreviewResult, error) {
	g := env.Git
	res := &UndoPreviewResult{
		DryRun:   true,
		Label:    entry.Label,
		Blockers: []string{},
	}

	if inProgress, err := g.RebaseInProgress(); err == nil && inProgress {
		res.Blockers = append(res.Blockers, "rebase_in_progress")
		return res, nil
	}
	if s != nil && s.Version > stateSchemaVersion {
		res.Blockers = append(res.Blockers, "state_too_new")
		return res, nil
	}
	if err := ValidateUndoState(entry.State); err != nil {
		if errors.Is(err, ErrStateTooNew) {
			res.Blockers = append(res.Blockers, "state_too_new")
		} else {
			res.Blockers = append(res.Blockers, "malformed_snapshot")
		}
		return res, nil
	}

	res.JournalDrop = true
	tips := map[string]*string{}

	// Branches the undone command created — the same discovery Undo performs:
	// current-state branches ∪ the recorded CreatedBranches, minus the
	// captured local-branch list, that still exist.
	var created []string
	if entry.LocalBranches != nil {
		candidates := map[string]bool{}
		if s != nil {
			candidates[s.Trunk] = true
			for name := range s.Branches {
				candidates[name] = true
			}
		}
		for _, name := range entry.CreatedBranches {
			candidates[name] = true
		}
		for name := range candidates {
			if entry.CreatesBranch(name) && g.BranchExists(name) {
				created = append(created, name)
			}
		}
		sort.Strings(created)
	}

	cur, _ := g.CurrentBranch() // detached HEAD → "" → protects nothing

	var wts []git.Worktree
	if len(created) > 0 || len(entry.CreatedWorktrees) > 0 {
		var err error
		if wts, err = g.Worktrees(); err != nil {
			return nil, fmt.Errorf("listing worktrees for undo preview: %w", err)
		}
	}

	// prepareUndoCurrentCreatedWorktree's gate: the current branch's worktree
	// is doomed by the undone op — removing it deletes the caller's cwd, so a
	// real undo needs the shell shim to teleport out first. The doom set is the
	// recorded CreatedWorktrees PLUS any discovered created branch whose live
	// worktree Undo removes (the journal only records worktrees that existed
	// when the command ran — one materialized later is still doomed).
	if entry.CreatedWorktrees[cur] != "" || (entry.DoomedBranch(s, cur) && g.BranchExists(cur)) {
		if _, ok := LinkedOwnerOf(wts, cur); ok && !canTeleport {
			res.Blockers = append(res.Blockers, "cwd_inside_created_worktree:"+cur)
		}
	}

	for _, name := range created {
		d := UndoDeletePreview{Branch: name}
		if owner, ok := LinkedOwnerOf(wts, name); ok {
			d.Worktree = owner.Path
			if recorded := entry.CreatedWorktrees[name]; recorded != "" && !sameWorktreePath(owner.Path, recorded) {
				res.Blockers = append(res.Blockers, "recorded_worktree_mismatch:"+name)
			} else {
				clean, err := g.IsCleanIn(owner.Path)
				if err != nil {
					return nil, fmt.Errorf("checking worktree %q for %q: %w", owner.Path, name, err)
				}
				d.WorktreeDirty = !clean
				if !clean {
					res.Blockers = append(res.Blockers, "worktree_dirty:"+name)
				}
			}
			if name == cur {
				d.IsCurrentWorktree = true
			}
		}
		res.WouldDelete = append(res.WouldDelete, d)
		if tip, err := g.RevParse(branchTipRef(name)); err == nil {
			t := tip
			tips[name] = &t
		} else {
			tips[name] = nil
		}
	}

	// WouldRestore: every recorded ref, sorted by branch. `commitsLostFromRef`
	// is rev-list to..from — the commits that stop being reachable from THAT
	// ref; a missing live ref or recorded object degrades it to "unknown"
	// (never a blocker — undo restores the ref by name).
	names := make([]string, 0, len(entry.Refs))
	for name := range entry.Refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		to := entry.Refs[name]
		r := UndoRestorePreview{Branch: name, To: to}
		live, err := g.RevParse(branchTipRef(name))
		if err != nil {
			r.From = zeroSHA
			r.CommitsLostFromRef = "unknown"
			tips[name] = nil
		} else {
			r.From = live
			t := live
			tips[name] = &t
			lost, err := g.CommitRange(to, live)
			if err != nil {
				r.CommitsLostFromRef = "unknown"
			} else {
				r.CommitsLostFromRef = len(lost)
			}
		}
		res.WouldRestore = append(res.WouldRestore, r)
	}

	if entry.CurrentBranch != "" && g.BranchExists(entry.CurrentBranch) {
		cb := entry.CurrentBranch
		res.WouldCheckout = &cb
	}
	// The same advisory a real undo prints for absorb entries — name the
	// amended commits before the user orphaning them has to be discovered.
	if len(entry.AbsorbedCommits) > 0 {
		res.Notes = append(res.Notes, absorbedCommitsNote(entry.AbsorbedCommits))
	}
	res.Observed = &undoPreviewObserved{EntryIndex: 1, Tips: tips}
	return res, nil
}

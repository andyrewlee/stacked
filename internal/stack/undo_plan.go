package stack

import (
	"errors"
	"fmt"
	"sort"

	"github.com/andyrewlee/stacked/internal/git"
)

// This file is the single computation both undo consumers share:
// stack.Undo EXECUTES the plan (deletes doomed branches/worktrees, restores
// refs, saves the snapshot, lands HEAD) and stack.UndoPreview RENDERS it into
// the published `st undo --dry-run` payload. A new created-resource kind,
// gate, or schema barrier lands here ONCE — the preview can no longer drift
// from the apply it mirrors.
//
// planUndo is read-only over the Git port: it probes worktrees, tips, dirt,
// and cwd containment but never moves a ref or touches the tree. The purity
// test's mutator decorator pins that.

// UndoProbes carries the step-invariant git readings a sequence of undo
// plans shares: the rebase-in-progress flag, every live branch tip, and the
// checked-out branch. cmd's multi-step preview loop captures them once via
// ReadUndoProbes instead of re-probing per entry; a nil *UndoProbes makes
// planUndo read them itself (the apply path undoes one step per call, and a
// prior step's mutations make hoisted readings stale anyway).
type UndoProbes struct {
	rebaseInProgress bool
	live             liveBranches
	cur              string
}

// ReadUndoProbes snapshots the step-invariant readings. Probe failures degrade
// exactly the way the consumers' inline reads did: a failed RebaseInProgress
// reads as not-in-progress, a failed Tips degrades to per-branch probes, and a
// failed CurrentBranch reads as detached ("") — a preview must never fail on a
// flaky read.
func ReadUndoProbes(g Git) *UndoProbes {
	p := &UndoProbes{}
	if in, err := g.RebaseInProgress(); err == nil {
		p.rebaseInProgress = in
	}
	p.live = probeLiveBranches(g)
	p.cur, _ = g.CurrentBranch()
	return p
}

// undoRefusal is one pre-mutation refusal gate: err is what Undo returns when
// the gate fires; codes are the blocker rows UndoPreview expands into its
// Blockers list. One refusal may expand to several codes — the drift refusal
// is one error naming every diverged ref but one blocker row each.
type undoRefusal struct {
	codes []string
	err   error
}

// undoDoomed is a branch the undone command created — Undo deletes it,
// UndoPreview lists it under WouldDelete. The worktree facts are what the
// plan's owner discovery observed: Owner is the byte-exact path of the live
// linked worktree owning the branch ("" when none), Dirty its observed
// cleanliness, Mismatch that the journal recorded a different worktree path.
// The remaining fields only resolve when IsCurrent — HEAD sits on the doomed
// branch, so the apply must move it before deleting.
type undoDoomed struct {
	name      string
	owner     string
	dirty     bool
	mismatch  bool
	isCurrent bool
	// Checkout-target resolution for the current-doomed case, computed against
	// the pre-undo live set (the preview's view): the snapshot's trunk, or the
	// branch's recorded parent while that parent is live.
	target     string
	targetLive bool
	targetRec  bool // target appears in entry.Refs — resurrectable
	// detachPred predicts the parked-HEAD outcome: the intermediate checkout
	// is expected to be blocked (target owned by a linked worktree, or local
	// changes in the caller's worktree) so HEAD detaches instead.
	detachPred bool
}

// undoPlan is everything one undo step will do, computed read-only. The gate
// fields come first: fatal is a structural refusal Undo returns and short
// marks that nothing past the gate was computed (UndoPreview emits fatalCode
// and stops). refusals are the pre-mutation refusal gates — Undo returns the
// first error, UndoPreview expands every code. The rest is intent.
type undoPlan struct {
	fatal     error
	fatalCode string
	short     bool
	refusals  []undoRefusal

	// tele is the doomed current branch whose live worktree holds the
	// process's cwd when the caller cannot teleport out — a preview blocker
	// row (cwd_inside_created_worktree:<name>); "" when not applicable.
	tele string

	doomed    []undoDoomed // sorted by name
	restores  []string     // sorted entry.Refs keys
	diverged  []string     // external-drift names (refusal text + force note)
	detach    bool         // any doomed-current detach prediction
	checkout  *string      // predicted final landing branch, nil when none
	noPostRef bool         // entry carries no PostRefs (unconditional restore)
	cas       bool         // the ref restore is a compare-and-swap

	// facts the consumers reuse rather than re-derive
	prev    *State
	liveSet liveBranches
	cur     string
	wts     []git.Worktree
}

// short marks the plan as stopped at a structural gate: fatal is Undo's
// returned error, code is UndoPreview's single blocker row.
func (p *undoPlan) stop(fatal error, code string) *undoPlan {
	p.fatal = fatal
	p.fatalCode = code
	p.short = true
	return p
}

func refusalCodes(prefix string, names []string) []string {
	codes := make([]string, 0, len(names))
	for _, n := range names {
		codes = append(codes, prefix+n)
	}
	return codes
}

// planUndo computes the one computation both undo consumers share. s is the
// currently-loaded state (nil when it could not be decoded). force downgrades
// the drift and paused-rebase refusals (only Undo passes it — a preview never
// moves refs, so --force previews are refused at the CLI). canTeleport tells
// the current-worktree doom check whether the shell shim could move the caller
// out (apply-path callers pass true: cmd already teleported, so the case is
// unreachable). journalIndex is the entry's 1-based position in `st undo
// --list` numbering — drift/paused preflights only run for the newest entry
// (index 1): deeper steps would compare against a world an earlier undo step
// already changed, reporting ghosts rather than real blockers. probes, when
// non-nil, supplies the step-invariant readings; nil means read them fresh.
func planUndo(env Env, s *State, entry *UndoEntry, force, canTeleport bool, journalIndex int, probes *UndoProbes) (*undoPlan, error) {
	g := env.Git
	if probes == nil {
		probes = ReadUndoProbes(g)
	}
	p := &undoPlan{
		liveSet:   probes.live,
		cur:       probes.cur,
		restores:  sortedRefNames(entry.Refs),
		noPostRef: entry.PostRefs == nil,
		cas:       entry.PostRefs != nil && !force,
	}

	// Structural gates, in the order a real run enforces them — rebase, then
	// the schema barriers (current state, then the snapshot), then the journal
	// self-check. Each short-circuits: nothing past the gate is computed, and
	// both consumers surface it (Undo returns fatal; the preview emits
	// fatalCode and renders nothing else).
	if probes.rebaseInProgress {
		return p.stop(fmt.Errorf("cannot undo while a rebase is in progress; run st abort or resolve conflicts and run st continue"), "rebase_in_progress"), nil
	}
	if s != nil && s.Version > stateSchemaVersion {
		return p.stop(fmt.Errorf("current state: %w (schema v%d; this st understands v%d) — upgrade st or check for a downgrade", ErrStateTooNew, s.Version, stateSchemaVersion), "state_too_new"), nil
	}
	prev, err := decodeState(entry.State)
	if err != nil {
		code := "malformed_snapshot"
		if errors.Is(err, ErrStateTooNew) {
			code = "state_too_new"
		}
		return p.stop(fmt.Errorf("parsing undo state: %w", err), code), nil
	}
	p.prev = prev
	if err := validateUndoEntry(entry); err != nil {
		return p.stop(err, "malformed_journal"), nil
	}

	// The newest-entry preflights: external drift first (the CAS expectation),
	// then the paused-rebase owners. Both produce refusal rows — one apply-side
	// error plus the preview codes it expands to.
	if journalIndex == 1 {
		p.diverged = undoExternalDrift(p.liveSet, g, s, entry)
		if len(p.diverged) > 0 && !force {
			p.refusals = append(p.refusals, undoRefusal{
				codes: refusalCodes("ref_moved_since:", p.diverged),
				err: fmt.Errorf("cannot undo %q: %s moved outside st since the command ran — refusing to overwrite %s; run `st undo --force` to restore anyway, or inspect first with `st undo --dry-run`",
					entry.Label, joinBranchList(p.diverged), pluralRefs(p.diverged)),
			})
		}
		if !force {
			paused, err := undoPausedRebases(g, s, p.liveSet, entry)
			if err != nil {
				return nil, err
			}
			if len(paused) > 0 {
				p.refusals = append(p.refusals, undoRefusal{
					codes: refusalCodes("paused_rebase:", paused),
					err: fmt.Errorf("cannot undo %q: %s %s a rebase in progress in a linked worktree; resolve it there (`st continue` or `st abort`) first — or run `st undo --force`",
						entry.Label, joinBranchList(paused), pluralHave(paused)),
				})
			}
		}
	}

	// Created-branch discovery: the candidates are every branch the current
	// state knows about plus the ones the entry recorded as created; a
	// candidate is doomed when the entry created it and it still lives.
	createdSet := map[string]bool{}
	if entry.LocalBranches != nil {
		for name := range createdBranchCandidates(s, entry) {
			if entry.CreatesBranch(name) && p.liveSet.exists(g, name) {
				createdSet[name] = true
				p.doomed = append(p.doomed, undoDoomed{name: name, isCurrent: name == p.cur})
			}
		}
		sort.Slice(p.doomed, func(i, j int) bool { return p.doomed[i].name < p.doomed[j].name })
	}

	// One worktree listing feeds the teleport gate and every per-doomed owner
	// fact. It is only fetched when something could own it — a doomed branch
	// or a recorded created-worktree.
	if len(p.doomed) > 0 || len(entry.CreatedWorktrees) > 0 {
		if p.wts, err = g.Worktrees(); err != nil {
			return nil, fmt.Errorf("listing worktrees: %w", err)
		}
	}

	// prepareUndoCreatedWorktree's gate, expressed as a blocker: the current
	// branch's worktree is doomed (recorded in the journal, or discovered —
	// the journal only records worktrees that existed when the command ran),
	// removing it deletes the caller's cwd, and without a teleport target a
	// real undo cannot proceed from here.
	if entry.CreatedWorktrees[p.cur] != "" || (entry.DoomedBranch(s, p.cur) && p.liveSet.exists(g, p.cur)) {
		if _, ok := LinkedOwnerOf(p.wts, p.cur); ok && !canTeleport {
			p.tele = p.cur
		}
	}

	// Per-doomed facts, in sorted order — the order Undo executes them and
	// UndoPreview emits their blockers. The owning worktrees' cleanliness is
	// probed in one parallel batch (a mismatched owner is never probed, same
	// as the serial version).
	var cleanPaths []string
	for i := range p.doomed {
		if owner, ok := LinkedOwnerOf(p.wts, p.doomed[i].name); ok {
			if recorded := entry.CreatedWorktrees[p.doomed[i].name]; recorded == "" || sameWorktreePath(owner.Path, recorded) {
				cleanPaths = append(cleanPaths, owner.Path)
			}
		}
	}
	verdicts := probeWorktrees(g, cleanPaths, false)
	for i := range p.doomed {
		d := &p.doomed[i]
		if owner, ok := LinkedOwnerOf(p.wts, d.name); ok {
			d.owner = owner.Path
			if recorded := entry.CreatedWorktrees[d.name]; recorded != "" && !sameWorktreePath(owner.Path, recorded) {
				d.mismatch = true
			} else {
				v := verdicts[owner.Path]
				if v.cleanErr != nil {
					return nil, fmt.Errorf("checking worktree %q for %q: %w", owner.Path, d.name, v.cleanErr)
				}
				d.dirty = !v.clean
			}
		}
		if d.isCurrent {
			// HEAD sits on the doomed branch: resolve the intermediate
			// checkout target against the pre-undo live set — the snapshot's
			// trunk, or the branch's recorded parent while that still lives.
			d.target = prev.Trunk
			if s != nil {
				if b, ok := s.Get(d.name); ok && p.liveSet.exists(g, b.Parent) {
					d.target = b.Parent
				}
			}
			d.targetLive = p.liveSet.exists(g, d.target)
			_, d.targetRec = entry.Refs[d.target]
			if d.targetLive || d.targetRec {
				// The intermediate checkout is predicted blocked — the
				// target checked out in a linked worktree, or local changes
				// in the caller's worktree (a dirty LINKED owner of cur
				// already refused above, so the local-changes case applies
				// only when cur is not linked-owned) — and HEAD parks
				// detached while the branch is deleted.
				if _, owned := LinkedOwnerOf(p.wts, d.target); d.targetLive && owned {
					d.detachPred = true
				} else if _, curLinked := LinkedOwnerOf(p.wts, p.cur); !curLinked {
					if clean, err := g.IsClean(); err == nil && !clean {
						d.detachPred = true
					}
				}
			}
			if d.detachPred {
				p.detach = true
			}
		}
	}

	// The recorded final checkout only describes a run that would proceed: a
	// blocker means refusal (no landing to report), a doomed recorded branch
	// cannot be checked out (the undo itself deletes it), and a detach means
	// the real run parks HEAD instead.
	if !p.blocked() && entry.CurrentBranch != "" && !createdSet[entry.CurrentBranch] && !p.detach && p.liveSet.exists(g, entry.CurrentBranch) {
		cb := entry.CurrentBranch
		p.checkout = &cb
	}
	return p, nil
}

// blocked reports whether the plan carries any refusal-level verdict — the
// rows a preview renders as blockers and a real run refuses on.
func (p *undoPlan) blocked() bool {
	if p.short || p.tele != "" || len(p.refusals) > 0 {
		return true
	}
	for _, d := range p.doomed {
		if d.mismatch || d.dirty || (d.isCurrent && !d.targetLive && !d.targetRec) {
			return true
		}
	}
	return false
}

func sortedRefNames(refs map[string]string) []string {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/andyrewlee/stacked/internal/git"
	"github.com/andyrewlee/stacked/internal/stack"
)

func init() {
	register(&Command{
		Name:       "undo",
		Summary:    "Undo the last stack-mutating command",
		Usage:      "st undo [<n>] [--list | --dry-run] [--force] [--json]",
		Run:        runUndo,
		NewFlagSet: undoFlagSet,
	})
}

// runUndo reverts the most recent mutating command by handing its recorded
// snapshot to the engine (stack.Undo): the stack metadata is rolled back and
// every recorded branch is reset to its prior tip. It does not touch the
// working tree, so uncommitted changes are preserved. With --list it only
// prints the journal — a pure read, so it runs without the repo lock (like
// the other read commands) and is allowed while a rebase is in progress.
func runUndo(args []string) error {
	var o undoOpts
	fs := newUndoFlags(&o)
	if err := parseFlagSet(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return fmt.Errorf("undo takes at most one positional argument, got %q", rest[1])
	}
	n := 1
	if len(rest) == 1 {
		v, err := strconv.Atoi(rest[0])
		if err != nil || v < 1 {
			return fmt.Errorf("undo takes a positive step count, got %q", rest[0])
		}
		n = v
	}
	if o.list && o.dryRun {
		return fmt.Errorf("--list and --dry-run are mutually exclusive")
	}
	if o.force && o.dryRun {
		return fmt.Errorf("--force and --dry-run are mutually exclusive: a preview never moves refs")
	}
	if o.force && o.list {
		return fmt.Errorf("--force and --list are mutually exclusive: listing never moves refs")
	}
	if o.list {
		if len(rest) == 1 {
			return fmt.Errorf("--list takes no step count")
		}
		return runUndoList(o.asJSON)
	}
	if o.dryRun {
		return runUndoDryRun(o.asJSON, n)
	}
	return runUndoApply(o.asJSON, n, o.force)
}

// undoRunResult is the single-entry undo report
// ({undone,label,restored,notes}).
type undoRunResult struct {
	Undone   bool     `json:"undone"`
	Label    string   `json:"label"`
	Restored []string `json:"restored"`
	Notes    []string `json:"notes,omitempty"`
}

// undoStepRunResult is the multi-step undo report — emitted in full on
// success and partially by failUndoSteps mid-sequence, so the wire shape is
// declared once ({undone,count,restored,steps,notes}).
type undoStepRunResult struct {
	Undone   bool             `json:"undone"`
	Count    int              `json:"count"`
	Restored []string         `json:"restored"`
	Steps    []undoStepResult `json:"steps"`
	Notes    []string         `json:"notes,omitempty"`
}

// undoStepResult is one completed step of a multi-step undo: Index is the
// entry's position in `st undo --list` numbering as of when the command ran
// (1 = newest), Label the reverted op, and Restored the branches whose tips it
// moved back.
type undoStepResult struct {
	Index    int      `json:"index"`
	Label    string   `json:"label"`
	Restored []string `json:"restored"`
	Notes    []string `json:"notes,omitempty"`
}

// runUndoApply reverts the newest n journal entries, newest-first — the order
// that makes each step's restore land on the state the next-older entry
// expected. Each step runs Undo + DropUndo inside the one lock acquisition,
// and only a successful step drops its entry — a mid-sequence failure leaves
// the failed entry in the journal for retry (the ref restore itself is
// atomic, but stack.Undo's earlier cleanup may already have run; see its
// failure-boundary comment). The already-undone prefix is reported like
// worktree rm --all's partial-progress contract.
func runUndoApply(asJSON bool, n int, force bool) error {
	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	if inProgress, err := git.RebaseInProgress(); err != nil {
		return err
	} else if inProgress {
		return errors.New(stack.UndoRebaseGateMsg)
	}

	entries, err := stack.ListUndo()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return emit(asJSON, struct {
			Undone bool `json:"undone"`
		}{false}, func() { out("nothing to undo\n") })
	}
	if n > len(entries) {
		return fmt.Errorf("cannot undo %d steps: only %d undoable operation(s) recorded", n, len(entries))
	}

	// The current state informs which branches the undone command created; when
	// it cannot be loaded the engine still reverts from the snapshot alone, and
	// the snapshot bytes are persisted directly. One load failure is fatal,
	// though: ErrStateTooNew means a newer st wrote the file and undo would
	// discard fields it cannot interpret — refuse before anything mutates.
	s, loadErr := stack.Load()
	if loadErr != nil {
		if errors.Is(loadErr, stack.ErrStateTooNew) {
			return loadErr
		}
		s = nil
	}

	// Schema barrier for EVERY snapshot in range, ahead of worktree
	// preparation (which may os.Chdir out of a to-be-deleted worktree) and
	// every engine mutation: a journal entry written by a newer st — or a
	// malformed one — refuses here, before step 1 touches state, journal,
	// refs, or cwd.
	for i := 0; i < n; i++ {
		e := &entries[len(entries)-1-i]
		if err := stack.ValidateUndoState(e.State); err != nil {
			return fmt.Errorf("step %d of %d: parsing undo state: %w", i+1, n, err)
		}
	}
	cdAfterSuccess, err := prepareUndoCreatedWorktrees(entries[len(entries)-n:], s)
	if err != nil {
		return err
	}

	var steps []undoStepResult
	restoredSet := map[string]bool{}
	var notes []string
	for i := 0; i < n; i++ {
		e := &entries[len(entries)-1-i]
		env := stack.Env{Git: newGitPort()}
		if s != nil {
			env.Save = s.Save
		} else {
			raw := e.State
			env.Save = func() error { return stack.RestoreState(raw) }
		}
		res, err := stack.Undo(env, s, e, force)
		if err != nil {
			return failUndoSteps(asJSON, n, steps, restoredSet, notes,
				fmt.Errorf("stopped at step %d of %d undoing %q: %w", i+1, n, e.Label, err))
		}
		if err := stack.DropUndo(); err != nil {
			return failUndoSteps(asJSON, n, steps, restoredSet, notes,
				fmt.Errorf("stopped at step %d of %d: dropping undo entry: %w", i+1, n, err))
		}
		step := undoStepResult{Index: i + 1, Label: e.Label, Restored: res.Restacked, Notes: res.Notes}
		if step.Restored == nil {
			step.Restored = []string{}
		}
		steps = append(steps, step)
		for _, r := range res.Restacked {
			restoredSet[r] = true
		}
		notes = append(notes, res.Notes...)
	}
	if cdAfterSuccess != "" {
		writeCDDirective(cdAfterSuccess)
	}

	restored := make([]string, 0, len(restoredSet))
	for name := range restoredSet {
		restored = append(restored, name)
	}
	sort.Strings(restored)
	if n == 1 {
		payload := undoRunResult{true, steps[0].Label, restored, steps[0].Notes}
		return emit(asJSON, payload, func() {
			out("undid: %s\n", sanitizeForTerminal(steps[0].Label))
			renderUndoTail(restored, notes)
		})
	}
	payload := undoStepRunResult{true, n, restored, steps, notes}
	return emit(asJSON, payload, func() {
		for _, st := range steps {
			out("undid: %s (step %d of %d)\n", sanitizeForTerminal(st.Label), st.Index, n)
		}
		renderUndoTail(restored, notes)
	})
}

// renderUndoTail prints the shared tail of an undo report: the branches whose
// tips moved back, advisory notes, and the working-tree disclaimer.
func renderUndoTail(restored, notes []string) {
	if len(restored) > 0 {
		out("restored branches: %s\n", joinTerminalNames(restored))
	}
	for _, n := range notes {
		out("note: %s\n", sanitizeForTerminal(n))
	}
	out("note: your working tree was not modified; run `git status` to review.\n")
}

// failUndoSteps reports a mid-sequence stop: the completed steps are emitted
// as the partial aggregate (the worktree rm --all precedent — JSON consumers
// see what already ran, text consumers see the same lines plus the error),
// then the wrapped cause carries "stopped at step k of n".
func failUndoSteps(asJSON bool, n int, steps []undoStepResult, restoredSet map[string]bool, notes []string, err error) error {
	restored := make([]string, 0, len(restoredSet))
	for name := range restoredSet {
		restored = append(restored, name)
	}
	sort.Strings(restored)
	payload := undoStepRunResult{len(steps) > 0, n, restored, steps, notes}
	if steps == nil {
		payload.Steps = []undoStepResult{}
	}
	_ = emit(asJSON, payload, func() {
		for _, st := range steps {
			out("undid: %s (step %d of %d)\n", sanitizeForTerminal(st.Label), st.Index, n)
		}
	})
	return err
}

// prepareUndoCreatedWorktrees hoists the current-worktree doom check over the
// whole undo range before ANY step mutates: the caller must leave a doomed
// worktree ahead of step 1 — a later step's worktree removal would otherwise
// delete the cwd mid-sequence. entries is the journal-ordered slice being
// undone (entries[len-n:] — last element is the newest); each entry's doom
// check runs against the state that step will actually see: the live state
// for the newest entry, and for every older entry the snapshot the step
// before it restores (DecodeUndoState of the next-newer entry).
func prepareUndoCreatedWorktrees(entries []stack.UndoEntry, live *stack.State) (string, error) {
	cur, err := currentBranch()
	if err != nil {
		return "", nil
	}
	doomed := false
	for i := len(entries) - 1; i >= 0; i-- {
		e := &entries[i]
		// The state entry i's undo sees: live for the newest entry; for an
		// older entry, the snapshot restored by undoing the next-newer one —
		// decode(entry[i+1].State). A malformed neighbor snapshot is not the
		// teleport's problem (the schema barrier refused it earlier), so
		// decode failure degrades to nil — DoomedBranch still honors the
		// recorded CreatedBranches.
		si := live
		if i+1 < len(entries) {
			var decErr error
			if si, decErr = stack.DecodeUndoState(entries[i+1].State); decErr != nil {
				si = nil
			}
		}
		// The doomed set is the journal's recorded created-worktrees PLUS any
		// branch Undo discovers was created by the entry — the journal only
		// records worktrees that existed when the command ran, so a worktree
		// materialized later (`st worktree <branch>` after `st create`) is
		// still removed by Undo and would take the caller's cwd with it.
		if undoEntryCreatedWorktree(e, cur) || (e.DoomedBranch(si, cur) && git.BranchExists(cur)) {
			doomed = true
			break
		}
	}
	if !doomed {
		return "", nil
	}
	wts, err := worktrees()
	if err != nil {
		return "", err
	}
	owner, ok := stack.LinkedOwnerOf(wts, cur)
	if !ok {
		return "", nil
	}
	main, ok := stack.MainWorktree(wts)
	if !ok || main.Path == "" {
		return "", fmt.Errorf("cannot undo creation of current worktree branch %q: main worktree not found", cur)
	}
	dest := main.Path
	// The final landing branch is the OLDEST undone entry's recorded checkout
	// (entries[0] in this journal-ordered slice): after the last step runs,
	// that is the branch a real undo restores — prefer its worktree for the
	// teleport destination, as the single-step path does.
	if cb := entries[0].CurrentBranch; cb != "" {
		if wt, ok := stack.LinkedOwnerOf(wts, cb); ok {
			dest = wt.Path
		}
	}
	if !shimActive() {
		return "", fmt.Errorf("cannot undo creation of current worktree branch %q from inside its worktree %q without the shell shim; run from the main worktree or run: cd %s && st undo", cur, owner.Path, main.Path)
	}
	if err := os.Chdir(main.Path); err != nil {
		return "", fmt.Errorf("leaving worktree %q before undo: %w", owner.Path, err)
	}
	// The process's cwd moved worktrees: the worktree list itself did not
	// change, but worktree paths and HEAD now read relative to the main
	// worktree — drop the memoized list so any later read re-lists (the
	// per-port CurrentBranch/RepoRoot memos do not need this: the undo loop
	// builds fresh ports after this move).
	resetProcCaches()
	if inProgress, err := git.RebaseInProgress(); err != nil {
		return "", err
	} else if inProgress {
		return "", fmt.Errorf("cannot undo while a rebase is in progress in the main worktree %q; run st abort or resolve conflicts and run st continue there", main.Path)
	}
	return dest, nil
}

func undoEntryCreatedWorktree(entry *stack.UndoEntry, name string) bool {
	if entry == nil || name == "" {
		return false
	}
	return entry.CreatedWorktrees[name] != ""
}

// undoListEntry is the --list projection of a journal entry: the fields a user
// needs to decide whether undoing is safe — what the op was (label), which
// branches it created and undo would delete (createdBranches, with their
// createdWorktrees), where HEAD would land (currentBranch), and which tips
// would move back (refs). The state snapshot and the captured local-branch
// list are restore plumbing, not display data, so they stay out. index counts
// down from the newest entry: 1 is what a bare `st undo` reverts, so a future
// multi-step undo can reference positions the list already numbers.
type undoListEntry struct {
	Index            int               `json:"index"`
	Label            string            `json:"label"`
	CurrentBranch    string            `json:"currentBranch,omitempty"`
	CreatedBranches  []string          `json:"createdBranches,omitempty"`
	CreatedWorktrees map[string]string `json:"createdWorktrees,omitempty"`
	Refs             map[string]string `json:"refs"`
}

// runUndoDryRun previews the newest n journal entries under the advisory lock —
// consistent reads, zero writes: no state/journal bytes change, no ref moves,
// no worktree removal. Blockers are data (a real undo's refusals, in its gate
// order), not errors; the command still exits 0. Previewing reserves nothing:
// a later real `st undo` revalidates everything.
//
// For n > 1 each step previews against the state that step's real undo would
// see: the live state for step 1, then the snapshot the preceding step would
// restore (DecodeUndoState of the next-newer entry). Live refs/tips still
// reflect the pre-undo world — a real run restores them per step — so deeper
// previews model state evolution but not ref evolution.
func runUndoDryRun(asJSON bool, n int) error {
	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	entries, err := stack.ListUndo()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return emit(asJSON, struct {
			DryRun bool `json:"dryRun"`
			Undone bool `json:"undone"`
		}{true, false}, func() { out("nothing to undo\n") })
	}
	if n > len(entries) {
		return fmt.Errorf("cannot preview %d undo steps: only %d undoable operation(s) recorded", n, len(entries))
	}
	newest := &entries[len(entries)-1]

	// Like the real run: an unloadable state degrades (the snapshot is what
	// undo restores) — except ErrStateTooNew, which is a blocker row rather
	// than the real run's fatal error. It gates the whole range (the real run
	// refuses before step 1), so a single-blocker preview answers it for any n.
	s, loadErr := stack.Load()
	if errors.Is(loadErr, stack.ErrStateTooNew) {
		res := &stack.UndoPreviewResult{
			DryRun:   true,
			Label:    newest.Label,
			Blockers: []string{"state_too_new"},
		}
		return renderUndoPreview(res, asJSON)
	}
	if loadErr != nil {
		s = nil
	}

	previews := make([]*stack.UndoPreviewResult, 0, n)
	si := s
	// The readings every step's plan needs — rebase flag, live tips, HEAD's
	// branch — are step-invariant: a real run changes them only by undoing,
	// which the preview already documents it does not model. Read once.
	p := newGitPort()
	probes := stack.ReadUndoProbes(p)
	for i := 0; i < n; i++ {
		e := &entries[len(entries)-1-i]
		res, err := stack.UndoPreview(stack.Env{Git: p}, si, e, shimActive(), i+1, probes)
		if err != nil {
			return err
		}
		previews = append(previews, res)
		// Chain: the next-older step's preview sees the state this step's undo
		// would restore. A snapshot that fails to decode degrades to nil — the
		// preview already recorded that blocker on THIS step.
		if si, err = stack.DecodeUndoState(e.State); err != nil {
			si = nil
		}
	}
	if n == 1 {
		return renderUndoPreview(previews[0], asJSON)
	}
	payload := struct {
		DryRun bool                       `json:"dryRun"`
		Count  int                        `json:"count"`
		Steps  []*stack.UndoPreviewResult `json:"steps"`
	}{true, n, previews}
	return emit(asJSON, payload, func() {
		for i, res := range previews {
			if i > 0 {
				out("\n")
			}
			renderUndoPreviewText(res, i+1, n)
		}
	})
}

// renderUndoPreview mirrors the JSON payload in text: the op being previewed,
// the per-ref restores, the created-branch deletions, the checkout target, and
// every blocker — all names/paths terminal-sanitized.
func renderUndoPreview(res *stack.UndoPreviewResult, asJSON bool) error {
	return emit(asJSON, res, func() {
		renderUndoPreviewText(res, 0, 0)
	})
}

// renderUndoPreviewText prints one preview; step/total are nonzero only for a
// multi-step dry-run, where the header gains a "(step k of n)" suffix.
func renderUndoPreviewText(res *stack.UndoPreviewResult, step, total int) {
	if step > 0 {
		out("would undo: %s (step %d of %d)\n", sanitizeForTerminal(res.Label), step, total)
	} else {
		out("would undo: %s\n", sanitizeForTerminal(res.Label))
	}
	for _, r := range res.WouldRestore {
		lost := "unknown"
		if n, ok := r.CommitsLostFromRef.(int); ok {
			lost = fmt.Sprintf("%d", n)
		}
		out("  restores: %s %s→%s (%s commits lost from ref)\n",
			sanitizeForTerminal(r.Branch), sanitizeForTerminal(r.From), sanitizeForTerminal(r.To), lost)
	}
	for _, d := range res.WouldDelete {
		line := "  deletes: " + sanitizeForTerminal(d.Branch)
		var bits []string
		if d.Worktree != "" {
			bits = append(bits, "worktree "+sanitizeForTerminal(d.Worktree))
		}
		if d.WorktreeDirty {
			bits = append(bits, "dirty")
		}
		if d.IsCurrentWorktree {
			bits = append(bits, "current worktree")
		}
		if len(bits) > 0 {
			line += " (" + strings.Join(bits, "; ") + ")"
		}
		out("%s\n", line)
	}
	if res.WouldDetach {
		out("  detach: HEAD would end detached — the checkout target is blocked\n")
	} else if res.WouldCheckout != nil {
		out("  checkout: %s\n", sanitizeForTerminal(*res.WouldCheckout))
	} else if len(res.Blockers) == 0 {
		out("  checkout: (recorded branch no longer exists)\n")
	}
	for _, b := range res.Blockers {
		out("  blocked: %s\n", sanitizeForTerminal(b))
	}
	for _, n := range res.Notes {
		out("  note: %s\n", sanitizeForTerminal(n))
	}
}

// runUndoList prints the undo journal newest-first without touching it. The
// empty journal matches bare undo's empty case: "nothing to undo" in text,
// {"entries": []} in JSON, exit 0 either way.
func runUndoList(asJSON bool) error {
	entries, err := stack.ListUndo()
	if err != nil {
		return err
	}
	payload := struct {
		Entries []undoListEntry `json:"entries"`
	}{Entries: []undoListEntry{}}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		refs := e.Refs
		if refs == nil {
			refs = map[string]string{}
		}
		payload.Entries = append(payload.Entries, undoListEntry{
			Index:            len(entries) - i,
			Label:            e.Label,
			CurrentBranch:    e.CurrentBranch,
			CreatedBranches:  e.CreatedBranches,
			CreatedWorktrees: e.CreatedWorktrees,
			Refs:             refs,
		})
	}
	return emit(asJSON, payload, func() {
		if len(payload.Entries) == 0 {
			out("nothing to undo\n")
			return
		}
		for _, e := range payload.Entries {
			line := fmt.Sprintf("%d: %s", e.Index, sanitizeForTerminal(e.Label))
			var bits []string
			if len(e.CreatedBranches) > 0 {
				bits = append(bits, "created "+joinTerminalNames(e.CreatedBranches))
			}
			if len(e.CreatedWorktrees) > 0 {
				names := make([]string, 0, len(e.CreatedWorktrees))
				for name := range e.CreatedWorktrees {
					names = append(names, name)
				}
				sort.Strings(names)
				bits = append(bits, "worktrees "+joinTerminalNames(names))
			}
			if e.CurrentBranch != "" {
				bits = append(bits, "on "+sanitizeForTerminal(e.CurrentBranch))
			}
			if len(bits) > 0 {
				line += " (" + strings.Join(bits, "; ") + ")"
			}
			out("%s\n", line)
		}
	})
}

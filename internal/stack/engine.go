// engine.go — shared machinery for the operations: the OpResult contract,
// error sentinels and ConflictError, the clean/paused gates, HEAD restore,
// and the finishUpstack epilogue every restack-producing op ends in.
package stack

import (
	"errors"
	"fmt"
	"sort"
)

// OpResult is the typed outcome of a stack-mutating operation. The CLI renders
// it as text or JSON; the engine never prints.
type OpResult struct {
	Summary   string   `json:"summary"`
	Branch    string   `json:"branch,omitempty"`
	Restacked []string `json:"restacked,omitempty"`
	Deleted   []string `json:"deleted,omitempty"`
	Notes     []string `json:"notes,omitempty"`
	// Tracked maps each branch a bulk adopt (`st track --all`) recorded to the
	// parent it was inferred under; unset for every other command.
	Tracked map[string]string `json:"tracked,omitempty"`
	// DryRun marks a preview: the Restacked/Deleted lists are what *would* happen,
	// nothing was changed.
	DryRun bool `json:"dryRun,omitempty"`
}

// ErrDirty is returned when an operation needs a clean working tree but the tree
// has uncommitted changes. ErrConflict is returned when a rebase stops on a
// conflict and the user must resolve it and run `st continue`. Both are sentinels
// the CLI maps to dedicated exit codes.
var (
	ErrDirty    = errors.New("working tree is dirty; commit or stash first")
	ErrConflict = errors.New("rebase conflict — resolve the conflicts, stage them with git add, then run: st continue")
)

// UndoRebaseGateMsg is the refusal both undo paths emit while a rebase is
// paused — the engine's planner and the cmd layer's fail-fast check share it
// so the wording cannot drift across the boundary.
const UndoRebaseGateMsg = "cannot undo while a rebase is in progress; run st abort or resolve conflicts and run st continue"

// ConflictError reports a rebase that stopped on a conflict, naming the branch
// being rebased and the parent it was moving onto. It Unwraps to ErrConflict, so
// errors.Is(err, ErrConflict) — and the exit-2 / "conflict" mappings — still
// hold, while errors.As lets the CLI surface Branch and Onto as structured JSON
// fields instead of leaving them buried in the message prose.
type ConflictError struct {
	Action string // the verb, e.g. "rebasing" or "moving"
	Branch string // the branch whose rebase stopped
	Onto   string // the parent it was being rebased onto
}

func (e *ConflictError) Error() string {
	// Onto is empty only on the rare re-stall of an untracked branch; drop the
	// "onto …" clause then rather than render an empty quoted parent.
	if e.Onto == "" {
		return fmt.Sprintf("%s %q: %s", e.Action, e.Branch, ErrConflict.Error())
	}
	return fmt.Sprintf("%s %q onto %q: %s", e.Action, e.Branch, e.Onto, ErrConflict.Error())
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// AlsoFailed joins an operation error with the error of a follow-up
// recovery/rollback step that also failed, keeping both matchable with
// errors.Is/errors.As: "<primary>; additionally failed to <what>: <secondary>".
func AlsoFailed(primary error, what string, secondary error) error {
	return fmt.Errorf("%w; additionally failed to %s: %w", primary, what, secondary)
}

// RequireNoPausedRebase refuses a stack mutation while a rebase is in progress:
// state edits mid-rebase can orphan the PendingReparent record or reorder the
// stack under the paused rebase (e.g. renaming the branch being rebased). st
// continue/st abort bypass this on purpose — they exist to resolve the pause.
//
// The sweep also covers LINKED worktrees: a worktree mid-rebase reports
// `detached` in `git worktree list`, so owner resolution cannot see the branch
// its paused rebase targets. Git protects the branch itself (`branch -D` and
// `rebase --onto` both refuse a worktree-owned branch), but the refusal lands
// mid-operation — after earlier prune/cascade steps already applied. Refusing
// here keeps every mutation all-or-nothing and names where to resolve it.
// Pauses targeting branches st does not track are ignored: st ops can never
// reach them.
func RequireNoPausedRebase(g Git, s *State) error {
	inProgress, err := g.RebaseInProgress()
	if err != nil {
		return fmt.Errorf("checking rebase state: %w", err)
	}
	if inProgress {
		return errors.New("a rebase is in progress; resolve it with `st continue` or `st abort` first")
	}
	wts, err := g.Worktrees()
	if err != nil {
		return fmt.Errorf("listing worktrees for paused-rebase check: %w", err)
	}
	if !IsMultiWorktree(wts) {
		return nil
	}
	paused, err := PausedRebaseOwners(g, wts)
	if err != nil {
		return err
	}
	// Deterministic refusal when several tracked branches are paused: report
	// the sorted first (each names its own worktree; resolve and retry).
	var names []string
	for head := range paused {
		if head == s.Trunk || s.IsTracked(head) {
			names = append(names, head)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		head := names[0]
		return fmt.Errorf("branch %q has a rebase in progress in worktree %q; resolve it there (`st continue` or `st abort`) before mutating the stack", head, paused[head].Path)
	}
	return nil
}

// requireClean returns ErrDirty when the working tree has uncommitted changes.
func requireClean(g Git) error {
	clean, err := g.IsClean()
	if err != nil {
		return fmt.Errorf("checking working tree: %w", err)
	}
	if !clean {
		return ErrDirty
	}
	return nil
}

// tracked returns the tracked branch named name, or the canonical
// "branch %q is not tracked" error. It is the single source of that message for
// the operations that act on an existing tracked branch.
func (s *State) tracked(name string) (*Branch, error) {
	b, ok := s.Get(name)
	if !ok {
		return nil, fmt.Errorf("branch %q is not tracked", name)
	}
	return b, nil
}

// currentTracked returns the checked-out branch and its tracked metadata, or an
// error when HEAD is not on a tracked branch — the shared preamble of the
// operations that rewrite the current branch (fold, squash, onto).
func currentTracked(g Git, s *State) (string, *Branch, error) {
	cur, err := g.CurrentBranch()
	if err != nil {
		return "", nil, err
	}
	b, err := s.tracked(cur)
	if err != nil {
		return "", nil, err
	}
	return cur, b, nil
}

// restoreHEAD checks out target (falling back to fallback when target is gone),
// returning HEAD to where the user started after an operation moved it.
func restoreHEAD(env Env, target, fallback string) error {
	g := env.Git
	if !g.BranchExists(target) {
		target = fallback
	}
	// A CurrentBranch failure (detached HEAD — sync parks there before pruning)
	// only skips the already-on-target shortcut; the checkout below either
	// restores the target or surfaces the real problem.
	if cur, err := g.CurrentBranch(); err == nil && cur == target {
		return nil
	}
	if err := g.Checkout(target); err != nil {
		return fmt.Errorf("restore branch %q: %w", target, err)
	}
	return nil
}

func restoreHEADAfterNonConflict(env Env, target, fallback string, err error) error {
	if errors.Is(err, ErrConflict) {
		return err
	}
	if restoreErr := restoreHEAD(env, target, fallback); restoreErr != nil {
		return AlsoFailed(err, fmt.Sprintf("restore %q", target), restoreErr)
	}
	return err
}

type upstackResult struct {
	restacked []string
	notes     []string
}

// finishUpstack is the common tail of the operations that rewrite a branch's
// commits (fold, squash, onto): it restacks anchor's descendants, persists, and
// restores HEAD to the original branch (or the trunk). On a non-conflict restack
// error it restores HEAD before returning; on a conflict it leaves the rebase in
// progress for `st continue`.
func finishUpstack(env Env, s *State, anchor string) (upstackResult, error) {
	rebased, err := s.restackUpstack(env, anchor)
	if err != nil {
		return upstackResult{}, restoreHEADAfterNonConflict(env, anchor, s.Trunk, err)
	}
	notes := skippedWorktreeNotes(s)
	if err := env.save(); err != nil {
		return upstackResult{}, err
	}
	if err := restoreHEAD(env, anchor, s.Trunk); err != nil {
		return upstackResult{}, err
	}
	return upstackResult{restacked: rebased, notes: notes}, nil
}

// restackEpilogue is the shared tail of Restack and RestackAllOp: restore HEAD
// to the branch the caller started on, turn any dirty-worktree skips into
// notes, and build the OpResult ("everything up to date" when nothing moved
// and nothing was skipped, else "restacked").
func restackEpilogue(env Env, s *State, start string, rebased []string) (*OpResult, error) {
	if err := restoreHEAD(env, start, s.Trunk); err != nil {
		return nil, err
	}
	notes := append(skippedWorktreeNotes(s), unreachableNotes(s)...)
	if len(rebased) == 0 && len(notes) == 0 {
		return &OpResult{Summary: "everything up to date"}, nil
	}
	return &OpResult{Summary: "restacked", Restacked: rebased, Notes: notes}, nil
}

// skippedWorktreeNotes drains the branches a restack skipped because their owning
// worktree was dirty or mid-rebase, turning each into a human-readable note.
// Empty when the cascade skipped nothing (the common, single-tree case).
func skippedWorktreeNotes(s *State) []string {
	skipped, rebase := s.drainSkippedWorktrees()
	return skippedWorktreeNotesFrom(skipped, rebase)
}

// unreachableNotes turns the tracked branches no trunk forest walk can reach
// — cycle members, dangling parents — into a repair-pointing warning. st
// validate and st repair already classify both kinds (ParentUntracked,
// ParentMissing, ParentCycle), so the advice actually resolves the condition.
func unreachableNotes(s *State) []string {
	un := s.UnreachableBranches()
	if len(un) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("tracked branches unreachable from trunk %q (cycle or dangling parent): %s — run `st repair`", s.Trunk, joinComma(un))}
}

func skippedWorktreeNotesFrom(skipped []string, rebase map[string]bool) []string {
	if len(skipped) == 0 {
		return nil
	}
	notes := make([]string, 0, len(skipped))
	for _, name := range skipped {
		notes = append(notes, skippedWorktreeNote(name, rebase[name]))
	}
	return notes
}

func skippedWorktreeNote(name string, rebase bool) string {
	if rebase {
		return fmt.Sprintf("skipped %s: a rebase is already in progress in its worktree (finish or abort it there, then re-run)", name)
	}
	return fmt.Sprintf("skipped %s: its worktree is dirty (commit/stash there, then re-run)", name)
}

func cloneState(s *State) *State {
	cp := &State{Version: s.Version, Trunk: s.Trunk, Branches: make(map[string]*Branch, len(s.Branches))}
	for name, branch := range s.Branches {
		b := *branch
		cp.Branches[name] = &b
	}
	if s.PendingReparent != nil {
		p := *s.PendingReparent
		cp.PendingReparent = &p
	}
	return cp
}

// restackAll restacks every stack rooted on the trunk, parents before children,
// reading live tips at each step. Used by sync and continue. The whole forest is
// exactly the trunk's upstack (every tracked branch descends from the trunk), so
// it delegates to the one canonical restack path — restackUpstack — which
// Descendants(trunk) walks in the same sorted, parents-first order.
func restackAll(env Env, s *State) ([]string, error) {
	return s.restackUpstack(env, s.Trunk)
}

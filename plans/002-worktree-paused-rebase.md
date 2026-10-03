# Plan 002: Detect paused rebases in linked worktrees before mutating their branches

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/worktree.go internal/stack/engine.go internal/stack/ops_delete_sync.go internal/stack/undo_op.go internal/git/worktree.go cmd/mutate.go`
> On any mismatch between "Current state" and live code, STOP.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW (adds refusals/skips only; no new mutation paths)
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Every mutating op consults `OwnerOf`/`LinkedOwnerOf` to find which worktree owns a branch — and every lookup matches `wt.Branch`. A worktree paused mid-rebase reports `detached` in `git worktree list` (no branch attribute), so the owning worktree is invisible. From another worktree, `st delete`/`st sync`/`st prune`/`st undo`/`st fold` can delete or rewrite a branch while its paused rebase still targets it via `rebase-merge/head-name`; when the user later runs `git rebase --continue`/`--abort` there, git unconditionally `update-ref`s the branch — resurrecting deleted branches or clobbering restacked tips. The protection already exists on `st worktree rm` (`RebaseInProgressIn` probe); it just never runs for these paths.

## Current state

- `internal/stack/worktree.go:125-158` — `OwnerOf`/`LinkedOwnerOf` match `wt.Branch == branch`; detached worktrees never match.
- `internal/git/worktree.go:~231-238` — the `worktree list --porcelain` parser sets `Branch` only when a `branch` attribute is present; a mid-rebase worktree emits `detached` and no `branch`.
- `internal/stack/worktree.go:176-196` — `ownerElsewhereWith` returns `elsewhere=false` when `OwnerOf` misses, so callers take the in-place path.
- `internal/stack/worktree.go:223-257` — `restackInWorktree` probes `RebaseInProgressIn(owner.Path)`, but only after `ownerElsewhereWith` named an owner — unreachable for a detached owner.
- `internal/stack/worktree.go:289-329` — `ownedWorktreeReleaseTarget` (used by delete/sync/prune teardown) sees no owner → leaks the paused worktree.
- `internal/stack/undo_op.go:213-247` — `removeCreatedWorktree` same blind spot.
- `cmd/mutate.go:55` — `RequireNoPausedRebase` probes only the *caller's* worktree (`g.RebaseInProgress()`), never linked worktrees.
- The port already exposes `RebaseInProgressIn(dir string) (bool, error)` — see its use in `restackInWorktree` and `cmd/worktree.go:242-246, 348-353` (the `worktree rm` gate — the pattern to mirror).
- Convention: engine pure over `Env`; add the probe to the port (it exists) and gate in the engine. fakeGit models worktrees via `f.worktrees`/rebase state — check `fakegit_test.go` for `rebaseIn`/`RebaseInProgressIn` knobs before writing tests.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Worktree tests | `go test ./internal/stack -run 'Worktree|Owner|Delete|Sync|Undo'` | all pass |
| E2E | `go test ./e2e -run 'Worktree'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**:
- `internal/stack/worktree.go` — new helper `pausedRebaseOwner`/`worktreePausedOn(g, branch)` plus wiring into `ownerElsewhereWith`/`ownedWorktreeReleaseTarget`.
- `internal/stack/undo_op.go` — `removeCreatedWorktree` gains the same check.
- `internal/stack/engine.go` — `RequireNoPausedRebase` gains a multi-worktree sweep (or a new `RequireNoPausedRebaseAnywhere` used by `mutateState`).
- `internal/git/worktree.go` — possibly a `head-name` reader for detached worktrees (see Step 1).
- `internal/stack/fakegit_test.go` — fake support for "worktree detached + rebase-merge/head-name=X".
- New/updated tests in `internal/stack/*_test.go`, `cmd/*_test.go`, `e2e/e2e_journey_worktree_test.go`.
- `docs/AGENT.md` — note the new refusal in the relevant JSON/behavior contract if visible.
- `CHANGELOG.md` `[Unreleased]` under `Fixed`.

**Out of scope**:
- `cmd/worktree.go` `rm` gates — already correct.
- The Undo/UndoPreview twin-site unification (Plan 010) — if `Undo` needs the same probe, add it minimally; do not restructure.
- `cachedPort` invalidation changes (Plan 003 covers that).

## Git workflow

- Branch: `advisor/002-worktree-paused-rebase`; imperative commit message; no push/PR unless instructed.

## Steps

### Step 1: Add a port method to find the branch a paused rebase targets

Git records it at `<worktree-gitdir>/rebase-merge/head-name` (or `rebase-apply/head-name`). Add `RebaseHeadNameIn(dir string) (string, error)` to the port (`internal/stack/git.go`), implemented in `internal/git/rebase.go` or `worktree.go` (a detached worktree's gitdir is `<common>/worktrees/<name>` — the existing `RebaseInProgressIn` already locates it; mirror that code path). Implement in fakeGit with a `rebaseHeadName[dir]` knob — **check `fakegit_test.go` first**: a `RebaseHeadName` probe may already exist (the test agent reported `RebaseHeadName` among armed fake methods). If it exists, reuse it.

**Verify**: `go build ./...` → exit 0; `go test ./internal/git` → pass.

### Step 2: Gate owner resolution on paused rebases

In `internal/stack/worktree.go`: after `OwnerOf`/`LinkedOwnerOf` miss a branch, iterate `worktrees` and for each `wt` call `RebaseInProgressIn(wt.Path)`; when true, read `RebaseHeadNameIn(wt.Path)` — if it names `branch`, treat the branch as owned-by-that-worktree-in-rebase. Export a helper like `PausedOn(wts []git.Worktree, g Git, branch string) (git.Worktree, bool, error)` and use it inside `ownerElsewhereWith` (return `elsewhere=true` with a marker the caller converts to refusal/skip) and inside `ownedWorktreeReleaseTarget`/`removeCreatedWorktree` (refuse removal — the worktree is mid-rebase).

Decide the surface: rebase-in-progress on the target branch should produce `ErrConflict`-adjacent refusal (`paused_rebase`-style message naming the worktree path and suggesting `st continue`/`st abort` *there*, or plain error — match how `restackInWorktree`'s existing `inRebase` skip is phrased).

**Verify**: `go build ./...` → exit 0; `go test ./internal/stack` → pass.

### Step 3: Extend the mutation gate

`RequireNoPausedRebase` currently checks `g.RebaseInProgress()` only. In a multi-worktree repo, add: for every *tracked* branch `b` in `s.Branches`, if `PausedOn(...)` reports a paused rebase, refuse with an error naming branch+worktree. Wire so `mutateState` callers get it; `undo`/`worktree` paths get the per-branch check at their own decision points (they must NOT blanket-refuse — undo of a create should skip the paused worktree rather than refuse? **Decide**: simplest correct = refuse with clear message; skipping mid-rebase worktree deletion is also defensible. Pick refuse — consistent with "don't touch a worktree mid-rebase".)

**Verify**: `make test-fast` → exit 0.

### Step 4: Tests

- fakeGit: fixture a linked worktree whose `rebaseHeadName` = `feat-a`, `rebaseInProgressIn` = true. Assert `st`-level: `Delete`/`Sync`/`prune` refuse naming the worktree; `restack --all` skips or refuses per Step 2's choice.
- cmd: real-git test creating a linked worktree, starting a rebase conflict inside it (`git -C <wt> rebase` onto a conflicting base, leave paused), then `st delete feat-a` from main worktree → refusal.
- e2e: extend `e2e_journey_worktree_test.go` with one journey covering the refusal.

**Verify**: `go test ./internal/stack ./cmd ./e2e` → all pass.

### Step 5: Docs + gate

`docs/AGENT.md`: if `delete`/`sync` gain a documented refusal condition, add a line. `CHANGELOG.md` `[Unreleased]` → `Fixed` entry. Run `make ci`.

**Verify**: `make ci` → exit 0.

## Test plan

- New fake-git tests + one cmd real-git test + one e2e journey (Step 4).
- Pattern: existing paused-rebase tests — `TestCrossWorktreeConflictAbortFailureSurfaces` in `internal/stack/engine_test.go`, and the `worktree rm` refusal tests in `cmd/commands_mutation_test.go`.
- Verification: `go test ./internal/stack ./cmd ./e2e` → all pass; `make ci` green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./internal/stack ./cmd ./e2e` exits 0 with new tests
- [ ] `make ci` exits 0
- [ ] `grep -n 'RebaseHeadName\|PausedOn' internal/stack/git.go internal/stack/worktree.go` shows the new seam in use
- [ ] A manual repro (linked worktree mid-rebase; `st delete` of its branch from main) refuses instead of deleting
- [ ] No files outside in-scope list (`git status`)
- [ ] `plans/README.md` row updated

## STOP conditions

- `RebaseHeadName` already exists in the port/fake with different semantics than assumed — adapt, don't duplicate; if it can't express head-name reads, STOP.
- The porcelain parser reports something other than detached-without-branch for a mid-rebase worktree on the repo's min git version (2.17) — verify with a real `git worktree list --porcelain` on a paused rebase; if behavior differs, STOP and report.
- `mutateState`'s gate change breaks `st continue`/`st abort` reachability — those must NOT be gated (they bypass `mutateState`; verify).

## Maintenance notes

- Plan 003 also touches `cachedPort`/undo ordering — land this first, rebase as needed.
- The fake's worktree model may need a "detached" state it lacks today; keep the fake minimal.
- Reviewer focus: that `st worktree rm`'s existing refusal and the new gates agree on message wording; that `continue`/`abort`/`undo` remain ungated where intended.

# Plan 007: Pin the failure arms of Sync, Delete, Continue, and cross-worktree restack

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/ops_delete_sync.go internal/stack/ops_onto.go internal/stack/worktree.go internal/stack/fakegit_test.go internal/stack/failure_injection_test.go internal/stack/sync_test.go`
> On mismatch between "Current state" and live code, STOP.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW (tests only — zero production-code changes expected; if a test exposes a real bug, that becomes a STOP-then-report)
- **Depends on**: none
- **Category**: tests
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`Sync`, `Delete`, and `Continue` are the most destructive routine ops — they detach HEAD, delete branches, rebase forests. Their *failure* contract (restore HEAD vs. `AlsoFailed` composition vs. leave-paused) is the recovery story, and about half the failure arms have never been exercised: a regression that swallows a parking `CheckoutDetach` error and then prunes with HEAD stranded would pass the whole suite today. `TestProbeFailuresSurface` exists precisely to pin these — extend it (and its siblings) to the dormant cells.

## Current state

The fake git (`internal/stack/fakegit_test.go`) exposes per-method failure knobs (`f.fail(...)`, `commitErr`, `checkoutErr`, `deleteErr`, `rebaseErr`, `fakeRemote.err`) and `envWithSaveErr` (`internal/stack/engine_test.go:~20`) injects `env.save()` failures. The inventory test is `TestProbeFailuresSurface` in `internal/stack/failure_injection_test.go:456-646`.

**Dormant arms to pin** (all verified by enumerating arming sites):

`internal/stack/ops_delete_sync.go` (Sync, lines 139–201):
- `FastForward` failure + `restoreHEAD` double-fault → `AlsoFailed` (~141-142)
- `RevParse("HEAD")`/`CheckoutDetach` failure in the detach-parking arm (~155-160)
- `Checkout(s.Trunk)` failure in the single-tree arm (~162-163)
- prune-failure + restore-failure `AlsoFailed` (~166-167)
- both `env.save()` checkpoints (~174-175, ~184-185)
- final `restoreHEAD` failure (~199-200)

`internal/stack/ops_delete_sync.go` (Delete, lines 29–79):
- `IsAncestor` failure in the non-force merged check (~29-31)
- `Checkout(parent)` failure when deleting current branch (~52-54)
- `DeleteBranch` failure *through* `stack.Delete` with restore attempt (~58-63 — `deleteErr` is armed only via `Fold`/`PruneMerged` today)
- post-delete `env.save()` checkpoint (~66-67)
- non-conflict `restackForest` failure → `restoreHEADAfterNonConflict` (~73-75 — only the conflict arm is pinned)
- final `restoreHEAD` failure (~78-79)

`internal/stack/ops_onto.go` (Continue, lines 117–231):
- `env.save()` after pending-reparent promotion (~186-188)
- `env.save()` after the foreign-target `ParentSHA` stamp (~201-203)
- non-conflict `restackAll` failure → `restoreHEADAfterNonConflict(conflicted)` (~207-212)
- final `env.save()` (~214-215), final `restoreHEAD` (~218-219)

`internal/stack/worktree.go` (restackInWorktree, lines 223–257):
- `IsCleanIn` failure (~232-234 — `failErr["IsCleanIn"]` is armed only via the absorb preflight path)
- `env.save()` checkpoint after the cross-worktree rebase (~253-255)

Probe-failure inventory cells to add to `TestProbeFailuresSurface` (or focused tests):
- `ChangesContainedIn` inside `mergedBranches` (`ops_delete_sync.go:~474`)
- `IsAncestor` at `ops_track.go:~316` (inferParentPick), `ops_track.go:~350` (UntrackBranch merged-check incl. missing-branch degrade), `ops_onto.go:~249` (rebaseTargetIsParentBase), `plan.go:~175` (DeletePlan)
- `CommitRange` at `absorb.go:~110` and `undo_preview.go:~275`
- `Commit`/`AmendNoEdit`/`AmendMessage` through `Modify` (`ops_lifecycle.go:132-144`)
- `CreateBranchAt` in `CreateInWorktreePrep` (`ops_lifecycle.go:87-88`)
- `ForceBranch` in `Fold` (`ops_combine.go:62`)
- `applyPrune`'s mid-loop `env.save()` (`ops_delete_sync.go:~442-443`)
- `refusePruneCurrent`'s swallowed `CurrentBranch` degrade (`ops_delete_sync.go:~333` — `cur, _ :=`; pin the degrade explicitly like `TestSnapshotUndoCurrentBranchDegrade` did)
- `RevParse`/`CheckoutDetach` inside undo's blocked-checkout detach fallback (`undo_op.go:103-109`)

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Target tests | `go test ./internal/stack -run 'Sync|Delete|Continue|Restack|ProbeFailures'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/*_test.go` — `failure_injection_test.go`, `sync_test.go`, `engine_test.go`, `cascade_test.go` (new tests only). `fakegit_test.go` may gain a knob ONLY if an arm is truly unreachable with existing knobs — check first.

**Out of scope**: all production `.go` files (this is a test-pinning plan — if pinning reveals a defect, STOP and report), `cmd/`, `e2e/`, `scripts/cover-allow.txt` (Plan 009).

## Git workflow

- Branch: `advisor/007-failure-injection-coverage`; commits per op-group; no push/PR unless instructed.

## Steps

### Step 1: Sync's six failure arms

In `sync_test.go` (extend the block at ~619-795 that pins owner-aware trunk handling), add one test per arm listed above. Use `fakeRemote.err` for `FastForward` failure, `f.checkoutErr["main"]`/`f.checkoutErr[orig]` for checkout/detach failures, `envWithSaveErr` with `failAfterN` for save checkpoints. Assert: `errors.Is`/`errors.As` composition (e.g. `AlsoFailed` wraps both), and `f.head` final position.

**Verify**: `go test ./internal/stack -run 'Sync'` → all pass.

### Step 2: Delete's six failure arms

New tests in `engine_test.go` or `failure_injection_test.go` next to `TestDeleteConflictContinueRecovers` (~1758): arm each knob (`f.fail("IsAncestor")`, `checkoutErr[parent]`, `deleteErr`, `envWithSaveErr`, `rebaseErr` on a former child, final `restoreHEAD`). Assert error surface + post-state (branch still tracked on early failure, HEAD restored, etc.).

**Verify**: `go test ./internal/stack -run 'Delete'` → all pass.

### Step 3: Continue's five arms

Same pattern in `failure_injection_test.go` near `TestRebaseContinueGenericFailure` (~424): `envWithSaveErr` at each save site, a sibling `rebaseErr` for the non-conflict resume, final `restoreHEAD`. Model on `TestRestackCascadeSaveCheckpointFailure` (~302).

**Verify**: `go test ./internal/stack -run 'Continue|Rebase'` → all pass.

### Step 4: restackInWorktree arms

`failErr["IsCleanIn"]` through the `restackInWorktree` callsite (arm it so the failure hits at `worktree.go:232`, not absorb's preflight), and `envWithSaveErr` at the post-rebase checkpoint. Assert the wrapped error names the worktree path and the owner's `ParentSHA` state.

**Verify**: `go test ./internal/stack -run 'Worktree|Cascade'` → all pass.

### Step 5: Probe-failure inventory cells

Add rows to `TestProbeFailuresSurface`'s table for each enumerated cell. Where a row needs distinct setup (`ChangesContainedIn` needs an unmerged candidate; undo's detach fallback needs a blocked `checkoutErr`), write focused tests instead of forcing the table.

**Verify**: `go test ./internal/stack -run 'ProbeFailures'` → all pass; `make test-fast` → exit 0.

### Step 6: Gate

**Verify**: `make ci` → exit 0 (coverage floor should improve; watch that no new test arms a case the floor now flags elsewhere).

## Test plan

This plan IS the test plan. Structural patterns: `TestRestackCascadeSaveCheckpointFailure` (`failure_injection_test.go:302`), `TestSnapshotUndoCurrentBranchDegrade` (~652), `TestProbeFailuresSurface` (~456-646), `TestDeleteConflictContinueRecovers` (`engine_test.go:1758`).

## Done criteria

- [ ] `go test ./internal/stack` exits 0
- [ ] `make ci` exits 0
- [ ] Every arm listed in "Current state" has a named test or table row (grep for the new test names)
- [ ] No production `.go` file modified (`git status` clean outside `*_test.go`)
- [ ] `plans/README.md` row updated

## STOP conditions

- An arming knob doesn't exist in `fakeGit` for a listed method — check `fakegit_test.go` for `f.fail(` coverage first; if absent, add the minimal knob rather than a bespoke stub.
- A pinned arm reveals the production behavior is actually wrong (e.g. HEAD left stranded, wrong error class) — STOP and report the defect; do not "fix" production code.
- A test is flaky across runs — fail-fast patterns only; no sleeps/retry loops.

## Maintenance notes

- These tests become the contract for Plan 010's Undo/Preview unification and any future engine refactor — the failure semantics are now load-bearing.
- When adding a new failure arm to these ops, the convention is now "pin it in the inventory or a focused test in the same commit".
- Reviewer focus: that each test asserts the *error composition*, not merely `err != nil` — the `AlsoFailed` wrapping is the contract.

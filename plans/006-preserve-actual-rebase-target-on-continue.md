# Plan 006: Record the rebase target actually incorporated by continue

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/git/git.go internal/git/shell.go internal/git/git_test.go internal/stack/git.go internal/stack/fakegit_test.go internal/stack/engine.go internal/stack/engine_test.go e2e/e2e_journey_test.go CHANGELOG.md plans/006-preserve-actual-rebase-target-on-continue.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** M
- **Risk:** LOW
- **Depends on:** 001 recommended; 014 is an independent documentation correction
- **Category:** bug
- **Audit item:** 5
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

A parent ref can move while a child rebase is paused. Continue currently records the parent's new tip even though Git finished rebasing onto the old target. That can suppress a needed follow-up restack and misstate which parent changes the child contains.

## Current state

`internal/stack/restack.go:138` starts the rebase with a resolved parent SHA:

```go
if rebaseErr := env.Git.RebaseOnto(parentTip, b.ParentSHA, name); rebaseErr != nil {
```

`internal/stack/engine.go:900`, Continue, runs RebaseContinue and then resolves the live parent:

```go
tip, err := g.RevParse(branchTipRef(b.Parent))
// ...
b.ParentSHA = tip
```

Pending Onto recovery already uses persisted pending.ParentSHA correctly. Normal restack recovery has no equivalent persisted target. Git keeps the actual target in worktree-local rebase-merge/onto or rebase-apply/onto until continuation finishes.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Git interactions belong in internal/git and the engine's Git port, not an exec call in Continue. Match RebaseHeadName/RebaseInProgress and their fake implementations. Existing engine patterns: TestRestackConflictContinueRecovers, TestContinueRestallCarriesBranch, and TestOntoConflictRecordsPendingReparentWithoutChangingParent.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/stack -run 'Test.*(Continue\|RestackConflict\|OntoConflict)' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
| Fast engine suite | `make test-fast` | all tests pass |
| Race unit/integration | `make test` | all tests pass |
| Black-box CLI | `make e2e` | all tests pass |
| Build | `make build` | exit 0; writes the ignored st binary |
| Formatting / lint checks | `make fmt-check lint` | exit 0 using pinned golangci-lint v2.12.2 |
| Complete contributor gate | `make ci` | exit 0, including dependency/pin checks, native/cross vet, build, lint, and coverage |
| Whitespace | `git diff --check` | exit 0 |

The audit baseline passed race tests, e2e, native/cross vet and coverage (87.0%) at the stamped commit. Full `make ci` was not run because golangci-lint was absent. That is historical evidence, not validation of your changes. Run the full gate when the pinned tools are available; if a required tool is absent, report the incomplete gate instead of substituting gofmt or changing tool pins.

## Scope

**Only modify:**

- `internal/git/git.go`
- `internal/git/shell.go`
- `internal/git/git_test.go`
- `internal/stack/git.go`
- `internal/stack/fakegit_test.go`
- `internal/stack/engine.go`
- `internal/stack/engine_test.go`
- `e2e/e2e_journey_test.go`
- `CHANGELOG.md`
- `plans/006-preserve-actual-rebase-target-on-continue.md`
- `plans/README.md`


**Out of scope:** persisting a new schema, changing PendingReparent semantics, global operation locking, cross-worktree restack redesign.

## Git workflow

Use a separate branch/worktree named `advisor/006-preserve-actual-rebase-target-on-continue` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: retain the actual rebase target when continuing`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Capture parent movement during a paused restack

Add TestContinueUsesActualRebaseOnto in engine_test.go. Pause child B onto parent A1, advance parent to A2 in the fake, then continue. Record Save checkpoints and subsequent rebase arguments: the first completed-rebase checkpoint must record A1; the following restack must recognize A2 and attempt the additional work. If that second restack succeeds, final ParentSHA should be A2 legitimately. If it conflicts, retain the completed A1 checkpoint. This distinction prevents an incorrect test that requires the final result to stay stale. Include a repeated conflict before completion. Write this regression against the existing fake paused-rebase behavior; add accessor-specific failure tests after introducing the accessor in Step 2 so the intentional failure is behavioral, not a compile error.

**Verify:** `go test ./internal/stack -run '^TestContinueUsesActualRebaseOnto$' -count=1` → the moved-parent case fails because the original code prematurely stamps A2

### Step 2: Read and preserve worktree-local rebase target metadata

Add `RebaseOntoSHA() (string, error)` to the engine port, Shell forwarding and fake. In internal/git implement it using the same worktree GitDir resolution as existing rebase helpers; do not use CommonDir. Read rebase-merge/onto or rebase-apply/onto, validate that the value resolves to the expected commit, and distinguish absence/read/parse failure from a valid target. Capture it before RebaseContinue can remove the metadata. For an ordinary tracked paused rebase, missing/invalid target is an actionable error before continuation. Keep the pending Onto path's persisted ParentSHA and its existing empty-head-name fallback; do not require a new metadata read that breaks that supported recovery path. Wire the captured SHA into Continue in this same step so its focused verification is green. The following step concentrates on real-Git integration evidence and documentation.

**Verify:** `go test ./internal/git ./internal/stack -run 'Test.*(RebaseOntoSHA|Continue|RestackConflict|OntoConflict)' -count=1` → metadata parser/backend tests pass and fake/production port implementations compile

### Step 3: Use the captured SHA and prove it with real Git

Confirm that after successful RebaseContinue the implementation assigns the captured target to the ordinary tracked branch's ParentSHA, Save, then run the existing restackAll cascade. Do not stamp it when continuation conflicts again. Add TestContinueAfterParentMoves in e2e/e2e_journey_test.go using a real conflict, advance the parent ref with a separate commit while detached in the paused child, resolve and continue. Prove parent changes are incorporated by the subsequent cascade, or correctly remain pending if that cascade conflicts. Cover a linked-worktree paused rebase in the Git adapter tests so metadata comes from the owner worktree.

**Verify:** `go test ./e2e -run '^TestContinueAfterParentMoves$' -count=1` → PASS with actual target recorded before the catch-up rebase; then `make ci` passes

## Test plan

- Test both rebase-merge and rebase-apply metadata readers with local test fixtures, plus a real default-backend conflict.
- Assert Save checkpoints and rebase arguments for A1/A2, not only final state.
- Missing/corrupt metadata must leave the paused rebase and refs untouched.
- Existing pending Onto, empty head-name fallback and repeated-conflict JSON tests remain green.

## Done criteria

- [ ] Continue never substitutes a newly moved parent tip for the completed rebase's actual target.
- [ ] The real-Git moving-parent journey and metadata failure-preservation tests pass.
- [ ] Both fake and production Git port implementations expose the new accessor.
- [ ] `make ci` passes.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The supported Git versions do not expose a reliable worktree-local onto value for the tested backend; do not guess from the current parent ref.
- The change requires new persistent state or changing pending Onto semantics.
- A regression asserts final A1 even though the existing cascade legitimately incorporated A2; correct the checkpoint-level assertion first.

## Maintenance notes

Any command that continues a paused operation must capture disappearing Git metadata before completion. Review checkpoint state separately from the final cascade state.

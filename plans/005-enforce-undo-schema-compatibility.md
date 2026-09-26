# Plan 005: Reject future state schemas before undo mutates anything

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/stack/store.go internal/stack/store_test.go internal/stack/undo_op.go internal/stack/undo_op_test.go cmd/undo.go cmd/commands_mutation_test.go CHANGELOG.md plans/005-enforce-undo-schema-compatibility.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** S
- **Risk:** LOW
- **Depends on:** 001 recommended for real-Git adapter regressions
- **Category:** bug
- **Audit item:** 4
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/005-enforce-undo-schema-compatibility` (stacked on `advisor/004`).

- Drift check: `git diff --stat cb31f06..HEAD -- <scoped files>` → only documented predecessor changes (001 test-env isolation, 002–004 absorb/rename work) plus plan-file additions; no unexplained source drift.
- Step 1: added `TestUndoRejectsFutureSnapshot` (engine: future snapshot with supported/nil current state, future nonnil current state, v0/v1 controls, malformed snapshot, no-mutation and no-Save assertions) plus cmd `TestUndoRejectsFutureCurrentState` and `TestUndoRejectsFutureSnapshotBeforeWorktreePreparation` (state/journal bytes, refs, worktree presence, cwd, and index all preserved). Pre-fix run failed as specified: all three future-schema cases returned nil error (undo proceeded); the worktree-ordering case errored through the shim path instead of the schema barrier.
- Step 2: factored a shared `decodeState` into `internal/stack/store.go` — `Load`, `Undo`, and the new `ValidateUndoState(data []byte) error` adapter all enforce the same barrier (malformed → plain parse error, version > schema → wrapped `ErrStateTooNew`, absent version → v0 legacy accepted, `Branches` always non-nil). `stack.Undo` validates the supplied current `State` and the snapshot before any git call or Save. `cmd.runUndo` returns `ErrStateTooNew` from the current-state load instead of taking the nil-state fallback (other load errors keep the existing snapshot recovery), and calls `stack.ValidateUndoState(entry.State)` before `prepareUndoCurrentCreatedWorktree` — so refusal happens before any chdir/worktree removal.
- Step 3: `go test ./cmd -run '^TestUndoRestoresSnapshotWhenCurrentStateIsMalformed$'` → PASS (malformed-current-state recovery unchanged); `go test ./internal/stack ./cmd -run 'Test.*(Undo|StateTooNew|Schema)'` → ok; `make test-fast` → ok; `make test` (race) → ok. One test-side fix during bring-up: the strict `create --worktree` JSON decoder needed the full emitted shape (`branch/parent/worktree/switched/summary`).
- `make ci` on commit `8e2e11c` in detached worktree `/private/tmp/st-ci-005` → exit 0 (golangci-lint v2.12.2 0 issues, vet native/windows/plan9, build, race tests, e2e, merged coverage 86.9% ≥ 75%). The first attempt failed on `golangci-lint not found` because the worktree shell lacked `$HOME/go/bin` on PATH; rerun with it passed — environmental, not a regression.
- `git diff --check` → exit 0; modified files all in Scope (`internal/stack/store.go`, `undo_op.go`, `undo_op_test.go`, `cmd/undo.go`, `commands_mutation_test.go`, `CHANGELOG.md`, plan files).
- Docs: `CHANGELOG.md` notes the refuse-before-mutating schema barrier for state and undo snapshots.

## Why this matters

Normal loading rejects metadata from a newer stacked version, but undo treats every load error as a recoverable missing/malformed state. Undo also decodes its saved snapshot without checking its version. An older binary can consequently delete branches/worktrees or overwrite metadata it does not understand.

## Current state

`cmd/undo.go:68` falls back on every load error:

```go
s, loadErr := stack.Load()
if loadErr == nil {
    env.Save = s.Save
} else {
    s = nil
    env.Save = func() error { return stack.RestoreState(entry.State) }
}
```

It then calls prepareUndoCurrentCreatedWorktree before stack.Undo. `internal/stack/undo_op.go:19` unmarshals entry.State and immediately proceeds to branch/worktree removal. `internal/stack/store.go:108` has the existing version guard:

```go
if s.Version > stateSchemaVersion {
    return nil, fmt.Errorf("%w (schema v%d; this st understands v%d) — upgrade st or check for a downgrade", ErrStateTooNew, s.Version, stateSchemaVersion)
}
```

Current schema is 1; legacy version 0 is accepted. Malformed-current-state recovery is intentional and covered by cmd/commands_mutation_test.go:TestUndoRestoresSnapshotWhenCurrentStateIsMalformed.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Use errors.Is(err, stack.ErrStateTooNew), not string matching. Preserve parse-error context and the existing nil-state snapshot recovery path. TestUndoCreateDeletesBranchAndRestoresHEAD in internal/stack/undo_op_test.go is the engine fixture pattern.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/stack ./cmd -run 'Test.*(Undo\|StateTooNew\|Schema)' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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

- `internal/stack/store.go`
- `internal/stack/store_test.go`
- `internal/stack/undo_op.go`
- `internal/stack/undo_op_test.go`
- `cmd/undo.go`
- `cmd/commands_mutation_test.go`
- `CHANGELOG.md`
- `plans/005-enforce-undo-schema-compatibility.md`
- `plans/README.md`


**Out of scope:** new schema versions, general corruption repair, changing undo's supported malformed-current-state recovery, journal format or deletion policy.

## Git workflow

Use a separate branch/worktree named `advisor/005-enforce-undo-schema-compatibility` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: enforce schema compatibility before undo`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Test both current-state and snapshot compatibility barriers

Add TestUndoRejectsFutureSnapshot in internal/stack/undo_op_test.go for nil and supported current state, with a snapshot version above stateSchemaVersion and a would-be-created branch/worktree. Assert no Git mutation and no Save callback. Add cmd TestUndoRejectsFutureCurrentState and TestUndoRejectsFutureSnapshotBeforeWorktreePreparation: prepare a valid undo journal then change only the relevant schema version in temporary metadata. Capture state/journal bytes, refs, worktree presence, cwd and index before invocation; all must remain unchanged after refusal. Include legacy version 0 and current version 1 controls.

**Verify:** `go test ./internal/stack ./cmd -run '^TestUndoRejectsFuture' -count=1` → new tests fail on the missing compatibility barrier before the fix, not on malformed test JSON

### Step 2: Share compatibility validation and place it before preparation

Factor state decoding/version checking in internal/stack/store.go so Load and Undo use the same guard while retaining their contextual error messages. Provide a narrowly scoped `ValidateUndoState(data []byte) error` adapter helper using that decoder; cmd must validate entry.State before prepareUndoCurrentCreatedWorktree. In cmd/undo.go return ErrStateTooNew from the current-state load instead of choosing the nil-state fallback; preserve the existing fallback for malformed/missing state. In stack.Undo independently validate the snapshot before Git calls or Save, and reject a supplied nonnil future-version current State as a defensive engine boundary. Keep branch-map initialization consistent for accepted legacy snapshots.

**Verify:** `go test ./internal/stack ./cmd -run '^TestUndoRejectsFuture' -count=1` → all schema refusals pass, wrap ErrStateTooNew where relevant, and perform no mutation or cwd preparation

### Step 3: Verify supported recovery remains available

Run the focused undo suite, especially TestUndoRestoresSnapshotWhenCurrentStateIsMalformed, plus all contributor checks. Add a changelog entry saying incompatible current state and undo snapshots require a compatible binary. Do not weaken the gate to recover from a future schema.

**Verify:** `go test ./cmd -run '^TestUndoRestoresSnapshotWhenCurrentStateIsMalformed$' -count=1` → PASS with existing recovery behavior; then `make ci` passes

## Test plan

- Future snapshot with nil/current supported state, future current state with supported snapshot, and future snapshot while cwd is a created worktree.
- Version 0 and 1 snapshots remain supported; malformed snapshot still errors before mutations.
- Use fake Git call history/Save counts plus real filesystem/ref snapshots; checking only the returned error is insufficient.

## Done criteria

- [ ] Future schemas return a wrapped ErrStateTooNew before checkout, branch/worktree removal, ref update, Save, DropUndo or cwd preparation.
- [ ] Supported malformed-current-state recovery and all existing undo tests pass.
- [ ] `make ci` passes.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- A new helper accidentally changes the malformed-current-state recovery contract.
- The command can reach prepareUndoCurrentCreatedWorktree before both compatibility checks complete.
- The proposed fix merely blocks RestoreState but allows earlier branch/worktree mutations.

## Maintenance notes

Every entry point that interprets serialized State must share compatibility rules. Version stamping during Save is not a compatibility check; it can erase evidence of an unsupported snapshot.


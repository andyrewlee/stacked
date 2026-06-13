# Plan 014: Cover `missingRestoredRef` (the package's only 0% function) and the corrupt-state.json load path

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/stack/undo_op.go internal/stack/store.go internal/stack/undo_op_test.go internal/stack/store_test.go`
> PR #33 edits `undo.go` (not `undo_op.go`) — unrelated. A mismatch in the
> excerpts below is a STOP condition.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW (test-only, plus at most one error-message improvement)
- **Depends on**: none
- **Category**: tests
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

Two small, sharp gaps. First: `missingRestoredRef` is the only 0%-covered
function in the stack package — and it is an *undo recovery fallback*, code
that runs precisely when a rename-undo is already in a weird state; a wrong
pick strands the user on a deleted branch mid-undo. Second: the behavior when
`state.json` is corrupt is unpinned — the undo journal got exactly this test
(corrupt-journal recovery) but the more critical state file did not; today a
corrupt file surfaces as a raw JSON parse error with no guidance.

## Current state

- `internal/stack/undo_op.go:163–176` (first-hand read):

  ```go
  // missingRestoredRef returns the single recorded ref whose branch is currently
  // missing, or "" when there is not exactly one.
  func missingRestoredRef(g Git, entry *UndoEntry) string {
      var missing []string
      for name := range entry.Refs {
          if !g.BranchExists(name) { missing = append(missing, name) }
      }
      sort.Strings(missing)
      if len(missing) == 1 { return missing[0] }
      return ""
  }
  ```

  Sole call site (`undo_op.go:62–66`): during undo of a `rename`, when the
  current branch was created by the undone command and
  `restoredRenameTarget(&prev, s, name)` returns `""` (its fallthrough: every
  snapshot branch is still tracked in the current state — see
  `undo_op.go:140–160` for `restoredRenameTarget`), the checkout target after
  restore comes from `missingRestoredRef`.
- `internal/stack/store.go:57–77` — `Load()`:

  ```go
  var s State
  if err := json.Unmarshal(data, &s); err != nil {
      return nil, fmt.Errorf("parse state file: %w", err)
  }
  ```

  No recovery hint. The exemplar for what "good" looks like:
  `internal/stack/undo_test.go:122`'s corrupt-journal test (read it for the
  structure) and the journal's tolerant load.
- Test exemplars: `internal/stack/undo_op_test.go` (fake-git undo restore
  tests; `newEnvState`/`mkBranch` helpers), `internal/stack/store_test.go`
  (Load/Save round-trip tests).
- Exit-code contract (docs/AGENT.md): a corrupt state file is a generic
  failure → exit 1, `error.code: "error"` under `--json`. Not exit 3
  (`not_initialized` is only for a *missing* file) — keep that distinction.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| Targeted | `go test ./internal/stack -run 'MissingRestoredRef|CorruptState' -count=1 -v` | PASS |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/undo_op_test.go`, `internal/stack/store_test.go`
(new tests); `internal/stack/store.go` ONLY for the one-line error-message
improvement in Step 3; `e2e/e2e_contract_test.go` (one black-box case).

**Out of scope**: `missingRestoredRef`'s logic and the rename-target
derivation (`undo_op.go:57`'s comment marks deriving the target from the
state diff as a known follow-up — do not attempt it here); any auto-recovery
of corrupt state files.

## Git workflow

- Branch: `undo-fallback-and-corrupt-state-tests`
- Commit message style: `test: cover the rename-undo fallback and corrupt state load`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Unit-test `missingRestoredRef` directly

In `undo_op_test.go`, table test with a fake git: (a) exactly one recorded
ref missing → returns it; (b) zero missing → ""; (c) two missing → "";
(d) determinism: map-ordered refs with one missing always return the same
name. Construct `UndoEntry{Refs: map[string]string{...}}` by hand; delete
branches from the fake to create "missing".

**Verify**: `go test ./internal/stack -run TestMissingRestoredRef -count=1 -v` → PASS.

### Step 2: Drive the call site through `Undo`

Add an integration-style fake-git test reaching `undo_op.go:62`: snapshot a
`rename` entry whose state diff is ambiguous so `restoredRenameTarget`
returns "" — per its code, that means every `prev.Branches` name is still
tracked in the current state (e.g. rename where the snapshot and current
track the same names but a ref branch is missing). Work backward from
`restoredRenameTarget`'s exact conditions (excerpt above); if you cannot
construct a state where it returns "" while the fallback returns non-empty,
that is itself a finding (the fallback may be unreachable) — see STOP.
Assert the post-undo current branch is the fallback's pick.

**Verify**: `go test ./internal/stack -run TestUndoRenameFallback -count=1 -v` → PASS
(or the STOP finding is filed).

### Step 3: Pin corrupt-state behavior

- `store_test.go`: write garbage (`{not json`) to the state path of an
  initialized temp repo, assert `Load()` returns an error that (a) wraps the
  parse failure and (b) mentions the file path or a recovery hint. To make
  (b) true, improve the one error line in `store.go`:
  `fmt.Errorf("parse state file %s (fix or delete it and re-run st init): %w", path, err)`
  — adjust wording to taste, keep it one line.
- `e2e/e2e_contract_test.go`: black-box case — corrupt
  `.git/stacked/state.json`, run `st log`, assert exit 1 (not 3, not 70) and
  stderr mentions the state file; with `--json`, assert `error.code` is
  `"error"` (follow `TestJSONError` at e2e_contract_test.go:286 as the
  pattern).

**Verify**: `go test ./internal/stack -run TestLoadCorrupt -count=1 -v` and
`go test ./e2e -run TestCorruptState -count=1 -v` → PASS.

### Step 4: Full gate

**Verify**: `make ci` → exit 0.

## Test plan

Steps 1–3 enumerate every new test. Pattern sources: `undo_op_test.go`
existing restore tests, `undo_test.go:122` corrupt-journal test,
`TestJSONError` e2e.

## Done criteria

- [ ] `go tool cover` over the stack package shows `missingRestoredRef` > 0% (run `go test ./internal/stack -coverprofile=/tmp/c.out && go tool cover -func=/tmp/c.out | grep missingRestoredRef`)
- [ ] Corrupt-state tests pass at both layers (unit + e2e)
- [ ] `make ci` exits 0
- [ ] Only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- Step 2's shape proves unconstructible — i.e. whenever the call site fires,
  `restoredRenameTarget` already returned non-empty. Then the fallback is
  dead code: report it (with your construction attempts) as a finding for the
  index instead of forcing a test.
- The corrupt-state e2e case exits 70 (a recovered panic) — that means Load's
  error path panics somewhere upstream; report it, don't patch beyond the
  one-line message change.

## Maintenance notes

- `undo_op.go:57`'s comment promises deriving the rename target from the
  state diff; when that lands, Step 1/2's tests define the fallback contract
  it must preserve.
- Reviewer: check the corrupt-state error stays exit 1 and never became 3 —
  agents branch on that distinction (docs/AGENT.md).

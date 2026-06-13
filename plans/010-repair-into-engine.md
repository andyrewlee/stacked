# Plan 010: Move `st repair` into the engine so the invariant tests cover it

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- cmd/repair.go internal/stack/ e2e/e2e_journey_test.go`
> PRs #29–34 touch engine tests and undo files — known drift; reconcile with
> live code. A mismatch in the repair.go excerpts below is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED — repair handles the corrupt shapes (missing branches,
  invalid parents, cycles); a behavior change here hurts exactly when users
  are already in trouble. The mitigation is the point of the plan: engine
  placement puts it under the model/invariant tests.
- **Depends on**: PRs #29–34 merged
- **Category**: tech-debt
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

`st repair` is the only mutation implemented outside the engine: its logic
lives in a closure in `cmd/repair.go`, calling `internal/git` directly and
editing `state.Branches` by hand. Consequences: the model/invariant test
(`internal/stack/model_test.go`), which validates every engine operation
against acyclicity/parent-validity/base-containment after thousands of random
sequences, never sees repair — the one operation whose whole job is fixing
broken topology is the one operation those tests can't check. It also can't be
exercised by the fast fake-git suite, and it bypasses any port-level policy
(e.g. plan 009's batching, plan 004's hardening — whatever lands at the
boundary). Moving it into `internal/stack` follows the architecture every
other command already uses.

## Current state

- `cmd/repair.go:39–101` — the mutation closure inside `mutateState("repair",
  asJSON, func(_ stack.Env, s *stack.State) error {...})`. Note it **ignores
  the `stack.Env` parameter** and calls package `git` directly:
  - `git.BranchExists(s.Trunk)` guard; `git.RevParse(s.Trunk)` for the trunk
    tip.
  - Per sorted branch: missing-branch case (untrack + re-parent children,
    `repair.go:55–80`), invalid-parent case (re-parent onto trunk with
    `repairedParentSHA`, `:82–88`), cycle case (`cyclePath`, `:90–95`).
  - Helpers `repairedParentSHA` and `cyclePath` live in `cmd/` (find them with
    `grep -n "func repairedParentSHA\|func cyclePath" cmd/*.go`).
- The engine convention to match (`CLAUDE.md` "How to add a command"):
  `func Repair(env Env, s *State) (*OpResult, error)` in `internal/stack`,
  taking everything through `env.Git`, returning fixes via
  `OpResult{Summary, Notes}` — look at how `PruneMerged`
  (`internal/stack/engine.go:754`) reports and checkpoints.
- The cmd adapter pattern to end with: see `cmd/restack.go` or any thin
  command — parse flags → `mutate(label, asJSON, fn)` → render. repair's
  current JSON payload (`cmd/repair.go:105–119`, fields `Repaired`, `Fixes`)
  must keep its shape.
- Port coverage check: the closure uses `BranchExists`, `RevParse`,
  `MergeBase` — all already on the `Git` port (`internal/stack/git.go:7–35`).
  No port additions needed.
- Tests pinning today's behavior: `e2e/e2e_journey_test.go:566`
  `TestValidateRepairDrift`, plus any repair cases in `cmd/` tests
  (`grep -rn "repair" cmd/*_test.go`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| Repair e2e | `go test ./e2e -run TestValidateRepairDrift -count=1` | PASS |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: new `internal/stack/repair.go` + `internal/stack/repair_test.go`,
`cmd/repair.go` (shrinks to a thin adapter), moving `repairedParentSHA` /
`cyclePath` into the stack package, `internal/stack/model_test.go` (extend the
random model to occasionally corrupt-then-Repair and assert invariants).

**Out of scope**: changing what repair *fixes* (no new fix classes — pure
move), the JSON payload shape, `cmd/validate.go` (reads only; a separate
concern), nav commands' direct git reads (DEBT-03's benign remainder — note
them in the commit message if touched accidentally, i.e. don't).

## Git workflow

- Branch: `repair-into-engine`
- Commit message style: `refactor: move repair into the engine; cover it with the invariant model`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Move the logic verbatim

Create `internal/stack/repair.go` with `func Repair(env Env, s *State)
(*OpResult, error)` containing the closure body, `git.X(...)` →
`env.Git.X(...)`, plus `repairedParentSHA` and `cyclePath` moved in (delete
the cmd copies). Return `&OpResult{Summary: <existing summary wording>,
Notes: fixes}`. Behavior-identical: same iteration order (sorted names), same
fix wording (the e2e test may assert on it).

**Verify**: `go build ./...` → exit 0.

### Step 2: Shrink the adapter

`cmd/repair.go` becomes the standard thin shape: flags → `mutate("repair",
asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) { return
stack.Repair(env, s) })` — but first read how the current file maps fixes into
its JSON payload and reproduce that mapping from `OpResult.Notes` so the
`{Repaired, Fixes}` JSON is byte-identical.

**Verify**: `go test ./cmd/... -race -count=1` → exit 0;
`go test ./e2e -run TestValidateRepairDrift -count=1` → PASS.

### Step 3: Fake-git unit tests

`internal/stack/repair_test.go` using `newEnvState()`/`mkBranch`
(exemplar: `engine_test.go`): one test per fix class — missing branch
(delete a branch in the fake, run Repair, assert untracked + children
re-parented), invalid parent, cycle (hand-wire `s.Branches` into a cycle).
End each with `checkInvariants(t, f, s, 0)` (`model_test.go:258`).

**Verify**: `go test ./internal/stack -run TestRepair -count=1 -v` → PASS.

### Step 4: Teach the random model about repair

In `model_test.go`, add a low-probability "corrupt then Repair" step to the
random op sequence (e.g. delete a random tracked branch behind the engine's
back via the fake, then call `Repair`), asserting invariants after — follow
how the existing conflict step (`model_test.go:188–212`) injects adversity.

**Verify**: `make test-fast` → exit 0 (model test still finishes in
milliseconds-to-seconds).

### Step 5: Full gate

**Verify**: `make ci` → exit 0.

## Test plan

Steps 3–4 above; plus the existing `TestValidateRepairDrift` e2e and cmd
repair tests must pass unchanged (they pin the user-visible contract).

## Done criteria

- [ ] `grep -c "git\." cmd/repair.go` → 0 (no direct internal/git calls remain)
- [ ] `internal/stack/repair.go` exists; `go test ./internal/stack -run 'TestRepair|TestModel' -count=1` exits 0
- [ ] `go test ./e2e -run TestValidateRepairDrift -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] Only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- Any pinning test fails on fix *wording* or JSON shape — adjust the move to
  preserve output, not the tests.
- The model test surfaces an invariant violation in repair's existing logic —
  that is a real bug found; report it with the failing seed instead of
  patching repair's semantics inside this refactor.
- Repair turns out to need a git method not on the port.

## Maintenance notes

- After this, plan 009's batching can be applied to Repair's per-branch
  `BranchExists` calls via one `Tips()` map (PERF-06) — deliberately deferred
  to keep this a pure move.
- Reviewer: diff the moved logic against the old closure side-by-side; the
  only acceptable differences are `git.` → `env.Git.` and the OpResult return.

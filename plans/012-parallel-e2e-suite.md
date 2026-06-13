# Plan 012: Run the hermetic e2e suite in parallel

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- e2e/`
> PR #31 adds e2e tests (TestOntoConflictContinue/Abort) and plan 013 may add
> more — expected drift: new `Test*` functions also need the `t.Parallel()`
> treatment. Anything structural (harness changes in `e2e_test.go`) is a STOP
> condition.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW — every e2e test is hermetic by construction.
- **Depends on**: PR #31 merged (so its new tests get the same treatment)
- **Category**: perf (CI wall-clock)
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

The e2e suite is the wall-clock-dominant test layer — every test spawns the
`st` binary and real git dozens of times — and it runs fully serially: there
is not a single `t.Parallel()` in the repo. Each test is already independently
sandboxed (own `t.TempDir`, own HOME, subprocess launched with `cmd.Dir` set —
no `os.Chdir` anywhere in the package), so parallelizing is mechanical and
cuts the e2e leg of `make ci` roughly by the core count.

## Current state

- `e2e/e2e_test.go:29` — `TestMain` builds the shared `st` binary once into a
  temp dir (read lines 29–65) — read-only shared state, parallel-safe.
- The harness (`e2e_test.go:~140–200`): `newRepo(t)` creates a per-test
  `t.TempDir` with its own HOME; `r.st(...)` runs the binary with `cmd.Dir =
  r.dir` and `cleanEnv(home)` — no process-global state.
- Test inventory at `ed9f2f7` (33 tests): `e2e_journey_test.go` —
  TestWorktreeSharesStackState(17), TestUndoAfterConflictAbort(41),
  TestLifecycle(64), TestConflictContinue(203), TestConflictAbort(236),
  TestSyncConflictContinue(265), TestModifyJSONRestacksDescendants(294),
  TestModifyMessageReword(322), TestFold(341), TestSquash(359), TestOnto(383),
  TestRename(409), TestDeleteReparent(439), TestUndo(471),
  TestTrackUntrack(502), TestRestackGuards(541), TestValidateRepairDrift(566),
  TestSyncPrunesMerged(591), TestSyncNoRemote(633), TestRestackDryRun(643);
  `e2e_contract_test.go` — TestVersion(14), TestHelpListsAllCommands(28),
  TestUnknownCommand(62), TestFlagErrorStaysOnStderr(90),
  TestNavigationEdges(105), TestExitCodeContract(156),
  TestSubmitDryRunAndURL(194), TestSubmitRealPushSetsUpstream(220),
  TestCompletion(254), TestUninitialized(276), TestJSONError(286),
  TestGuide(294). (Plus any added since — treat them identically.)
- `Makefile` — `e2e: go test ./e2e/... -count=1` (default `-parallel` is
  GOMAXPROCS; usually fine as-is).
- `scripts/cover.sh:27` — the e2e leg runs with `GOCOVERDIR`; covdata
  handles concurrent writers (one file per process), parallel-safe.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| e2e timed | `time make e2e` | exit 0; record before/after wall time |
| Race+cover leg | `make ci` | exit 0 |

## Scope

**In scope**: `e2e/e2e_journey_test.go`, `e2e/e2e_contract_test.go` (a
`t.Parallel()` first line in each `Test*` function), `Makefile` only if a
`-parallel` bump is demonstrably needed.

**Out of scope**: `cmd/` tests — they use `t.Chdir`, which **panics** under
`t.Parallel()`; do not touch them. `e2e/e2e_test.go` harness internals.

## Git workflow

- Branch: `parallel-e2e-suite`
- Commit message style: `perf: parallelize the hermetic e2e suite`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Record the baseline

`time make e2e` three times; note the median wall time in the commit message.

### Step 2: Add `t.Parallel()` to every e2e test

First statement of every `Test*` function in both files (not `TestMain`).
Mechanical check that none was missed:

**Verify**:
`grep -c '^func Test' e2e/e2e_journey_test.go e2e/e2e_contract_test.go` equals
`grep -c 't.Parallel()' e2e/e2e_journey_test.go e2e/e2e_contract_test.go`.

### Step 3: Shake out hidden coupling

Run the suite five times (`go test ./e2e -count=1` ×5, at least once with
`-race` if the harness compiles with it — e2e drives subprocesses, so race
mode matters little; plain repetition is the flake detector here).

**Verify**: 5/5 green. Then `time make e2e` → wall time materially below the
Step 1 baseline (expect ≥2× on a multi-core machine).

### Step 4: Full gate

**Verify**: `make ci` → exit 0 (the covdata merge in `scripts/cover.sh` must
still report the same coverage ballpark — check the printed total).

## Test plan

No new tests; Steps 2–3 are the verification. Flakes found here are real
isolation bugs worth reporting (STOP condition), not retry fodder.

## Done criteria

- [ ] Every `Test*` in `e2e/` starts with `t.Parallel()` (grep counts match)
- [ ] `go test ./e2e -count=1` passes 5 consecutive runs
- [ ] `make ci` exits 0
- [ ] Only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- Any test fails under parallelism that passes serially — that is a real
  shared-state bug (most likely a fixed port number or a shared remote dir in
  one of the submit tests — `TestSubmitRealPushSetsUpstream` uses a local bare
  repo; check it's under the test's own TempDir). Report the test and the
  shared resource; do not just remove its `t.Parallel()` silently — removing
  it is acceptable only WITH a comment explaining the coupling.
- Coverage total drops by more than a point (covdata merge anomaly).

## Maintenance notes

- New e2e tests must start with `t.Parallel()`; cheap to enforce in review.
- If the suite ever gains a test that genuinely cannot be parallel, the
  comment convention from the STOP section is the mechanism.

# Plan 020: Remaining spawn optimizations — `st log` ancestry batching, parallel probes, restack tip refresh, journal write measurement

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- cmd/log.go internal/stack/ops_track.go internal/stack/ops_delete_sync.go internal/stack/restack.go internal/stack/undo.go internal/stack/undo_preview.go`
> On mismatch, STOP.

## Status

- **Priority**: P3
- **Effort**: M
- **Risk**: MED (concurrency introduces real risk: bounded workers, deterministic ordering, error aggregation)
- **Depends on**: none hard; Plan 011 covers the memoization side (land either order; this plan is the batching/parallelism side)
- **Category**: perf
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Four smaller perf items Plan 011 didn't absorb:

1. `st log` (`cmd/log.go:259-281` `tipAncestors`) runs one `git merge-base --is-ancestor` spawn per distinct tip pair — ~10 extra processes on a 10-branch stack, serialized into the most-run read command.
2. Independent probes run serially: `inferParentPick`'s `IsAncestor` tie-breaks (`ops_track.go:299-330`), `mergedBranches`' `ChangesContainedIn` diffs (`ops_delete_sync.go:466+`), `trackAllPlan`'s per-branch `MergedInto`+`MergeBase` (`ops_track.go:102-118`). All read-only, pairwise independent — embarrassingly parallel.
3. Restack refreshes each rebased internal node's tip with a `RevParse` spawn; `RebaseOnto` already knows the result — the value just isn't threaded back.
4. Journal/state fsync-writes multiply on `prune`/`sync` (per-branch checkpoints) — worth measuring under `ST_DEBUG` before optimizing.

## Current state

- `cmd/log.go:259-281` — `tipAncestors` loops `s.Branches` calling `git.IsAncestor` per distinct `(tip, parentTip)` pair; the comment at 255-258 documents a deliberate choice not to materialize history — a bounded `rev-list --parents` preserves that.
- `internal/stack/ops_track.go:299-330` — `inferParentPick` serial `IsAncestor` tie-breaks; `ops_track.go:102-118` serial per-branch probes.
- `internal/stack/ops_delete_sync.go:466+` — `mergedBranches` serial `ChangesContainedIn` (a `git diff`) per unmerged branch.
- `internal/stack/restack.go` — post-rebase tip refresh via `RevParse` (~per internal node); `restackBranch`/`Restack`/`restackForest` open with `CurrentBranch`+`TipsFor`.
- `internal/stack/undo.go:297-371` — `RecordUndo` writes `undo.json`, `FinalizeUndo` reads+rewrites it; `ops_delete_sync.go` checkpoints `env.save()` per deleted branch.
- Convention: fakeGit `callsSnapshot()` tracks spawn counts; `ST_DEBUG=1` traces every spawn live (added in plan 029) — use it to measure.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Log tests | `go test ./cmd -run 'Log'` | all pass |
| Timing | `ST_DEBUG=1 ./st log` on a multi-branch fixture | fewer spawns visible |
| Race check | `go test -race ./internal/stack` | clean |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `cmd/log.go`, `internal/stack/ops_track.go`, `internal/stack/ops_delete_sync.go`, `internal/stack/restack.go`, `internal/stack/git.go` (port additions if needed), `internal/stack/fakegit_test.go` (fake must model any new port shape), `internal/git/*.go` (new batched probe methods).

**Out of scope**: `cmd/gitenv.go`/`worktree_cache.go` (Plan 011), undo-preview probe hoisting (Plan 010 folds PERF-04), `cover.sh`/`Makefile` (Plan 012).

## Git workflow

- Branch: `advisor/020-remaining-perf`; imperative commits per item; no push/PR unless instructed.

## Steps

### Step 1: Batch `st log`'s ancestry probes

Replace `tipAncestors`'s per-pair `IsAncestor` with ONE `git rev-list --parents <tip1> <tip2> …` over the rendered tips — parse the parent lists into a reachability check in-process (a `(child → parent)` pair is answered by walking the DAG). Alternative if the DAG is too heavy: bound-parallelize the existing `IsAncestor` calls (`errgroup`-style — but no external deps allowed: spawn N goroutines with a semaphore channel + `sync.WaitGroup`, collect into a map under a mutex). The comment's constraint ("don't materialize the whole history") means `--max-count`/boundary-bound the rev-list, OR pick the parallel-probe variant — simpler and provably bounded.

**Recommendation**: parallelize the existing probes first (smaller diff, no history materialization); only go to `rev-list` if spawn overhead still dominates.

**Verify**: `go test ./cmd -run 'Log'` → pass; `ST_DEBUG=1 ./st log` on a 10-branch fixture shows parallel probes (interleaved traces) or 1 rev-list.

### Step 2: Bound-parallelize the independent probe loops

`inferParentPick`, `mergedBranches`, `trackAllPlan`'s probe loops: fan out with a worker bound (`min(GOMAXPROCS, 8)`), collect into a pre-sized map, then run the existing deterministic pass over the collected results (sorted iteration already exists). Errors aggregate in deterministic order (sorted by branch name).

**Critical**: fakeGit must be safe under concurrent probe calls — check whether its map accesses are mutex-guarded; if not, either guard the fake's reads or run the parallel fan-out only in the Shell-backed path (risky divergence — prefer guarding the fake).

**Verify**: `go test -race ./internal/stack` → clean; `make test-fast` → pass.

### Step 3: Thread the post-rebase tip back instead of re-resolving

`restackAgainstTips`/`restackBranchWith` re-resolve a rebased branch's tip via `RevParse`. If the port's `RebaseOnto` can return the new tip (a `rev-parse` is already inside its success path — or the rebase itself writes the ref, so `RevParse` on the just-written ref is the same cost), thread it through; if the port method would need a signature change to expose it, that's fine (it's our port). Apply to the tips-map write rather than a second spawn.

**Verify**: `go test ./internal/stack -run 'Restack'` → pass; one fewer `rev-parse` per internal node under `ST_DEBUG=1`.

### Step 4: Measure journal/state write cost (investigate-only)

Under `ST_DEBUG=1` or a small benchmark: measure `RecordUndo`+`FinalizeUndo` fsync time and the per-branch `env.save()` checkpoint cost in `applyPrune`. If material (say >10ms/op on representative hardware), consider coalescing journal annotate+finalize into one write — the checkpoint granularity is a recovery feature, so batch ONLY where safe (e.g. checkpoint every N deletes or on error). If immaterial, document the measurement and drop the item — that's a legitimate outcome.

**Verify**: measurement recorded in the commit message or a comment; `make ci` → pass.

## Test plan

- `ST_DEBUG=1` spawn-count diffs are the observable proof — write a test asserting spawn counts where a fake seam exists (`f.callsSnapshot()`).
- `-race` on the parallelized loops is mandatory.
- Verification: `go test -race ./...` → clean; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test -race ./internal/stack ./cmd` clean
- [ ] `st log` on an N-branch fixture issues ≤1 spawn per tip-pair batch (or measurably fewer)
- [ ] The parallelized probe loops preserve deterministic result ordering
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- Fake git isn't safe under concurrent calls and guarding it cascades — guard it (fake is test infra) or scope parallelism to the shell path ONLY if the divergence is documented; prefer guarding.
- `rev-list --parents` can't bound history enough to satisfy the "don't materialize" constraint — use the parallel-probe variant; the constraint is a stated design choice.
- A probe loop's ordering is load-bearing (results feed a later decision mid-loop) — sequentialize that loop; not all are candidates.
- `RebaseOnto` can't expose the new tip without an awkward signature — keep the `RevParse`, note the failed simplification.

## Maintenance notes

- Plan 011 handles the memoization half; together they're the spawn-dedup story — reviewer should see both.
- The `-race` gate is now load-bearing for the parallel loops — don't let anyone weaken it.
- Deferred: undo-journal write coalescing if Step 4's measurement says material — checkpoint granularity is a recovery feature; batch conservatively.

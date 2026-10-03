# Plan 012: Parallelize the `make ci` / `cover.sh` legs

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- Makefile scripts/cover.sh`
> On mismatch, STOP.

## Status

- **Priority**: P3
- **Effort**: S–M
- **Risk**: LOW–MED (log interleave must stay readable; legs must not share mutable state)
- **Depends on**: none
- **Category**: perf / dx
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`make ci` runs every leg serially — check-deps → fmt-check → vet → vet-cross → build → lint → the two `go test` invocations inside `cover.sh` (in-process with `-race`, then e2e with `GOCOVERDIR`) → check-install. The two heavy test legs are independent (disjoint `GOCOVERDIR`s, disjoint packages, one binary build) and so are the read-only check legs. On a multicore box, running them concurrently plausibly cuts gate wall time 20–40% — the feedback loop the project optimizes for gets faster for free.

## Current state

- `Makefile:22-35` (approx) — `ci` prerequisites run in declaration order; `make -j` isn't part of the documented workflow (`.DEFAULT_GOAL := ci`, `make ci` invoked plainly).
- `scripts/cover.sh:23-37` — runs `go test -race ./...` (writes covdata for in-process packages) THEN `go test ./e2e` (coverage-instrumented binary writes to a separate `GOCOVERDIR`) — sequential; each is minutes.
- `e2e/e2e_test.go` — `TestMain` builds the coverage binary once; `parallel_meta_test.go` enforces `t.Parallel()` on all ~107 tests — the e2e suite itself is already internally parallel.
- `Makefile` `test`/`e2e`/`golden` have timeouts; `check-*` legs are read-only greps/sed — safe to run concurrently.
- Constraint: `cover.sh` merges the two covdata dirs afterward; they must remain disjoint.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Timing before | `time make ci` | baseline (record) |
| Cover direct | `./scripts/cover.sh` | green, coverage ≥ floor |
| Gate | `make ci` | exit 0 |
| Timing after | `time make ci` | measurable drop |

## Scope

**In scope**: `Makefile` (ci aggregate/ordering), `scripts/cover.sh` (parallelize the two `go test` invocations).

**Out of scope**: e2e test internals (already parallel), `check-*` script internals, `.githooks/*` (they call `make ci` — free win), CI config (none exists).

## Git workflow

- Branch: `advisor/012-parallel-ci`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Parallelize `cover.sh`'s two legs

In `scripts/cover.sh`, launch the in-process `-race` coverage run and the e2e `GOCOVERDIR` run concurrently — each writes its own covdata dir:

```sh
go test -race -covermode=atomic -coverprofile="$INCIDENT_COVDIR/..." ./internal/... ./cmd/... & pid1=$!
go test ./e2e ... GOCOVERDIR="$E2E_COVDIR" ... & pid2=$!
wait $pid1; s1=$?
wait $pid2; s2=$?
```

Match the script's actual structure — read it first; the covdata dirs, package globs, and merge step are already there. Prefix each leg's output (or redirect each to a log and dump on failure) so interleaved `go test` output stays readable — e.g. `> "$tmp/inproc.log" 2>&1` then `cat` on failure only. `set -e`/`trap` cleanup must cover both dirs (the existing trap does).

**Verify**: `./scripts/cover.sh` → exits 0, coverage total printed, both legs reported; `ls` the covdata dirs during a run confirms disjoint writes.

### Step 2: Group the independent read-only Makefile legs

`make ci` runs prerequisites serially even under `make -j` when the developer doesn't pass `-j`. Two options:
(a) Document `make -j ci`/`MAKEFLAGS` — cheap but relies on the caller.
(b) Preferred: split `ci` into an `ci-fast` aggregate (`check-deps fmt-check vet vet-cross build lint check-shell` — all read-only, `-j`-safe) invoked as a single recipe `$(MAKE) -j ci-fast`, then `cover check-install` after. Under GNU make the recursive `$(MAKE) -j` parallelizes the aggregate internally regardless of the caller's flags. Keep `ci` as the single entry — pre-push/CI untouched.

Check that no leg writes shared state: `check-*` are greps/seds (read-only); `fmt-check`/`vet`/`vet-cross`/`lint` read-only; `build` writes `st` — run it in the serial portion or accept it's the only writer. `cover` must stay serial-after-build (it builds the e2e binary).

**Verify**: `make ci` → exit 0; `make -j ci` → exit 0; `make -n ci` → shows the aggregate recipe.

### Step 3: Measure + document

Run `time make ci` before and after; record the delta in the commit message. Update `CONTRIBUTING.md`'s ci description only if it documents ordering assumptions ("legs run in order") — check.

**Verify**: `make ci` green twice consecutively (flakiness check on the parallel legs).

## Test plan

- No new tests — this is build plumbing. The test is `make ci` itself, run repeatedly.
- Verification: `time make ci` before/after; `make ci` twice green; `./scripts/cover.sh` standalone green.

## Done criteria

- [ ] `make ci` exits 0
- [ ] `scripts/cover.sh` runs its two `go test` invocations concurrently (visible in the script source; both covdata dirs still merge)
- [ ] The read-only Makefile legs run under `$(MAKE) -j` or an equivalent aggregate
- [ ] Wall-time drops measurably (recorded in the commit message)
- [ ] `make ci` green twice in a row
- [ ] `plans/README.md` row updated

## STOP conditions

- The e2e coverage run and the race run share a covdata dir or a build artifact path — if disjointness can't be preserved cheaply, STOP.
- Parallel output becomes unreadable without log capture — implement the log-buffering in Step 1 or STOP.
- A `check-*` leg turns out to write state (none should — verify by `git status` after a run; if it dirties the tree, STOP).
- The gate gets slower or flakes — revert; parallelism that costs stability is a loss.

## Maintenance notes

- The recursive-make aggregate is the part a reviewer should scrutinize — `$(MAKE) -j` inside a recipe is correct but subtle; comment it.
- If a leg ever needs serialization (e.g. `build` before `cover`), keep it in the serial tail — document why.
- Deferred: timing/benchmark script for the gate itself.

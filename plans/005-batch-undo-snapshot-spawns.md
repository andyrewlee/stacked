# Plan 005: Cut the per-mutation undo-snapshot cost from N+3 git spawns to 2

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/stack/undo.go internal/stack/git.go internal/stack/fakegit_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: perf
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

Every mutating command (`create`, `modify`, `restack`, `sync`, …) records an
undo snapshot before running. `SnapshotUndo` resolves the trunk and **each
tracked branch with its own `git rev-parse` spawn**, then spawns
`LocalBranches` and `CurrentBranch` — N+3 subprocesses of pure bookkeeping
before the command does anything. On a 15-branch stack that's ~17 spawns
(~0.2–0.8s) of fixed latency per command, and the journal-finalize path
(`refsUnchanged`, `createdBranchesSince`) re-spawns per recorded ref again.
The port already has the batched primitive — `Tips()`, one `for-each-ref`
spawn returning every local branch tip — used by `st log`/`st validate` since
commit `eccbe8f`. This plan applies it to the undo paths: N+3 → 2 spawns per
mutation, no behavior change.

## Current state

- `internal/stack/undo.go:80–100` — `SnapshotUndo`:

  ```go
  refs := map[string]string{}
  if sha, err := g.RevParse("refs/heads/" + s.Trunk); err == nil {
      refs[s.Trunk] = sha
  }
  for name := range s.Branches {
      if sha, err := g.RevParse("refs/heads/" + name); err == nil {
          refs[name] = sha
      }
  }
  localBranches, _ := g.LocalBranches()
  currentBranch, _ := g.CurrentBranch()
  ```

  Note the semantics to preserve: a branch that doesn't resolve is simply
  *absent* from `refs` (no error), and `LocalBranches`/`CurrentBranch` failures
  degrade to zero values.
- `internal/stack/undo.go:275–283` — `refsUnchanged(g, entry)`: one
  `g.RevParse("refs/heads/"+name)` per recorded ref; missing or moved → false.
- `internal/stack/undo.go:249–267` — `createdBranchesSince(g, entry)`: calls
  `g.LocalBranches()` (one spawn — fine, but if you already fetched Tips in
  the caller, reuse follows naturally; only change it if the call site shares
  a Tips map cleanly, otherwise leave it).
- `internal/stack/git.go:12` — the port already declares
  `Tips() (map[string]string, error)`.
- `internal/git/git.go:120–135` — production `Tips()`: one
  `git for-each-ref --format='%(refname) %(objectname)' refs/heads` spawn;
  map keys are bare branch names.
- `internal/stack/fakegit_test.go:110` — the fake git implements `Tips()`.
- `internal/git/git.go:99–106` — `LocalBranches()` is also a `for-each-ref`
  over `refs/heads`, so Tips' key set is exactly the same name set
  (for-each-ref sorts by refname; `LocalBranches` order is therefore
  lexicographic — match it by sorting Tips' keys).
- `UndoEntry` (same file, around line 93) persists `Refs`, `LocalBranches`,
  `CurrentBranch` to `undo.json` — the on-disk shape must not change.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| Undo tests | `go test ./internal/stack -run Undo -count=1` | exit 0 |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope** (the only files you should modify):
- `internal/stack/undo.go`
- `internal/stack/undo_test.go` (only if a new assertion is added)

**Out of scope**:
- `internal/stack/git.go` (the port — `Tips` already exists; add nothing)
- `internal/git/` (production impl — unchanged)
- `internal/stack/fakegit_test.go` (fake — unchanged)
- The other spawn-heavy paths found in the same audit (`PruneMerged`,
  `inferParent`, `stackedDir` memoization, batched push) — each has its own
  trade-offs and is listed separately in plans/README.md; do not fold them in.

## Git workflow

- Branch: `batch-undo-snapshot-spawns`
- Commit message style (match `eccbe8f`): `perf: one for-each-ref spawn for undo snapshots and finalize checks`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Rewrite `SnapshotUndo` over `Tips()`

Replace the per-branch loop, `LocalBranches`, with:

- `tips, err := g.Tips()` — on error, fall back to the zero-value behavior the
  current code has for `LocalBranches` failure: keep `refs` from whatever can
  be resolved. Simplest faithful translation: on Tips error, keep the old
  per-ref RevParse loop as the fallback OR degrade to empty refs/branches.
  **Choose the former only if you keep it small; otherwise degrade to empty —
  but check first what `FinalizeUndo`/`Undo` do with empty `Refs`** (read
  `undo.go` around the `refsUnchanged` call sites at lines ~193 and ~240). The
  current code can already produce a partially-empty `refs` map (every
  RevParse can fail), so empty-on-error is within today's contract.
- `refs` = `{name: tips[name]}` for trunk + each `s.Branches` key present in
  `tips`.
- `localBranches` = sorted keys of `tips` (sort with `sort.Strings` to match
  for-each-ref's lexicographic order).
- Keep the single `g.CurrentBranch()` call.

**Verify**: `go test ./internal/stack -run Undo -count=1` → exit 0, and
`make test-fast` → exit 0.

### Step 2: Rewrite `refsUnchanged` over `Tips()`

One `g.Tips()` call; for each `name, want` in `entry.Refs`, the entry is
unchanged iff `tips[name] == want` (a missing key means the branch is gone →
return false, same as today's RevParse-error path). On Tips error, return
false (conservative: today, a RevParse error also yields false).

**Verify**: `go test ./internal/stack -run Undo -count=1` → exit 0.

### Step 3: Assert the spawn reduction

The fake git records rebases/conflicts but has no spawn counter, so assert
structurally instead: add a small test in `undo_test.go` (model it on the
file's existing table style) that wraps the fake in a counting decorator —
a struct embedding the `Git` interface that increments a counter in
`RevParse` and `Tips` — and asserts `SnapshotUndo` on a 5-branch state calls
`RevParse` **0 times** and `Tips` **once**. Keep the decorator local to the
test file.

**Verify**: `go test ./internal/stack -run TestSnapshotUndoSpawns -count=1 -v` → PASS

### Step 4: Full gate

**Verify**: `make ci` → exit 0 (the race suite and e2e exercise the real
`Tips` against real git; e2e undo journeys in `e2e/e2e_journey_test.go` cover
the end-to-end behavior).

## Test plan

- Existing coverage that pins behavior: `internal/stack/undo_test.go` (corrupt
  journal, atomicity, no-op finalize), `internal/stack/undo_op_test.go`
  (restore paths), `cmd/commands_mutation_test.go` undo-related cases, e2e undo
  journeys. All must pass unchanged.
- New: the spawn-count test from Step 3.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -n "RevParse" internal/stack/undo.go` shows no call inside a `for` loop over `s.Branches` or `entry.Refs`
- [ ] `go test ./internal/stack -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] `undo.json` shape unchanged: `git diff internal/stack/undo.go` shows no edits to the `UndoEntry` struct definition
- [ ] `git status --porcelain` shows changes only in `internal/stack/undo.go` and `internal/stack/undo_test.go`
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Any existing undo test fails in a way that suggests the absent-vs-empty
  `refs` semantics differ from the analysis above — report the failing test
  and the semantic difference.
- You find `refsUnchanged` callers that rely on per-ref error distinctions
  (they don't, per the current signature returning only `bool` — but verify).
- The change appears to require touching the `Git` port or the fake.

## Maintenance notes

- After this lands, the remaining per-mutation fixed cost is dominated by
  repeated `GitCommonDir` spawns in `stackedDir()` (~8/command) — a separate
  finding (PERF-03 in plans/README.md) with a caching-vs-test-isolation
  trade-off that this plan deliberately avoids.
- Reviewer should scrutinize Step 1's error-path choice (Tips failure) — it is
  the only place behavior could subtly differ from today.

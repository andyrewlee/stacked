# Plan 011: Dedupe redundant git spawns on the mutation paths — HEAD memo, tips seeding, remote URL fold, worktree-loop hoists

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- cmd/gitenv.go cmd/worktree_cache.go internal/stack/undo.go internal/stack/store.go cmd/sync.go cmd/prune.go cmd/submit.go cmd/open.go internal/stack/worktree.go`
> On mismatch — especially if Plan 003 landed (it edits `gitenv.go`) — adapt or STOP.

## Status

- **Priority**: P3
- **Effort**: M
- **Risk**: MED (caching HEAD/refs is a correctness hazard if invalidation misses — that's why each step has its own test)
- **Depends on**: plans/003-undo-order-cache.md (it edits `cmd/gitenv.go`)
- **Category**: perf
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Every mutating `st` command pays ~5 identical subprocess probes it doesn't need: `rev-parse --abbrev-ref HEAD` runs 3–5× per command (`snapshotUndo` + `Modify`/`restoreHEAD` + restack's `currentBranchOr` + `refusePruneCurrent`); `git for-each-ref` runs 3× (undo snapshot + op seed + `FinalizeUndo`); `git remote get-url` runs 2–3× (sync's `RemoteExists` + `resolveTrunkRef`'s `RemoteExists` + `RemoteURL`); and worktree-removal loops re-spawn `RepoRoot`/`worktree list`/`status` per item. Each spawn is ~8–15 ms — a `st modify` spends ~50–80 ms pure duplication. The fixes are mechanical because the invalidation surface is already mapped (the worktree cache proves the pattern).

## Current state

- `cmd/gitenv.go` — `cachedPort` decorates `stack.Git`; `Worktrees()` is memoized and invalidated on the worktree/HEAD-mutating methods (post-Plan-003 also the `Rebase*` set). `currentBranch()` at `gitenv.go:108-110` passes straight through — no memo.
- `internal/stack/undo.go:90` — `snapshotUndo` calls `g.Tips()`; `undo.go:332` `readUndoTips` (in `FinalizeUndo`) calls it again; op bodies fetch a third time (`ops_track.go:75`, `restack.go:253`, `undo_op.go:189`, `repair.go:90`).
- `cmd/sync.go:43` `RemoteExists(remote)` → `resolveTrunkRef` (`sync.go:61-70`) calls `RemoteExists(remote)` again; `cmd/submit.go` calls `RemoteExists` + `RemoteURL` (same underlying `git remote get-url`); `cmd/open.go:76-81` same pair.
- `internal/stack/worktree.go:231` — `CwdWithinWorktree` → `RepoRoot()` per candidate; `cmd/worktree.go:341-355` per-name `CwdWithinWorktree`+`RebaseInProgressIn`+`IsCleanIn`; `worktree.go:271-319` `ownedWorktreeReleaseTarget` calls `Worktrees`+`RepoRoot`+`IsCleanIn` per candidate (and `WorktreeRemove` invalidates the cache mid-loop).
- `internal/git/git.go:147,159` + `worktree.go:18` — `GitDir`/`GitCommonDir`/`CurrentBranch` are 3 separate `rev-parse` spawns per command.
- Convention: `Env.Git` is the port; `cmd/gitenv.go` `cachedPort` is the production decorator; `git.Shell` is the real impl. Any memo must live in `cachedPort` (or `Env`), NOT inside `internal/git.Shell` — the shell layer stays honest per-call.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Adapter tests | `go test ./cmd` | all pass |
| Timing sanity | `time ./st log` / `time ./st modify …` in a fixture repo | visibly fewer spawns under `ST_DEBUG=1` |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `cmd/gitenv.go`, `cmd/worktree_cache.go`, `cmd/sync.go`, `cmd/prune.go`, `cmd/submit.go`, `cmd/open.go`, `internal/stack/store.go` (repoDirsForCwd memo — may already be memoized; check), `internal/stack/worktree.go` (RepoRoot hoist), `internal/stack/undo.go` (PostRefs seeding — ONLY if Plan 001 landed; otherwise skip that piece), `internal/stack/git.go` if a new port method is needed.

**Out of scope**: `internal/git` shell impl internals (keep honest per-call), the `st log` `tipAncestors` batching (Plan 020), parallel probe execution (Plan 020), journal write coalescing (Plan 020).

## Git workflow

- Branch: `advisor/011-spawn-dedup`; imperative commits (one per dedup arm is fine); no push/PR unless instructed.

## Steps

### Step 1: Fold `RemoteExists`+`RemoteURL` into one spawn

`RemoteExists` is literally `RemoteURL`'s error check. In `cmd/sync.go`, `cmd/prune.go`, `cmd/submit.go`, `cmd/open.go`: call `git.RemoteURL(remote)` once; treat `err == nil` as exists and reuse the URL. For `resolveTrunkRef`'s remote-existence arm, thread the already-fetched result in instead of re-probing.

**Verify**: `go test ./cmd -run 'Sync|Prune|Submit|Open'` → pass; under `ST_DEBUG=1` a sync/open run shows one `remote get-url`.

### Step 2: Memoize `CurrentBranch` on `cachedPort` with correct invalidation

Add a `currentMemo`/`currentBranch()` cache to `cachedPort` (or extend `worktree_cache.go`'s seam), invalidated by exactly the HEAD-moving port methods: `Checkout`, `CheckoutDetach`, `RebaseOnto`, `RebaseOntoIn`, `RebaseOntoQuiet`, `RebaseContinue`, `RebaseAbort`, `RebaseAbortIn` — the same set Plan 003 wired for the worktree cache (these ops move HEAD). The two invalidation sets are identical — implement once (e.g. a shared `resetProcCaches()`).

**Verify**: `go test ./cmd` → pass; a `ST_DEBUG=1 st modify` shows `rev-parse --abbrev-ref HEAD` once (not 3–5×).

### Step 3: Pass the snapshot tips into the op as a seed

`snapshotUndo` already fetched `tips` at `undo.go:90`. Add an optional seed field on `Env` (e.g. `TipSeed map[string]string` or a `SeedTips()` accessor) that op bodies consult before calling `g.Tips()` — `trackAllPlan`, `restackForest`, `probeLiveBranches`, `Repair` can consume it. `FinalizeUndo`'s `readUndoTips` MUST stay a fresh read (tips legitimately moved mid-op). If `Env` surgery is too invasive, skip this step and note it deferred — the value is real but it's the most invasive of the set.

**Verify**: `make test-fast` → pass.

### Step 4: Hoist `RepoRoot`/`Worktrees`/`IsCleanIn` out of removal loops

- `internal/stack/worktree.go:231` `CwdWithinWorktree`: hoist the `RepoRoot()` call — accept it as a parameter or compute once per loop and pass it in. Check its callers (`cmd/worktree.go:341`, `undo_op.go:213-247`, `worktree.go` internals) and give each loop one fetched root.
- `ownedWorktreeReleaseTarget`/`worktreeRemoveAll`: fetch `Worktrees` once outside the loop; maintain a local snapshot updated as removals succeed (the code already does this via `noteWorktreeAdded`/`liveSet` patterns — reuse them). `WorktreeRemove` cache-invalidation is fine as long as the loop doesn't re-read; if the loop relies on the cache being fresh per-iteration, maintain the local slice.

**Verify**: `go test ./internal/stack ./cmd -run 'Worktree|Delete|Prune|Undo'` → pass.

### Step 5: One `rev-parse` for the startup probes (optional, smallest win)

`git rev-parse --abbrev-ref HEAD --absolute-git-dir --git-common-dir` answers three startup probes in one spawn. Add a `RepoProbe()` port method returning all three, called from the dispatch/store path. This is the least-valuable item — implement only if clean; a `--abbrev-ref` detached-HEAD edge (`HEAD` output vs. branch name) must match `CurrentBranch` semantics.

**Verify**: `go test ./cmd ./internal/git` → pass.

### Step 6: Gate

**Verify**: `make ci` → exit 0. Sanity: `ST_DEBUG=1 st status`/`st log` on a fixture → count spawns vs. before (expect ~30–50% fewer on mutating commands).

## Test plan

- Step 2 needs a test asserting `CurrentBranch` memoization + invalidation — mirror the worktree-cache test pattern in `cmd` (or add a `cachedPort` unit test stubbing the embedded `Git` and counting calls).
- Steps 3-4 are behavior-preserving — existing tests are the net; add a fake-call-count assertion if a seam exists (`f.callsSnapshot()`).
- Verification: `go test ./...` → all pass; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./...` exits 0
- [ ] `ST_DEBUG=1` on a `st modify` shows one `abbrev-ref HEAD` spawn
- [ ] `grep -n 'RemoteExists' cmd/sync.go cmd/prune.go` shows no double-probe per remote
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- `CurrentBranch` is called from somewhere the memo can't invalidate (e.g. a `os.Chdir`-level HEAD move outside the port) — grep all callers; if an unportable site exists, STOP or scope the memo to only the guaranteed-invalidated paths.
- `Env` surgery in Step 3 touches every op signature — if it cascades past ~30 call sites, defer that step and report.
- A probe's cached value must reflect mid-command mutation (e.g. tips after a rebase inside one `Sync`) — the seed must be scoped to "pre-op" only; if that boundary is ambiguous for a given op, leave it uncached.

## Maintenance notes

- Plan 003's `Rebase*` invalidation wrappers and this plan's `CurrentBranch` memo share the invalidation set — keep them driven by the same `resetProcCaches`.
- Reviewer focus: the `FinalizeUndo`/`readUndoTips` MUST-fresh-read — a seed reaching it would break undo's correctness.
- Deferred to Plan 020: `tipAncestors` batching, probe parallelism, journal-write coalescing.

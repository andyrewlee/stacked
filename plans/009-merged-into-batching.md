# Plan 009: One `for-each-ref --merged` spawn for prune checks and dry-run plans

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/stack/git.go internal/stack/engine.go internal/stack/restack.go internal/git/ internal/stack/fakegit_test.go`
> PRs #31–#33 touch engine tests, `internal/git/git.go`, and `undo.go` — known
> drift; reconcile with the live code. A mismatch in the specific excerpts
> below (PruneMerged, SyncPlanAgainst, restackPlanAgainst) is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M (port addition + fake + three call sites)
- **Risk**: LOW-MED — touches sync's prune decision; semantics must match
  `merge-base --is-ancestor` exactly.
- **Depends on**: PRs #29–34 merged (overlapping files)
- **Category**: perf
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

`st sync` asks "is this branch merged into trunk?" once per tracked branch via
`IsAncestor`, and each bare-name `IsAncestor` call costs up to 3 spawns
(`localBranchRef` → `BranchExists` per argument + the `merge-base` itself).
The dry-run paths (`sync --dry-run`, `restack --dry-run`) likewise spawn one
`RevParse` per branch through `needsRestackAgainst`. Git answers the merged
question for *all* branches in one spawn:
`git for-each-ref refs/heads --format=%(refname:short) --merged <ref>`. The
repo already took this exact medicine for log/validate (`Tips()` +
`DriftAgainst`, commit `eccbe8f`) — this plan extends it to the prune and plan
paths. Mutation-path restacks keep their live per-step reads by design
(`restack.go` documents that invariant; do not touch it).

## Current state

- `internal/stack/engine.go:754–779` — `PruneMerged`: loops
  `sortedBranchNames(s)`, calls `g.IsAncestor(name, trunk)` per branch
  (bare names → 3 spawns each), deletes + checkpoints per merged branch.
- `internal/stack/engine.go:620–649` — `SyncPlanAgainst`: same per-branch
  `g.IsAncestor(name, trunkRef)` loop on a cloned state, then
  `restackPlanAgainst`.
- `internal/stack/restack.go:97–122` — `restackPlanAgainst` →
  `needsRestackAgainst` per branch (one `RevParse` each; read
  `needsRestackAgainst` around restack.go:20–30 before changing anything).
  `RestackPlan` (restack.go:126–143) is the `restack --dry-run` entry.
- `internal/stack/restack.go:73–88` — `DriftAgainst(tips map[string]string)`:
  the existing batched pattern and its doc comment (the exemplar; note its
  missing-parent semantics: drift=false when the parent is absent from the
  map).
- `internal/stack/git.go:7–35` — the `Git` port. `Tips()` already exists;
  this plan adds one method.
- `internal/git/git.go:120–135` — production `Tips()` (the implementation
  pattern to copy for `MergedInto`).
- `internal/stack/fakegit_test.go` — the fake git; `Tips()` at line 110 is
  the exemplar for the fake's `MergedInto` (the fake models commits/ancestry —
  read its `IsAncestor` to reuse the same ancestry walk).
- Pinning tests that must stay green: `internal/stack/sync_test.go` (prune +
  plan cases), `e2e/e2e_journey_test.go:591` `TestSyncPrunesMerged`,
  `:643` `TestRestackDryRun`, `cmd` golden `log_json`/sync outputs.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| Sync e2e | `go test ./e2e -run 'TestSync|TestRestackDryRun' -count=1` | exit 0 |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/git.go` (port method), `internal/git/git.go` +
`internal/git/shell.go` (production impl + wrapper), `internal/git/git_test.go`,
`internal/stack/fakegit_test.go` (fake impl), `internal/stack/engine.go`
(PruneMerged, SyncPlanAgainst), `internal/stack/restack.go` (plan paths only),
engine/sync test files.

**Out of scope**: `RestackBranch` / `RestackUpstack` / any mutation-path read —
`restack.go:73–79`'s comment marks live tips as load-bearing. `Delete`'s
single `IsAncestor` merged-check (engine.go:488) — one branch, not a loop.
The `Remote` port.

## Git workflow

- Branch: `merged-into-batching`
- Commit message style: `perf: one for-each-ref --merged spawn for prune and plan paths`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add `MergedInto(ref string) (map[string]bool, error)` to the port

Port declaration in `internal/stack/git.go` (doc comment: the batched
equivalent of per-branch `IsAncestor(branch, ref)`, keys are local branch
names). Production impl in `internal/git/git.go` next to `Tips()`:
`for-each-ref refs/heads --format=%(refname:short) --merged <ref>` → set of
names. Wire the one-line `Shell` method in `internal/git/shell.go`. Fake impl
in `fakegit_test.go` reusing the fake's ancestry logic so fake and real agree.

**Verify**: `go build ./...` → exit 0; add a real-git test in
`internal/git/git_test.go` (repo with one merged + one unmerged branch;
`MergedInto("main")` contains exactly the merged one) → PASS.

### Step 2: Use it in `PruneMerged` and `SyncPlanAgainst`

One `g.MergedInto(...)` call before each loop (`trunk` for PruneMerged —
pass `branchTipRef(trunk)` if the impl wants a full ref; match what you built
in Step 1 — and the supplied `trunkRef` for SyncPlanAgainst); the loop
consults the map. Keep iteration order (`sortedBranchNames`) and the
per-deletion checkpoint in PruneMerged exactly as they are.

**Verify**: `make test-fast` → exit 0;
`go test ./e2e -run TestSyncPrunesMerged -count=1` → PASS.

### Step 3: Thread a tips map through the plan paths

Give `restackPlanAgainst` (and its `needsRestackAgainst` per-branch read) a
batched variant: fetch `g.Tips()` once at the top of `RestackPlan` and
`SyncPlanAgainst`, and decide "needs restack" from the map the same way
`DriftAgainst` does — **including its documented missing-parent semantics**.
Where `SyncPlanAgainst`'s `trunkRef` is a remote ref (`refs/remotes/...`,
from `cmd/sync.go`'s dry-run path), the trunk tip must come from a `RevParse`
of that ref, not from the local tips map — keep that one spawn.

**Verify**: `make test-fast` → exit 0;
`go test ./e2e -run TestRestackDryRun -count=1` → PASS.

### Step 4: Full gate

**Verify**: `make ci` → exit 0.

## Test plan

- New real-git test for `MergedInto` (Step 1).
- Existing pins: `sync_test.go` fake-git prune/plan tests,
  `TestSyncPrunesMerged`, `TestRestackDryRun`, golden outputs — all unchanged.
- Add one fake-git test asserting `SyncPlanAgainst` output is identical
  before/after by construction (a stack where one branch is merged and one
  needs restack; assert Deleted and Restacked lists).

## Done criteria

- [ ] `grep -n "IsAncestor" internal/stack/engine.go` shows no call inside the PruneMerged or SyncPlanAgainst loops
- [ ] `go test ./internal/... ./cmd/... -race -count=1` exits 0
- [ ] `go test ./e2e -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] Only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- `for-each-ref --merged` and `merge-base --is-ancestor` disagree on any test
  case (they shouldn't — same definition; a disagreement means the format/ref
  argument is wrong; report it).
- The change appears to need edits to `RestackBranch`/`RestackUpstack` or any
  mutation-path read.
- Fake-vs-real divergence: the fake's `MergedInto` needs ancestry semantics
  the fake doesn't model.

## Maintenance notes

- Plan 011 (track inference) reuses `MergedInto` — land this first.
- Reviewer: the one subtle spot is Step 3's remote-trunkRef case in the sync
  dry run; check the e2e sync dry-run test covers a remote-ahead trunk.

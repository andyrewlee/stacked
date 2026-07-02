# Plan 020: Drive dry-run previews and the undo epilogue from a single Tips() read

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 0a76742..HEAD -- internal/stack/restack.go internal/stack/engine.go internal/stack/undo.go internal/stack/plan_test.go internal/stack/undo_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (best executed before plans/021 — both touch `internal/stack/restack.go`; do not run them concurrently)
- **Category**: perf
- **Planned at**: commit `0a76742`, 2026-07-01

## Why this matters

Every `internal/git` call spawns a `git` subprocess (~10-50ms). Two read-only
paths spawn one per branch where one spawn total suffices:

1. `st restack --dry-run` and `st sync --dry-run` call `git rev-parse` once
   per tracked branch to compute the preview, even though the codebase already
   has a batched drift primitive (`DriftAgainst`, one `Tips()` =
   one `git for-each-ref`) built for exactly this — the previews just don't
   use it. N branches → N+const spawns that should be ~2.
2. The undo epilogue after **every** mutation runs 2–3 back-to-back identical
   `Tips()` reads (`FinalizeUndo` calls `createdBranchesSince` + `refsUnchanged`;
   the error path stacks up to 3) with no ref-moving operation between them.

Both fixes are pure read-path batching with no mutation-ordering risk. (The
*mutation* restack loop's per-branch probe is the separate, riskier plan 021.)

## Current state

### Part 1 — previews

- `internal/stack/restack.go:127-152` — the per-branch probe:

```go
func (s *State) restackPlanAgainst(g Git, start, trunkRef string) ([]string, error) {
	var order []string
	if start != s.Trunk {
		order = append(order, start)
		order = append(order, s.Descendants(start)...)
	} else {
		order = s.Descendants(s.Trunk)
	}
	inPlan := map[string]bool{}
	var plan []string
	for _, name := range order {
		b, ok := s.Get(name)
		if !ok {
			continue
		}
		needs, err := s.needsRestackAgainst(g, name, trunkRef)   // <- one RevParse per branch
		if err != nil {
			return nil, err
		}
		if needs || inPlan[b.Parent] {
			plan = append(plan, name)
			inPlan[name] = true
		}
	}
	return plan, nil
}
```

- `needsRestackAgainst` (restack.go:16-30) resolves the parent tip via
  `g.RevParse(parentRef)`, substituting `trunkRef` when the parent is the
  trunk (that's how sync previews against a freshly-fetched
  `refs/remotes/<remote>/<trunk>`).
- The batched primitive that already exists (restack.go:110-117):

```go
func (s *State) DriftAgainst(tips map[string]string) map[string]bool {
	drift := make(map[string]bool, len(s.Branches))
	for name, b := range s.Branches {
		parentTip, ok := tips[b.Parent]
		drift[name] = ok && parentTip != b.ParentSHA
	}
	return drift
}
```

- Consumers of the per-branch path:
  - `RestackPlan` (restack.go:156-179) via `restackPlan` (:123-125), which
    passes `trunkRef = branchTipRef(s.Trunk)` — i.e. the local trunk tip,
    which a `Tips()` map already contains under the trunk's name.
  - `SyncPlanAgainst` (engine.go:697-733), which passes a possibly-remote
    `trunkRef` and calls `restackPlanAgainst` on a cloned, pruned state
    (engine.go:722). Its `IsAncestor` merged-check loop (engine.go:711) is
    OUT OF SCOPE (that's the separately-planned MergedInto batching).
- The `Git` port method `Tips() (map[string]string, error)` returns
  branch-name → tip SHA for all local branches (see `internal/stack/git.go`).
- Preview correctness note: no rebase occurs during a preview, so a one-shot
  snapshot is exactly as correct as per-branch live reads.

### Part 2 — undo epilogue

- `internal/stack/undo.go` — the three sites, each doing its own `Tips()`:

```go
// undo.go:285-296
func refsUnchanged(g Git, entry *UndoEntry) bool {
	tips, err := g.Tips()
	if err != nil {
		return false
	}
	for name, want := range entry.Refs {
		if tips[name] != want {
			return false
		}
	}
	return true
}

// undo.go:261-281 (excerpt)
func createdBranchesSince(g Git, entry *UndoEntry) []string {
	if entry == nil || entry.LocalBranches == nil {
		return nil
	}
	...
	tips, err := g.Tips()
	if err != nil {
		return nil
	}
	...
}
```

- Call graph (all in undo.go):
  - `FinalizeUndo` (:186-207): `createdBranchesSince` (:190) then possibly
    `refsUnchanged` (:203) → up to 2 `Tips()` per **successful** mutation.
  - `CleanupUndoOnError` (:214-229): `dropNoopUndo` → `refsUnchanged` (:250)
    + `createdBranchesSince` (:253), then `createdBranchesSince` **again**
    (:221) when not dropped → up to 3 `Tips()` per failed/no-op mutation.
- Current error semantics to preserve: `refsUnchanged` returns `false` when
  `Tips()` errors; `createdBranchesSince` returns `nil`. Passing a `nil` map
  reproduces both behaviors (lookups miss → `refsUnchanged` false; range over
  nil → no created branches), so fetch-once-pass-nil-on-error is
  behavior-preserving.

Repo conventions: engine/undo helpers are unexported and take explicit
parameters; tests use `newEnvState()` + the fake git in
`internal/stack/fakegit_test.go`. Existing preview tests:
`internal/stack/plan_test.go` (`TestRestackPlanListsOutOfDateAndDescendants`,
`TestRestackPlanRejectsUntrackedBranch`, `TestSyncPlanPreviewsPrune`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | ok, ~1s |
| Undo tests | `go test ./internal/stack -run Undo -count=1` | ok |
| Plan tests | `go test ./internal/stack -run Plan -count=1` | ok |
| Race suite | `go test ./cmd/... ./internal/... -race -count=1` | ok |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope** (the only files you should modify):
- `internal/stack/restack.go` (`restackPlanAgainst` and its callers' plumbing)
- `internal/stack/engine.go` (only `SyncPlanAgainst`/`SyncPlan` plumbing)
- `internal/stack/undo.go` (`FinalizeUndo`, `CleanupUndoOnError`,
  `dropNoopUndo`, `refsUnchanged`, `createdBranchesSince`)
- `internal/stack/plan_test.go`, `internal/stack/undo_test.go` (tests)
- `internal/stack/fakegit_test.go` — ONLY if you need to add a call counter
  (see Test plan); no behavior changes.

**Out of scope** (do NOT touch):
- `RestackBranch` / `RestackUpstack` / `needsRestackAgainst`'s use on the
  mutation path — live per-step reads there are load-bearing (see the comment
  at restack.go:103-109) and are plan 021's carefully-scoped subject.
- `SyncPlanAgainst`'s `IsAncestor` merged-branch loop (engine.go:709-721) —
  already planned separately (MergedInto batching, plan 009).
- `SnapshotUndo`'s own `Tips()` at record time — that is a *different*
  point-in-time snapshot and must stay.

## Git workflow

- Branch: `advisor/020-batch-preview-and-undo-reads`
- Commit style: conventional commits, e.g.
  `perf: compute previews and the undo epilogue from one Tips() read`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add a tips-driven plan walk

In `internal/stack/restack.go`, replace the body of `restackPlanAgainst` with
a version that takes the tips map, and keep the git-reading signature as a
thin wrapper so callers choose:

```go
// restackPlanFromTips computes the preview purely from a branch→tip map (one
// Tips() read). trunkTip overrides the trunk's entry so sync can preview
// against a freshly-fetched remote trunk ref.
func (s *State) restackPlanFromTips(tips map[string]string, trunkTip, start string) []string {
	drift := s.DriftAgainst(tipsWithTrunk(tips, s.Trunk, trunkTip))
	// ... same order/inPlan walk as today, with `needs := drift[name]`
}
```

where `tipsWithTrunk` copies the map with `tips[trunk] = trunkTip` when
`trunkTip != ""` (copy, don't mutate the caller's map). Preserve the existing
walk order and `inPlan` cascade exactly; only the per-branch `needs`
computation changes. Note `DriftAgainst` reports `false` for a branch whose
parent is missing from the map — identical to today's behavior surface for
previews (a missing parent branch is reported by validate/repair, not the
preview), but see STOP conditions.

### Step 2: Rewire the two preview entry points

- `RestackPlan` (restack.go:156-179): replace the `s.restackPlan(env.Git, start)`
  call with one `env.Git.Tips()` + `s.restackPlanFromTips(tips, "", start)`.
- `SyncPlanAgainst` (engine.go:697-733): it receives `trunkRef` (may be a
  remote ref). Resolve it once — `trunkTip, err := g.RevParse(trunkRef)` —
  take one `g.Tips()`, and call
  `planState.restackPlanFromTips(tips, trunkTip, planState.Trunk)`.
  Leave the `IsAncestor` loop untouched.
- Delete `restackPlan` and the old `restackPlanAgainst` if now uncalled
  (`grep -rn "restackPlanAgainst\|restackPlan(" internal/ cmd/` to confirm no
  other callers).

**Verify**: `go test ./internal/stack -run Plan -count=1` → ok, and
`make test-fast` → ok.

### Step 3: Thread one Tips() through the undo epilogue

In `internal/stack/undo.go`:

- Change `refsUnchanged(g Git, entry *UndoEntry)` →
  `refsUnchanged(tips map[string]string, entry *UndoEntry)` (drop the fetch;
  nil map → false path preserved because every lookup misses only if
  `entry.Refs` is non-empty — see STOP conditions for the empty-Refs edge).
- Change `createdBranchesSince(g Git, entry *UndoEntry)` →
  `createdBranchesSince(tips map[string]string, entry *UndoEntry)` (nil map →
  returns nil, matching today's error behavior).
- In `FinalizeUndo` and `CleanupUndoOnError` (and `dropNoopUndo`, which gains
  a `tips` parameter), fetch once at the top:

```go
tips, err := g.Tips()
if err != nil {
	tips = nil // preserve prior per-call error semantics
}
```

**Verify**: `go test ./internal/stack -run Undo -count=1` → ok.

### Step 4: Pin the spawn counts

See Test plan. Then run the full suite.

**Verify**: `make test-fast` → ok;
`go test ./cmd/... ./internal/... -race -count=1` → ok; `make ci` → exit 0.

## Test plan

- Existing behavior locks: `plan_test.go` (3 tests) and the undo suites
  (`undo_test.go`, `undo_op_test.go`) must pass unchanged — they pin the
  outputs; this plan only changes how many git calls produce them.
- New spawn-count tests (the point of the plan): the fake git lives in
  `internal/stack/fakegit_test.go` and is test-owned. Add an exported-to-tests
  counter (e.g. a `calls map[string]int` incremented in `Tips` and `RevParse`,
  or a wrapper type embedding the fake that counts) — whichever is least
  invasive. Then:
  - In `plan_test.go`: build a 4-branch stack via `newEnvState()`/`mkBranch`,
    run `RestackPlan`, assert `RevParse` calls == 0 and `Tips` calls == 1
    (plus whatever `requireClean`/`CurrentBranch` do — count only the two
    methods asserted).
  - In `undo_test.go`: drive one successful mutation through
    `SnapshotUndo`+`FinalizeUndo` and assert `FinalizeUndo` performed exactly
    1 `Tips` call (snapshot's own call excluded — count between the two
    phases or reset the counter).
- Model new tests on `TestRestackPlanListsOutOfDateAndDescendants`
  (plan_test.go:5) and `TestUndoProtocol` (undo_test.go:234).

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `make test-fast` exits 0
- [ ] `go test ./cmd/... ./internal/... -race -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] `grep -n "needsRestackAgainst" internal/stack/restack.go` shows it used only by `NeedsRestack`/mutation-path code, not by any preview/plan function
- [ ] New count assertions exist: `grep -rn "Tips" internal/stack/plan_test.go internal/stack/undo_test.go` shows the counter assertions
- [ ] Only in-scope files modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The excerpts above don't match the live code (drifted — especially if plan
  021 ran first and restructured `restack.go`).
- Behavior drift on missing parents: if any existing test fails because the
  old preview *errored* on a missing parent branch where `DriftAgainst`
  reports false — the analysis says previews only run over tracked branches
  whose parents exist (validate catches the rest), but if a test disagrees,
  report; do not silently change the error contract.
- The empty-`entry.Refs` edge: if an `UndoEntry` can have zero recorded refs,
  `refsUnchanged(nil, entry)` returns `true` (vacuous loop) where the old
  code returned `false` on a `Tips()` error. If any test exercises
  Tips-failure with empty Refs, report rather than pick a semantic.
- You need to modify `RestackBranch`/`RestackUpstack` — that's plan 021;
  stop.

## Maintenance notes

- Plan 021 (mutation-loop batching) touches `restack.go` next; execute
  sequentially, 020 → 021.
- If sync's preview ever needs to account for remote-moved *non-trunk*
  parents, `tipsWithTrunk` is the seam to generalize.
- Reviewer should scrutinize: the map copy in `tipsWithTrunk` (no caller-map
  mutation), and that `SyncPlanAgainst` still resolves `trunkRef` *after*
  `requireClean` so error ordering (and thus exit codes) is unchanged.

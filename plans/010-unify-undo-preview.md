# Plan 010: Unify `Undo` and `UndoPreview` onto one planning path

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/undo_op.go internal/stack/undo_preview.go internal/stack/undo.go cmd/undo.go`
> On mismatch — especially if Plans 001/003 haven't landed — STOP: this refactor must run against the post-CAS, post-reorder undo.

## Status

- **Priority**: P2
- **Effort**: M–L
- **Risk**: MED (Undo is the recovery path; the read-only invariant of preview is pinned by a decorator test but the refactor touches both)
- **Depends on**: plans/001-undo-ref-guards.md, plans/003-undo-order-cache.md
- **Category**: tech-debt
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`internal/stack/undo_preview.go:13-16` carries an explicit `TWIN SITE` warning: "UndoPreview models Undo (undo_op.go) and must change with it — a new created-resource kind, gate, or schema barrier lands in both files in the same commit, or the preview lies." ~300 lines of gate ordering, candidate discovery, worktree mismatch detection, dirty-owner refusal, checkout resolution, and detach fallback are re-implemented in parallel — with only `absorbedCommitsNote` shared. `st undo --dry-run` is the preview agents are told to trust (AGENT.md contract); drift makes it over-promise or invent blockers. Plans 001 and 003 make it worse by adding more gates — unify before the surface grows again.

## Current state

- `internal/stack/undo_op.go:38-121` — `Undo`'s apply path: gates → created-branch discovery → worktree removal → checkout-target resolution/detach fallback → ref restore.
- `internal/stack/undo_preview.go:103-252` — `UndoPreview` re-implements: `RebaseInProgress` gate, dual schema barriers, created-branch candidate discovery (139-157), recorded-vs-live worktree mismatch, dirty-owner refusal, checkout-target resolution + detach fallback (217-251).
- `internal/stack/undo.go:232-245` — `absorbedCommitsNote`, the only shared piece.
- `internal/stack/undo_op_test.go` — a decorator test pins "preview is read-only" (fails on mutating port calls) — that's your safety net.
- `cmd/undo.go` — calls `stack.Undo` for apply, `stack.UndoPreview` per entry for `--dry-run`/`--list` adjacency; multi-step undo chains previews through decoded state (`stack.DecodeUndoState`).
- Convention: engine pure over `Env`; planning-without-mutation is the established pattern (`*Plan` functions in `restack.go`/`plan.go` — e.g. `RestackPlan`/`RestackAllPlan` compute then apply).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Undo tests | `go test ./internal/stack -run 'Undo'` | all pass |
| Preview purity | `go test ./internal/stack -run 'Preview'` | all pass (the read-only pin) |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/undo_op.go`, `internal/stack/undo_preview.go`, `internal/stack/undo.go`, possibly `internal/stack/undo_plan.go` (new file), `internal/stack/undo*_test.go`, `cmd/undo.go` (only if the exported surface changes).

**Out of scope**: `cmd/undo.go`'s CLI structure/multi-step loop (keep); journal schema (`Plans 001` owns `PostRefs`); `git.go` port changes.

## Git workflow

- Branch: `advisor/010-unify-undo-preview`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Extract a shared `planUndo`

Create `internal/stack/undo_plan.go` with a function like:

```go
// planUndo computes everything Undo will do: gate verdicts, branches to
// delete, worktrees to release, refs to restore, the checkout target, and
// whether HEAD detaches. Read-only over the port. Both Undo (apply) and
// UndoPreview (render) consume it.
type undoPlan struct {
    blockers      []string
    doomed        []doomedBranch      // created branches (+ worktree, dirty, current-worktree flags)
    restores      map[string]string   // ref restores (post-001: with expected-old values)
    checkout      *string
    detach        bool
    notes         []string
    absorbed      map[string]string
}
func planUndo(env Env, s *State, prev *State, entry *UndoEntry, liveSet ...) (*undoPlan, error)
```

Move the discovery/gate logic out of both twins into this function. The apply path then *executes* the plan (deletes, restores refs, saves state, checks out); the preview path *renders* it into `UndoPreviewResult`.

**Key invariant**: `planUndo` must make zero mutating port calls — enforce by running it through the fake's call log or the existing read-only decorator in tests.

**Verify**: `go build ./internal/stack` → exit 0; `go test ./internal/stack -run 'Undo'` → still green (behavior preserved).

### Step 2: Re-point `Undo` to consume the plan

`Undo` becomes: `planUndo` → execute. Keep its exact ordering (post-Plan-003: created-branch deletion → `UpdateRefs`/`UpdateRefsCas` → `*s = *prev` + `env.save()` → checkout restore). The preview's `blockers` computation must match `Undo`'s early-returns — that's the whole point: one ordered gate list, two consumers.

**Verify**: `go test ./internal/stack -run 'Undo'` → pass.

### Step 3: Re-point `UndoPreview` to render the plan

`UndoPreview` becomes `planUndo` → map into `UndoPreviewResult` fields (`WouldRestore`, `WouldDelete`, `WouldCheckout`, `WouldDetach`, `Blockers`, `Notes`, `AbsorbedCommits` note). Preserve the emitted JSON field names/values exactly — `cmd` tests and `docs/AGENT.md` pin them.

**Verify**: `go test ./internal/stack ./cmd` → pass; `grep -n 'TWIN SITE' internal/stack/undo_preview.go` → the comment is replaced by the shared-path doc.

### Step 4: Hoist the per-step probes (PERF-04, while the shape is open)

While unifying: `UndoPreview` calls `RebaseInProgress`, `probeLiveBranches` (a `Tips()`), `CurrentBranch`, and `CommitRange` per step — for multi-step preview (`cmd/undo.go` loops), these are step-invariant except `liveSet`/`CommitRange`. Compute `rebaseInProgress`/`currentBranch`/`liveSet` once in the caller (or memoize inside `planUndo` with an explicit "chain" input) so `st undo 3 --dry-run` doesn't re-probe thrice. `CommitRange` per recorded ref can stay per-step (ref sets differ) — note it, don't batch it unless trivial.

**Verify**: `go test ./internal/stack -run 'Undo'` → pass; a `cmd` test or fake-call-count assertion shows probes hoist (optional but nice — `f.callsSnapshot()` exists in the fake tests).

### Step 5: Docs

- `internal/stack/undo_preview.go` header comment → describe the shared-plan shape.
- `docs/AGENT.md`: the `undo --dry-run` contract fields are unchanged — confirm, don't rewrite.
- `CHANGELOG.md` `[Unreleased]` → `Changed`/`Internal`: undo plan/apply unified; preview provably can't drift from apply.

**Verify**: `make ci` → exit 0.

## Test plan

- The existing `undo_op_test.go` + `undo_preview_test.go` suites ARE the regression net — they must pass unchanged or with only mechanical updates.
- Add ONE new test: a focused "plan-parity" test asserting `planUndo`'s blockers for a fixture equal the union both consumers would report (or simply that both functions now funnel through it — e.g. instrument via fake call counts).
- Verification: `go test ./internal/stack ./cmd ./e2e` → all pass; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./internal/stack ./cmd ./e2e` exits 0
- [ ] `grep -c 'TWIN SITE' internal/stack/undo_preview.go` → 0
- [ ] `make ci` exits 0
- [ ] `grep -n 'planUndo\|undoPlan' internal/stack/` shows both `Undo` and `UndoPreview` consume the shared planner
- [ ] `plans/README.md` row updated

## STOP conditions

- Plans 001/003 not landed: the preview mirrors a shape that's about to change — STOP and land dependencies first.
- The apply path genuinely needs data the planner's read-only shape can't produce (e.g. mid-apply probe results feeding later decisions) — if a real sequencing dependency exists, document it and STOP rather than weakening the purity invariant.
- The emitted `UndoPreviewResult` JSON shape would change — STOP; the contract is published in `docs/AGENT.md`.
- Refactor balloons past ~400 lines touched — split: first share the gate/blocker computation only, ship that, then the rest.

## Maintenance notes

- After this lands, any new undo gate lands ONCE in `planUndo` — the TWIN SITE hazard is gone.
- This refactor is the foundation DIR-01-style "push staleness" or future undo features build on.
- Reviewer focus: the per-step `journalIndex` handling (`UndoPreview` takes it since plan 032) — chained-state previews must keep reporting honest entry indices.

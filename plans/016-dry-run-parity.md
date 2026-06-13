# Plan 016: `--dry-run` for the destructive topology ops (onto, fold, squash, delete)

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/stack/engine.go internal/stack/restack.go cmd/onto.go cmd/fold.go cmd/squash.go cmd/delete.go docs/AGENT.md README.md CHANGELOG.md`
> PRs #29–34 and plans 009/010 touch overlapping files — reconcile with live
> code. A mismatch in the SyncPlanAgainst/RestackPlan excerpts below is a
> STOP condition.

## Status

- **Priority**: P3 (direction — feature, not a fix)
- **Effort**: M
- **Risk**: LOW-MED — new read-only paths; the risk is plan/actual divergence,
  mitigated by testing plans against subsequent real runs.
- **Depends on**: best after plan 009 (plan paths get batched there; building
  on the batched helpers avoids re-doing this)
- **Category**: direction
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

`st` is positioned as agent-first: no prompts, stable exit codes, `--json`
everywhere. Agents (and cautious humans) preview before mutating — and today
they can preview `restack`, `sync`, and `submit`, but **not** the four most
destructive topology operations: `onto` (re-parents a subtree), `fold`
(deletes a branch into its parent), `squash` (rewrites history), `delete`
(removes a branch and re-parents children). The engine already contains the
plan machinery these need: `cloneState` + `restackPlanAgainst` power
`SyncPlanAgainst` and `RestackPlan`. This plan extends that existing pattern —
it is mostly wiring, not new invention.

## Current state

- The pattern to extend (first-hand reads at `ed9f2f7`):
  - `internal/stack/restack.go:126–143` — `RestackPlan(env, s)`: resolve
    start, `s.restackPlan(env.Git, start)`, return
    `&OpResult{Summary, Restacked: plan, DryRun: true}`.
  - `internal/stack/engine.go:620–649` — `SyncPlanAgainst`: `planState :=
    cloneState(s)`, simulate removals on the clone, run `restackPlanAgainst`
    on the clone, return `{Deleted, Restacked, DryRun: true}`.
  - `internal/stack/engine.go:651–663` — `cloneState` (deep copy incl.
    PendingReparent).
- The mutations to mirror (what each plan variant must predict):
  - `Onto` (engine.go:402–464): validations (lines 404–428: tracked, not
    self, target tracked-or-trunk, not own descendant, not already there) →
    rebase cur → restack descendants. Plan = same validations on the clone,
    re-parent cur in the clone, then the clone's restack plan from cur;
    `Restacked` = [cur if it would rebase] + descendants; `Branch: cur`.
  - `Fold` (engine.go:263–331): validations (tracked, parent != trunk, no
    pending restack) → parent absorbs cur, cur deleted, upstack restacks.
    Plan: `Deleted: [cur]`, `Branch: parent`, `Restacked` = plan over cur's
    children re-parented onto parent in the clone.
  - `Squash` (engine.go:337+): rewrites cur then restacks descendants. Plan:
    `Branch: cur`, `Restacked` = descendants of cur (they always need restack
    after the rewrite — confirm against Squash's actual tail, which uses
    `finishUpstack`).
  - `Delete` (engine.go:469–539): validations incl. the merged-into-parent
    check (lines 487–495, needs `IsAncestor` — a real read, fine in a plan) →
    children re-parented + restacked. Plan: `Deleted: [name]`,
    `Restacked` = plan over former children in the clone.
- The cmd wiring exemplar: `cmd/sync.go:41–58` (dry-run branch: `loadState()`
  — **no lock, no undo entry** — call the plan function, `renderResult`) and
  `cmd/restack.go` (find its `--dry-run` flag wiring). Note dry-run uses
  `loadState()`, not `mutate()` — read-only paths don't lock.
- Output contract (docs/AGENT.md "Result shapes"): mutating-command shape
  `{summary, branch, restacked, deleted, notes, dryRun}` — all omitempty;
  preview-capable commands add `"dryRun": true`. The four new previews reuse
  this shape — no new schema.
- Docs to update when done: docs/AGENT.md (the "Preview-capable commands
  (`restack`, `sync`)" sentence), README command table rows + detailed
  sections, CHANGELOG Unreleased, usage strings (shifts `help.golden` —
  regenerate deliberately per CLAUDE.md).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| Goldens | `make golden` | only intended diffs |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/engine.go` (or a new `internal/stack/plan.go`
if engine.go would grow past ~1100 lines) — `OntoPlan`, `FoldPlan`,
`SquashPlan`, `DeletePlan`; `cmd/onto.go`, `cmd/fold.go`, `cmd/squash.go`,
`cmd/delete.go` (flag + dry-run branch); engine tests; one e2e per command;
docs (AGENT.md, README, CHANGELOG); `cmd/testdata/help.golden` via
`make golden`.

**Out of scope**: changing any mutation's behavior; `--dry-run` for
`modify`/`create`/`rename`/`track`/`untrack` (cheap or non-destructive —
deliberately excluded; record in the index if someone asks); a `--porcelain`
stability guarantee (separate direction item).

## Git workflow

- Branch: `dry-run-parity`
- Commit message style: `feat: --dry-run previews for onto, fold, squash, and delete`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Engine plan functions, one mutation at a time

For each of Onto/Fold/Squash/Delete, add `XPlan(env Env, s *State, <same
params>) (*OpResult, error)`: run the *same validation prologue as the real
operation* (copy the conditions verbatim — a preview that allows what the real
op rejects is a bug), then simulate on `cloneState(s)` and compute
`Restacked` via the clone's `restackPlanAgainst`. Never call `env.save()`,
never call a mutating git method. Summary strings: mirror the real op's
wording prefixed appropriately (e.g. `"would fold a into b"`); always
`DryRun: true`.

**Verify** after each: a fake-git engine test (exemplar:
`TestOntoPersistsReparentBeforeRestackingDescendants` at
engine_test.go:592 for stack-building) asserting (a) the plan's
Deleted/Restacked/Branch fields, (b) **state and fake git unchanged** after
the call (compare snapshots), (c) validation errors match the real op's for
the same bad inputs. `make test-fast` → exit 0.

### Step 2: Plan/actual consistency tests

For each op, one fake-git test: run `XPlan`, capture `Restacked`; run the
real `X` on the same state; assert the real `OpResult.Restacked` equals the
plan (and `Deleted` matches). This is the load-bearing property — a preview
that lies is worse than none.

**Verify**: `go test ./internal/stack -run Plan -count=1 -v` → PASS.

### Step 3: cmd wiring

In each of the four cmd files: add `fs.BoolVar(&dryRun, "dry-run", false,
...)`, update the `Usage` string, and branch exactly like `cmd/sync.go:41–58`
(`loadState()` → `stack.XPlan(...)` → `renderResult`) before the `mutate`
call. Regenerate goldens (`make golden`) — `help.golden` shifts; review the
diff.

**Verify**: `go test ./cmd/... -race -count=1` → exit 0; `make golden &&
git diff --stat cmd/testdata/` shows only expected golden changes.

### Step 4: e2e + docs

- One e2e per command in `e2e/e2e_journey_test.go` (exemplar:
  `TestRestackDryRun` at line 643): build a stack, run `st <op> --dry-run
  --json`, assert `dryRun: true` and the expected names, **then assert
  nothing changed** (`st log --json` identical before/after, branch still
  exists for fold/delete).
- docs/AGENT.md: update the preview-capable sentence to list all six
  commands. README table + sections, CHANGELOG Unreleased "Added".

**Verify**: `go test ./e2e -run DryRun -count=1 -v` → PASS; `make ci` →
exit 0.

## Test plan

Steps 1–2 (fake-git: field assertions, no-mutation assertions, plan/actual
consistency — 8+ tests), step 3 goldens, step 4 e2e (4 tests). Patterns named
inline above.

## Done criteria

- [ ] `st onto --dry-run`, `st fold --dry-run`, `st squash --dry-run`, `st delete <name> --dry-run` all exist with `--json` support
- [ ] Plan/actual consistency tests pass for all four ops
- [ ] e2e asserts no-mutation for all four previews
- [ ] docs/AGENT.md + README + CHANGELOG updated; goldens regenerated deliberately
- [ ] `make ci` exits 0; `plans/README.md` row updated

## STOP conditions

- A plan/actual consistency test cannot be made to pass without duplicating
  large parts of the real op — the simulation approach is wrong for that op;
  report which op and why (likely candidate: Delete's merged-check
  interaction) instead of shipping an approximate preview.
- The dry-run path turns out to need the lock (it shouldn't — sync's doesn't;
  if you find a reason it does, report it).
- engine.go layout: if you create `plan.go`, keep the real ops where they
  are — moving them is out of scope.

## Maintenance notes

- Every future change to Onto/Fold/Squash/Delete must keep its Plan twin in
  sync; the Step 2 consistency tests are the enforcement — reviewers should
  reject changes to one without the other.
- The deliberate exclusions (modify/create/rename/track/untrack) and the
  reasoning live in this plan; revisit only on user demand.

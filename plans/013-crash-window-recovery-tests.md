# Plan 013: Crash-window tests — prove validate/repair/undo converge on half-completed operations

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- e2e/ internal/stack/engine.go cmd/repair.go`
> PR #31 and plan 012 also edit e2e files (new tests / t.Parallel) — expected
> drift. If plan 010 has landed, repair logic lives in
> `internal/stack/repair.go` instead of `cmd/repair.go` — fine; this plan is
> black-box. A mismatch in the engine excerpts below is a STOP condition.

## Status

- **Priority**: P3
- **Effort**: M
- **Risk**: LOW for the tests themselves; MED chance they uncover real
  recovery gaps (that is their purpose — expect to file findings, not fixes).
- **Depends on**: PR #31 merged (e2e file overlap); plan 012 optional
- **Category**: tests
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

The engine deliberately orders git mutations before state saves and documents
the windows (e.g. Fold: force-move parent → checkout → delete branch → only
then `RemoveBranch`+save). A process killed inside such a window leaves git
and `state.json` disagreeing. Nothing in the test tree simulates that — the
closest test (`TestValidateRepairDrift`) moves a ref, which is a different,
milder shape. Whether `st validate` flags each post-crash shape and
`st repair`/`st undo` actually converge is unverified. No process-killing is
needed: each window's aftermath can be constructed directly with raw git
commands against a healthy stack.

## Current state

Windows to model (from first-hand reads of `internal/stack/engine.go` at
`ed9f2f7`):

1. **Fold, killed after git, before save** (`engine.go:303–325`): parent
   force-moved to cur's tip, cur deleted from git, but state still tracks
   cur. Construct: build `main→a→b→c`, then with raw git from the test:
   `git branch -f b <tip of c>` (wait — fold moves the *parent*; folding c
   into b means `git branch -f b $(git rev-parse c); git checkout b;
   git branch -D c`) while `state.json` still lists `c`.
2. **Delete, killed after branch deletion + save, before child restacks**
   (`engine.go:503–529`): state consistent (children re-parented, saved) but
   children not yet rebased — this one should be merely "needs restack";
   assert validate/log reports drift, `st restack` converges.
3. **Create, killed after `git checkout -b`, before save**: a git branch
   exists that state doesn't know; assert `st track` adopts it or validate
   stays clean (untracked branches are legal — this case documents the
   expectation).
4. **Modify, killed mid-upstack** (restack of child done in git, save raced):
   simulate by amending a parent with raw git (`git commit --amend`) so a
   child's recorded `parentSHA` is stale — close to `TestValidateRepairDrift`;
   include for completeness of the table, assert drift + `st restack` fixes.

- Exemplar for the construction style: `e2e/e2e_journey_test.go:566`
  `TestValidateRepairDrift` (build healthy stack, mutate behind st's back with
  `r.git(...)`, assert validate fails / repair fixes / validate passes).
- Harness: `newRepo(t)`, `r.initStack()`, `r.create(...)`, `r.git(...)`,
  `r.st(...)` / `r.stOK(...)`, `wantExit`, `wantStdoutContains`. State file
  path: `filepath.Join(r.dir, ".git", "stacked", "state.json")` — tests may
  read it to assert recorded parents.
- Exit codes (docs/AGENT.md): validate exits 1 with problems; conflict 2;
  dirty 4.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| New tests | `go test ./e2e -run TestCrashShape -count=1 -v` | PASS |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: a new `e2e/e2e_crash_test.go` (keeps the journey file from
growing; same package `e2e`).

**Out of scope**: ANY production-code change. If a recovery path is broken,
the deliverable is a skipped/failing-documented test plus a report — see STOP
conditions. `internal/stack` fake-git tests (the shapes here are about real
git/worktree state, which the fake doesn't model).

## Git workflow

- Branch: `crash-window-recovery-tests`
- Commit message style: `test: table-driven crash-shape recovery coverage`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Build the table skeleton

`e2e/e2e_crash_test.go` with a table-driven `TestCrashShapeRecovery`: each
case = {name, build func(r) constructing the post-crash shape, recover
[]string (the st commands a user would run), wantHealthy bool}. Driver: build
healthy stack → apply shape → `st validate` (expect exit 1 for shapes 1–2/4,
exit 0 for shape 3) → run recovery commands → `st validate` → expect exit 0
→ `st log --json` parses. Start with shape 4 (known-good: existing
TestValidateRepairDrift proves the machinery).

**Verify**: `go test ./e2e -run TestCrashShape -count=1 -v` → the shape-4 case
PASSES.

### Step 2: Add shape 1 (fold window) and shape 2 (delete window)

Shape 1 recovery to try first: `st repair` (should untrack the missing branch
and re-parent its children — that is repair's documented missing-branch fix),
then `st restack`. Shape 2 recovery: `st restack` alone.

**Verify**: each new case either PASSES, or fails in a way you can attribute
precisely (see STOP conditions).

### Step 3: Add shape 3 (create window) as a documentation case

Assert the actual behavior (likely: validate clean, branch adoptable via
`st track`) and write the case's comment as the statement of expected
behavior.

**Verify**: `go test ./e2e -run TestCrashShape -count=1 -v` → all cases PASS
(or documented-failing per STOP handling).

### Step 4: Full gate

**Verify**: `make ci` → exit 0. If plan 012 landed, the new test starts with
`t.Parallel()` like its siblings.

## Test plan

This plan is tests. The table cases above are the complete list; each case's
comment names the engine window it models (file:line at time of writing).

## Done criteria

- [ ] `e2e/e2e_crash_test.go` exists with ≥4 table cases, each commented with the engine window it models
- [ ] `go test ./e2e -run TestCrashShape -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] Zero production files modified (`git status --porcelain` shows only the new test file and plans/README.md)
- [ ] `plans/README.md` row updated

## STOP conditions

- A recovery sequence does NOT converge (validate still failing after
  repair/restack/undo): do not patch production code. Mark the case with
  `t.Skip("recovery gap: <description> — see plans/README.md")`, add a
  finding line to the index's findings list, and report it as the primary
  outcome of this plan.
- A shape cannot be constructed black-box (needs internal hooks) — report
  rather than adding test hooks to production code.

## Maintenance notes

- Any future change to engine save-ordering (the
  "git mutations before save" windows) should add/adjust a case here in the
  same PR.
- These cases double as executable documentation of what `st repair` promises
  after each crash shape — reviewers of repair changes should read them.

# Plan 001: Cover conflict-resume from Fold, Squash, Delete, and Restack, and exercise Onto's conflict path against real git

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/stack/engine_test.go internal/stack/fakegit_test.go internal/stack/engine.go e2e/e2e_journey_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW (test-only changes; the risk is *finding* an engine bug, which is the point)
- **Depends on**: none
- **Category**: tests
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

A restack conflict leaves a real git rebase in progress, and `st continue` /
`st abort` are the recovery story for the product's hairiest behavior. Today,
conflict-resume is tested only from **modify**, **onto** (fake git only), and
**sync**. Four other operations can also pause on a conflict mid-flight —
**Fold**, **Squash**, **Delete** (re-parenting children), and explicit
**Restack** — and `Continue`'s generic recovery has *never* been executed from
any of those shapes. Fold and Delete are the scariest: both delete a branch and
save state *before* restacking the upstack, so a conflict there pauses with the
branch already gone from state and git. Additionally, Onto's conflict path
(`PendingReparent` record → conflict → `st continue` resumes with the right
base) has only ever run against the in-memory fake git — never against a real
`git rebase --onto` where `RebaseHeadName` parses `.git/rebase-merge/head-name`.
CLAUDE.md explicitly calls this out as a gotcha. This plan closes both gaps.

## Current state

- `internal/stack/engine.go` — the engine. Relevant operations:
  - `Restack(env Env, s *State)` at line 224
  - `Fold(env Env, s *State)` at line 263 — force-moves the parent to cur's
    tip, deletes cur, saves, then `finishUpstack(env, s, parent)` restacks the
    parent's descendants (a conflict can happen there).
  - `Squash(env Env, s *State, message string)` at line 337 — rewrites cur,
    then restacks descendants.
  - `Delete(env Env, s *State, name string, force bool)` at line 469 —
    deletes the branch, `s.RemoveBranch(name)` re-parents children, saves, then
    restacks each former child (lines 515–529); a conflict there returns
    `ErrConflict` with state already saved.
  - `Onto(...)` at line 402, `Continue(env Env, s *State)` at line 667.
- `internal/stack/fakegit_test.go` — the fake git. `conflictOn(branch)` (line
  66) makes the next rebase of `branch` stop mid-rebase; `rebaseErr` map (line
  41) makes a rebase fail *without* a conflict.
- `internal/stack/engine_test.go` — engine unit tests. Helpers:
  `newEnvState()` (line 10) returns `(*fakeGit, *State, Env)`; `mkBranch(t,
  env, s, f, parent, name)` (line 16) creates and tracks a branch.
- `internal/stack/model_test.go:258` — `checkInvariants(t *testing.T, f
  *fakeGit, s *State, step int)` asserts the forest is acyclic, parents valid,
  every branch contains its recorded base, restack reconciles. Same package, so
  engine tests can call it directly.
- Existing conflict-resume tests (the pattern to copy):
  - Fake-git: `TestOntoConflictRecordsPendingReparentWithoutChangingParent`
    in `internal/stack/engine_test.go` (around line 558): builds a stack,
    `f.conflictOn("b")`, asserts `errors.Is(err, ErrConflict)`, calls
    `Continue(env, s)`, asserts final metadata.
  - Real-git e2e: `TestConflictContinue` and `TestConflictAbort` in
    `e2e/e2e_journey_test.go` (lines ~201–262): drive a real conflict via
    `modify`, assert exit code 2 and `.git/rebase-merge` exists, resolve with
    `r.writeFile` + `r.git("add", ...)`, then `r.stOK("continue")`.
- The only `conflictOn` call sites today (proving the gap):
  `engine_test.go:569,606` (Onto), `model_test.go:200` (Modify-shaped),
  `sync_test.go:37` (sync). Nothing for Fold/Squash/Delete/Restack.
- e2e harness helpers (in `e2e/e2e_test.go` and used throughout
  `e2e_journey_test.go`): `newRepo(t)`, `r.initStack()`, `r.create(name, file,
  content, msg)`, `r.st(...)` / `r.stOK(...)`, `r.git(...)`, `r.writeFile(...)`,
  `wantExit(t, res, n)`, `wantStdoutContains` / `wantStderrContains`. The
  harness is fully hermetic (own HOME, `GIT_CONFIG_GLOBAL=/dev/null`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0, all `./internal/stack` tests pass |
| Race suite | `make test` | exit 0 |
| e2e only | `make e2e` | exit 0 |
| Full gate | `make ci` | exit 0, coverage ≥75% |

## Scope

**In scope** (the only files you should modify):
- `internal/stack/engine_test.go` (add tests)
- `e2e/e2e_journey_test.go` (add two tests)

**Out of scope** (do NOT touch, even though they look related):
- `internal/stack/engine.go` and any other production code. If a new test
  exposes an engine bug, that is a STOP condition — report the failing test and
  the suspected bug; do not fix the engine in this plan.
- `internal/stack/fakegit_test.go` — `conflictOn` and `rebaseErr` already
  support everything needed. Only touch it if a fake limitation blocks a test,
  and then only after re-reading its conflict-modeling comments (lines 29–66).
- `cmd/` tests and golden files.

## Git workflow

- Branch: `conflict-recovery-test-coverage` (repo convention: descriptive kebab-case).
- Commit message style (from `git log`): lowercase type prefix, e.g.
  `test: cover conflict-resume from fold, squash, delete, and restack`.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Fake-git conflict-resume test for Restack

In `internal/stack/engine_test.go`, add `TestRestackConflictContinueRecovers`:
build `main → a → b` with `mkBranch`, amend `a` so `b` needs restack (look at
how `model_test.go` or `sync_test.go:37` advances a parent — the fake's
`Commit`/`mkBranch` flow; the simplest is `mkBranch` a new commit on `a` via
the fake's commit method used by existing tests), checkout `a`,
`f.conflictOn("b")`, call `Restack(env, s)`, assert `errors.Is(err,
ErrConflict)`. Then `Continue(env, s)` must succeed; finish with
`checkInvariants(t, f, s, 0)`.

**Verify**: `go test ./internal/stack -run TestRestackConflict -count=1 -v` → PASS

### Step 2: Fake-git conflict-resume test for Fold

Add `TestFoldConflictContinueRecovers`: build `main → a → b → c`, checkout
`b`, `f.conflictOn("c")` (the descendant restacked by `finishUpstack` after
the fold), call `Fold(env, s)`, assert `ErrConflict`. Assert the
already-committed half: `b` is gone from state (`s.IsTracked("b") == false`)
and `c`'s parent is now `a`. Then `Continue(env, s)` succeeds and
`checkInvariants` passes.

**Verify**: `go test ./internal/stack -run TestFoldConflict -count=1 -v` → PASS

### Step 3: Fake-git conflict-resume tests for Squash and Delete

- `TestSquashConflictContinueRecovers`: `main → a → b`, two commits on `a`
  (so squash does something), checkout `a`, `f.conflictOn("b")`,
  `Squash(env, s, "squashed")` → `ErrConflict`; `Continue` → invariants hold.
- `TestDeleteConflictContinueRecovers`: `main → a → b`, checkout `main`,
  `f.conflictOn("b")`, `Delete(env, s, "a", true)` → `ErrConflict`. Assert `a`
  is untracked and `b`'s parent is `main` (the re-parent is recorded before the
  restack — that is the documented design). `Continue` → invariants hold.

**Verify**: `go test ./internal/stack -run 'TestSquashConflict|TestDeleteConflict' -count=1 -v` → PASS, and `make test-fast` → exit 0

### Step 4: Real-git e2e test for Onto conflict → continue

In `e2e/e2e_journey_test.go`, add `TestOntoConflictContinue`, modeled
structurally on `TestConflictContinue` (line ~201):

1. `newRepo` + `initStack`; `r.create("feat-a", "f.txt", "A\n", "a")`;
   `r.create("feat-b", "f.txt", "A\nB\n", "b")` (feat-b stacked on feat-a,
   its diff depends on feat-a's content).
2. Still on feat-b, run `res := r.st("onto", "main")` — rebasing feat-b's
   change onto main (where `f.txt` lacks the `A` line) conflicts.
   `wantExit(t, res, 2)`; `wantStderrContains(t, res, "st continue")`; assert
   `.git/rebase-merge` exists (copy the `os.Stat` check from
   `TestConflictContinue`).
3. Resolve: `r.writeFile("f.txt", "B\n")`; `r.git("add", "f.txt")`;
   `r.stOK("continue")`.
4. Assert the reparent landed: `r.stOK("validate")` reports no problems, and
   the state file records the new parent — read
   `filepath.Join(r.dir, ".git", "stacked", "state.json")` and assert it
   contains `"parent": "main"` for feat-b and does NOT contain
   `"pendingReparent"`.

**Verify**: `go test ./e2e -run TestOntoConflictContinue -count=1 -v` → PASS

### Step 5: Real-git e2e test for Onto conflict → abort

Add `TestOntoConflictAbort`: same setup through the exit-2 assertion, then
`r.stOK("abort")`; assert `.git/rebase-merge` is gone (copy from
`TestConflictAbort`), state.json does NOT contain `"pendingReparent"`, feat-b's
parent is still `"feat-a"`, and `r.stOK("validate")` passes.

**Verify**: `go test ./e2e -run TestOntoConflictAbort -count=1 -v` → PASS

### Step 6: Full gate

**Verify**: `make ci` → exit 0 (coverage gate ≥75% — these tests only raise it)

## Test plan

This plan *is* tests. New tests, all listed above: 4 fake-git engine tests in
`internal/stack/engine_test.go` (pattern:
`TestOntoConflictRecordsPendingReparentWithoutChangingParent`) and 2 e2e
journeys in `e2e/e2e_journey_test.go` (pattern: `TestConflictContinue` /
`TestConflictAbort`).

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go test ./internal/stack -run 'Conflict' -count=1` exits 0 and the output lists the 4 new tests
- [ ] `go test ./e2e -run 'TestOntoConflict' -count=1` exits 0 and runs 2 tests
- [ ] `grep -c "conflictOn" internal/stack/engine_test.go` ≥ 6 (was 2)
- [ ] `make ci` exits 0
- [ ] `git status --porcelain` shows changes only in the two in-scope files
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Any new test fails because of *engine* behavior (e.g. `Continue` after a
  Fold/Delete conflict corrupts state or `checkInvariants` fails). That is a
  real bug discovery — the deliverable becomes the failing test plus a
  description, not a fix.
- The fake git cannot model a needed shape (e.g. `conflictOn` doesn't trigger
  from `finishUpstack`) — report what's missing rather than extending the fake.
- The e2e `onto` invocation does not conflict (exit code ≠ 2) — the content
  recipe above is wrong for the real git in use; report the observed behavior.
- The code at the cited lines doesn't match the excerpts (drift).

## Maintenance notes

- These tests pin the documented design that Fold/Delete persist state
  *before* the upstack restack. If that ordering is ever changed, these tests
  must change with it — deliberately.
- A future "crash-window" test table (death between git mutation and state
  save) was considered and deferred; see plans/README.md findings list
  (TESTS-04).
- Reviewer should scrutinize: that each fake-git test really asserts the
  *post-Continue* state, not just the ErrConflict return.

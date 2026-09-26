# Plan 014: Document pending Onto intent and its commit/abort behavior

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- CLAUDE.md plans/014-correct-onto-recovery-documentation.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P3
- **Effort:** S
- **Risk:** LOW
- **Depends on:** none
- **Category:** docs
- **Audit item:** 14
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/014-correct-onto-recovery-documentation` (stacked on `advisor/013`).

- Step 1 confirmation: `internal/stack/engine.go` still stores `s.PendingReparent` on a paused conflict (`Onto`, ~line 569) and publishes `b.Parent`/`b.ParentSHA` only after a successful rebase; `Continue` promotes the stored intent (`isPendingReparent`, ~line 930); `Abort` clears it (~line 891). `TestOntoConflictRecordsPendingReparentWithoutChangingParent` (`engine_test.go:858`) passes against unchanged source → `go test ./internal/stack -run '^TestOntoConflictRecordsPendingReparentWithoutChangingParent$' -count=1` → PASS.
- Step 2: the CLAUDE.md bullet was replaced with the plan's prescribed wording (unchanged parent on conflict, pending intent, promote on continue, clear on abort) naming the regression test. `grep -n 'PendingReparent|TestOntoConflictRecordsPendingReparent' CLAUDE.md` → both terms present.
- `git diff -- CLAUDE.md` touches only the incorrect bullet; `git diff --check` → exit 0.
- Full `make ci` not run: this is a documentation-only edit (plan declares the full gate not required); the targeted regression plus scoped diff is the recorded evidence.

## Why this matters

The architecture guide tells future contributors that Onto changes the parent before rebasing. The implementation and regression test deliberately do the opposite on conflict. Correcting that invariant prevents later agents from undoing the safety behavior while believing they are following the guide.

## Current state

`CLAUDE.md:113` currently says:

```text
- `Onto` records the new parent in state **before** rebasing so `st continue`
  computes the right base after a conflict.
```

Actual behavior in internal/stack/engine.go:569 stores PendingReparent on a paused conflict; line 592 publishes the parent on success; Continue promotes pending intent around line 939; Abort clears it around line 891. TestOntoConflictRecordsPendingReparentWithoutChangingParent in internal/stack/engine_test.go:858 protects this contract.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

This is a focused documentation correction in the existing Conflicts & gotchas list. Do not treat the architecture guide as runtime behavior to implement. The surrounding paragraph already distinguishes paused conflict recovery from ordinary error handling.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Targeted existing regression | `go test ./internal/stack -run '^TestOntoConflictRecordsPendingReparentWithoutChangingParent$' -count=1` | PASS |
| Inspect corrected term | `rg -n 'PendingReparent' CLAUDE.md` | corrected paragraph appears |
| Whitespace | `git diff --check` | exit 0 |
| Build/lint/full gate (not required for this docs-only edit) | `make build fmt-check lint ci` | if run, exit 0 with pinned tools |

Do not install tooling or add tests for this documentation-only correction. The targeted existing engine regression plus a scoped diff is sufficient; report it separately from the historical full test baseline.

## Scope

**Only modify:**

- `CLAUDE.md`
- `plans/014-correct-onto-recovery-documentation.md`
- `plans/README.md`


**Out of scope:** all source and test code, state schema, rebase behavior, unrelated architecture-guide prose, changelog churn for this internal wording correction.

## Git workflow

Use a separate branch/worktree named `advisor/014-correct-onto-recovery-documentation` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `docs: correct onto conflict recovery invariant`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Confirm the invariant still matches source and its test

Read Onto, Continue and Abort in internal/stack/engine.go and the named test. Confirm conflict leaves Parent/ParentSHA unchanged and records PendingReparent, successful continuation promotes that stored intent, and abort clears it. If any of those facts changed since cb31f06, stop rather than copying stale prose.

**Verify:** `go test ./internal/stack -run '^TestOntoConflictRecordsPendingReparentWithoutChangingParent$' -count=1` → PASS against the unchanged source

### Step 2: Replace the incorrect bullet precisely

Replace the quoted bullet with: “`Onto` changes `Parent`/`ParentSHA` only after a successful rebase. A paused conflict preserves the old parent and records `PendingReparent`; `st continue` promotes that intent after the rebase completes, while `st abort` clears it and keeps the old parent. See `TestOntoConflictRecordsPendingReparentWithoutChangingParent` in `internal/stack/engine_test.go`.” Wrap to the surrounding Markdown style. Change no other guidance.

**Verify:** `rg -n 'PendingReparent|TestOntoConflictRecordsPendingReparentWithoutChangingParent' CLAUDE.md` → the corrected bullet names both the pending intent and its regression; `git diff --check` exits 0

## Test plan

- Run the existing named engine regression; create no new tests.
- Inspect `git diff -- CLAUDE.md`: only the incorrect Onto bullet is replaced.

## Done criteria

- [ ] The architecture guide describes unchanged parent during conflict, pending intent, promotion on continue, and clearing on abort.
- [ ] The named existing regression passes.
- [ ] Only CLAUDE.md and plan/index status documentation are modified.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The live implementation no longer preserves the documented pending-intent invariant.
- Any runtime change appears necessary to make the proposed wording true.

## Maintenance notes

Keep this bullet synchronized with the pending-reparent regression when recovery behavior changes. It describes an invariant, not an implementation suggestion.


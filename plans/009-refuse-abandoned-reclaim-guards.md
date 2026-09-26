# Plan 009: Fail closed when the fallback reclaim guard is abandoned

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/stack/lock_stale.go internal/stack/lock_stale_test.go internal/stack/lock_access_test.go internal/stack/lock_access_windows_test.go docs/AGENT.md CONTRIBUTING.md CHANGELOG.md plans/009-refuse-abandoned-reclaim-guards.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** M
- **Risk:** HIGH
- **Depends on:** 001 recommended; no algorithmic dependency
- **Category:** bug
- **Audit item:** 8
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/009-refuse-abandoned-reclaim-guards` (stacked on `advisor/008`).

- Drift check: `git diff --stat cb31f06..HEAD -- internal/stack/lock_stale.go internal/stack/lock_stale_test.go internal/stack/lock_access_test.go internal/stack/lock_access_windows_test.go docs/AGENT.md CONTRIBUTING.md CHANGELOG.md plans/009-refuse-abandoned-reclaim-guards.md plans/README.md` → only documented predecessor changes; no unexplained source drift. `git status --short` clean at start.
- `acquireReclaimGuard` now acquires strictly by `O_CREATE|O_EXCL` — no path unlinks another process's guard. An existing live or freshly-malformed guard stays ordinary contention (`(nil, nil)`); a guard whose owner is provably gone or whose malformed bytes are older than `malformedLockReclaimAfter` returns the new `*abandonedReclaimGuardError`, which names the guard path and tells the operator to stop all st processes, verify no writer is active, then remove the file. A guard vanishing between create-conflict and read still earns exactly one retry.
- `acquireExclLock` propagates `abandonedReclaimGuardError` unwrapped instead of collapsing it to `ErrLocked` — it exits 70 (`internal`), distinct from live contention (exit 5) and from the access-denied stale-owner branch, which is preserved verbatim. `lock.excl` reclamation still runs only under a freshly owned guard, and a process's own guard is still removed on release/write failure via the token-checked `removeLockFileIfContent`.
- Step-1 contract change: `TestAcquireReclaimGuardRecoversMalformedOldFile`/`...RecoversDeadOwner` replaced by `TestAcquireReclaimGuardRefusesAbandonedMalformedFile`/`...RefusesDeadOwnerGuard` — each asserts the typed error, the operator-facing message (names the path, "never removes", "no writer"), and that the guard's bytes are untouched.
- New deterministic contention test `TestAbandonedReclaimGuardRefusesAllContenders`: stale `lock.excl` + dead-owner `lock.reclaim`, 8 synchronized contenders — every one refused with the maintenance error (not the busy sentinel), both files byte-identical afterward.
- Focused verification: `go test ./internal/stack -run 'Test.*(Lock|Reclaim)' -race -count=1` → ok; `go test ./internal/stack -run 'Test(AbandonedReclaimGuard|StaleLockSingleReclaimer|AcquireExclLockMutualExclusion)' -race -count=20` → ok; `make test-fast` → ok; `make test` (race) → ok; `make e2e` → ok; `make build` → ok; `make fmt-check` + `make lint` (v2.12.2) → 0 issues; `make vet` + `make vet-cross` (windows, plan9) → ok.
- `make ci` on commit `1608b18` in detached worktree `/private/tmp/st-ci-009` → exit 0 (lint 0 issues, vet native/windows/plan9, build, race tests, e2e, merged coverage 87.0% ≥ 75%, per-function floor holds). First CI run failed the 50% per-function floor on `abandonedReclaimGuardError.Error` — fixed by asserting the message content in the refusal tests rather than allowlisting new code.
- `git diff --check` → exit 0; modified files all in Scope (`internal/stack/lock_stale.go`, `internal/stack/lock_stale_test.go`, `CONTRIBUTING.md`, `CHANGELOG.md`, `docs/AGENT.md`).
- Docs: `CONTRIBUTING.md` gained a Troubleshooting section with the operator procedure (stop all st processes, verify no writer, then remove the named guard — no unconditional deletion command), the exit-70-vs-5 distinction, and Windows CI coverage notes; `docs/AGENT.md` tells agents the abandoned guard is non-retryable maintenance, not contention; `CHANGELOG.md` records the fail-closed policy. Unix `flock` behavior untouched (`lock_unix.go` unmodified).

## Why this matters

The fallback lock reclaims lock.reclaim with a read/compare/unlink sequence that is not atomic. Two processes can remove different generations of that guard and both enter stale-lock reclamation. This plan stops automatic recovery of the guard itself, while retaining safe reclamation of lock.excl under a freshly owned guard. A rare abandoned guard will require operator maintenance instead of risking two writers.

## Current state

`internal/stack/lock_stale.go:50` compares file contents and then removes the path:

```go
if string(content) != want {
    return false, nil
}
err = os.Remove(path)
```

`acquireReclaimGuard` at line 118 uses O_EXCL but reclaims an existing abandoned guard with the same helper. `acquireExclLock` at line 162 trusts that guard to serialize removal of lock.excl. The helper is shared into native tests, while lock_other.go selects the fallback for Windows/Plan 9. TestStaleLockSingleReclaimer currently covers a stale lock.excl with no stale guard.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Preserve ErrLocked for genuine live contention and existing access-denied classification. Do not change Unix flock behavior. Existing lock tests use real temporary files and current/dead owner tokens; follow TestMalformedLockIsAbandonedOnlyWhenOldAndUnowned and TestStaleLockSingleReclaimer.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/stack -run 'Test.*(Lock\|Reclaim)' -race -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
| Fast engine suite | `make test-fast` | all tests pass |
| Race unit/integration | `make test` | all tests pass |
| Black-box CLI | `make e2e` | all tests pass |
| Build | `make build` | exit 0; writes the ignored st binary |
| Formatting / lint checks | `make fmt-check lint` | exit 0 using pinned golangci-lint v2.12.2 |
| Complete contributor gate | `make ci` | exit 0, including dependency/pin checks, native/cross vet, build, lint, and coverage |
| Whitespace | `git diff --check` | exit 0 |

The audit baseline passed race tests, e2e, native/cross vet and coverage (87.0%) at the stamped commit. Full `make ci` was not run because golangci-lint was absent. That is historical evidence, not validation of your changes. Run the full gate when the pinned tools are available; if a required tool is absent, report the incomplete gate instead of substituting gofmt or changing tool pins.

## Scope

**Only modify:**

- `internal/stack/lock_stale.go`
- `internal/stack/lock_stale_test.go`
- `internal/stack/lock_access_test.go`
- `internal/stack/lock_access_windows_test.go`
- `docs/AGENT.md`
- `CONTRIBUTING.md`
- `CHANGELOG.md`
- `plans/009-refuse-abandoned-reclaim-guards.md`
- `plans/README.md`


**Out of scope:** OS-specific lock replacement, new dependencies/syscall packages, automatic deletion of abandoned reclaim guards, Unix flock changes, CLI force-unlock commands.

## Git workflow

Use a separate branch/worktree named `advisor/009-refuse-abandoned-reclaim-guards` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: refuse unsafe stale reclaim guard recovery`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Specify abandoned-guard refusal deterministically

Replace the tests that expect automatic recovery of a dead-owner or old-malformed lock.reclaim with explicit refusal expectations. Add TestAbandonedReclaimGuardRefusesAllContenders: precreate both stale lock.excl and abandoned lock.reclaim, start synchronized contenders, and require every contender to refuse with neither file's bytes changed. This does not depend on probabilistically hitting the original race; the old automatic-reclaim policy must fail the new safety assertion. Keep TestStaleLockSingleReclaimer unchanged as the positive control for reclaiming lock.excl when the guard starts absent.

**Verify:** `go test ./internal/stack -run 'Test(AbandonedReclaimGuard|AcquireReclaimGuard)' -count=1` → the original automatic-guard-recovery implementation fails the new refusal expectations

### Step 2: Make guard acquisition exclusive without stale unlink

In acquireReclaimGuard, acquire only by O_CREATE|O_EXCL. If an existing guard is live or newly malformed, return ordinary contention without modifying it. If its owner is provably gone, or malformed data is old under the existing conservative predicate, return a distinguishable internal abandoned-guard error with its path. Never remove an existing guard to obtain ownership. Continue removing this process's own guard on release/write failure as today. In acquireExclLock propagate the abandoned-guard condition as an actionable maintenance error instead of collapsing it to ErrLocked. Keep access-denied errors distinguishable. Lock.excl reclamation remains under a successfully owned guard; do not remove its token checks.

**Verify:** `go test ./internal/stack -run 'Test.*(Lock|Reclaim)' -race -count=1` → all refusal, live contention, permissions, replacement-release and stale-lock-only recovery cases pass

### Step 3: Document the recovery tradeoff and verify platform builds

Document that abandoned lock.reclaim is not automatically reclaimed. The diagnostic and docs must tell the operator to stop all stacked processes and verify no writer is active before manually removing the named guard; do not print an unconditional ready-to-run deletion command. Explain that live contention remains the existing lock exit category, while an abandoned guard is a maintenance error. Update the changelog and contributor troubleshooting notes. Run native race tests repeatedly and native/cross vet; require the existing Windows CI test job for release confidence.

**Verify:** `go test ./internal/stack -run 'Test(AbandonedReclaimGuard|StaleLockSingleReclaimer|AcquireExclLockMutualExclusion)' -race -count=20` → all repetitions pass; `make vet vet-cross` and `make ci` pass, with Windows CI runtime results recorded when available

## Test plan

- Absent guard + stale lock.excl still admits exactly one owner.
- Dead-owner guard and old-malformed guard refuse all contenders without changing either file.
- Live owner, fresh malformed guard, inaccessible files, replaced-token release and ordinary uncontended acquisition retain their contracts.
- No timing-only stress test substitutes for deterministic abandoned-guard preservation assertions.

## Done criteria

- [ ] acquireReclaimGuard contains no path that unlinks another process's existing guard.
- [ ] Deterministic dual-stale-file refusal and stale-lock-only recovery tests pass under -race.
- [ ] Maintenance errors remain distinct from live contention and permission failures.
- [ ] `make ci` and `make vet-cross` pass; platform runtime coverage limitations are recorded.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The fix proposes another read-then-unlink stale-guard scheme, inode comparison without an atomic deletion primitive, or a timeout that deletes a live guard.
- Tests require modifying Unix flock or platform owner-detection behavior to pass.
- A diagnostic recommends guard removal while writers may still be active.

## Maintenance notes

This is an explicit availability-for-integrity tradeoff. Automatic abandoned-guard recovery can return only with a proven platform-appropriate atomic ownership protocol and deterministic concurrency tests.


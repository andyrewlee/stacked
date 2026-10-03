# Plan 022: Serialize navigation and initialization with stack mutations

> **Executor instructions**: Follow the steps and gates; stop rather than broadening scope. Update the index status when complete unless a reviewer maintains it.
>
> **Drift check first**: `git diff --stat 159648a..HEAD -- cmd/mutate.go cmd/checkout.go cmd/up.go cmd/down.go cmd/top.go cmd/bottom.go cmd/shell.go cmd/init.go cmd/navigation_lock_test.go cmd/json_envelope_test.go cmd/commands_json_test.go e2e/e2e_navigation_lock_test.go docs/AGENT.md CHANGELOG.md plans/README.md`
> Inspect uncommitted changes to these paths and compare the excerpts before editing.

## Status

- **Priority**: P1
- **Effort**: S–M
- **Risk**: MED — changes which navigation/init calls may return locked
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `159648a`, 2026-10-02
- **Selection**: deep-audit F02 (both navigation and init)

## Why this matters

Modify/create hold a repository lock but navigation does not. A concurrent st checkout can switch HEAD after Modify captures its branch, causing the amend to hit another branch, even trunk, while the cascade uses the old branch name. Init also performs an unlocked check-then-replace; concurrent initializers can overwrite metadata another st process has begun using. Extend the existing lock protocol to these operations without creating undo entries for navigation or initialization.

## Current state

- cmd/mutate.go:23 is the existing acquire-before-load exemplar:

```go
release, err := acquireLock()
if err != nil {
    return nil, nil, err
}
s, err := loadState()
if err != nil {
    release()
    return nil, nil, err
}
return s, release, nil
```

- cmd/checkout.go:38 loads state unlocked, then cmd/shell.go:218 executes git.Checkout. up/down/top/bottom use unlocked loadStateAndCurrent before the same helper.
- internal/stack/ops_lifecycle.go:101 captures cur, checks trunk, and later calls AmendMessage/AmendNoEdit against current HEAD. The captured cur is used for finishUpstack.
- cmd/init.go:43,58 performs Load then Init without acquireLock:

```go
if existing, err := stack.Load(); err == nil {
    return emit(asJSON, initResult{Trunk: existing.Trunk, AlreadyInitialized: true}, ...)
} else if !errors.Is(err, stack.ErrNotInitialized) {
    return err
}
// trunk selection and branch existence check
if _, err := stack.Init(trunk); err != nil {
    return fmt.Errorf("initializing stacked: %w", err)
}
```

The emit body is abbreviated; preserve its current payload/text exactly.
- internal/stack/store.go:97 uses Stat before Save; atomic replacement is not mutual exclusion. Existing Lock creates its stacked directory before state exists, so init can acquire it.
- cmd/json_envelope_test.go:231 is the held-lock fixture: stack.Lock then mutating Execute calls must yield exit 5/code locked; read-only log/status/preview still succeed.
- cmd/commands_json_test.go:671 pins fresh/repeated init shape. cmd/integration_test.go:82 supplies a temp repo and resets cache; never t.Parallel these cwd-changing cmd tests.
- docs/AGENT.md:280 promises concurrent st serialization. Arbitrary external Git does not participate in this lock.
- Hard constraints: stdlib-only Go 1.26; thin cmd adapters, unchanged engine API and lock implementation, zero require entries/no go.sum.

## Commands you will need

- Focused adapters: `go test -timeout 20m ./cmd -run 'TestNavigationLock|TestInitLock|TestJSONEnvelopeLocked|TestInitJSONFreshAndRepeatedShape' -count=1` → all pass.
- Navigation regressions: `go test -timeout 20m ./cmd -run 'TestCheckout|TestUp|TestDown|TestTop|TestBottom|TestInit' -count=1` → all pass.
- Cross-process proof and suite convention: `go test -timeout 20m ./e2e -run 'TestNavigationLock|TestEveryE2ETestIsParallel' -count=1` → all pass.
- Engine baseline: `make test-fast` → exit 0.
- Full gate: `make ci` → exit 0.

Audit tests/vet passed at this HEAD; full CI is blocked locally by absent golangci-lint v2.12.2. Do not replace the gate or install dependencies into go.mod to work around tooling.

## Scope

**Only modify**:
- cmd/mutate.go — locking/current-branch helper and correct lock comment
- cmd/checkout.go, cmd/up.go, cmd/down.go, cmd/top.go, cmd/bottom.go
- cmd/shell.go — caller-lock precondition comment; cache reset after direct checkout
- cmd/init.go
- cmd/navigation_lock_test.go (create)
- cmd/json_envelope_test.go and cmd/commands_json_test.go
- e2e/e2e_navigation_lock_test.go (create)
- docs/AGENT.md, CHANGELOG.md
- plans/README.md, status only

**Out of scope**: lock timing/reclamation/platform internals; mutateState undo protocol; engine Modify/Create; read-only completion/log/status; new flags or JSON fields; the newline-path shell-shim defect; telemetry or subprocess caching refactors.

## Git workflow

- Branch `advisor/022-navigation-init-locking`; no overwriting other work.
- Imperative commit examples: “Serialize navigation with stack mutations”, “Lock initialization before reading state”.
- No push or PR unless instructed.

## Steps

### Step 1: Lock selection through navigation

Add lockAndLoadCurrent in cmd/mutate.go, returning state, current branch, release, error. Call lockAndLoad first, then currentBranch. Release immediately if currentBranch fails; on success the caller defers release exactly once. Keep the old unlocked loadStateAndCurrent for genuinely read-only callers.

Update runUp/runDown/runTop/runBottom to use the locked helper before topology/current reads and hold it through every return/teleportCheckout. For runCheckout, parse/count-check first; zero positional args remains loadState/listBranches without locking. A single target uses lockAndLoad and defers release before tracked-name validation or checkout.

Do not acquire another lock inside teleportCheckout: its callers now own the lock. Document that precondition and search all callers to ensure coverage. Retain Git's existing rebase/dirty refusals; do not route navigation through mutateState or RecordUndo. Reset the worktree cache after a direct checkout attempt, including an error, matching existing cachedPort policy.

**Verify**: `go test -timeout 20m ./cmd -run 'TestCheckout|TestUp|TestDown|TestTop|TestBottom' -count=1` → exit 0; `rg -n 'teleportCheckout\(' cmd --glob '*.go'` → every production caller is one of the reviewed locked navigation paths.

### Step 2: Lock the entire init check/write protocol

In runInit, after parse/repo validation and before stack.Load, acquireLock and defer release. Keep detection, existing-state outcome, branch check, stack.Init, and emission inside that lifetime. Do not use lockAndLoad: a not-yet-initialized repository is the successful input.

Correct acquireLock's stale comment claiming non-flock platforms are a no-op; they use a fallback lock. Preserve fresh/repeated init payload keys, no undo capture, and existing malformed-state refusal.

**Verify**: `go test -timeout 20m ./cmd -run 'TestInit|TestInitJSONFreshAndRepeatedShape' -count=1` → all pass.

### Step 3: Pin blocking and release without sleeps

In cmd/navigation_lock_test.go, use newRepo and a main→a→b chain with a checked out. Hold stack.Lock with ST_LOCK_WAIT=0. Table-drive checkout b, up, down, top, bottom, and init; each must return errors.Is(err, stack.ErrLocked) and preserve HEAD, refs, state bytes, and undo bytes. Include aliases through Execute in the JSON-envelope test and require exit 5/code locked.

Under the same held lock, checkout with no target, status/log, and existing dry-run readers still succeed. Test init before state exists: acquire Lock in a fresh temp repo, assert Init refuses and creates no state; after release initialize once, create a tracked branch, and repeat init with a different valid trunk. It must report the existing trunk and preserve tracked metadata.

Test error-return release: unknown navigation target or a failing current-branch read must not strand the lock; a subsequent stack.Lock succeeds. Use deterministic held locks/channel handshakes or subprocess barriers, not timing sleeps.

**Verify**: `go test -timeout 20m ./cmd -run 'TestNavigationLock|TestInitLock|TestJSONEnvelopeLocked|TestInitJSONFreshAndRepeatedShape' -count=1` → all pass.

### Step 4: Prove cross-process blocking and update contract

Add TestNavigationLockAcrossProcesses in the new e2e file. r.stInEnv applies cleanEnv to children, but stack.Lock in the **parent test process** uses the parent's inherited Git environment. Before acquiring the lock, isolate that parent too: save inherited GIT_* values in memory, unset those variables with os.Unsetenv, and register cleanup restoring them (never log values). Pin GIT_CONFIG_GLOBAL and GIT_CONFIG_SYSTEM to os.DevNull with t.Setenv. Do not clear variables merely by assigning empty strings: Git's config-count variables need to be absent. Then t.Chdir into the fixture to acquire stack.Lock and drive the separate binary via r.stInEnv with ST_LOCK_WAIT=0. Apply this isolation before the first parent-side Git/common-dir probe.

This test must carry the exact structured doc-comment opt-out `// not-parallel: changes process cwd/environment to hold the repository lock across child-process calls`. Do not call t.Parallel or weaken e2e/parallel_meta_test.go; that existing guard explicitly permits a documented opt-out. All other new e2e tests should start with t.Parallel.

A locked checkout must exit 5 without moving HEAD; after releasing the lock it must succeed. Similarly assert fresh init refuses while the lock is held and succeeds afterward. Register lock cleanup after t.Chdir, so it releases before cwd cleanup. No competing mutation or timing sleep is needed: the test holds the lock before starting the child.

Update docs to identify navigation-with-target and init as lock participants, while checkout listing and pure readers remain available. Add Unreleased Fixed notes.

**Verify**: `go test -timeout 20m ./e2e -run 'TestNavigationLock|TestEveryE2ETestIsParallel' -count=1`, `make test-fast`, and `make ci` → exit 0.

## Test plan

- Held-lock nonmutation table covers every HEAD-changing navigation route, init before/after initialization, and JSON/aliases.
- Release tests cover successful calls and early errors without sleeps.
- Real binary plus a lock held by another process proves advisory-lock participation.
- Existing teleport, branch-point, dirty checkout, and idempotent init tests preserve output and behavior when the lock is free.

## Done criteria

- [ ] TestNavigationLock*, TestInitLock*, and existing nav/init/envelope tests pass with commands above.
- [ ] Every production teleportCheckout caller holds the repository lock through selection and checkout.
- [ ] Pure checkout listing/observation succeeds under a held lock; navigation/init produce exit 5/code locked.
- [ ] No navigation/init undo entry is added and JSON shapes remain unchanged.
- [ ] `make ci` exits 0; only scoped files changed; index updated.

## STOP conditions

- A teleportCheckout caller outside scoped navigation paths also needs locking.
- A proposed helper would reacquire a held lock, change lock reclamation/wait policy, or require engine edits.
- Cross-process fixture cannot acquire the existing lock in an uninitialized temp repository.
- A cmd test needs t.Parallel despite global cwd/env use; isolate it rather than adding nondeterminism.
- A verification fails twice, required tooling is missing, or the scope must expand.

## Maintenance notes

Lock before reading the state/current branch, not only immediately before checkout. Future navigation must participate even if it sometimes only teleports. This serializes st commands, not external Git or the user's interactive shell after the process exits.

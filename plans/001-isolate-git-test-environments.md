# Plan 001: Isolate real-Git fixtures from host configuration

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/git/testmain_test.go internal/stack/testmain_test.go cmd/integration_test.go cmd/testenv_test.go plans/001-isolate-git-test-environments.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** S
- **Risk:** LOW
- **Depends on:** none
- **Category:** tests
- **Audit item:** 9
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/001-isolate-git-test-environments` (stacked on `docs-round-6-plans`).

- Step 1 expected-failure check: `go test ./internal/git ./internal/stack ./cmd -run '^TestGitFixtureEnvironmentIsolation$' -count=1` → all three regressions FAILED pre-fix (internal/git and cmd children broke on the commit; internal/stack asserted the decoy git dir). Assertions kept.
- Step 2: added `TestMain` + `normalizeGitTestEnv` to `internal/git/testmain_test.go` and `internal/stack/testmain_test.go`; extended the existing cmd `TestMain` to call the helper in `cmd/testenv_test.go` (stdout silencing and `ST_TEST_DEBUG` preserved). Re-ran the focused regression → all three PASS, sentinels absent, decoy repo untouched.
- Step 3: `GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=commit.gpgSign GIT_CONFIG_VALUE_0=true GIT_CONFIG_KEY_1=gpg.program GIT_CONFIG_VALUE_1=/usr/bin/false go test ./internal/git -run '^TestCurrentBranchAndExists$' -count=1` → PASS.
- `go test ./internal/git ./internal/stack ./cmd -count=1` → ok (91s / 14s / 340s).
- `make ci` → exit 0 (deps, pins, fmt-check, vet native+windows+plan9, build, golangci-lint v2.12.2 0 issues, race tests, e2e, merged coverage 86.9% ≥ 75%).
- `git diff --check` → exit 0; modified files all in Scope.

## Why this matters

Real-Git tests currently inherit configuration and repository-routing variables from the invoking developer. A command-scoped signing configuration made an otherwise passing test fail while creating its fixture. Isolate the harness so later safety regressions test stacked rather than the machine's hooks, signing setup, or repository location.

## Current state

`internal/git/git_test.go:212` creates repositories with `newRepo`; its `mustGit` helper at line 226 inherits the process environment:

```go
out, err := exec.Command("git", args...).CombinedOutput()
```

`internal/stack/store_test.go:18` similarly creates real repositories. `cmd/integration_test.go:21` already has a `TestMain`, but only overrides selected variables:

```go
os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
os.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
os.Setenv("GIT_TERMINAL_PROMPT", "0")
```

That does not remove inherited `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_*`, signing overrides, or `GIT_DIR`. `e2e/e2e_test.go` already uses a deliberately isolated subprocess environment; preserve that implementation.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Fixture exemplar: `newRepo` calls `t.TempDir()`, `t.Chdir(dir)`, then `mustGit(t, "config", "user.email", "test@example.com")`. Keep temporary repositories and cleanup owned by the test harness. Tests that deliberately call `t.Setenv` after initialization must retain that ability.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/git ./internal/stack ./cmd -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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

- `internal/git/testmain_test.go`
- `internal/stack/testmain_test.go`
- `cmd/integration_test.go`
- `cmd/testenv_test.go`
- `plans/001-isolate-git-test-environments.md`
- `plans/README.md`

The two package TestMain files and cmd/testenv_test.go are new, test-only files. Avoid introducing a shared production utility just to remove a few lines of test duplication.
**Out of scope:** production gitEnv, user Git configuration, Go/tool versions, e2e environment policy, dependency additions.

## Git workflow

Use a separate branch/worktree named `advisor/001-isolate-git-test-environments` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `test: isolate git fixtures from inherited configuration`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Add a subprocess regression that actually creates a commit

Add `TestGitFixtureEnvironmentIsolation` in each scoped package's test file. A parent launches the same test binary with a marker variable and a narrowly selected helper test. Supply a temporary hostile global Git config, inherited `GIT_CONFIG_COUNT` signing settings, and repository-routing variables pointing to a separate disposable repository. The child uses the package's ordinary fixture helper to initialize and commit; assert the resulting repository is its own temporary directory. A temporary hook/signing helper should record invocation to a sentinel so the parent can prove it was not used. Preserve coverage/runtime variables in the child environment. Bound recursion with the marker and make parent errors include child output.

**Verify:** `go test ./internal/git ./internal/stack ./cmd -run '^TestGitFixtureEnvironmentIsolation$' -count=1` → before the fix, the new tests fail specifically because inherited Git configuration/routing contaminates fixture creation; keep the failing assertions, not a test expecting contamination

### Step 2: Normalize the environment once before each package suite

Add `TestMain` to internal/git and internal/stack; extend the existing cmd TestMain without losing stdout silencing or ST_TEST_DEBUG. At process startup, remove inherited variables whose names begin `GIT_`, including indexed config variables. Then set explicit null global/system config paths using os.DevNull, disable terminal prompting, and set deterministic noninteractive editor/pager values matching the existing cmd/e2e conventions. Keep PATH and platform variables such as SYSTEMROOT, HOME, temporary directory and coverage settings intact. Fixture-local identity setup remains authoritative. Do not strip variables again inside each Git invocation, since individual tests intentionally set Git behavior after startup. Check setup errors instead of continuing with partially normalized configuration.

**Verify:** `go test ./internal/git ./internal/stack ./cmd -run '^TestGitFixtureEnvironmentIsolation$' -count=1` → all three regressions pass, hostile sentinels remain absent, and the helper's commit belongs to its temporary repository

### Step 3: Run the contaminated invocation and normal gates

Run the exact signing contamination that failed in the audit, then all contributor checks. Confirm existing tests that deliberately set Git configuration after TestMain still exercise their intended behavior. Do not change production configuration sanitization as part of this test-only change.

**Verify:** `GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=commit.gpgSign GIT_CONFIG_VALUE_0=true GIT_CONFIG_KEY_1=gpg.program GIT_CONFIG_VALUE_1=/usr/bin/false go test ./internal/git -run '^TestCurrentBranchAndExists$' -count=1` → PASS on Unix; use the subprocess regression's portable helper on Windows. Then `make ci` exits 0

## Test plan

- Model real fixture creation on internal/git/git_test.go:newRepo and internal/stack/store_test.go:initGitRepo, not a unit test that only checks environment strings.
- Exercise indexed config overrides, explicit global config, routing variables, and a host hook/signing sentinel. All hostile configuration must live under test temporary directories.
- `go test ./internal/git ./internal/stack ./cmd -race -count=1` passes; existing per-test environment overrides still work.

## Done criteria

- [ ] The previously failing command-scoped signing invocation passes on Unix.
- [ ] Three subprocess regressions prove repository isolation and no hostile hook/signer invocation.
- [ ] `make ci` passes without changing production Go files or adding module dependencies.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- A package already has another TestMain at execution time; merge deliberately after reconciling its setup rather than defining a second entry point.
- A Git variable intentionally required by the harness cannot be restored explicitly after startup; identify it before broadening the environment policy.

## Maintenance notes

New real-Git test packages should adopt the same startup isolation. Keep test configuration policy separate from production Git behavior, which must still respect legitimate user settings.


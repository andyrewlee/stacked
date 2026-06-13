# Plan 002: Isolate the cmd integration tests from the machine's global git config

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- cmd/integration_test.go`
> If the file changed since this plan was written, compare the "Current state"
> excerpt against the live code before proceeding; on a mismatch, treat it as a
> STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: tests
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

The repo's contract is "if `make ci` is green you can refactor without manual
testing." The `cmd/` integration tests run real git in temp repos, but unlike
the e2e harness they do **not** disable the user's global/system git config. On
a contributor machine with `commit.gpgsign=true`, `core.hooksPath`,
`init.templateDir`, or `rebase.autoStash`, the ~40 cmd integration tests can
fail spuriously or silently change behavior (`rebase.autoStash` converts
dirty-tree failures into successes). CI runners are clean, so this only bites
locally — exactly where the "green = safe" promise matters most.

## Current state

- `cmd/integration_test.go:19–24` — `TestMain` only silences stdout:

  ```go
  func TestMain(m *testing.M) {
      if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
          os.Stdout = devnull
      }
      os.Exit(m.Run())
  }
  ```

- `cmd/integration_test.go:43–54` — `newRepo(t)` creates a temp repo and sets
  only repo-local `user.email` / `user.name`. Nothing disables the global or
  system config for the git processes the tests (and the in-process `st` code)
  spawn.
- The e2e harness already does this correctly and documents why —
  `e2e/e2e_test.go` `cleanEnv()` (around line 116) sets:

  ```go
  "GIT_CONFIG_GLOBAL=" + os.DevNull,
  "GIT_CONFIG_SYSTEM=" + os.DevNull,
  ...
  "GIT_TERMINAL_PROMPT=0",
  "GIT_PAGER=cat",
  "GIT_EDITOR=true",
  ```

  Match that pattern. (The cmd tests run `st` in-process, so the env must be
  set on the *test process itself* — `os.Setenv` in `TestMain` before
  `m.Run()`, which propagates to every git subprocess.)

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| cmd tests | `go test ./cmd/... -race -count=1` | exit 0 |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope** (the only file you should modify):
- `cmd/integration_test.go`

**Out of scope**:
- `e2e/e2e_test.go` — already hermetic; don't touch.
- Any production code, any other test file.
- Do not try to make the cmd tests parallel — they `t.Chdir` between temp
  repos, which is incompatible with `t.Parallel()`.

## Git workflow

- Branch: `hermetic-cmd-integration-tests`
- Commit message style: `test: isolate cmd integration tests from host git config`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Establish the failing baseline

Confirm the problem is real on this machine shape:

```sh
tmphome=$(mktemp -d) && printf '[commit]\n\tgpgsign = true\n' > "$tmphome/.gitconfig" && HOME="$tmphome" go test ./cmd/... -count=1
```

**Verify**: the command FAILS (gpg signing breaks commits in the temp repos).
If it passes, the host git honors a different config path — try
`XDG_CONFIG_HOME="$tmphome" git config --global commit.gpgsign true` style
setup, and if you cannot produce a failure at all, record that in your report
and continue (the fix is still correct hardening).

### Step 2: Set the hermetic env in TestMain

In `cmd/integration_test.go`'s `TestMain`, before `m.Run()`, set (via
`os.Setenv`):

- `GIT_CONFIG_GLOBAL` = `os.DevNull`
- `GIT_CONFIG_SYSTEM` = `os.DevNull`
- `GIT_TERMINAL_PROMPT` = `0`
- `GIT_PAGER` = `cat`
- `GIT_EDITOR` = `true`

Add a short comment mirroring the e2e harness's wording (see
`e2e/e2e_test.go` `cleanEnv` doc comment) explaining that the cmd suite must
not depend on host git config. Do not set author/committer env vars —
`newRepo` already sets repo-local identity, and tests may rely on that.

**Verify**: `go test ./cmd/... -race -count=1` → exit 0

### Step 3: Re-run the hostile-config canary

Re-run the Step 1 command verbatim.

**Verify**: it now PASSES (exit 0).

### Step 4: Full gate

**Verify**: `make ci` → exit 0

## Test plan

No new test functions — the change is to the test harness itself. The
verification is the Step 1/Step 3 canary pair: failing before, passing after.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -n "GIT_CONFIG_GLOBAL" cmd/integration_test.go` → 1 match in `TestMain`
- [ ] `go test ./cmd/... -race -count=1` exits 0
- [ ] The Step 3 hostile-HOME canary exits 0
- [ ] `make ci` exits 0
- [ ] `git status --porcelain` shows only `cmd/integration_test.go` modified
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Setting the env makes existing cmd tests FAIL — that means a test was
  accidentally depending on host config; report which test, don't patch it
  silently.
- The fix appears to require touching any file other than
  `cmd/integration_test.go`.

## Maintenance notes

- Any future test helper that spawns git from the cmd package inherits this
  env automatically (process-wide). If a test ever *needs* custom git config,
  it should set repo-local config in its temp repo, like `newRepo` does.
- Reviewer should check no author/committer env vars were added (they'd
  shadow repo-local identity and weaken what the tests verify).

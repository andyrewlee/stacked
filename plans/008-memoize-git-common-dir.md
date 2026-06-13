# Plan 008: Resolve the stacked metadata dir once per process, not ~8 times per command

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/stack/store.go internal/stack/lock_unix.go internal/stack/undo.go cmd/integration_test.go`
> PR #33 edits `internal/stack/undo.go` (snapshot batching) — known drift,
> compatible with this plan. Any other mismatch with the excerpts below is a
> STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: MED — a naive process-global cache breaks the cmd integration
  tests, which `t.Chdir` between different repos inside one test process.
- **Depends on**: none
- **Category**: perf
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

Every path computation in the stack package spawns
`git rev-parse --path-format=absolute --git-common-dir`. One mutating command
hits it ~7–8 times (lock → load → undo journal reads/writes → save), and each
engine checkpoint during sync adds one more — ~20 identical subprocess spawns
on a busy sync, all answering a question whose answer cannot change within a
single command run. Memoizing it keyed by working directory removes the
repeated spawns without breaking the in-process tests.

## Current state

- `internal/stack/store.go:16–22`:

  ```go
  func stackedDir() (string, error) {
      gitDir, err := git.GitCommonDir()
      if err != nil { return "", fmt.Errorf("locate git dir: %w", err) }
      return filepath.Join(gitDir, "stacked"), nil
  }
  ```

  Callers: `statePath()` (store.go:26–32, used by `Load`, `Save`, `Init`),
  the lock (`lock_unix.go:19` and `lock_other.go:21`), and `undoPath()`
  (`undo.go:28`) — every journal read/write resolves it again.
- `internal/git/git.go:443–461` — `GitCommonDir()` spawns
  `rev-parse --path-format=absolute --git-common-dir` (plus a fallback).
- **The test constraint**: `cmd/integration_test.go` tests call
  `newRepo(t)` which does `t.Chdir(dir)` into a *fresh repo per test*
  (integration_test.go:43–54), all within one process. A `sync.Once` global
  would leak repo A's git dir into repo B's test. The e2e suite is unaffected
  (each `st` invocation is its own process).
- Repo conventions: the stack package reaches git only through package
  `internal/git` functions or the `Git` port; keep the memo in the `stack`
  package (it's a policy about reuse, not about git).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| cmd tests (the risk area) | `go test ./cmd/... -race -count=1` | exit 0 |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/store.go` (the memo), `internal/stack/store_test.go`
(one test).

**Out of scope**: `internal/git/git.go` (`GitCommonDir` itself — the memo is
the caller's concern), the lock files, `undo.go` (they call `stackedDir()`
and inherit the memo), any cmd file.

## Git workflow

- Branch: `memoize-git-common-dir`
- Commit message style: `perf: resolve the stacked dir once per working directory`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add a cwd-keyed memo to `stackedDir`

In `internal/stack/store.go`:

```go
var stackedDirCache sync.Map // cwd -> dir

func stackedDir() (string, error) {
    cwd, err := os.Getwd()
    if err == nil {
        if dir, ok := stackedDirCache.Load(cwd); ok {
            return dir.(string), nil
        }
    }
    gitDir, gerr := git.GitCommonDir()
    if gerr != nil {
        return "", fmt.Errorf("locate git dir: %w", gerr)
    }
    dir := filepath.Join(gitDir, "stacked")
    if err == nil {
        stackedDirCache.Store(cwd, dir)
    }
    return dir, nil
}
```

Keying by `os.Getwd()` is what makes the `t.Chdir` tests correct: each test
repo is a different cwd, so each gets its own entry; within one CLI run the
cwd is stable, so every later call is spawn-free. Do **not** cache errors.
Add a comment stating exactly this constraint (the cmd tests chdir between
repos in-process), so nobody "simplifies" it to `sync.Once` later.

**Verify**: `make test-fast` → exit 0; `go test ./cmd/... -race -count=1` →
exit 0 (this is the suite that would catch cross-repo leakage; `-race` also
exercises the `sync.Map` under the parallel-capable engine tests).

### Step 2: Pin the behavior with a unit test

In `internal/stack/store_test.go`, add `TestStackedDirPerWorkingDirectory`:
create two temp git repos (follow the file's existing temp-repo setup
pattern), `t.Chdir` into the first, call the package's path machinery (e.g.
`Init`/`Load` or `statePath` via an exported seam — if `stackedDir` is
unexported and untestable directly, assert through `Init` in repo A then repo
B and check each `state.json` landed under its own `.git`), and assert the
two resolved paths differ.

**Verify**: `go test ./internal/stack -run TestStackedDirPerWorking -count=1 -v` → PASS.

### Step 3: Full gate

**Verify**: `make ci` → exit 0.

## Test plan

- The Step 2 test pins the per-cwd isolation property.
- The whole existing suite is the regression net: cmd integration tests
  chdir-per-test and e2e runs subprocesses; both must stay green.

## Done criteria

- [ ] `grep -c "GitCommonDir" internal/stack/store.go` — still exactly 1 call site, now behind the memo
- [ ] `go test ./internal/stack ./cmd/... -race -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] Only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- Any cmd integration test fails with state appearing in the wrong repo —
  the memo key is insufficient on this platform (e.g. symlinked temp dirs
  where `os.Getwd` differs from git's view); report the failing test.
- You find a call path where the cwd changes mid-command in the production
  CLI (it shouldn't — `st` never chdirs).

## Maintenance notes

- If `st` ever gains a `-C <dir>` flag (run as if in another directory), the
  memo key must incorporate that flag's value — flag this in review of any
  such feature.
- The lock and undo paths inherit the memo automatically; no further wiring.

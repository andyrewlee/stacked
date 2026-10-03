# Plan 008: Drive `st prune`, `st open`, `st track --all`, and `st sync` flag arms through the real binary

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- e2e/ cmd/prune.go cmd/open.go cmd/sync.go`
> On mismatch between "Current state" and live code, STOP.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW (additive tests only)
- **Depends on**: none
- **Category**: tests
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`st prune` is the only **mutating** command (lock + undo journal via `mutateState`) with zero black-box coverage — an adapter-boundary regression (argv plumbing into remote-ref resolution, exit-code/JSON envelope mapping, `st undo` after `st prune`) passes every layer and reaches users. `st open` has none either; it builds URLs and spawns the platform opener through `openFunc`, which an e2e must intercept via PATH shim. `track --all` and `sync --no-fetch`/`--no-delete`/`--remote` likewise have no e2e hits.

## Current state

- `e2e/` — grep finds no `"prune"` or `"open"` argument anywhere in the e2e suite; `rename`, `untrack`, `sync`, `track` (bare/`--parent`) are covered (`e2e_journey_test.go:298,381-391,363-422`, `e2e_journey_sync_test.go:34+`).
- `cmd/prune.go:43-57` — `runPrune` enumerates remotes via `fs.Visit` on `refs/remotes` and resolves `remote/branch` into tracking refs — non-thin adapter logic.
- `cmd/open.go:62-124` — `runOpen` computes compare URLs via `stack.SubmitPlan`/`PRHintsFor`/`RemoteToHTTPS`; `--dry-run`/`--json` never spawn; spawner injectable via `openFunc` (and via PATH shim at e2e level — `TestOpenInBrowserSpawnsOpener` in `cmd/open_test.go` shows the PATH-shim pattern).
- e2e conventions: `e2e/e2e_test.go` harness — `stOK(t, args...)`, `st(t, ...)` returning `{code, stdout, stderr}`, hermetic HOME/gitconfig; journeys grouped in `e2e_journey*_test.go`; `t.Parallel()` mandatory (enforced by `e2e/parallel_meta_test.go`).
- `git test fixture`: the harness creates repos; remotes are set via `git remote add origin <path-or-file-url>` — check `e2e_journey_sync_test.go` for how it fakes a remote (probably a `file://` bare repo).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| E2E suite | `go test ./e2e` | all pass |
| New tests only | `go test ./e2e -run 'Prune|Open|TrackAll|SyncFlags'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `e2e/*_test.go` — new `e2e_journey_prune_test.go`, additions to `e2e_journey_sync_test.go` and/or a contract test for `open`. Possibly `e2e/e2e_test.go` if a small helper (e.g. PATH-shim installer) is reusable — prefer keeping it test-local.

**Out of scope**: all production code; `cmd/*_test.go` (in-process coverage already exists there); `internal/`.

## Git workflow

- Branch: `advisor/008-e2e-prune-open`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: `st prune` journey

New `e2e/e2e_journey_prune_test.go`. Mirror the sync journeys: build a repo via the harness (`newRepo`-equivalent — check `e2e_test.go` for the fixture helper name), `st init`, create + merge a branch into trunk (merge via real `git` commands the harness provides or `exec.Command("git", …)` — see how `e2e_journey_sync_test.go` merges), then:

- `st prune --json` → exits 0, `deleted` names the merged branch.
- `st log` → branch gone.
- `st undo` → branch restored (pins undo's created/deleted ref restore end-to-end).
- `st prune --dry-run --json` → `dryRun: true`, nothing deleted.
- `st prune --remote origin` against a `file://` bare remote where `refs/remotes/origin/main` exists → covers the remote arm (optional if setup is heavy — the plain arm is the mandatory one).

**Verify**: `go test ./e2e -run 'Prune'` → all pass.

### Step 2: `st open` contract

Extend a contract test file (`e2e_contract_test.go` or new `e2e_open_test.go`):

- Set a github-shaped remote on the fixture: `git remote add origin git@github.com:owner/repo.git` (or `file://` — but github shape exercises `RemoteToHTTPS`'s scp-like path).
- `st open --dry-run --json` → exits 0, payload carries the expected `https://github.com/owner/repo/compare/…` URL(s); no browser spawned.
- `st open --dry-run` text mode → prints URLs.
- Spawning arm: place a `open`/`xdg-open` stub on PATH (see `cmd/open_test.go`'s `TestOpenInBrowserSpawnsOpener` for the shim) that records its argv to a file; `st open` (no `--dry-run`) → stub invoked with the URL. Only if the hermetic HOME/PATH plumbing allows it — check how e2e sets env.

**Verify**: `go test ./e2e -run 'Open'` → all pass.

### Step 3: `track --all` + `sync` flag smoke

In `e2e_journey_test.go` or the sync file: `st track --all --dry-run --json` on a repo with untracked branches → `tracked` map emitted, nothing tracked. `st sync --no-fetch --dry-run --json` → `dryRun`, no fetch evidence needed (a `file://` remote with nothing to fetch suffices — the assertion is exit 0 + shape). These are smoke-level pins, not deep journeys.

**Verify**: `go test ./e2e -run 'TrackAll|Sync'` → all pass.

### Step 4: Gate

**Verify**: `go test ./e2e` → all pass; `make ci` → exit 0.

## Test plan

- The new tests ARE the deliverable; patterns: `e2e_journey_sync_test.go` for remote-fixture setup and merge plumbing, `e2e_journey_worktree_test.go` for multi-step journey shape, `cmd/open_test.go:TestOpenInBrowserSpawnsOpener` for the PATH-shim idiom.
- Verification: `go test ./e2e` → all pass; `make ci` → green.

## Done criteria

- [ ] `go test ./e2e` exits 0 including the new tests (run `go test ./e2e -v | grep -c '^=== RUN\|^--- PASS'` sanity)
- [ ] `grep -rn '"prune"' e2e/` → at least one hit in a test invocation
- [ ] `grep -rn '"open"' e2e/` → at least one hit
- [ ] `make ci` exits 0
- [ ] No production files modified
- [ ] `plans/README.md` row updated

## STOP conditions

- The e2e harness can't model a remote or a rebase-merged branch — extend the fixture minimally; if that requires production code, STOP.
- The `open` spawn test can't run portably (no `open`/`xdg-open` semantics on the CI-less local box) — pin only the `--dry-run`/`--json` arms and note the spawn arm as covered in-process by `cmd` tests.
- Any test flakes — e2e must be deterministic; a timing-dependent spawn assertion needs the shim's side-effect file, not a sleep.

## Maintenance notes

- The `prune` journey pins the adapter's `fs.Visit` remote enumeration and `resolveTrunkRef` — if Plan 017 consolidates remote resolution, these tests are the regression net.
- `st open`'s URL is derived from `SubmitPlan` — if DIR-01 (push staleness) or remote-resolution changes ship, this journey is the contract.
- Reviewer focus: that the `open` test can't actually spawn a real browser (PATH shim must be verified to have run).

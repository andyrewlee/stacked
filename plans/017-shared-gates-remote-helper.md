# Plan 017: Share the restack-skip gates between plan and apply; centralize remote-name resolution

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/plan.go internal/stack/worktree.go internal/stack/restack.go cmd/sync.go cmd/prune.go cmd/submit.go cmd/open.go`
> On mismatch — especially if Plan 002 changed `worktree.go`'s owner gates — adapt or STOP.

## Status

- **Priority**: P3
- **Effort**: S–M
- **Risk**: LOW–MED (gates are read-only in both paths today — sharing is mostly mechanical; remote-resolution consolidation is behavior-preserving)
- **Depends on**: none hard; interacts softly with Plan 011 (remote dedup in the same cmd files — either order works, rebase the loser)
- **Category**: tech-debt
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Two small duplications that can drift silently:

1. `wouldSkipWorktreeRestack` (`internal/stack/plan.go:290-310`) re-implements `restackInWorktree`'s gates (`internal/stack/worktree.go:215-257`) — the same `RebaseInProgressIn`-then-`IsCleanIn` sequence for the dry-run preview. The comment says "ordered to mirror" — a new gate added to the apply path won't reach the preview, making `--dry-run` misreport what a real restack would skip.
2. Four commands hand-roll remote-name policy: `cmd/sync.go:37-45` and `cmd/prune.go:42-57` duplicate the `fs.Visit` explicitness check + `RemoteExists` + `refs/remotes/<r>/<trunk>` resolution; `cmd/submit.go:80-82` and `cmd/open.go:76-78` duplicate the unconditional `RemoteExists`+error wording. `resolveTrunkRef` (`sync.go:61-70`) vs. prune's inline block encode the same "remote tracking ref else local trunk" twice.

## Current state

- `internal/stack/plan.go:286-310` — `wouldSkipWorktreeRestack` mirrors the apply path's gate sequence with a documented mirroring contract.
- `internal/stack/worktree.go:223-257` — `restackInWorktree` — the apply path it mirrors.
- `internal/stack/plan.go:312-373` — `restackPlanAgainstWithWorktrees`/`finishUpstackPlan` — the broader plan/apply pair.
- `cmd/sync.go:37-70` — `fs.Visit` remote-explicit + `resolveTrunkRef`; `cmd/prune.go:42-57` — near-identical inline; `cmd/submit.go:80-82`, `cmd/open.go:76-78` — unconditional `RemoteExists` errors.
- Convention: plan/apply split is deliberate (previews must not mutate); the fix is extracting the *shared predicate*, not merging the paths.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Plan tests | `go test ./internal/stack -run 'Plan|DryRun|Restack'` | all pass |
| Remote tests | `go test ./cmd -run 'Sync|Prune|Submit|Open'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/plan.go`, `internal/stack/worktree.go`, `cmd/sync.go`, `cmd/prune.go`, `cmd/submit.go`, `cmd/open.go`, possibly a new `cmd/remote.go` for the shared helper, tests.

**Out of scope**: `internal/stack/restack.go` apply logic itself (the gates move OUT of it into a shared helper, but the restack algorithm stays), `internal/git`, Plan 011's spawn-fold work (this is consolidation; that one is caching — different change).

## Git workflow

- Branch: `advisor/017-shared-gates-remote`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Extract the shared worktree-restack disposition

In `internal/stack/worktree.go` (or a new `worktree_gate.go`), extract:

```go
// worktreeRestackDisposition answers whether a foreign-owned worktree's
// branch gets restacked: skipped-with-reason or proceed. Both the apply path
// (restackInWorktree) and the plan path (wouldSkipWorktreeRestack) consume it,
// so a new gate lands once.
func worktreeRestackDisposition(env Env, owner git.Worktree, branch string) (skip bool, reason string, err error)
```

Move the `RebaseInProgressIn`-then-`IsCleanIn` sequence into it; have `restackInWorktree` and `wouldSkipWorktreeRestack` both call it. Keep each caller's distinct error-wrap wording (apply wraps with the worktree path; plan returns the skip flag) — the shared piece is the gate ORDER, not the error text.

**Verify**: `go test ./internal/stack -run 'Restack|Plan|Worktree'` → pass.

### Step 2: Centralize remote explicitness + existence

New `cmd/remote.go` (or `cmd/remote_helpers.go`):

```go
// remoteFlag reports whether --remote was explicitly set (flag.Visit) and
// validates existence only when explicit — the shared policy for sync/prune.
func remoteFlag(fs *flag.FlagSet, val string) (explicit bool, err error)

// requireRemote validates that name exists — used by submit/open where a
// remote is mandatory regardless of explicitness.
func requireRemote(name string) error
```

Convert `cmd/sync.go`, `cmd/prune.go`, `cmd/submit.go`, `cmd/open.go` to call them. Keep each site's error wording identical to today's unless the message is generic enough to share — check the existing tests pin the wording (`grep -rn 'does not exist' cmd/*_test.go`).

### Step 3: Share `resolveTrunkRef`

Move `resolveTrunkRef` from `cmd/sync.go` into `cmd/remote.go` (unexported helper); have `cmd/prune.go`'s inline block call it — prune's extra "fetch it first" error when the tracking ref is absent stays (it's a policy difference, not duplication: sync tolerates a missing remote ref, prune requires it). The shared piece is the ref-name computation; the tolerance policy is per-command.

**Verify**: `go test ./cmd` → pass.

### Step 4: Gate

**Verify**: `make ci` → exit 0.

## Test plan

- Existing `TestRestackPlan*`/`worktree` tests pin the gate parity — a drift between plan and apply would surface there.
- For the remote helpers: existing cmd tests pin error wording; if a message consolidates, update tests to the new shared wording deliberately.
- Verification: `go test ./internal/stack ./cmd ./e2e` → all pass; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./internal/stack ./cmd ./e2e` exits 0
- [ ] `grep -n 'worktreeRestackDisposition' internal/stack/` shows both consumers
- [ ] `grep -n 'fs.Visit' cmd/sync.go cmd/prune.go` → gone (helper instead)
- [ ] `grep -n 'resolveTrunkRef' cmd/` → one definition, two+ callers
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- Plan 002 added a paused-rebase gate that this refactor would have to absorb — land 002 first; the shared `worktreeRestackDisposition` then covers it automatically.
- The plan/apply error surfaces legitimately differ beyond wording (e.g. one must abort, the other must skip) — keep per-caller wrapping; share only the boolean/kind decision.
- The remote helpers' error wording is pinned by tests you can't satisfy without changing them — update tests in the same commit, don't workaround.

## Maintenance notes

- After this, adding a worktree gate lands ONCE — same promise as Plan 010's undo unification.
- DIR-01 (push staleness) will add another remote-reading site — it should consume `requireRemote`/`resolveTrunkRef`, not reinvent them.
- Reviewer focus: whether `worktreeRestackDisposition`'s `reason` string is the same in both paths (it should be — the preview and apply must name identical reasons).

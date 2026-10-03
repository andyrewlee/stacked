# Plan 009: Clean the coverage allowlist and fix the duplicated `assertNoMutation` calls

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- scripts/cover-allow.txt scripts/cover.sh internal/stack/worktree_include.go internal/stack/worktree_include_fs_test.go internal/stack/undo_preview_test.go internal/git/shell.go`
> On mismatch, STOP.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: tests + tech-debt
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`scripts/cover-allow.txt` is the ratchet for the per-function 50% coverage floor, and it's rotting: the `ensureSafeDestinationDir` entry points at `cmd/worktree_copy.go` but the function moved to `internal/stack/worktree_include.go` (plan-era refactor) — the entry is dead, so a *future* same-named function in `cmd/` would silently pass the floor. Two other entries (`retryableLockFileAccess` stub, `shell.go Worktrees` delegate) are likely already covered and gratuitous. Meanwhile `internal/stack/undo_preview_test.go` has duplicated `assertNoMutation(t, f, callsBefore)` lines (61-62, 110-111, ~392-393) that shipped to main — dead duplicate assertions.

## Current state

- `scripts/cover-allow.txt` — 4 entries: `cmd/worktree_copy.go\tensureSafeDestinationDir` (dead — function now at `internal/stack/worktree_include.go:341-370`), `internal/git/shell.go\tWorktrees` (one-line delegate, trivially coverable), `internal/stack/lock_access_other.go\tretryableLockFileAccess` (`!windows` stub — invoked on every unix reclaim-path run, likely already covered), `internal/stack/lock_stale.go\tremoveLockFileIfContentErr` (defensible — windows-only retry arms).
- `internal/stack/worktree_include.go:341-370` — `ensureSafeDestinationDir` real function; untested arms: `EvalSymlinks` failure on a dangling symlinked parent (~350-352), symlink resolving to a NON-directory (~357-363 — `os.Stat` follows the link), regular file at the parent path (~366-367). Existing tests at `internal/stack/worktree_include_fs_test.go:175-201` cover happy + outside-resolving-symlink only.
- `internal/stack/undo_preview_test.go` — `assertNoMutation(t, f, callsBefore)` called twice in a row at lines 61-62, 110-111, and ~392-393 (verify exact set with grep).
- `scripts/cover.sh` — matches allowlist on `path<TAB>function`; a stale path never matches (confirmed by reading its lookup).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Coverage gate | `COVERAGE_MIN=75 ./scripts/cover.sh` | passes, prints total % |
| Target tests | `go test ./internal/stack -run 'Include|Destination|Safe'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `scripts/cover-allow.txt`, `internal/stack/worktree_include_fs_test.go` (new tests), `internal/stack/undo_preview_test.go` (remove dup lines), possibly `internal/git/git_test.go` or `internal/stack/lock_stale_test.go` if a dropped entry needs coverage.

**Out of scope**: production source; `cover.sh` mechanics (unless the matching semantics themselves are wrong — out of scope, report instead); `cmd/worktree_copy.go`.

## Git workflow

- Branch: `advisor/009-allowlist-hygiene`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Fix the dead entry and cover the function's real arms

Add ~3 tempdir tests in `internal/stack/worktree_include_fs_test.go` (next to the existing `ensureSafeDestinationDir` tests ~175-201): dangling-symlink parent (`EvalSymlinks` error arm), symlink-to-regular-file parent, regular-file-at-parent-path. Follow the file's existing fixture helpers.

Then fix `cover-allow.txt`: either delete the `ensureSafeDestinationDir` entry entirely (if new tests lift it ≥50% — expected) or correct the path to `internal/stack/worktree_include.go`. Prefer deletion — the header says the list is a ratchet.

**Verify**: `go test ./internal/stack -run 'Destination|Include'` → pass; `./scripts/cover.sh` → function no longer below floor.

### Step 2: Re-justify or drop the remaining entries

For each of `retryableLockFileAccess`, `Worktrees` (shell.go), `removeLockFileIfContentErr`: temporarily remove the entry from `cover-allow.txt` and run `./scripts/cover.sh` — if the function isn't flagged below floor, the entry stays deleted. If flagged:
- `shell.go Worktrees`: add a one-line test calling `git.Shell{}.Worktrees()` against a fixture repo (or the package-level `Worktrees()` if the delegate is what's flagged — check which symbol the coverage names).
- `retryableLockFileAccess` / `removeLockFileIfContentErr`: if the unix-reachable arms are the gap, add a test (unreadable lockfile → non-retryable error; lockfile-as-directory → non-retryable `Remove` error). If only windows arms are below floor, keep the entry with a sharpened comment.

**Verify**: `./scripts/cover.sh` green with the minimal allowlist.

### Step 3: Remove duplicated assertions

In `internal/stack/undo_preview_test.go`, delete the second `assertNoMutation(t, f, callsBefore)` where it appears twice consecutively (lines ~61-62, ~110-111, ~392-393 — grep to confirm all sites). Keep single calls.

**Verify**: `go test ./internal/stack -run 'UndoPreview'` → pass; `grep -c 'assertNoMutation' internal/stack/undo_preview_test.go` → drops by the dup count.

### Step 4: Gate

**Verify**: `make ci` → exit 0.

## Test plan

- The new `ensureSafeDestinationDir` tests and any allowlist-coverage tests are the deliverable; pattern: `worktree_include_fs_test.go`'s existing tempdir style.
- Verification: `go test ./internal/stack` + `./scripts/cover.sh` + `make ci`.

## Done criteria

- [ ] `scripts/cover-allow.txt` has no entries pointing at the wrong path
- [ ] `./scripts/cover.sh` passes with the reduced allowlist
- [ ] `grep -n 'assertNoMutation' internal/stack/undo_preview_test.go | wc -l` equals the distinct-assertion count (no consecutive duplicates)
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- Removing an allowlist entry drops a function below the floor AND the uncovered arms are genuinely windows-only (can't test on this box) — keep the entry with a precise comment; don't fake coverage.
- `cover.sh`'s path matching treats the entry differently than assumed — read the lookup before editing.
- `undo_preview_test.go` line numbers drifted (Plan 010 may touch the file) — re-grep; the criterion is "no consecutive duplicate calls", not specific line numbers.

## Maintenance notes

- Reviewer focus: whether `removeLockFileIfContentErr` should keep its entry (windows-only arms) vs. gaining unix-arm coverage — pick whichever the measurement supports, not the easier one.
- Future: consider a `make` check that flags allowlist entries that match no function — a meta-check that would have caught this rot.
- This plan is deliberately tiny; don't fold in unrelated coverage work.

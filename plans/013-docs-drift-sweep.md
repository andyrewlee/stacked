# Plan 013: Sweep the docs/comments that drifted from the implementation

> **Executor instructions**: Follow this plan step by step. This is a docs-only plan — every item is verified drift, not opinion. Run every verification command; on STOP conditions, report. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- README.md CONTRIBUTING.md CLAUDE.md CHANGELOG.md docs/AGENT.md cmd/mutate.go cmd/flags.go scripts/check-install-assets.sh`
> On mismatch, STOP (some items may be independently fixed — re-check each against live text).

## Status

- **Priority**: P2 (agent-facing contract docs — drift misleads automation)
- **Effort**: S
- **Risk**: LOW (doc/comment only)
- **Depends on**: none
- **Category**: docs
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Several docs describe an older implementation: the completion contract claims 4 dynamic commands (there are 7), CONTRIBUTING's add-a-command recipe points at `engine.go` (ops moved to `ops_*.go` files), CLAUDE.md names a nonexistent `branchCompletionCommands` switch, the changelog omits `st open`, two scripts claim a CI leg that no longer exists, and two source comments describe pre-change behavior. Each is small; together they erode trust in the contract docs agents rely on.

## Current state — the verified items (each with its fix)

**A. `docs/AGENT.md:375-379` + `README.md:402-404` + `CHANGELOG.md`** — completion candidate policy names only `checkout`/`co`, `onto`/`move`, `track`, `worktree`/`wt` and asserts "All other commands and positions emit nothing." Reality: `delete`, `untrack`, `rename` also register `Completion` handlers (`cmd/delete.go:20-25`, `cmd/untrack.go:17-22`, `cmd/rename.go:19-24`). Fix: update the AGENT.md policy to name all seven (`delete`/`untrack` → tracked branches; `rename` positional 0 → tracked + trunk), fix the README blurb, add a changelog line noting the three newer completers.

**B. `CONTRIBUTING.md:61`** — recipe step 1 says "add the operation to `internal/stack/engine.go`". `engine.go` now holds only shared machinery (`OpResult`, error sentinels, `finishUpstack`); ops live in `ops_lifecycle.go`/`ops_track.go`/`ops_delete_sync.go`/`ops_combine.go`/`ops_onto.go`. Fix: mirror CLAUDE.md's wording — "the matching `internal/stack/ops_*.go` file (or a new one for a new domain)".

**C. `CLAUDE.md:94-98`** — recipe step 5 says "register a completion policy in `cmd/complete.go`'s `branchCompletionCommands`/`completeCandidates` switch". No `branchCompletionCommands` symbol exists (`grep` returns zero); policy is the `Completion` field on `Command` at registration (`cmd/root.go:48-53`), exercised by `completeCandidates` in tests. Fix: "set the `Completion` field on the `Command` registration; `completeCandidates` exercises it in tests without a repo."

**D. `scripts/check-install-assets.sh:27-28` and `:36`** — comments claim "CI installs it via goreleaser-action on the ubuntu leg". No `.github/workflows/` exists. Fix: "skips cleanly when goreleaser isn't installed; `make release` and `CI_STRICT=1` runs require it."

**E. `CHANGELOG.md` `[Unreleased]`** — no `st open` entry; `cmd/open.go:13-20` ships the command (`--all`/`--remote`/`--dry-run`/`--json`), documented at `README.md:337-345` and `docs/AGENT.md:165-175`. Fix: add an `Added` bullet describing `st open`.

**F. `docs/AGENT.md:63-64`** — the "Stack-mutating commands" shared-shape list omits `sync`, though `Sync` returns the same `*OpResult` (`ops_delete_sync.go:203-208`). Fix: add `sync` to the enumeration.

**G. `cmd/mutate.go:14` + `cmd/flags.go:27-28`** — two stale comments:
- `mutate.go:14` says the stack lock is "a no-op on platforms without flock" — `lock_other.go`/`lock_stale.go` implement a real exclusive lock file with stale-owner reclamation (CONTRIBUTING.md:188-199 documents it). Fix: "flock on unix; exclusive lock file with stale-owner reclamation elsewhere".
- `flags.go:27-28` `parsePlain` doc lists `undo` among callers — `cmd/undo.go:33-34` uses `newUndoFlags`/`parseFlagSet` (it needs `-h` + positional). Fix: drop `undo` from the list (actual callers: abort, bottom, continue, guide, log, repair, status, top, validate).

**H. `CHANGELOG.md` `[Unreleased]` `undo --dry-run` entry (~37-40)** — lists six `blockers` kinds claiming exhaustiveness but omits `missing_restore_target:<b>` (emitted at `internal/stack/undo_preview.go:232`; documented in `docs/AGENT.md:224-228`). Fix: append it.

**I. `README.md:133-134`** — "Flags may be placed before or after the positional branch name." `st undo <n>` uses stdlib `parseFlagSet` (flags stop at the first positional) — `st undo 2 --json` errors, while `st up 2 --json` works (`up` uses `parseArgs` which reshuffles). Fix: qualify the sentence — flags may precede or follow positional *branch names*; `undo`'s count positional requires flags first. (Alternative: change `runUndo` to `parseArgs` — larger behavioral change, do NOT take it in this plan.)

**J. `README.md:409-413`** — `st validate`'s problem list omits `ParentMissing` (tracked parent whose git ref is gone — distinct from `ParentUntracked`) and `StalePendingReparent` (pending reparent whose rebase is gone; `st repair` clears it — `repair.go:47-49,69-71` — which the README's repair section also omits). Fix: add both, and note `st repair` clears stale pending reparents.

**K. `README.md:240-243` + `docs/AGENT.md:143` + `:316-319`** — `worktree rm --all` skip-reason lists name only "dirty" and "main worktree"; `cmd/worktree.go:344-353` also emits `"you are inside it"` and `"a rebase is in progress there"`. Fix: enumerate all four.

**L. `docs/AGENT.md:9-10`** — "the rebase path runs with `GIT_EDITOR`/sequence-editor disabled". Actually `RebaseOnto`/`RebaseOntoIn`/`RebaseOntoQuiet` (`internal/git/rebase.go:18-59`) run with the inherited environment (no override); only `RebaseContinue`/`RebaseContinueQuiet` set `GIT_EDITOR=true`/`GIT_SEQUENCE_EDITOR=true` (lines ~174,186) and wire `cmd.Stdin`. Fix: reword to describe the real guarantee — "rebase --onto never invokes an editor; `st continue` pins `GIT_EDITOR=true`/`GIT_SEQUENCE_EDITOR=true`" — the outcome claim (never blocks on a prompt) is what matters.

**M. `README.md:22-27`** — "stores all of its state in `.git/stacked/state.json`" understates: `undo.json` and the lock files (`lock`/`lock.excl`/`lock.reclaim`) live in the same `.git/stacked/` dir. Fix: "stack topology in `state.json`, plus the undo journal (`undo.json`) and lock files under the same `.git/stacked/` dir".

**N. `CLAUDE.md:115-119`** — "journeys in `e2e_journey_test.go`, CLI-contract in `e2e_contract_test.go`" — journeys now span `e2e_journey_test.go`, `e2e_journey_conflict_test.go`, `e2e_journey_sync_test.go`, `e2e_journey_undo_test.go`, `e2e_journey_worktree_test.go` plus domain suites (`e2e_absorb_test.go`, `e2e_completion_test.go`, `e2e_crash_test.go`, `parallel_meta_test.go`). Fix: "journeys in `e2e_journey*_test.go`; contract in `e2e_contract_test.go`; domain suites for absorb/completion/crash".

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Docs tests | `go test ./cmd -run 'Help|Golden|Guide'` | all pass (goldens pin help text — if a `Usage:` string changed it'd fail; pure docs shouldn't) |
| AGENT contract | `go test ./cmd -run 'Contract|EmittedKeys|JSON'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `docs/AGENT.md`, `README.md`, `CONTRIBUTING.md`, `CLAUDE.md`, `CHANGELOG.md`, `cmd/mutate.go` (comment only), `cmd/flags.go` (comment only), `scripts/check-install-assets.sh` (comment/message only — IF plan 004 hasn't already fixed item D; check first and skip if done).

**Out of scope**: all `.go` logic (only the two stale comments); golden files (regenerate only if a `Usage:`/`Summary` field actually changed — none should); `docs/qa/` (frozen by design); CHANGELOG historical entries.

## Git workflow

- Branch: `advisor/013-docs-drift-sweep`; one commit ("Sweep docs for drift against the implementation") or per-section; no push/PR unless instructed.

## Steps

### Step 1: Apply items A–N

Each item above names file, line, current text gist, and the fix. Apply all. For AGENT.md edits keep the existing prose style (terse, contract-focused). For the completion-policy item (A), mirror the existing format: "<command> → <candidate set>".

**Verify**: `go build ./...` → exit 0 (comment edits can't break, but the flags.go/mutate.go edits must stay comments).

### Step 2: Verify doc-pinned tests still pass

Some docs are pinned by contract tests (`docs/AGENT.md`'s JSON field docs vs. emit sites — `cmd`'s emitted-keys tests). Run the cmd suite.

**Verify**: `go test ./cmd` → all pass.

### Step 3: Gate

**Verify**: `make ci` → exit 0.

## Test plan

- No new tests — docs only. The existing contract/golden tests are the check.
- Verification: `go test ./cmd ./e2e` → all pass; `make ci` → green.

## Done criteria

- [ ] `git diff` touches only in-scope files
- [ ] `grep -n 'four.*branch-taking\|checkout.*onto.*track.*worktree' docs/AGENT.md README.md CHANGELOG.md` — the "four commands" claim is gone/updated
- [ ] `grep -n 'engine.go' CONTRIBUTING.md` — recipe no longer points at it for new ops
- [ ] `grep -n 'branchCompletionCommands' CLAUDE.md` → no match
- [ ] `grep -n 'ubuntu leg\|goreleaser-action' scripts/check-install-assets.sh` → no match
- [ ] `grep -n 'st open' CHANGELOG.md` → present
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- An item was already fixed by Plan 004 (item D) or another landed plan — skip it, don't double-edit.
- A doc describes behavior the code contradicts in a way that's a CODE bug not a doc bug — STOP and report (e.g. if `sync` doesn't actually return the shared shape).
- A doc edit would shift a golden file — update the golden via `go test ./cmd -run Golden -update` deliberately, not silently.

## Maintenance notes

- The pattern that keeps producing this drift: commands land without a doc update step. Consider a `make check-docs` grepping for completion commands vs. AGENT.md — worth suggesting in review, not required here.
- `docs/qa/` is frozen-by-design — don't sync it.
- The 0.0.1 "Windows CI test job" changelog entry was historically accurate (CI was removed later) — deliberately NOT a finding.

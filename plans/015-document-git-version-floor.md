# Plan 015: Establish and document the minimum supported git version

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/git/git.go README.md docs/AGENT.md CONTRIBUTING.md`
> PR #32 adds a validator to `internal/git/git.go` and PR #34 edits docs —
> known drift. A mismatch in the GitCommonDir excerpt below is a STOP
> condition.

## Status

- **Priority**: P3
- **Effort**: S (mostly research + docs; optional one-line fallback removal decision is OUT of scope)
- **Risk**: LOW
- **Depends on**: none
- **Category**: docs / dependencies
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

README says only "a working `git` on your PATH". The code, however, uses
specific git flags, and one (`rev-parse --path-format`) even has a fallback
for older gits — a fallback no CI job ever executes. Nobody currently knows
the true floor. The audit flagged this with an **unverified** version claim
("2.24"; `--path-format` may actually be 2.31) — so the first job of this plan
is to establish the facts, then document them. A documented floor turns
"works on my machine" reports into a one-line support answer, and decides
whether the untested fallback can be deleted later.

## Current state

- `internal/git/git.go:443–461` — `GitCommonDir()` (first-hand read):

  ```go
  dir, err := Run("rev-parse", "--path-format=absolute", "--git-common-dir")
  if err == nil && isSingleAbsolutePath(dir) { return dir, nil }
  // Fall back for git versions without --path-format: resolve a possibly
  // relative --git-common-dir from the current working directory.
  ```

- Inventory of git invocations to check: every `Run(`/`RunInteractive(` call
  in `internal/git/git.go` and `internal/git/remote.go`. Notable flags beyond
  the basics: `for-each-ref --format=%(refname) %(objectname)` (and plan 009
  may add `--merged` + `%(refname:short)`), `push -u --force-with-lease`,
  `rebase --onto`, `merge-base --is-ancestor`, `branch -f/-m/-D`,
  `rev-parse --git-common-dir`, `commit --amend`, `reset --soft`,
  `rev-parse --abbrev-ref`. Also the env pinning in
  `internal/git/shell.go`/`git.go` top (locale pinning from commit `58034dc`).
- Doc surfaces to update: `README.md` "Install / build" (the "Requires Go
  1.26+ and a working git" sentence), `CONTRIBUTING.md` if it states
  prerequisites, `docs/AGENT.md` only if it makes environment claims.
- CI reality (a floor sanity check, not a guarantee): ubuntu-latest and
  macos-latest runner git versions — find them in a recent Actions run log.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Flag inventory | `grep -n 'Run\(' internal/git/*.go` | the list to research |
| Local git | `git --version` | noted in the report |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `README.md`, `CONTRIBUTING.md` (one sentence each), and a short
"supported git" note. Optionally a comment on `GitCommonDir`'s fallback
stating the version it serves.

**Out of scope**: a runtime `git --version` check at startup (adds a spawn to
every command — exactly what plans 005/008/009 are removing; if wanted, it
belongs behind `st validate`, a separate decision). Deleting the
`GitCommonDir` fallback. Any behavior change.

## Git workflow

- Branch: `document-git-version-floor`
- Commit message style: `docs: state the minimum supported git version`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Research the floor

For each flag in the inventory, find its introduction version from git's
release notes (https://github.com/git/git/tree/master/Documentation/RelNotes
— WebFetch/WebSearch as available). Record a table flag → version in the PR
description. Verify specifically: `--path-format` (resolve the 2.24-vs-2.31
question), `--force-with-lease`, `%(refname:short)`, `for-each-ref --merged`
(if plan 009 landed), `merge-base --is-ancestor`. The floor = max version
across flags **excluding** `--path-format` (it has a working fallback); state
both numbers ("requires git ≥ X; ≥ Y recommended, below Y a fallback path is
used for worktree resolution").

**Verify**: the table is complete — every distinct flag in the grep inventory
has a researched version or an explicit "ancient/always available" entry.

### Step 2: Document

- README "Install / build": replace "a working `git` on your PATH" with the
  researched sentence (both numbers, one line).
- `CONTRIBUTING.md`: same fact where prerequisites are stated, if anywhere.
- `internal/git/git.go`: extend the fallback comment in `GitCommonDir` with
  the concrete version (`// Fall back for git < <Y> (no --path-format): ...`).

**Verify**: `grep -rn "working git" README.md` → no vague claim remains;
`make ci` → exit 0 (docs don't shift goldens).

### Step 3: Sanity-check the floor exists in CI

Note in the PR description the git versions on the two CI runners (from any
recent Actions log) and your local `git --version`, confirming all ≥ the
documented floor.

## Test plan

None (docs + comment). `make ci` green is the only gate.

## Done criteria

- [ ] README states a concrete minimum git version with the fallback caveat
- [ ] The `GitCommonDir` fallback comment names the version boundary
- [ ] The flag→version table is in the commit/PR description
- [ ] `make ci` exits 0; only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- Research shows a flag in current use that is newer than the git shipped on
  a supported platform's default install (e.g. macOS system git) — that is a
  compatibility bug, not a docs task; report it.
- You cannot establish a version for some flag with confidence — document the
  floor from the flags you could verify and list the unknowns in the PR
  description rather than guessing.

## Maintenance notes

- Any PR adding a new git flag should bump/confirm the documented floor —
  one-line review check.
- If the floor ends up ≥ the `--path-format` version anyway, delete the
  fallback in a follow-up (it would be dead code) — note this in the PR if it
  applies.

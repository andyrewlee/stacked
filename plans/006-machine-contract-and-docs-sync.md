# Plan 006: Make `log --json` match the documented contract, and sync the five stale doc claims

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- cmd/log.go cmd/guide.go cmd/testdata/ docs/AGENT.md CLAUDE.md README.md CONTRIBUTING.md CHANGELOG.md`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW (one deliberate JSON output change; rest is docs)
- **Depends on**: none
- **Category**: docs
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

`st` markets a machine interface (docs/AGENT.md) that agents script against.
One claim is actively wrong in a way that breaks consumers: the `log --json`
example shows `"children": []` on leaf nodes, but the field is `omitempty`, so
real leaves omit the key entirely — `node["children"]` raises on every stack's
top branch. Four smaller doc drifts compound the trust cost: CLAUDE.md (the
file agents are told overrides everything) still says the lock is "no-op off
unix" (false since commit `32167ec`), `st guide` claims every command accepts
`--json` (false for `completion`), the README command table under-documents
flags, and the CHANGELOG's Unreleased section is missing user-visible changes
including a JSON-shape change. One pass fixes all of it; the `log --json` fix
is code-side (always emit `children`) because that is the friendlier contract
and the one AGENT.md already promises.

## Current state

1. `cmd/log.go:69` — `Children []*logNode \`json:"children,omitempty"\``, and
   `printLogJSON`'s `build` (lines ~73–87) only appends children, so leaves
   marshal without the key. `docs/AGENT.md` (~lines 57–62) shows a leaf with
   `"children": []`. Contrast: `status --json` always emits `children`
   (`cmd/status.go` initializes it), so the docs' expectation is the repo's own
   convention.
2. `CLAUDE.md:104` — "Mutators take the flock lock
   (`internal/stack/lock_unix.go`); no-op off unix." Reality:
   `internal/stack/lock_other.go` (build-tagged for non-flock platforms)
   implements a real O_CREATE|O_EXCL lock file with stale-owner reclamation;
   helpers in `lock_stale.go`, `lock_owner_windows.go`, `lock_owner_plan9.go`.
3. `cmd/guide.go:54` — `out("\nEvery command accepts --json; ...")`. Reality:
   `completion` does not (`cmd/completion.go` registers no `json` flag);
   AGENT.md and the CHANGELOG both correctly say "except completion".
4. `README.md` command table (~lines 109–134): `create`/`modify`/`delete` rows
   show only short flags (`-m`, `-a`, `-f`) while the usage strings and
   detailed sections document `-a|--all`, `-f|--force`; the long form
   `--message` (registered in `cmd/create.go`, `cmd/modify.go`,
   `cmd/squash.go`) appears in no doc surface; the table shows `--json` only on
   `log`/`status` rows although every listed command accepts it.
5. `CHANGELOG.md` `[Unreleased]` — missing entries for user-visible changes
   since it was last touched: `8e15a09` (submit `--json` unified into one
   result shape — a documented-schema change), `32167ec` (real lock on
   non-flock platforms), `58034dc` (git locale pinned; fast-forward decided by
   plumbing), `298b8e6` (deterministic `inferParent`), `82bd166` (`delete` now
   reports `restacked`).
6. Bonus accuracy fix: CLAUDE.md:21, CONTRIBUTING.md:9, CHANGELOG.md:27 call
   `make test-fast` "sub-second"; it measures ~1.1–1.3s. Change the wording to
   "about a second".

Golden-file convention (CLAUDE.md): output changes shift goldens; regenerate
deliberately with `make golden` (`go test ./cmd -run Golden -update`) and
review the diff.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Regenerate goldens | `make golden` | exit 0; only intended golden diffs |
| cmd tests | `go test ./cmd/... -race -count=1` | exit 0 |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope** (the only files you should modify):
- `cmd/log.go`
- `cmd/guide.go`
- `cmd/testdata/log_json.golden` (via `make golden` only)
- `docs/AGENT.md`, `CLAUDE.md`, `README.md`, `CONTRIBUTING.md`, `CHANGELOG.md`

**Out of scope**:
- `cmd/status.go` and every other command's JSON shape — already consistent.
- `cmd/completion.go` — do NOT add `--json` to it; the exception is
  documented; adding it would change the contract the other direction.
- Adding `--message` long-form anywhere it isn't already registered — the
  flags exist; this plan only documents them.
- `cmd/testdata/help.golden` — should not shift (no usage strings change). If
  it does, STOP.

## Git workflow

- Branch: `machine-contract-and-docs-sync`
- Commit message style: `fix: always emit children in log --json; sync stale doc claims`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Make `log --json` always emit `children`

In `cmd/log.go`: change the field tag to `json:"children"` and initialize
`Children: []*logNode{}` when constructing each node in `build` (so leaves
emit `[]`, not `null`). Then run `make golden` and inspect the diff: only
`cmd/testdata/log_json.golden` should change, gaining `"children": []` on leaf
nodes.

**Verify**: `make golden && git diff --stat cmd/testdata/` → only
`log_json.golden` changed; `go test ./cmd/... -race -count=1` → exit 0.
Also confirm leaves emit an array, not null:
`go test ./cmd -run Golden -count=1` → exit 0 and
`grep -c '"children": \[\]' cmd/testdata/log_json.golden` ≥ 1.

### Step 2: Fix the `st guide` claim

In `cmd/guide.go:54`, change the text to: `Every command except completion
accepts --json; ...` (keep the rest of the line). Check for a guide golden:
`ls cmd/testdata/ | grep -i guide` — if one exists, regenerate.

**Verify**: `go test ./cmd/... -count=1` → exit 0; `./st guide 2>/dev/null | grep "except completion"` after `make build` → 1 line (or run via `go run ./cmd/st guide`).

### Step 3: Fix CLAUDE.md's lock sentence and AGENT.md's example

- `CLAUDE.md:104`: replace the line with: "Mutators take an advisory lock:
  flock on unix-like platforms (`internal/stack/lock_unix.go`), an exclusive
  lock file with stale-owner reclamation elsewhere
  (`internal/stack/lock_other.go`, `lock_stale.go`)."
- `docs/AGENT.md` `log --json` example: now accurate after Step 1 — verify the
  example matches the new output shape exactly (leaf shows `"children": []`);
  no edit expected, but confirm.

**Verify**: `grep -n "no-op off unix" CLAUDE.md` → no matches.

### Step 4: README command table and the "sub-second" wording

- In the README table: align the `create`, `modify`, `delete` rows with their
  usage strings (`[-m|--message <msg>]`, `[-a|--all]`, `[-f|--force]`), and add
  one sentence directly above the table: "Every command below (and `help`/
  `version`) also accepts `--json`; see docs/AGENT.md." Leave per-row `--json`
  mentions as they are.
- Change "sub-second" → "about a second" at `CLAUDE.md:21`,
  `CONTRIBUTING.md:9`, and `CHANGELOG.md:27` (the changelog line describes
  `make test-fast` too).

**Verify**: `grep -rn "sub-second" CLAUDE.md CONTRIBUTING.md CHANGELOG.md` → no matches; `grep -n "\-\-message" README.md` → ≥1 match.

### Step 5: CHANGELOG catch-up

Under `## [Unreleased]`, add:

- **Changed**: `submit --json` now emits one unified result shape (was
  per-mode shapes). `delete` results now include the `restacked` list.
  `log --json` always includes `children` (empty array on leaves).
- **Fixed**: a real lock on non-flock platforms (previously a no-op);
  git output parsing is locale-pinned and fast-forward detection uses
  plumbing, not message text; parent inference (`st track`) is deterministic.

Match the file's existing bullet voice (terse, user-visible phrasing).

**Verify**: `grep -n "restacked" CHANGELOG.md` → ≥1 match.

### Step 6: Full gate

**Verify**: `make ci` → exit 0.

## Test plan

- The golden regeneration in Step 1 *is* the test update; no new test
  functions. The e2e contract suite (`e2e/e2e_contract_test.go`) runs in
  `make ci` and will catch any unintended JSON shape change.
- If `e2e` asserts on `log --json` leaf shape anywhere
  (`grep -rn "children" e2e/`), update that assertion deliberately and note it
  in the commit message.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -n "children,omitempty" cmd/log.go` → no matches
- [ ] `grep -c '"children": \[\]' cmd/testdata/log_json.golden` ≥ 1
- [ ] `grep -n "no-op off unix" CLAUDE.md` → no matches
- [ ] `grep -rn "sub-second" CLAUDE.md CONTRIBUTING.md CHANGELOG.md` → no matches
- [ ] `go run ./cmd/st guide | grep -c "except completion"` = 1 (run in any initialized repo, or assert via the cmd test output)
- [ ] `make ci` exits 0
- [ ] Only in-scope files modified (`git status --porcelain`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `make golden` changes any golden other than `log_json.golden` (and a guide
  golden if one exists) — an unintended output change is in play.
- Any e2e test fails on the new `children` shape in a way that suggests an
  external consumer contract beyond AGENT.md (e.g. a versioned schema).
- The cited doc lines don't match the excerpts (drift since `ed9f2f7`).

## Maintenance notes

- The `log --json` change is the only behavioral edit; reviewers should
  eyeball the golden diff and nothing else.
- Future schema changes to any `--json` output should update docs/AGENT.md and
  CHANGELOG in the same PR — the drift this plan cleans up came from skipping
  that step.
- Deferred: a doc-lint check (e.g. a test asserting AGENT.md's examples parse
  against the real structs) was considered; worthwhile only if drift recurs.

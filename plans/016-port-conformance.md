# Plan 016: Add a fake↔shell behavioral conformance harness for the `stack.Git` port

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/git.go internal/stack/fakegit_test.go internal/git/*.go`
> On mismatch, STOP.

## Status

- **Priority**: P3
- **Effort**: M
- **Risk**: LOW (test-only; may initially surface real fake↔shell divergences — that's the point)
- **Depends on**: none
- **Category**: tests / tech-debt
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`make test-fast`'s authority rests on `fakeGit` faithfully modeling git — it's a ~1,057-line reimplementation with independently invented semantics (`replay` approximates `rebase --onto`; `Worktrees` synthesizes `Path: "."` where real git reports absolute paths; `UpdateRefs` resolves-all-before-mutating). The port's contracts are comment-only (`internal/stack/git.go:43-47` — "all or nothing — the fake and the shell must both honor this"). Nothing asserts the two implementations agree on a shared scenario — a divergence yields green fast-loop runs while the same path fails against real git.

## Current state

- `internal/stack/git.go` — the `Git` port interface; comments encode the contracts (`UpdateRefs` atomicity, `DeleteBranches` partial success).
- `internal/stack/fakegit_test.go` — ~1,057-line fake; `replay` (849-889), `ForceBranch` current-branch refusal (676-689), `UpdateRefs` (706-719), `DeleteBranches` (665-674), `Worktrees` synthesizes `Path: "."` (447-465).
- `internal/git/git_test.go` — exercises port methods in isolation against real git, never the same op sequences the engine issues.
- `internal/stack/model_test.go` — the random-op invariant harness, fake-only.
- `git.Shell{}` is the production impl (`internal/git/shell.go`); `stack.Git` is the interface both satisfy.
- Constraint: the conformance harness must normalize SHA/path representation (fake uses synthetic SHAs; shell produces real ones) — compare *relational* outcomes, not literal values.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Port tests | `go test ./internal/git` | all pass |
| New harness | `go test ./internal/stack -run 'Conformance'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/conformance_test.go` (new), possibly `internal/stack/fakegit_test.go` (if a divergence is a fake bug, fix the fake) — but NO production `internal/git` changes; a real divergence between fake and shell semantics is either a fake fix (test file) or a STOP-and-report.

**Out of scope**: `internal/git` production code, `internal/stack` production code, `cmd/`, `e2e/`.

## Git workflow

- Branch: `advisor/016-port-conformance`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Design the scenario table

Create `internal/stack/conformance_test.go` with a table of `(name, setup, op, assert)` triples run twice — once over `newFakeGit()`, once over `git.Shell{}` against a `t.TempDir()` repo (`exec.Command("git", "init", …)` + `git -C` helpers; the `internal/git` tests already have a temp-repo idiom — check `internal/git/git_test.go` for the fixture helper and reuse its shape).

Scenarios to cover (each a row):
- `Tips` on a fresh repo (empty vs. non-empty)
- `CreateBranch` + `Tips` reflects it
- `UpdateRefs` all-or-nothing: batch with one bad ref → no ref moved
- `DeleteBranches` partial success: one protected (current) branch in the list → others deleted, error names the failure
- `RebaseOnto` happy path: commits move
- `RebaseOnto` conflict → `RebaseInProgress` true, `RebaseHeadName`/`head-name` semantics agree
- `IsAncestor` true/false
- `Worktrees` on single-tree repo: fake's `Path: "."` vs shell's absolute path — the contract is "one entry, Branch=current" — normalize before comparing
- `IsClean`/`IsCleanIn` on clean vs. dirty tree
- `CommitSubjects` shape

Assertions compare *relational* outcomes: "after UpdateRefs-bad-batch, zero refs moved" is checkable on both; "tip == X" is not (SHAs differ).

### Step 2: Build the dual-driver

One helper runs the same op against an `Env{Git: g}` where `g` is either `newFakeGit()` or `git.Shell{}` (wrapped in a `git -C <dir>`-style scoped shell — check whether `git.Shell` supports a dir arg or whether you `os.Chdir`; the existing git tests will show the convention).

```go
func runConformance(t *testing.T, fn func(t *testing.T, g stack.Git)) {
    t.Run("fake", func(t *testing.T) { fn(t, newFakeGit()) })
    t.Run("shell", func(t *testing.T) { fn(t, shellAt(t.TempDir())) })
}
```

### Step 3: Write the assertions + adjudicate divergences

Where fake and shell disagree, decide which is right (the contract comments in `git.go` are the arbiter — e.g. "UpdateRefs is all-or-nothing" means a fake that partially applies is the bug). Fix the fake in `fakegit_test.go` (test file — in scope). If the SHELL violates a documented contract, STOP — that's a production bug to report.

**Verify**: `go test ./internal/stack -run 'Conformance'` → all pass; any fixed fake divergence documented in the commit.

### Step 4: Wire into the suite + gate

The file lands in `internal/stack` so `make test-fast` picks it up (the shell arm spawns real git — that's fine, it's a handful of `git init`+op calls, still sub-second each).

**Verify**: `make test-fast` → exit 0 and stays fast (< a few seconds); `make ci` → exit 0.

## Test plan

- The conformance suite IS the deliverable.
- Verification: `go test ./internal/stack` → all pass including `runConformance` rows; `make test-fast` stays fast; `make ci` → green.

## Done criteria

- [ ] `internal/stack/conformance_test.go` exists with ≥8 scenario rows, each run against both implementations
- [ ] `go test ./internal/stack -run 'Conformance'` exits 0
- [ ] `make test-fast` still fast (< ~3s) and exits 0
- [ ] `make ci` exits 0
- [ ] Any fake↔shell divergences found are fixed in `fakegit_test.go` or documented as STOP reports
- [ ] `plans/README.md` row updated

## STOP conditions

- `git.Shell` can't be scoped to a temp dir cleanly (needs a dir parameter the impl lacks) — check the port's methods for how they take dirs; if it requires `os.Chdir` the tests must serialize (no `t.Parallel` on the shell arm).
- A real divergence means the SHELL is wrong — STOP and report; this plan fixes the fake, not the port.
- Normalization gets too fuzzy (e.g. a scenario where "equivalent" is undefinable) — drop that row and note it; coverage of the definable subset beats a flaky oracle.

## Maintenance notes

- Every new port method needs a conformance row — that's the contract this establishes. Add it to the add-a-command recipe comment or CLAUDE.md test-layers bullet if you touch those.
- The fake's `Worktrees` `Path: "."` normalization is the canonical example of "relational not literal" — future rows follow it.
- Deferred: running `model_test.go`'s random-op harness against the shell (the bigger win, but slow — could be a nightly/CI-tiered run; note it as follow-up).

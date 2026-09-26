# Plan 015: Specify structured recovery and skip results for orchestrators

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- plans/design/015-structured-recovery-status.md plans/design/015-recovery-examples.json plans/design/015-recovery-acceptance.md plans/design/check-recovery.py plans/015-design-structured-recovery-status.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P2
- **Effort:** M
- **Risk:** MED
- **Depends on:** 006 (actual rebase target), 007 (include destination safety), and 012 (worktree path framing) before finalizing implementation assumptions
- **Category:** direction
- **Audit item:** D1
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/015-design-structured-recovery-status` (stacked on `advisor/014`).

- Step 1 trace: mapped `runRestack`/`runSync`/`runContinue`/`runAbort` adapters, the shared mutation JSON shape, `s.skippedWorktrees`→`skippedWorktreeNote` prose drain (`internal/stack/engine.go:371-386`), the `ConflictError`→exit-2 stderr envelope (`cmd/root.go:405-419`), the cross-worktree rolled-back conflict (plain error, exit 1, `internal/stack/worktree.go:230-243`), `status --json`'s existing `rebaseInProgress`/`rebaseBranch`/`conflictedFiles`, and the already-structured `worktree --all`/`rm --all` `skipped` precedent.
- Step 2 chose ONE additive contract: optional `recovery` array on success results and conflict envelopes; closed reason registry `{worktree_dirty, conflict_paused, conflict_rolled_back}`; `state` ∈ `{skipped, paused, rolled_back}`; `action.kind` ∈ `{argv (only when the next command is known-resumable), manual}`; omission-vs-empty, ordering, stderr/stdout placement, uninitialized-repo behavior and the strict-decoder boundary all specified. Proposed fields carry a `proposed` marker list per fixture.
- Artifacts: `plans/design/015-structured-recovery-status.md` (7 required sections), `015-recovery-examples.json` (8 scenarios), `015-recovery-acceptance.md` (scenario→test map + invariants), `check-recovery.py` (stdlib validator: JSON syntax, required scenarios, registered reasons, argv/cwd shapes, proposed-path resolution, acceptance cross-refs, required design sections).
- Verify commands: `rg -n '^## (Current contract|Proposed contract|Compatibility|State and concurrency|Examples|Acceptance|Deferred work)$' plans/design/015-structured-recovery-status.md` → all 7 sections present; `python3 plans/design/check-recovery.py` → `OK: 8 scenarios, 3 reasons, all shapes and cross-references consistent` (exit 0).
- Focused source tests confirming the traced surface: `go test ./cmd -run 'Test(ContinueJSONAfterConflict|StatusJSONSurfacesConflict)' -count=1` → ok; `go test ./internal/stack -run 'TestOntoConflict|TestRestack' -count=1` → ok.
- `git diff --check` → exit 0; created files all in Scope (`plans/design/*` + this plan + `plans/README.md`). No runtime source, tests, or repository metadata changed.

## Why this matters

An orchestrator currently has to inspect human notes to identify a skipped dirty worktree after a restack. Structured reasons and safe next-action descriptors could remove that prose dependency. This plan produces a bounded design and acceptance fixtures; it does not ship new CLI fields or behavior.

## Current state

`docs/AGENT.md:181` describes a loop that observes log, restacks, then re-checks notes for skipped branches. `internal/stack/engine.go:375` converts skipped branches to prose:

```go
return fmt.Sprintf("skipped %s: its worktree is dirty (commit/stash there, then re-run)", name)
```

The same codebase already has structured ConflictError fields and a separate worktree aggregate result with skipped entries. These surfaces are related but have different meanings: a cross-worktree conflict may have been rolled back, while a current-worktree conflict can leave a rebase paused. `cmd/commands_json_test.go` tests strict result shapes and structured conflicts. Plans 006/007/012 respectively preserve actual continuation bases, refuse include collisions, and preserve or conservatively reject unusual worktree paths; the design must not assume those correctness gaps are features.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Preserve login-free local operation, existing exit codes, human notes, and JSON stdout/error-envelope separation. Design actions as data with explicit argv and cwd, never a command string to execute. Existing docs distinguish transient lock contention from recovery work; retain that distinction.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Source baseline | `go test ./cmd -run 'Test.*(ContinueJSON\|StatusJSONSurfacesConflict\|WorktreeAll)' -count=1` | existing tests pass; this does not validate a future feature |
| Design artifact validation | `python3 plans/design/check-recovery.py` | exit 0; required sections, examples and acceptance cases are present and consistent |
| Whitespace | `git diff --check` | exit 0 |
| Future implementation build/lint/full gate (not required for this design-only task) | `make build fmt-check lint ci` | a later implementation must pass the repository's pinned gates |

This task writes design documents and fixture data only. The source baseline establishes the current contracts; validation checks the handoff artifacts, not an implemented API. Do not modify runtime files to make examples appear to work.

## Scope

**Only modify:**

- `plans/design/015-structured-recovery-status.md`
- `plans/design/015-recovery-examples.json`
- `plans/design/015-recovery-acceptance.md`
- `plans/design/check-recovery.py`
- `plans/015-design-structured-recovery-status.md`
- `plans/README.md`


**Out of scope:** all source/test/CI files, new commands/fields at runtime, automatic commit/stash/continue, a daemon or remote orchestration service.

## Git workflow

Use a separate branch/worktree named `advisor/015-design-structured-recovery-status` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `docs: specify structured recovery status`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Trace the existing result and error surfaces

Read cmd/restack.go, cmd/sync.go, cmd/continue.go and cmd/abort.go, cmd/worktree.go, cmd/commands_json_test.go, internal/stack/engine.go, restack.go, and docs/AGENT.md. In the design file create sections `Current contract`, `Proposed contract`, `Compatibility`, `State and concurrency`, `Examples`, `Acceptance`, and `Deferred work`. Under Current contract map each initiating command to success JSON, partial-progress JSON, stderr conflict envelope, exit code, skipped-branch collection and whether a rebase remains active. Record exact source symbols; do not infer current-worktree recovery from a dirty-worktree skip.

**Verify:** `rg -n '^## (Current contract|Proposed contract|Compatibility|State and concurrency|Examples|Acceptance|Deferred work)$' plans/design/015-structured-recovery-status.md` → all seven required sections are present and Current contract has source-backed entries

### Step 2: Choose a minimal additive contract and model uncertainty

Specify one recommended contract, not a menu requiring the executor to invent the answer. Start with an additive optional recovery list whose entries carry a stable reason code, branch, optional exact worktree path, operation state and a next-action descriptor. Limit initial reason codes to cases proven by existing source: dirty worktree skip and paused conflict, plus a separate rolled-back conflict state if needed. Distinguish an observation from an operation outcome. An action descriptor may be structured argv/cwd only when the next command is known; dirty worktrees require an explicit manual-resolution action, not a guessed automatic stash. Keep notes for humans. Define omission versus empty arrays, stable ordering, partial success, stderr/stdout placement, behavior outside initialized repositories and compatibility with strict JSON decoders. If an additive field still needs a versioned opt-in for supported clients, choose and document that boundary. Do not promise a repository-wide atomic snapshot or a reservation of state.

**Verify:** `rg -n 'reason|argv|cwd|strict|partial|snapshot' plans/design/015-structured-recovery-status.md` → the chosen schema and compatibility/concurrency decisions explicitly cover all six concepts

### Step 3: Write fixtures, acceptance cases and a handoff validator

Create 015-recovery-examples.json as an array of objects with scenario, command, stdout, stderr and exitCode. Include clean success, dirty remote worktree skip, current-worktree paused conflict, continuation that conflicts again, cross-worktree conflict rolled back, partial earlier progress, no initialization and unusual escaped path bytes. Create the acceptance document mapping each example to future engine, adapter and e2e test locations and exact assertions, including unchanged legacy fields. Add check-recovery.py using only Python's standard library: validate JSON syntax, required scenario coverage, unique reason definitions, argv as arrays when present, and references between examples and acceptance cases. Mark invented/proposed fields clearly so no reader treats these fixtures as current output. End the design with the specific files/interfaces a future implementation would touch and an estimated split into small implementation steps.

**Verify:** `python3 plans/design/check-recovery.py` → exit 0, all mandatory cases and contract definitions are consistent; the existing focused source tests also pass

## Test plan

- Artifact validator checks structure and cross-references; it is not a substitute for future runtime tests.
- Future acceptance must test old field compatibility, exact path round trips, stable reason codes, partial outcomes, and no automatic execution of action descriptors.
- Existing TestContinueJSONAfterConflict, TestStatusJSONSurfacesConflict and worktree aggregate tests are named as implementation exemplars.
- A proposed next action is valid only for the actual remaining operation state; rolled-back remote conflicts must not be presented as locally resumable.

## Done criteria

- [ ] Four scoped design artifacts exist and check-recovery.py passes.
- [ ] One recommended schema, compatibility policy, reason-code set and concurrency model are written without unresolved behavior choices.
- [ ] Every example has a source-backed current-state explanation and future assertion plan.
- [ ] No runtime source, tests or user repository metadata changed.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The contract needs a new persisted operation model or a daemon; record that as deferred scope instead of implementing it.
- A next-action descriptor cannot distinguish paused from rolled-back work; narrow the initial contract.
- A proposed additive field breaks a known strict consumer and no compatibility boundary is specified.

## Maintenance notes

A later implementation plan should be generated against the then-current source and this chosen contract. Keep the reason-code registry small and stable; human prose may change independently.

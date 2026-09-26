# Plan 016: Specify a read-only preview of the next undo's impact

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- plans/design/016-undo-preview.md plans/design/016-undo-preview-examples.json plans/design/016-undo-preview-acceptance.md plans/design/check-undo-preview.py plans/016-design-undo-impact-preview.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P2
- **Effort:** M
- **Risk:** MED
- **Depends on:** 005-enforce-undo-schema-compatibility.md before finalizing preview validation rules
- **Category:** direction
- **Audit item:** D2
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

Undo can restore multiple refs and remove branches or worktrees. The existing journal list shows recorded data, but not how it differs from the live repository. A preview could make the next undo reviewable while being honest that the repository can change before the user actually executes it.

## Current state

`cmd/undo.go:153` exposes recorded refs and created branches through undoListEntry:

```go
type undoListEntry struct {
    Index int `json:"index"`
    Label string `json:"label"`
    // ... currentBranch, createdBranches, createdWorktrees
    Refs map[string]string `json:"refs"`
}
```

runUndoList is explicitly a pure read allowed during rebase and without the repository lock. Actual mutation in internal/stack/undo_op.go first removes branches/worktrees created by the operation, then restores recorded refs and state. The command also has special preparation when cwd is a created worktree. Plan 005 requires both current state and snapshot compatibility checks before any undo preparation; the preview must share those rules rather than inventing a weaker decoder.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Do not label commits as globally lost merely because one branch will move away from them. The current undo preserves uncommitted working-tree changes subject to its existing refusal rules. A preview cannot promise those rules will still pass later, reserve state, or turn multi-step undo into an atomic transaction.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Source baseline | `go test ./internal/stack ./cmd -run 'Test.*Undo' -count=1` | existing tests pass; this does not validate a future feature |
| Design artifact validation | `python3 plans/design/check-undo-preview.py` | exit 0; required sections, examples and acceptance cases are present and consistent |
| Whitespace | `git diff --check` | exit 0 |
| Future implementation build/lint/full gate (not required for this design-only task) | `make build fmt-check lint ci` | a later implementation must pass the repository's pinned gates |

Produce a specification and acceptance fixtures only. Running existing undo tests is safe because their mutations occur in harness-owned temporary repositories; do not invoke st undo against this checkout.

## Scope

**Only modify:**

- `plans/design/016-undo-preview.md`
- `plans/design/016-undo-preview-examples.json`
- `plans/design/016-undo-preview-acceptance.md`
- `plans/design/check-undo-preview.py`
- `plans/016-design-undo-impact-preview.md`
- `plans/README.md`


**Out of scope:** runtime flags or behavior, new undo implementation, actually moving refs/deleting worktrees, journal schema changes, commit-content dumping by default.

## Git workflow

Use a separate branch/worktree named `advisor/016-design-undo-impact-preview` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `docs: specify undo impact preview`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Map the live undo decision order

Read cmd/undo.go, internal/stack/undo_op.go, undo.go, store.go, and their tests. Create sections `Current contract`, `Proposed command`, `Impact model`, `Validation and blockers`, `Concurrency`, `Examples`, `Acceptance`, and `Deferred work` in the design file. Document the actual order of compatibility checks, created-branch/worktree discovery, cwd handling, dirty-worktree refusal, ref restore, state restore and journal removal. Distinguish captured worktrees from live owning worktrees that appeared later. Record what undo --list already promises so the preview does not silently change it.

**Verify:** `rg -n '^## (Current contract|Proposed command|Impact model|Validation and blockers|Concurrency|Examples|Acceptance|Deferred work)$' plans/design/016-undo-preview.md` → all eight sections appear with a source-backed operation map

### Step 2: Define the preview contract and uncertainty explicitly

Recommend `st undo --dry-run [--json]` for the next entry only; define --list/--dry-run as mutually exclusive and preserve all existing undo flags. Specify deterministic branch/ref ordering, before/after SHAs, created branches to remove, discovered owning worktrees, expected cwd/checkout effect, metadata restore and journal-drop intent. When comparing current versus recorded refs, report commits that would cease to be reachable from that particular ref; distinguish missing objects/refs as unknown instead of zero. Define behavior for empty journal, active rebase, dirty created worktree, future schema and malformed current state under the existing supported recovery contract. The preview must never call preparation, Save, DropUndo, checkout or a mutating Git method. Choose read-only observation without reservation; include captured tips/journal identity so consumers can see what was observed, but state that actual undo revalidates current state. Do not require a new persisted fingerprint schema.

**Verify:** `rg -n 'dry-run|mutually exclusive|unknown|future|reservation|DropUndo' plans/design/016-undo-preview.md` → the command, blocker, unknown-value and nonmutation policies are explicit

### Step 3: Create examples and mutation-free acceptance criteria

Write 016-undo-preview-examples.json with scenario, command, expected stdout/stderr/exitCode and mustRemainUnchanged fields. Include no journal, ordinary modify, create-with-worktree, later-created owning worktree, dirty created worktree, current cwd inside a created worktree, changed live tips, missing ref/object, future schema, malformed supported current state and active rebase. The acceptance document must specify byte-for-byte state/journal/index comparisons, unchanged refs/worktrees/cwd, and fake-Git assertions excluding every mutating method. Add a standard-library Python validator for mandatory scenarios, example shape, explicit unknown values and cross-references. Close the spec with future implementation boundaries and the expected fast/real-Git/e2e commands; do not write the feature now.

**Verify:** `python3 plans/design/check-undo-preview.py` → all required examples and acceptance mappings validate; existing undo tests remain passing

## Test plan

- Artifact validation covers explicit absent/unknown values and all blockers, including future schema refusal.
- Future engine tests must prohibit Save and every mutating Git call during preview.
- Future real-Git tests must snapshot state, journal, index, refs, worktrees and cwd; read-only is an observable contract.
- A concurrency acceptance case changes a ref after preview and proves later undo revalidates, without asserting that preview reserved the earlier state.

## Done criteria

- [ ] Four scoped artifacts exist and check-undo-preview.py passes.
- [ ] The design defines one command/JSON contract and all listed blockers/unknown states.
- [ ] The preview makes no global lost-commit or atomicity promise.
- [ ] No runtime source or repository state has been modified.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- Accurate impact requires applying the undo or creating/removing a worktree; that violates preview scope.
- The design would bypass the future-schema guard to show speculative results.
- A proposed result implies commits are globally unreachable without checking all relevant refs; narrow the wording to per-ref reachability.

## Maintenance notes

Preview and execution must eventually share a planning/validation model without turning read-only inspection into mutation. Any future undo action must gain a corresponding preview impact and nonmutation acceptance case.


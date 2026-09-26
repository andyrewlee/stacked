# Design 016: A read-only preview of the next `st undo`

> Status: **proposed contract** — nothing here ships. `dryRun`, `blockers`,
> `wouldRestore`, `wouldDelete`, `wouldCheckout`, `wouldRemoveWorktree`,
> `observed`, and `journalDrop` are invented in this document; the fixtures in
> `016-undo-preview-examples.json` are proposals, not current output. A future
> implementation plan must re-derive `Current contract` against then-current
> source.

## Current contract

Verified against this stack's HEAD (includes plan 005's schema barriers and
plan 012's byte-exact worktree paths).

### Invocation shape (`cmd/undo.go`)

- `st undo [--list] [--json]`; `runUndo` (`cmd/undo.go:27`).
- `--list` (`runUndoList`) is a pure read — no lock, allowed mid-rebase — and
  emits `{ "entries": [ { "index", "label", "currentBranch"?, "createdBranches"?,
  "createdWorktrees"?, "refs" } ] }` newest-first (`index` 1 = the entry a bare
  `st undo` reverts). It projects RECORDED data only: it never compares against
  live refs. The state snapshot and `localBranches` capture stay out of the
  projection by design. Text prints `N: label (created …; worktrees …; on …)`.
- Bare `st undo` order of operations, each step a gate the preview must model:

  1. `acquireLock()` — advisory lock; contender → exit 5.
  2. `git.RebaseInProgress()` — refuse while a rebase is paused (exit 1).
  3. `stack.PeekUndo()` — empty journal → `{ "undone": false }` / `nothing to
     undo`, exit 0.
  4. `stack.Load()` — `ErrStateTooNew` is fatal *before anything mutates*;
     other load failures degrade to `s = nil` and the snapshot bytes are
     persisted directly on success.
  5. `stack.ValidateUndoState(entry.State)` — snapshot schema/parse barrier,
     ahead of worktree preparation and every mutation (plan 005).
  6. `prepareUndoCurrentCreatedWorktree` — when the CURRENT branch's worktree
     was created by the undone op: needs the shell shim, else an error naming
     the `cd` target; chdirs to the main worktree and re-checks for a rebase
     in progress *there*.
  7. `stack.Undo(env, s, entry)` (`internal/stack/undo_op.go:16`):
     a. `s.Version > stateSchemaVersion` → `ErrStateTooNew` (current-state
        barrier inside the engine, independent of cmd's load check).
     b. `decodeState(entry.State)` — malformed snapshot fails before mutation.
     c. Created-branch discovery (only when `entry.LocalBranches != nil`):
        candidates = current-state branches ∪ `CreatedBranches`, minus the
        captured `LocalBranches` list (`branchCreatedByEntry`); sorted.
        For each: `removeCreatedWorktree` finds the LIVE owning worktree —
        not just the recorded one — refuses a recorded-path mismatch, refuses
        a DIRTY owner ("commit/stash there or `st worktree rm`"), then
        `WorktreeRemove`; if HEAD is on the doomed branch, checkout parent/
        trunk (falling back to detached HEAD when blocked by local changes or
        another worktree), then `DeleteBranch(name, force)`.
     d. `*s = *prev` + `env.save()` — state restore.
     e. `UpdateRefs` — ALL recorded refs restored in one transaction.
     f. Final `Checkout(entry.CurrentBranch)` — tolerated failures: local
        changes or the branch being checked out in another worktree.
  8. `stack.DropUndo()` — journal entry removed only after success.
  9. `writeCDDirective` when the cwd had to move.
- JSON result: `{ "undone": true, "label", "restored": [...] }`
  (`restored` = sorted restored-ref names).

### What `undo --list` already promises

Recorded-data projection only: no drift detection, no worktree discovery, no
per-ref diff. The preview must not silently strengthen `--list` semantics —
previewing is `--dry-run`'s job.

## Proposed command

`st undo --dry-run [--json]` — previews the NEXT journal entry only (the same
entry `PeekUndo` returns; no `--index` selector — keep it singular). `--dry-run`
and `--list` are **mutually exclusive** (exit 1 usage error); all existing
flags keep meaning. The command takes the advisory lock (consistent reads of
state+journal) but never writes: no `Save`, no `DropUndo`, no `Checkout`, no
worktree removal, no ref updates — read-only observation **without
reservation**.

JSON (stdout, exit 0):

```json
{
  "dryRun": true,
  "label": "create feat-x",
  "wouldRestore": [
    { "branch": "feat-a", "from": "<live tip>", "to": "<recorded sha>",
      "commitsLostFromRef": 3 }
  ],
  "wouldDelete": [
    { "branch": "feat-x", "worktree": "/abs/path-or-omitted",
      "worktreeDirty": false, "isCurrentWorktree": false }
  ],
  "wouldCheckout": "main",
  "journalDrop": true,
  "observed": { "entryIndex": 1,
                "tips": { "feat-a": "<live sha>" } },
  "blockers": []
}
```

- `wouldRestore` — sorted by branch name; `from` = live tip (or `null` +
  `missing: true` when the ref is gone), `to` = recorded SHA. A branch whose
  live tip equals the recorded one is still listed (it is restored
  transactionally either way) but with `commitsLostFromRef: 0`.
- `commitsLostFromRef` — count of commits on the live tip not reachable from
  the recorded tip for THAT ref only (`rev-list --count <to>..<from>`);
  `"unknown"` (JSON string) when either object is missing or the walk fails.
  It is a per-ref measure — NOT a claim that commits become globally
  unreachable (other refs/tags/worktrees may still reach them).
- `wouldDelete` — sorted; `worktree` carries the live owning path (byte-exact,
  discovered via `Worktrees()`, even when the journal recorded none — matching
  `removeCreatedWorktree`'s live-owner rule); `worktreeDirty` reports the
  observed cleanliness; `isCurrentWorktree` marks the cwd-contained case.
- `wouldCheckout` — `entry.CurrentBranch` when that branch exists, else `null`
  with a `notes`-style prose hint in text output.
- `journalDrop: true` — the entry WOULD be dropped on a real run (preview
  never drops).
- `observed` — what the preview looked at: `entryIndex` and the live tips it
  compared. No fingerprint schema; purely informational.
- `blockers` — conditions that would make a real `st undo` fail, as structured
  strings/codes, in decision order: `journal_empty`, `rebase_in_progress`,
  `state_too_new` (current OR snapshot schema), `malformed_snapshot`,
  `worktree_dirty:<branch>`, `cwd_inside_created_worktree:<branch>` (shim
  absent), `recorded_worktree_mismatch:<branch>`, `missing_ref:<branch>` is
  NOT a blocker (undo restores it) — it downgrades that entry's
  `commitsLostFromRef` to `"unknown"` instead.
- Text output mirrors the JSON: `would undo: <label>`, `restores: name
  <from>→<to>` lines, `deletes: name (worktree …)`, `checkout: <branch>`,
  `blocked: …` lines; sanitized as usual.
- Exit codes: 0 when the preview computed (even with blockers listed — they
  are data); 1 for usage errors (`--dry-run --list`); 3/5 per the usual
  envelope for uninitialized/locked.

## Impact model

Per-ref, not global: `commitsLostFromRef` counts commits that would stop being
reachable **from that branch ref**; it never asserts global unreachability.
`wouldDelete` lists branches and their live owning worktrees. `wouldCheckout`
describes the expected HEAD/cwd effect including the "cwd lives inside a
deleted worktree" case (currently a blocker unless the shim teleports). State
restore is summarized as `journalDrop` + the wouldRestore set; the metadata
snapshot bytes themselves are not diffed field-by-field — the label +
created/restore sets communicate intent.

## Validation and blockers

Modeled in the real undo's gate order (section Current contract steps 1–7):
lock contention is a normal exit-5 error, not a blocker row; an active rebase,
empty journal, `ErrStateTooNew` (current or snapshot), malformed snapshot,
dirty created-worktree owner, cwd-inside-created-worktree without the shim,
and recorded-vs-live worktree mismatch are `blockers` entries in code order.
`future` schema (`state_too_new`) is refused, not previewed — the preview does
not bypass the guard to speculate on fields it cannot interpret.

## Concurrency

The preview holds the advisory lock for a consistent read but reserves
nothing: a ref may move, a worktree may get dirty, or the journal may change
between preview and the real `st undo`, which revalidates everything. The
`observed` block lets consumers SEE staleness (compare `observed.tips` against
live `git rev-parse`); it is not a guard. A future concurrency acceptance test
moves a ref after preview and asserts the real undo revalidates — never that
the preview reserved the earlier state.

## Examples

`plans/design/016-undo-preview-examples.json` — eleven scenarios: no journal,
ordinary modify, create-with-worktree, later-created owning worktree, dirty
created worktree, cwd inside a created worktree, changed live tips, missing
ref/object, future schema, malformed current state, and an active rebase.
Each object carries `scenario`, `command`, `stdout`, `stderr`, `exitCode`,
`proposed` (dotted paths that are invented), and `mustRemainUnchanged`
(observable invariants a real repo can assert: state file bytes, journal
bytes, index, refs, worktrees, cwd).

## Acceptance

`plans/design/016-undo-preview-acceptance.md` maps every scenario to future
test locations — fake-Git engine tests that must fail if the preview calls any
mutating port method (`Save`, `DropUndo`, `Checkout`, `UpdateRefs`,
`DeleteBranch`, `WorktreeRemove`, `RebaseOnto*`), real-Git byte-for-byte
snapshots of state/journal/index/refs/worktrees/cwd, and e2e contract cases —
plus the concurrency non-reservation case.

## Deferred work

- Multi-entry preview (`undo --dry-run --index N` or `--all`) — the singular
  contract first; the `--list` numbering is already compatible.
- Commit-content dumping (diffs of restored ranges) — explicitly out of
  scope.
- Reusing the preview's blockers to pre-flight OTHER destructive commands.
- Estimated implementation split: (1) `internal/stack` — a pure
  `UndoPreview(env, s, entry)` planner sharing validation with `Undo` (the
  maintenance note's "shared planning/validation model"); (2) `cmd` — the
  `--dry-run` flag wiring + rendering; (3) tests per the acceptance map;
  (4) docs/AGENT.md + CHANGELOG.

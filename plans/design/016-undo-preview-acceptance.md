# Design 016 acceptance: example → future test mapping

Every scenario in `016-undo-preview-examples.json` maps to future tests.
`mustRemainUnchanged` names observable invariants a real-Git test must
snapshot byte-for-byte BEFORE and AFTER the preview: state file bytes,
journal bytes, index, refs, worktrees, cwd. A preview that mutates any of
them fails — read-only is an observable contract, not a convention.

## Universal nonmutation harness (engine)

`internal/stack/undo_op_test.go`, next to the existing undo tests: a fake-Git
decorator that `t.Fatal`s on ANY mutating port call —
`Save` (via `Env.Save`), `DropUndo` (journal write path), `Checkout`,
`CheckoutDetach`, `UpdateRef(s)`, `DeleteBranch`, `WorktreeRemove`,
`WorktreeAdd`, `RebaseOnto`, `RebaseOntoIn`, `RebaseAbort*`, `RebaseContinue`,
`PushBranches`, `SetHEAD`. Every preview test runs under this decorator;
acceptance is "returns a preview AND zero mutating calls". `PeekUndo`/
`ListUndo`/`Worktrees`/`RevParse`/`IsCleanIn`/`RebaseInProgress`/
`BranchExists`/`CurrentBranch`/`IsAncestor`/`DiffCachedPatchFor`-style reads
stay allowed.

| scenario | future test (file · shape) | key assertions |
|---|---|---|
| no_journal | engine `TestUndoPreviewEmptyJournal` | `dryRun:true, undone:false`, no blockers, zero mutating calls, exit 0 |
| ordinary_modify | engine `TestUndoPreviewListsRestore` + adapter `TestUndoDryRunJSON` (cmd/commands_json_test.go, next to undo list tests) | `wouldRestore[0]` exact `{branch,from,to,commitsLostFromRef:1}`; `journalDrop:true`; `observed.entryIndex==1`; journal still intact afterwards |
| create_with_worktree | engine `TestUndoPreviewDeletesCreated` + e2e `TestUndoDryRunCreateWorktree` | `wouldDelete[0].worktree` = recorded path; `worktreeDirty:false`; `wouldCheckout:"main"`; real-Git test asserts the worktree still exists after preview |
| later_created_owning_worktree | engine `TestUndoPreviewFindsLiveOwner` | journal recorded no worktree, but `wouldDelete[0].worktree` names the LIVE owner path (mirrors `removeCreatedWorktree`) |
| dirty_created_worktree | engine `TestUndoPreviewDirtyWorktreeBlocker` + real-Git variant | `blockers` contains `worktree_dirty:feat-z`; `worktreeDirty:true`; wouldDelete still listed (intent); real undo afterwards STILL refuses — preview changed nothing |
| cwd_inside_created_worktree | e2e `TestUndoDryRunInsideCreatedWorktree` | `isCurrentWorktree:true`; `blockers` contains `cwd_inside_created_worktree:feat-x`; cwd unchanged; no chdir happened |
| changed_live_tips | engine `TestUndoPreviewCountsDrift` | `commitsLostFromRef == 2`; `from` = live tip, `to` = recorded; per-ref only — assertion must not claim global unreachability |
| missing_ref_object | real-Git test (delete ref + object unavailability via fake `RevParse` error) | `commitsLostFromRef == "unknown"`; `tips.<branch> == null`; NOT 0; preview still exits 0 |
| future_schema | engine `TestUndoPreviewFutureStateBlocked` | `blockers` contains `state_too_new`; no `wouldRestore`/`wouldDelete` computed; preview never bypasses the schema guard |
| malformed_current_state | engine `TestUndoPreviewMalformedSnapshotBlocked` | `blockers` contains `malformed_snapshot`; `label` still shown (journal header is readable); exit 0 |
| active_rebase | adapter + e2e `TestUndoDryRunDuringRebase` | `blockers` contains `rebase_in_progress`; exit 0; no mutation; the paused rebase itself is untouched |

## Cross-cutting acceptance

- **Mutual exclusion**: `st undo --dry-run --list` → usage error exit 1;
  `--dry-run` alone targets the newest entry (`PeekUndo`).
- **Ordering**: `wouldRestore` and `wouldDelete` are sorted by branch name —
  deterministic across map iteration.
- **Concurrency non-reservation** (real-Git test): preview → move a ref →
  real `st undo` → undo revalidates and still restores the RECORDED sha; the
  test asserts the preview never reserved the earlier tip.
- **Text rendering**: `worktreeDirty`, paths, and branch names pass through
  `sanitizeForTerminal`; path bytes in JSON stay exact (plan 012).
- **Backward compatibility**: `st undo --list` output is unchanged; bare
  `st undo` JSON keeps `{undone,label,restored}`.

## Validator

`check-undo-preview.py` asserts: all eleven scenarios exist; each has
`command`, `stdout`, `stderr`, `exitCode`, `proposed`,
`mustRemainUnchanged`; `commitsLostFromRef` is an int or the string
`"unknown"` (never negative, never silently absent on `wouldRestore`
entries); blocker codes come from the registered set; every scenario appears
in this document's table. Consistency only — runtime truth is the future
tests' job.

# Implementation Plans

This directory only tracks actionable implementation plans that have not
landed yet.

## Active Plans

| Plan | Title | Priority | Status |
|------|-------|----------|--------|
| 007 | Batch successful `st submit` while preserving partial-failure JSON | P2 | TODO |

## Reconciliation Notes

Implemented or stale plans were removed from this directory after reconciling
against `main` at `b866bf1`:

- 001-006: already landed in earlier PRs.
- 008-013 and 015-022: implemented on `main`.
- 014: stale/rejected; its original target was removed and the relevant undo
  restore paths are covered by current tests.

The remaining plan 007 is intentionally still present: `internal/git` already
has `PushBranches`, but `cmd/submit.go` still uses `PushRemote` per branch on
the normal path. The refreshed plan keeps the partial-failure JSON contract
while batching the successful submit path.

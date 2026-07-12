# Implementation Plans

Round-5 plans (032-058, planned 2026-07-06 at `e4a6abd`) are fully executed
and reconciled below; their plan files are removed. The only live plan file
is `058-spike-absorb-design.md`, kept for its GO design (the seed for a
future `absorb-v1` build plan). Each executor: read a plan fully before
starting, honor its STOP conditions, and update this index when done.

## Reconciliation notes

Round 5 executed 2026-07-11 (main `e4a6abd` → `452f1a7`, PRs #111-#133,
every merge gated on `make ci` + review; 033/041 additionally validated on
the `ci-windows` leg):

- 032→#111 (undo releases unrecorded created-branch worktree), 035→#112
  (worktree-cache invalidation on mutation), 034→#113 (stderr sanitization),
  033→#114 (lock permission-error surfacing + cross-platform classifier
  tests), 036→#115 (sync from a linked worktree), 038→#116, 039→#117
  (contract tests), 042→#118 + 043→#119 (docs/guide/AGENT.md), 040→#120
  (materialize spawn diet), 046→#121 (`st restack --all`), 049→#122 +
  050→#123 (fault-injection / fallback tests), 051→#124 (per-function
  coverage floor + allowlist), 037→#125 (create --worktree on mutateState),
  044→#126 (`st worktree --all`), 045→#127 (`.worktreeinclude` globs),
  041→#128 (single Go pin in CI), 047→#129 (delete single-pass restack),
  048→#130 (transactional undo ref restore), 052→#131 (cmd/util.go split),
  054→#132 (QA snapshot relocated), 055→#133 (canonical module path;
  `go install …@latest` effective once a version tag is pushed).
- 053 (worktree materialize/copy into the engine): **BLOCKED by its own
  STOP** — post-045 a faithful move needs a 9-method WorktreeFS port
  (ReadFile/Lstat/Stat/EvalSymlinks/MkdirAll/Mkdir/CopyTree/Glob/WalkDir)
  vs the plan's ~6 ceiling, and shrinking it means rewriting the
  containment guards the plan forbids touching. Needs a re-scope (e.g.
  decompose expansion vs guards vs copy), not force.
- 056 (port-level Tips cache): investigated, **NO-GO on an invalidating
  cache** — a correct cache saves the same single ~15-20ms `for-each-ref`
  as a risk-free explicit seed (SnapshotUndo already holds the map), while
  one missed mutator silently corrupts undo snapshots. Real follow-up with
  10-20x the ceiling: batch the 24-45 per-op `rev-parse` spawns.
- 057 (unified preview/execute traversal): spiked, **NO-GO** — see the
  rejected list below. Open gap worth a small test plan:
  `Restack`↔`RestackPlan` has no MatchesActual parity test (the other four
  pairs do).
- 058 (`absorb` design spike): **GO, sliced** — dry-run hunk→commit mapping
  first, tip-only single-target absorb second, refuse everything ambiguous
  (multi-commit hunks, pure additions, non-tip targets). Full design +
  prototype evidence in `058-spike-absorb-design.md`.

Earlier rounds, reconciled at `e4a6abd` (2026-07-06):

- 001-006, 008-013, 015-022: landed in earlier PRs (see git history).
- 014: stale/rejected; its target was removed.
- 007: landed as commit `7321511` (batch submit push with per-branch
  partial-failure fallback).
- 023-031 (2026-07-05 round): all landed — 023→#99, 024→#100, 025→#101,
  026→#102, 029→#103, 027→`65a41ba`, 028→`c6e4def`+`ef32124`+`4ea20e3`,
  031→`075272d`, 030→#110; the three gaps a reconciliation audit found
  became round-5 plans 033/038/039 (now landed, above).

## Findings considered and rejected (do not re-audit)

- Argv-injection / worktree-copy containment — verified closed (#93/#94);
  re-verified for the new delta surfaces (PR URLs escape via
  `url.PathEscape`; undo worktree removal targets only live
  `LinkedOwnerOf` paths behind clean/mismatch guards).
- Locale breakage of git-error string matching — `internal/git` pins
  `LC_ALL=C` on every parsed invocation; classifiers are safe.
- Batch-push fallback double-force-push — `--force-with-lease` re-push of
  already-pushed refs is a no-op; safe.
- Undo worktree removal as O(N) subprocess spawns — refuted; `Worktrees()`
  is memoized via `cachedShell`.
- `internal/git/git.go` size as a god module — cohesive flat wrapper.
- `st status` printing the main-worktree path (log/status divergence) —
  arguably intentional; cosmetic; rejected twice, do not resurrect.
- PowerShell shim/completion — exclusion is deliberate and test-pinned;
  revisit only with a real Windows product decision.
- A new "orchestrator status" command — `st log --json` already carries
  per-node `worktree`/`dirty`/`needsRestack`; the read surface exists.
- `worktreeAdd` allowing a trunk worktree — behaves correctly under
  existing guards; arguably useful; not a defect.
- `Squash` rollback/cross-worktree exposure — swept 2026-07-06, clean
  (`ResetSoft` rollback correct; current-branch-only surface).
- CONTRIBUTING not mentioning the `ci-windows` subset job — its local
  guidance stays correct; simplification, not an error.
- Unified plan/engine traversal (one visitor for preview+execute) —
  spiked 2026-07-11 (plan 057), NO-GO: the per-branch decision bodies
  differ by contract (validate-vs-resolve, silent-skip-vs-error) and the
  execute side's dirty-skip lives inside the conflict-rollback path
  (restackBranchWith); post-047 residual duplication is ~40 symmetric
  lines under order-asserting parity tests. Do not re-spike; the open
  follow-up is a Restack<->RestackPlan MatchesActual parity test.
- Unicode bidi-override ("Trojan Source") characters in subjects — distinct
  class from terminal-control injection, refnames can't carry them; noted
  at LOW confidence, below the fix bar.

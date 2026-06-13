# Plan 011: Batch `st track`'s parent inference (≤9 spawns per tracked branch → ~3 total)

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/stack/engine.go internal/stack/fakegit_test.go internal/stack/engine_test.go`
> Plan 009 (MergedInto) edits the same files — that drift is expected and
> required. A mismatch in the `inferParent` excerpt below is a STOP condition.

## Status

- **Priority**: P3
- **Effort**: M
- **Risk**: MED — the tie-break between incomparable ancestors is documented
  as deliberately deterministic; the batched rewrite must reproduce it
  exactly.
- **Depends on**: plans/009-merged-into-batching.md (the `MergedInto` port
  method)
- **Category**: perf
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

`st track` without `--parent` runs `inferParent`, which for every candidate
(every tracked branch + trunk) issues up to three `IsAncestor` checks — and
each bare-name `IsAncestor` costs up to 3 spawns. Worst case ~9 spawns per
tracked branch: ~1–2s of lag at 20 branches for an interactive command. Two
`for-each-ref --merged` spawns answer the first two checks for *all*
candidates at once; only the rare "closest among comparable ancestors" check
needs pairwise calls, normally a single chain.

## Current state

`internal/stack/engine.go:822–864` — `inferParent(g Git, s *State, cur string)`:

```go
best := s.Trunk
candidates := []string{s.Trunk}
for name := range s.Branches { if name != cur { candidates = append(candidates, name) } }
// Iterate in a fixed order so the choice between incomparable ancestors (two
// tracked branches where neither is an ancestor of the other, e.g. across a
// merge) is deterministic rather than dependent on map iteration order.
sort.Strings(candidates)
for _, c := range candidates {
    if c == cur || c == best { continue }
    ancestor, err := g.IsAncestor(c, cur)        // check 1: candidate is an ancestor of cur
    ...
    mergedIntoTrunk, err := g.IsAncestor(c, s.Trunk)  // check 2: skip merged-out candidates
    ...
    if best != s.Trunk {
        bestIsAncestor, err := g.IsAncestor(best, c)  // check 3: keep the closest
        ...
        if !bestIsAncestor { continue }
    }
    best = c
}
return best, nil
```

Semantics to preserve exactly (derive a truth table before coding):

1. Candidates are trunk + all tracked branches except `cur`, scanned in
   sorted order.
2. A candidate survives iff it is an ancestor of `cur` AND not merged into
   trunk.
3. Among survivors, `best` advances only when the current `best` is an
   ancestor of the candidate (so the final pick is the *deepest* candidate on
   the chain through `best`; incomparable survivors lose to the earlier-sorted
   incumbent). Error on any git failure — errors are returned, not skipped.

- Caller: `TrackBranch` (`engine.go:784–818`); after inference it runs one
  `g.MergeBase(parent, cur)` — out of scope, keep it.
- Available after plan 009: `g.MergedInto(ref) (map[string]bool, error)` on
  the port, plus the fake's implementation.
- Tests pinning behavior: `grep -rn "inferParent\|TestTrack" internal/stack/*_test.go`
  — commit `298b8e6` ("deterministic inferParent") added the determinism
  cases; they are the contract.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| Track e2e | `go test ./e2e -run TestTrackUntrack -count=1` | PASS |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/engine.go` (`inferParent` only),
`internal/stack/engine_test.go` (new cases).

**Out of scope**: `TrackBranch`'s validation and `MergeBase` call; the port
(009 already added what's needed); `cmd/track.go`.

## Git workflow

- Branch: `batch-track-parent-inference`
- Commit message style: `perf: batch track's parent inference over MergedInto sets`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Pin the current behavior harder

Before changing anything, add table-driven cases to `engine_test.go` for the
shapes the rewrite could get wrong: (a) a linear chain `main→a→b`, track `c`
created off `b` → expects `b`; (b) two incomparable ancestors (branch `x` and
`y` both ancestors of `cur`, neither of the other) → expects the
sorted-first one; (c) a merged-out ancestor → skipped; (d) candidate set
empty → trunk. Use `newEnvState()`/`mkBranch`; assert via `TrackBranch` with
empty parent.

**Verify**: `go test ./internal/stack -run TestTrack -count=1 -v` → all PASS
on the *unmodified* code.

### Step 2: Rewrite over two MergedInto sets

- `ancestorsOfCur, err := g.MergedInto(<cur's tip ref>)` — branches whose
  tips are ancestors of (or equal to) `cur`; this answers check 1 for all
  candidates. Mind the ref form: use the same convention plan 009 settled on
  (bare name vs `refs/heads/`-prefixed).
- `mergedIntoTrunk, err := g.MergedInto(<trunk ref>)` — answers check 2.
- Survivors = sorted candidates ∩ ancestorsOfCur − mergedIntoTrunk − {cur}.
- Keep the existing loop *structure* for check 3 (pairwise
  `g.IsAncestor(best, c)` among survivors only, same order, same
  `best != s.Trunk` gate) — survivors are typically one chain, so this is
  O(chain), not O(N).
- Error semantics: a `MergedInto` failure returns the error, same as today's
  per-call failures.

One subtlety: `IsAncestor(c, cur)` is true also when `c == cur`'s tip equals
an ancestor's tip (identical tips). `for-each-ref --merged X` includes
branches whose tip *is* X. Today's code skips `c == cur` explicitly and
identical-tip candidates pass check 1 — confirm the set-based version
preserves the identical-tip case (Step 1's chain test with an empty branch on
top covers it; add such a case if not).

**Verify**: `go test ./internal/stack -run TestTrack -count=1 -v` → all PASS
(including Step 1's new cases, unchanged).

### Step 3: Full suite

**Verify**: `make test-fast` → exit 0; `go test ./e2e -run TestTrackUntrack
-count=1` → PASS; `make ci` → exit 0.

## Test plan

Step 1's characterization cases are the core. The determinism tests from
commit `298b8e6` and the e2e `TestTrackUntrack` journey are the regression
net.

## Done criteria

- [ ] `grep -n "IsAncestor" internal/stack/engine.go` — within `inferParent`, only the check-3 pairwise call remains
- [ ] `go test ./internal/stack -count=1` exits 0 with the new cases present
- [ ] `make ci` exits 0
- [ ] Only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- Any Step 1 characterization case changes outcome under the rewrite — the
  set translation is wrong; report the case rather than adjusting the test.
- The fake git's `MergedInto` lacks the identical-tip semantics and fixing it
  would change plan 009's landed behavior.

## Maintenance notes

- If `track` ever batch-adopts multiple branches (a direction idea), the two
  MergedInto sets can be computed once for the whole batch — the structure
  this plan introduces makes that trivial.
- Reviewer: focus on the incomparable-ancestors tie-break; it's the only
  subtle semantic.

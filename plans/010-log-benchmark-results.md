# Plan 010 benchmark results: `st log` history materialization

`BenchmarkLogHistory` (cmd/log_bench_test.go) renders `st log --json` over a
disposable repo built by one `git fast-import` — linear history on `main` with
K tracked branches stacked at evenly spaced tips (`feats-i` at commit
`N·i/(K+1)`). Fixture setup is excluded from timing; `ReportAllocs` is on.

- Platform: darwin/arm64 (Apple Silicon), local NVMe
- Toolchain: go1.26.5, git 2.54.0 (Apple Git-157)
- Command: `ST_TEST_DEBUG=1 go test ./cmd -run '^$' -bench '^BenchmarkLogHistory$' -benchmem -count=3`
  (`ST_TEST_DEBUG=1` keeps benchmark lines off the suite's /dev/null stdout
  redirect; fixture chatter interleaves with results in the raw log.)

## Before — `rev-list --parents` graph materialization (cb31f06-era `tipGraph`)

| commits | branches | ns/op (median) | B/op (median) | allocs/op (median) |
|---|---|---|---|---|
| 1,000  | 0  | 254,640,802 |   808,149 |  1,499 |
| 1,000  | 1  | 177,001,847 |   865,836 |  1,537 |
| 1,000  | 10 | 150,977,863 | 1,321,596 |  1,822 |
| 1,000  | 50 | 128,123,682 | 3,441,432 |  2,975 |
| 20,000 | 0  | 329,967,802 | 13,182,248 | 20,658 |
| 20,000 | 1  | 375,016,861 | 14,205,021 | 20,755 |
| 20,000 | 10 | 410,530,139 | 22,329,997 | 21,675 |
| 20,000 | 50 | 430,494,028 | 59,455,677 | 25,530 |

Full runs: 8 / 3 / 4, 6 / 4 / 3, 6 / 7 / 8, 7 / 9 / 8, 4 / 4 / 3, 3 / 3 / 3,
3 / 3 / 3, 2 / 3 / 4 iterations (the suite's 10-minute timeout bounds b.N).

## After — one bounded `merge-base --is-ancestor` probe per distinct tip pair

| commits | branches | ns/op (median) | B/op (median) | allocs/op (median) |
|---|---|---|---|---|
| 1,000  | 0  |  78,786,753 |   185,684 |   354 |
| 1,000  | 1  | 138,064,008 |   204,420 |   458 |
| 1,000  | 10 | 477,052,194 |   384,640 | 1,405 |
| 1,000  | 50 | 1,831,566,708 | 1,407,528 | 5,437 |
| 20,000 | 0  |  94,920,036 |   185,894 |   354 |
| 20,000 | 1  | 180,090,333 |   209,857 |  468 |
| 20,000 | 10 | 709,696,792 |   396,740 | 1,415 |
| 20,000 | 50 | 1,511,532,166 | 1,425,848 | 5,440 |

## Reading

- **Go-side memory no longer scales with history.** branches=0 holds ~186KB at
  both 1k and 20k commits (was 0.8MB → 13.2MB); the worst case drops from
  ~59MB to ~1.4MB. Allocations likewise flatten (25k → 5.4k at 20k/50).
- **Latency now scales with distinct tip pairs, not commits.** Trunk-only and
  single-branch logs are *faster* than baseline at both depths (no history
  walk at all). At 10 rendered pairs the probe overhead is ~300–500ms; at 50
  pairs ~1.1–1.4s of pure spawn cost (≈25–30ms/probe including process start).
- **Where the cost went:** each pair is one `git merge-base --is-ancestor`
  spawn (fully-qualified `refs/heads/` args skip two `show-ref` existence
  probes inside `git.IsAncestor`). Git may still walk history inside
  merge-base; the bound removed is on *materialized output and Go memory*.
- **Tradeoff call:** the regression is confined to unusually wide stacks
  (10–50 tracked branches) and is bounded and spawn-linear there; the common
  0–2 branch case is flat-to-faster and Go memory is history-independent
  everywhere. Per plan Step 3 this is recorded rather than cherry-picked; a
  future batching design (e.g. a single spawn answering all pairs) would need
  an internal/git API change and is out of this plan's scope.

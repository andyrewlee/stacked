# Plan 010: Answer log ancestry questions without loading the entire history

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- cmd/log.go cmd/commands_json_test.go cmd/log_bench_test.go plans/010-log-benchmark-results.md CHANGELOG.md plans/010-bound-log-history-materialization.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P2
- **Effort:** M
- **Risk:** LOW
- **Depends on:** 001 recommended; no functional dependency
- **Category:** perf
- **Audit item:** 10
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

Log materializes all history reachable from rendered tips, then repeatedly walks that graph to decide whether to show a branch's top subject. Even a trunk-only log loads trunk history. Move the small set of ancestry questions into Git and bound Go memory/output by the rendered branch set, while measuring the extra subprocess cost.

## Current state

`cmd/log.go:216`, tipGraph, starts an unbounded traversal:

```go
args := []string{"rev-list", "--parents"}
```

Its results populate commitGraph, and each reachable call creates another seen map. `topSubject` at line 275 asks only:

```go
if !ok || graph.reachable(parentTip, tip) {
    return "", false
}
```

`runLog` constructs this graph even when only trunk is rendered. Existing semantic tests include TestLogOmitsTopCommitWhenBranchTipIsReachableFromParent and TestLogJSONOmitsUnrelatedLocalBranch in cmd/commands_json_test.go.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Keep prefetched branch tips/subjects and existing JSON/text output. internal/git already provides IsAncestor, which runs Git's bounded-result ancestry query. This plan bounds materialized output/Go memory; it does not claim constant Git CPU or eliminate all history traversal inside Git.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./cmd -run '^TestLog' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
| Fast engine suite | `make test-fast` | all tests pass |
| Race unit/integration | `make test` | all tests pass |
| Black-box CLI | `make e2e` | all tests pass |
| Build | `make build` | exit 0; writes the ignored st binary |
| Formatting / lint checks | `make fmt-check lint` | exit 0 using pinned golangci-lint v2.12.2 |
| Complete contributor gate | `make ci` | exit 0, including dependency/pin checks, native/cross vet, build, lint, and coverage |
| Whitespace | `git diff --check` | exit 0 |

The audit baseline passed race tests, e2e, native/cross vet and coverage (87.0%) at the stamped commit. Full `make ci` was not run because golangci-lint was absent. That is historical evidence, not validation of your changes. Run the full gate when the pinned tools are available; if a required tool is absent, report the incomplete gate instead of substituting gofmt or changing tool pins.

## Scope

**Only modify:**

- `cmd/log.go`
- `cmd/commands_json_test.go`
- `cmd/log_bench_test.go`
- `plans/010-log-benchmark-results.md`
- `CHANGELOG.md`
- `plans/010-bound-log-history-materialization.md`
- `plans/README.md`


**Out of scope:** persistent caches, new dependencies, subject/JSON changes, graph visualization features, engine-wide ancestry rewrites, timing thresholds in ordinary CI.

## Git workflow

Use a separate branch/worktree named `advisor/010-bound-log-history-materialization` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `perf: avoid materializing full history for log`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Characterize output and establish history-size benchmarks

Add cases to the existing log tests for equal tips, child behind parent, child ahead, divergence, merge ancestry and missing tips. Add TestLogDoesNotMaterializeHistory with a temporary Git wrapper that records commands and rejects unbounded `rev-list --parents`; trunk-only should require no ancestry query. Add BenchmarkLogHistory in cmd/log_bench_test.go for 1,000 and 20,000 commits and 0/1/10/50 tracked branches. Create fixtures outside the timer using fast-import or commit-tree batching in a disposable repository rather than thousands of shell commits. Follow the cmd fixture environment/cwd conventions, avoid parallel cwd changes, discard rendered output, and call ReportAllocs. Capture baseline results in the scoped benchmark Markdown file with Go/Git/platform and fixture sizes.

**Verify:** `go test ./cmd -run '^$' -bench '^BenchmarkLogHistory$' -benchmem -count=3` → baseline measurements are recorded; the new no-materialization test intentionally fails before implementation

### Step 2: Replace graph construction with cached ancestry answers

Remove commitGraph, tipGraph and reachable from cmd/log.go. Resolve the exact child/parent tip pairs needed for rendered tracked branches using existing tips. Equal tips suppress the subject without a subprocess; missing tips retain existing suppression. For each other distinct pair, call git.IsAncestor(childTip, parentTip) once and cache the boolean for that invocation. Propagate query errors through runLog instead of silently showing or hiding a subject. Pass the resulting branch/pair decision into topSubject while retaining prefetched subjects. No query is needed for trunk-only output. Keep the pair direction correct: the question is whether the child's tip is reachable from the parent.

**Verify:** `go test ./cmd -run '^TestLog' -count=1` → all semantic/output cases and the no-materialization command assertion pass

### Step 3: Measure the tradeoff and run the full gate

Repeat the identical benchmark matrix and record before/after allocations, bytes/op and ns/op without cherry-picking only the large-history case. Go memory should no longer scale with total commit count; note that Git may still walk history and one query per pair adds spawn overhead. Add a changelog entry describing the resource improvement. If typical short-history cases show a substantial regression, stop for a reviewed batching design instead of inventing a new cache or changing output semantics.

**Verify:** `go test ./cmd -run '^$' -bench '^BenchmarkLogHistory$' -benchmem -count=3` → results show removed full-history materialization and bounded Go-side allocation growth; `make ci` also passes

## Test plan

- Top subject equality/behind/ahead/diverged/merge/missing-tip cases extend TestLogOmitsTopCommitWhenBranchTipIsReachableFromParent.
- A command-recording test proves no full rev-list graph is requested and duplicate tip pairs share one query.
- The benchmark matrix varies both history size and rendered branch count; setup is excluded from timing.
- Existing text/JSON golden tests pass unchanged; do not refresh golden files to hide semantic differences.

## Done criteria

- [ ] `rg -n 'commitGraph|tipGraph|func .*reachable' cmd/log.go` returns no matches.
- [ ] TestLogDoesNotMaterializeHistory passes; trunk-only log performs no ancestry query.
- [ ] The scoped benchmark report contains the complete comparable before/after matrix.
- [ ] `make ci` passes with unchanged log output contracts.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- Reversing the IsAncestor arguments appears necessary to pass a fixture; inspect the intended parent-reachability predicate instead.
- Performance evidence shows a material regression for expected small stacks; report results and request a revised optimization plan.
- A solution needs a persistent history cache or changes existing top-subject meaning.

## Maintenance notes

Keep the number of ancestry probes tied to distinct rendered tip pairs. Future performance claims should distinguish Git's internal traversal from Go heap growth and subprocess output size.


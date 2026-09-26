# Plan 002: Keep both sides of renames in the prune containment check

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/git/git.go internal/git/git_test.go e2e/e2e_journey_test.go CHANGELOG.md plans/002-include-rename-sources-in-containment.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** S
- **Risk:** LOW
- **Depends on:** 001 recommended before running real-Git regressions; no source dependency
- **Category:** bug
- **Audit item:** 1
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

A branch that renames A to B is currently considered contained when upstream merely copies A to B and keeps A. The containment predicate only compares the destination, overlooking the branch's deletion. Sync can consequently prune a branch whose full change has not landed.

## Current state

`internal/git/git.go:249`, `ChangesContainedIn`, enumerates changed paths and then checks equality only for those paths:

```go
out, err := run("diff", "--name-only", "-z", up+"..."+br)
```

The later command uses literal pathspecs:

```go
cmd.Env = append(gitEnv(), "GIT_LITERAL_PATHSPECS=1")
```

`internal/stack/engine.go:1058` uses this answer in `mergedBranches`; `PruneMerged` can force-delete the branch. Existing real-Git cases are in `internal/git/git_test.go:TestChangesContainedIn`.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

The Git port's documented invariant is: “Exact content equality, never a heuristic: a branch carrying any content upstream lacks is not contained.” Keep the NUL-separated raw output and literal-path handling that already implement this conservative contract.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/git -run '^TestChangesContainedIn' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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

- `internal/git/git.go`
- `internal/git/git_test.go`
- `e2e/e2e_journey_test.go`
- `CHANGELOG.md`
- `plans/002-include-rename-sources-in-containment.md`
- `plans/README.md`


**Out of scope:** prune policy, force-deletion semantics, engine interfaces, similarity-based merge detection, dependency or schema changes.

## Git workflow

Use a separate branch/worktree named `advisor/002-include-rename-sources-in-containment` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: include rename sources in containment checks`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Reproduce the rename-versus-copy false positive

Extend TestChangesContainedIn with a shared base containing A. On topic, rename A to B without content changes. On main, independently copy A to B while keeping A. Assert ChangesContainedIn(main, topic) is false with diff.renames both enabled and disabled. Add the positive case where main really removes A and contains matching B, and negative cases where either endpoint differs. In e2e/e2e_journey_test.go add TestSyncPreservesUncontainedRename using the existing remote/sync harness; assert the topic ref and tracked metadata survive sync.

**Verify:** `go test ./internal/git -run '^TestChangesContainedIn' -count=1` → the new rename-versus-copy assertion fails on the original implementation; all fixture setup succeeds

### Step 2: Disable rename collapsing only for path collection

Add `--no-renames` to the name-only path enumeration in ChangesContainedIn. Retain the merge-base triple-dot range, raw `run`, `-z`, literal pathspec environment, endpoint comparison and existing error handling. Do not change rename behavior globally or parse human-readable rename output. This makes the path set include both A and B while leaving the equality predicate intact.

**Verify:** `go test ./internal/git -run '^TestChangesContainedIn' -count=1` → all original and new containment cases pass

### Step 3: Verify pruning behavior and document the fix

Run the new sync journey and the complete contributor gate. Add a brief CHANGELOG entry explaining that squash-merge detection now includes rename source deletions. Check that a truly contained rename remains eligible for normal pruning.

**Verify:** `go test ./e2e -run '^TestSyncPreservesUncontainedRename$' -count=1` → PASS with the uncontained topic retained; then `make ci` passes

## Test plan

- Add rename-versus-copy, fully contained rename, changed source, changed destination, and diff.renames configuration variants to TestChangesContainedIn.
- Retain existing unusual-path/literal-pathspec cases; do not replace them with filename splitting on whitespace.
- The sync regression asserts refs and stack metadata, not just an output phrase.

## Done criteria

- [ ] All containment regressions pass and TestSyncPreservesUncontainedRename passes.
- [ ] The path collection includes `--no-renames`, `--name-only`, and `-z`; literal-pathspec comparison remains present.
- [ ] `make ci` passes.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The fixture is already reported uncontained before the change because rename detection did not trigger; increase deterministic content/rename similarity and prove the precondition before implementing.
- A proposed fix changes branch-deletion policy instead of completing the path set.

## Maintenance notes

Any later replacement of containment detection must prove completeness over added, removed and renamed paths. False positives are destructive here; false negatives merely preserve a branch.


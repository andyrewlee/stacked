# Plan 007: Refuse worktree include collisions before copying any files

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- cmd/worktree_copy.go cmd/worktree.go cmd/worktree_test.go e2e/e2e_journey_test.go README.md CHANGELOG.md plans/007-protect-worktree-include-destinations.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** M
- **Risk:** MED
- **Depends on:** 001 recommended for real-Git worktree tests
- **Category:** bug
- **Audit item:** 6
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/007-protect-worktree-include-destinations` (stacked on `advisor/006`).

- Drift check: `git diff --stat cb31f06..HEAD -- <scoped files>` → only documented predecessor changes (001–006) plus plan-file additions; no unexplained source drift.
- Step 1: added `TestCopyWorktreeIncludesRefusesDestinationCollision` — seven subtests covering file/file, later-collision-blocks-earlier-copy, dir-with-tracked-descendant, tracked-file-blocking-descendant-include, tracked-but-absent index entry, and existing untracked file/dir. Fixture note: the destination branch's tracked files are written inside the new worktree via `git worktree add -b`, because switching the source worktree onto a branch that tracks the colliding paths and back would delete the source's ignored copies. Pre-fix proof came from stashing the implementation: `st worktree feat-b` then reported `copied: secret.txt`, overwriting the tracked destination file.
- Step 2: `copyWorktreeIncludes` now selects candidates in a write-free phase 1, then calls `refuseDestinationCollisions(dstRoot, candidates)` before the first `prepareSafeDestination`/`reflinkCopy`. The preflight reads `git -C dst ls-files -z` (tracked exact path, tracked descendant via sorted-prefix probe, tracked ancestor via `path.Dir` walk) and Lstats the destination root (symlink refused first as the more specific hazard, then any existing entry — including an empty directory). Non-git destinations yield an empty tracked set; a real ls-files failure is an error. A symlink keeps the pre-existing "unsafe destination symlink" message; the e2e regression `TestWorktreeIncludeCopyFailureRollsBackWorktree` still sees that context.
- Step 3: `reflinkCopy` returns the native `cp` error without falling back when the destination exists after the failure (partial output is never merged by `plainCopy`); it falls back only when `cp` left no destination. `TestReflinkCopyFallsBackWhenCpFails` gains a partial-output cp shim subtest.
- e2e: `TestWorktreeCopyCollisionRollsBackWorktree` proves a newly materialized worktree is removed (registry and disk) when the copy refuses, while a preexisting sibling worktree and the ignored source file are untouched.
- Focused verification: `go test ./cmd -run 'Test.*(CopyWorktreeIncludes|ReflinkCopy|PlainCopy)' -count=1` → ok; `make test-fast` → ok; `make test` (race) → ok; `make e2e` → ok; `make build` → ok; `make fmt-check` + `make lint` (v2.12.2) → 0 issues.
- `make ci` on commit `e1ffc51` in detached worktree `/private/tmp/st-ci-007` → exit 0 (lint 0 issues, vet native/windows/plan9, build, race tests, e2e, merged coverage 87.0% ≥ 75%).
- `git diff --check` → exit 0; modified files all in Scope (`cmd/worktree_copy.go`, `cmd/worktree_test.go`, `e2e/e2e_journey_test.go`, `CHANGELOG.md`, plan files).
- Docs: `CHANGELOG.md` notes the destination preflight and the no-merge partial-failure rule.

## Why this matters

An ignored source file may be tracked by the destination branch. The include copier checks only source ignored status and can overwrite the freshly checked-out tracked file. Existing destination directories also behave differently under native cp and the fallback copier. A conservative collision policy protects the destination tree and makes both copy paths agree.

## Current state

`cmd/worktree_copy.go:61` checks ignored status only in the source:

```go
ignored, err := gitIgnoredSet(srcRoot, entries)
```

Each surviving entry then calls prepareSafeDestination and reflinkCopy. `plainCopy` at line 419 writes directly:

```go
return os.WriteFile(dst, in, info.Mode().Perm())
```

The destination symlink guard does not reject regular files. `cmd/worktree.go` first adds the destination branch worktree, then copies includes, rolling the new worktree back on copy failure. Existing tests in cmd/worktree_test.go cover traversal/symlinks and an ignored child under a tracked parent directory.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Retain source validation, ignored-only filtering, nested-selection deduplication, containment guards, and symlink-preserving copy behavior. Extend TestCopyWorktreeIncludes and TestReflinkCopyFallsBackWhenCpFails rather than changing include syntax.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./cmd -run 'Test.*(CopyWorktreeIncludes\|ReflinkCopy\|PlainCopy)' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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

- `cmd/worktree_copy.go`
- `cmd/worktree.go`
- `cmd/worktree_test.go`
- `e2e/e2e_journey_test.go`
- `README.md`
- `CHANGELOG.md`
- `plans/007-protect-worktree-include-destinations.md`
- `plans/README.md`


**Out of scope:** merging existing destination trees, overwrite options, copying tracked source files, changing glob syntax, deleting preexisting worktrees or arbitrary directories.

## Git workflow

Use a separate branch/worktree named `advisor/007-protect-worktree-include-destinations` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: reject worktree include destination collisions`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Reproduce destination branch and directory collisions

Add TestCopyWorktreeIncludesRefusesDestinationCollision. Source main ignores config.local; destination branch tracks config.local with different bytes. Copy must fail without touching the destination file or copying any earlier manifest entry. Add a selected ignored directory whose destination contains tracked descendants, an existing untracked destination file/directory, and an absent tracked index entry (remove the worktree file but keep it tracked). Preserve TestCopyWorktreeIncludesKeepsIgnoredFileUnderTrackedDir as a positive control: copying a new ignored child under an existing tracked parent is valid.

**Verify:** `go test ./cmd -run '^TestCopyWorktreeIncludesRefusesDestinationCollision$' -count=1` → new collision cases fail on the original overwriting/merging behavior

### Step 2: Preflight all actual copy candidates against the destination

After existing source filtering and nested deduplication, build the complete list of entries that would actually copy. Before copying any of them, query destination tracked paths with raw `git -C dstRoot ls-files -z`, retaining exact path bytes. Reject an entry equal to a tracked path or containing any tracked descendant; also reject conflicts with tracked parent entries (files, symlinks or gitlinks). Lstat every selected destination root and refuse any existing root, including an empty directory. Retain destination ancestor containment/symlink checks; an existing safe parent directory is allowed. Return an error naming the manifest-relative path and collision reason. Do not call a helper that creates destination directories during this preflight unless that behavior is separated from validation.

**Verify:** `go test ./cmd -run '^TestCopyWorktreeIncludes' -count=1` → all collision cases and existing path-safety/ignored-child cases pass; no earlier candidate is copied on a later preflight failure

### Step 3: Align native-copy and fallback failure behavior

Both copy strategies must start with an absent selected destination root. If native cp fails and has created any destination content, return its error so the existing fresh-worktree rollback handles the partial copy; do not merge a fallback into that partial tree. Falling back remains allowed when cp fails without creating a destination. Retain plainCopy's existing symlink behavior and recursive creation. Add a fake cp that creates partial content then fails, and one that fails without touching the destination. Add TestWorktreeIncludeCollisionRollsBack in e2e to verify the newly added worktree registration/path is cleaned up and the preexisting source branch/files remain intact. Document collision refusal and add a changelog entry.

**Verify:** `go test ./cmd -run 'Test.*(CopyWorktreeIncludes|ReflinkCopy)' -count=1` → native/fallback success and partial-failure cases pass; `go test ./e2e -run '^TestWorktreeIncludeCollisionRollsBack$' -count=1` and `make ci` also pass

## Test plan

- File/file, directory/descendant, file/directory, existing untracked root, tracked-but-absent file and tracked parent entry collisions.
- A later colliding manifest entry prevents all earlier copies.
- Native cp absent, immediate failure, partial failure and success; tests must not require a particular filesystem's reflink capability.
- E2E rollback must distinguish the freshly owned destination from all preexisting paths.

## Done criteria

- [ ] No selected include root overwrites or merges into existing destination content.
- [ ] Tracked entries remain protected even if their worktree file is missing.
- [ ] Existing ignored children under safe tracked parent directories still copy.
- [ ] Native and fallback collision behavior agrees; `make ci` passes.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The implementation would delete a preexisting destination to make fallback succeed.
- A destination alias/case-folding path cannot be proven safe by index checks plus filesystem existence/containment; refuse it instead of guessing.
- Rollback would affect a worktree not created by this command.

## Maintenance notes

Keep the source and destination policies separate: ignored in one branch says nothing about another branch's tracked files. Any future merge/overwrite feature needs an explicit preservation contract and separate tests.


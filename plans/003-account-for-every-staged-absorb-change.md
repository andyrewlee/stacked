# Plan 003: Account for every staged change before absorb can reset the index

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/git/git.go internal/git/git_test.go internal/stack/absorb_test.go e2e/e2e_absorb_test.go docs/AGENT.md CLAUDE.md CHANGELOG.md plans/003-account-for-every-staged-absorb-change.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** M
- **Risk:** MED
- **Depends on:** 001 recommended for reproducible real-Git tests
- **Category:** bug
- **Audit item:** 2
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

The staged-diff parser can silently omit empty added/deleted files and changes rendered with unexpected Git formatting. When a supported hunk targets an ancestor, absorb later resets HEAD and can discard the omitted staged content. A zero-refusal plan must account for the entire staged diff before any mutation.

## Current state

`internal/git/git.go:461`, DiffCachedHunks, invokes configurable diff output with `run("-c", "core.quotepath=false", "diff", "--cached", "-U0")`. Its flush logic includes:

```go
if sec.modeChange {
    unsupported = append(unsupported, UnsupportedRecord{File: name, Reason: "mode change"})
}
hunks = append(hunks, sec.hunks...)
```

Mode detection at line 510 recognizes old/new mode, but not new-file/deleted-file mode headers. An empty file section can therefore yield neither hunk nor refusal. `internal/stack/absorb.go:317` calls `ResetHardIn("", "HEAD")` after applying patches. The Git port explicitly promises every staged change appears as a hunk or UnsupportedRecord.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Extend `TestDiffCachedHunks` and `TestDiffCachedPatchFor` in internal/git/git_test.go, plus existing refusal tests in internal/stack/absorb_test.go. Absorb's published rule is conservative: any refusal blocks the whole apply, including otherwise attributable hunks. Preserve that rule and current JSON keys.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/git -run '^TestDiffCached(Hunks\|PatchFor)' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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
- `internal/stack/absorb_test.go`
- `e2e/e2e_absorb_test.go`
- `docs/AGENT.md`
- `CLAUDE.md`
- `CHANGELOG.md`
- `plans/003-account-for-every-staged-absorb-change.md`
- `plans/README.md`


**Out of scope:** supporting binary changes, additions or renames as new absorb features; coordinate relocation; state schema changes; broad diff-parser rewrites.

## Git workflow

Use a separate branch/worktree named `advisor/003-account-for-every-staged-absorb-change` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: reject unaccounted staged changes during absorb`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Add regressions for omitted sections and hostile formatting

Extend TestDiffCachedHunks for empty added and deleted files, metadata-only sections, text plus mode changes, binary/rename sections, and malformed/unclassified section content where parser testing permits. Add table cases for diff.noprefix, diff.mnemonicPrefix, color.ui=always, and a configured external diff/textconv command that writes a sentinel. Add TestAbsorbPreservesMixedEmptyFileChanges in e2e: stage a valid edit attributable to an ancestor together with an empty file addition/deletion, run dry-run and apply, and compare exact index tree, worktree bytes, refs and effective undo history before/after refusal.

**Verify:** `go test ./internal/git -run '^TestDiffCachedHunks' -count=1` → new completeness/format cases fail for the original omission; setup and existing cases still work

### Step 2: Make staged diff capture deterministic and complete

Use one shared private argument builder for DiffCachedHunks, DiffCachedPatch and DiffCachedPatchFor. Explicitly request `--no-color --no-ext-diff --no-textconv --src-prefix=a/ --dst-prefix=b/` with the existing cached/U0 settings. Use only flags compatible with the documented Git floor; do not use the newer --default-prefix shortcut. Retain conservative rename/copy classification. Mark new-file/deleted-file metadata and all non-hunk sections as explicit refusals when unsupported, including empty changes. Any recognized section must produce hunks and/or a refusal; malformed headers or unaccounted data must return an error/refusal rather than disappear. Cross-check against a raw NUL-separated staged path inventory using rename-disabled enumeration so a staged entry absent from parsed output cannot yield a zero-refusal plan. For renamed records, account for both endpoints or refuse the whole operation; do not guess path equality from pretty output.

**Verify:** `go test ./internal/git -run '^TestDiffCached(Hunks|PatchFor)' -count=1` → all parser/patch cases pass, configured diff programs are not invoked, and every nonempty staged inventory is accounted for or rejected

### Step 3: Prove refusal preserves all staged data

Verify the engine keeps its existing gate before any amend/reset. Add only targeted engine tests if the Git adapter's new refusal coverage already feeds the existing gate. Document empty metadata-only changes and normalized diff behavior in the absorb sections of docs/AGENT.md and CLAUDE.md, and add a concise changelog entry. Do not change hunk identifiers or JSON field names.

**Verify:** `go test ./e2e -run '^TestAbsorbPreservesMixedEmptyFileChanges$' -count=1` → PASS for add/delete variants with unchanged refs, index, files and effective undo history; then `make ci` passes

## Test plan

- Use existing TestDiffCachedHunks and TestDiffCachedPatchFor fixtures; add the specific empty-file and formatting regressions rather than generic parser snapshots.
- Test unsupported-only and mixed supported/unsupported staging; dry-run and apply must agree on refusal.
- Retain supported text deletion, UTF-8 filenames and multiple targets when their existing rules allow them.
- Add an inventory-mismatch regression demonstrating fail-closed behavior even if a diff section cannot be classified.

## Done criteria

- [ ] No staged section or inventory entry can be silently omitted from a zero-refusal plan.
- [ ] Diff configuration cases and mixed empty-file preservation regressions pass.
- [ ] No external diff/textconv sentinel is created.
- [ ] `make ci` passes and absorb JSON contracts remain unchanged.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- Completeness cannot be established without broadening supported change types; refuse the uncertain case and report the remaining design question.
- A normalization flag is unavailable on supported Git; do not raise the floor silently.
- New refusal paths reach AmendTipWithPatch or ResetHardIn in a regression.

## Maintenance notes

Every future staged-diff feature must preserve the accounting invariant. Parser support and patch rendering must consume the same normalized representation; configurable pretty output is not a stable machine protocol.


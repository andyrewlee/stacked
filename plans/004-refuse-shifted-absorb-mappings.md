# Plan 004: Refuse absorb hunks whose ancestor coordinates cannot be applied safely

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/git/git.go internal/git/shell.go internal/git/git_test.go internal/stack/git.go internal/stack/fakegit_test.go internal/stack/absorb.go internal/stack/absorb_test.go e2e/e2e_absorb_test.go docs/AGENT.md CLAUDE.md CHANGELOG.md plans/004-refuse-shifted-absorb-mappings.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** M
- **Risk:** MED
- **Depends on:** 003-account-for-every-staged-absorb-change.md
- **Category:** bug
- **Audit item:** 3
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/004-refuse-shifted-absorb-mappings` (stacked on `advisor/003`).

- Drift check: `git diff --stat cb31f06..HEAD -- <scoped files>` → only documented predecessor changes (001 test-env isolation, 002 `--no-renames` enumeration, 003 staged-diff accounting) plus plan-file additions; no unexplained source drift. An uncommitted `e2e/e2e_absorb_test.go` change was this plan's own regression-in-progress.
- Step 1: added `TestAbsorbRefusesShiftedRepeatedLines` in `e2e/e2e_absorb_test.go` (insert-shift, delete-shift, historical rename each refuse; unshifted control absorbs). Pre-fix run failed as specified: all three unsafe cases reported a zero-refusal plan that would apply at the wrong coordinate.
- Step 2: `BlamePorcelain` now runs `git blame --line-porcelain` and returns `map[int]BlameLine{Commit, OriginalLine, FinalLine, Path}`; the parser requires a 40-char SHA, both coordinates, and a decodable `filename` record (C-quoted octal/UTF-8 via `strconv.Unquote`, unquoted spaces, malformed records dropped rather than guessed). Port updated in `internal/stack/git.go`, `internal/git/shell.go`, and the fake.
- Step 3: `absorbPlan` refuses when any removed line's provenance is missing/malformed, original≠final line, historical path≠current path, ownership is mixed, or old-side coordinates are noncontiguous — all before any amend/reset/checkpoint. All `absorb_test.go` fixtures now supply explicit provenance via a `blameID` helper; new engine cases cover identity, insert/delete shifts, missing provenance, and rename.
- Parser tests added for repeated commits, spaces, UTF-8 (quoted and raw `core.quotePath=false`), encoded names, control escapes, and malformed/missing provenance.
- Post-fix: `go test ./internal/git ./internal/stack -run 'Test.*(Absorb|Blame|DiffCachedPatchFor)' -count=1` → ok; e2e regression → PASS (the first test version snapshotted the index before staging; fixed to snapshot after staging so "index preserved" is meaningful).
- `make test-fast` → ok; `make test` covered by `make ci`; `git diff --check` → exit 0; all modified files in Scope.
- `make ci` on commit `86b0e46` in detached worktree `/private/tmp/st-ci-004` → exit 0 (golangci-lint v2.12.2 0 issues, vet native/windows/plan9, build, race tests, e2e, merged coverage 86.8% ≥ 75%).
- Docs updated: `CLAUDE.md` and `docs/AGENT.md` absorb sections now describe the shifted/historically-renamed refusal; `CHANGELOG.md` notes the conservative refusal.

## Why this matters

Blame identifies an owning commit but discards original line coordinates and path. Absorb then applies a zero-context patch using HEAD coordinates against that ancestor. Repeated text can make the patch succeed at the wrong occurrence. This plan deliberately refuses shifted or renamed mappings until a separate design can safely relocate patches.

## Current state

`internal/git/git.go:800`, BlamePorcelain, parses a header but keeps only the final line and SHA:

```go
fields := strings.Fields(line[41:])
final, err := strconv.Atoi(fields[1])
// ...
lines[final] = sha
```

`DiffCachedPatchFor` at line 665 keeps `h.OldStart`; `AmendTipWithPatch` at line 748 applies with `--cached --unidiff-zero` into the target tree. Example ancestor contents are `top`, `same`, `middle`, `same`, `bottom`; a descendant inserts two lines at the start. Editing the first `same` now at HEAD line 4 can target the ancestor's second `same` at line 4. This scenario was source-verified in the audit; the new regression must establish runtime behavior.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

The engine's Git port is in internal/stack/git.go, production forwarding methods are in internal/git/shell.go, and fake behavior is in internal/stack/fakegit_test.go. Change all three together. Preserve the existing all-or-nothing refusal contract and per-target hunk identifiers.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/git ./internal/stack -run 'Test.*(Absorb\|Blame\|DiffCachedPatchFor)' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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
- `internal/git/shell.go`
- `internal/git/git_test.go`
- `internal/stack/git.go`
- `internal/stack/fakegit_test.go`
- `internal/stack/absorb.go`
- `internal/stack/absorb_test.go`
- `e2e/e2e_absorb_test.go`
- `docs/AGENT.md`
- `CLAUDE.md`
- `CHANGELOG.md`
- `plans/004-refuse-shifted-absorb-mappings.md`
- `plans/README.md`


**Out of scope:** relocating or splitting hunks onto different coordinates, enabling renamed-file absorb, patch fuzzy matching, new public JSON fields, force apply options.

## Git workflow

Use a separate branch/worktree named `advisor/004-refuse-shifted-absorb-mappings` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: refuse unsafe absorb line mappings`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Capture the repeated-line wrong-target case

Add TestAbsorbRefusesShiftedRepeatedLines in e2e/e2e_absorb_test.go using the exact ancestor and descendant arrangement above. Stage only the first repeated-line edit. Assert dry-run has a refusal; apply preserves all branch refs, exact index/worktree bytes and effective undo history. Add a second case with a line deletion shifting coordinates and a historical path rename. Keep an unshifted repeated-line case that succeeds so the test does not merely ban repeated text.

**Verify:** `go test ./e2e -run '^TestAbsorbRefusesShiftedRepeatedLines$' -count=1` → the shifted case fails its expected-refusal assertion before the fix, demonstrating the unsupported mapping

### Step 2: Preserve original blame provenance through the port

Define a small Git value type with Commit, OriginalLine and OriginalPath. Change BlamePorcelain's map values to that type; adapt Shell, the engine Git interface and fake implementation atomically. Read `git blame --line-porcelain` so each line carries filename metadata, instead of relying on commit metadata repetition. Parse header original/final integers and the filename record without whitespace splitting the filename. Decode Git's C-quoted filename format with a tested decoder (strconv.Unquote is a candidate only after proving Git-generated octal/control/quote cases round-trip); never split the filename on whitespace or strip quotes and pretend the result is exact. Malformed provenance must be represented as missing/invalid attribution so the engine refuses it. Preserve existing supported UTF-8 paths. Preserve current conservative handling of malformed or missing provenance. Update existing fake fixtures to supply explicit original coordinates and paths instead of defaulting zero values to apparent safety.

**Verify:** `go test ./internal/git -run 'Test.*Blame' -count=1 && go test ./internal/stack -run '^$'` → provenance parser tests pass and the engine package compiles; Step 3 runs the full attribution regressions after adding the safety gate

### Step 3: Gate attribution on identity coordinates and path

In internal/stack/absorb.go, before a hunk becomes an apply candidate, require every removed/replaced old line's provenance to identify the same supported owner, original path equal to the current path, and OriginalLine equal to its HEAD old-line position. Missing, mixed, shifted, renamed or noncontiguous mappings produce a clear refusal. Preserve existing refusal of pure additions and other ambiguous cases. Do not remap patch headers in this plan. Keep the check before any amend/reset/checkpoint that represents an applied mutation. Update docs and changelog to say shifted historical coordinates are refused conservatively.

**Verify:** `go test ./e2e -run '^TestAbsorbRefusesShiftedRepeatedLines$' -count=1` → all shifted/renamed cases refuse without mutation and the unshifted control succeeds; then `make ci` passes

## Test plan

- Add original/final line and filename metadata parser tests in internal/git/git_test.go, including repeated commits, spaces/UTF-8 and encoded names.
- Add fake-engine cases for identity mapping, shifted insert/delete, missing provenance and historical rename.
- The e2e repeated-line fixture must verify exact content of both occurrences and preservation on refusal, not only exit status.
- Retain existing multiple-target absorb and DiffCachedPatchFor tests to prove this guard does not alter safe patch generation.

## Done criteria

- [ ] Shifted repeated-line and renamed-path fixtures refuse before any mutation.
- [ ] Same-coordinate multi-target absorb still passes.
- [ ] All Git port implementations and fake fixtures provide explicit provenance.
- [ ] `make ci` passes with unchanged public absorb JSON keys.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The repeated-line fixture does not reproduce unsafe application or incorrect zero-refusal classification; inspect the preconditions before changing code.
- Safe application would require coordinate relocation, index blob remapping, or a new supported change class; that is deferred work.
- A provenance parser silently supplies guessed identity coordinates when metadata is absent.

## Maintenance notes

This intentionally reduces the accepted set of hunks. A future relocation feature needs its own ancestor-tree patch design and adversarial repeated-text regressions; removing this guard without that work reopens the data-integrity risk.

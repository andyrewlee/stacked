# Plan 012: Parse worktree paths losslessly with a guarded legacy fallback

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/git/git.go internal/git/git_test.go internal/git/fuzz_test.go e2e/e2e_journey_test.go docs/AGENT.md CHANGELOG.md plans/012-preserve-worktree-path-bytes.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P2
- **Effort:** M
- **Risk:** MED
- **Depends on:** 001 recommended; this plan must land before design 015 treats worktree paths as structured identifiers
- **Category:** bug
- **Audit item:** 12
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** DONE

## Execution record

Executed on branch `advisor/012-preserve-worktree-path-bytes` (stacked on `advisor/011`), commit `3ce04ad` (CI-verified tree `0486ebf` — identical code; only plan docs differ).

- Drift check: `git diff --stat cb31f06..HEAD -- internal/git/git.go internal/git/git_test.go internal/git/fuzz_test.go e2e/e2e_journey_test.go docs/AGENT.md CHANGELOG.md plans/012-preserve-worktree-path-bytes.md plans/README.md` → only documented predecessor changes; no unexplained drift.
- Injection demonstrated pre-fix: `git worktree add` at a path containing `\nworktree /fake\nHEAD deadbeef` made the legacy listing emit a forged `worktree /fake` record with a fabricated `HEAD` — the exact truncation+injection this plan removes.
- Implementation: `Worktrees()` invokes `git worktree list --porcelain -z` with stdout/stderr separated (`worktreeListZ`). Success → `parseWorktreesZ` (NUL-terminated attributes, empty attribute = record boundary; validates structural fields, refuses unknown attributes/duplicated inline records/HEAD-less non-bare records; `locked`/`prunable` annotations handled without becoming records). Failure → fallback only on exit 129 + C-locale `unknown option` stderr (`unsupportedWorktreeListZ`); any other error propagates untouched (`TestWorktreesDoesNotFallbackOnError`).
- Guarded legacy path (`worktreesLegacy`): `checkLegacyWorktreeMetadata` reads `rev-parse --git-common-dir` via raw `run` (exactly one trailing newline stripped, no TrimSpace), refuses CR/LF in the common dir or the derived main-worktree path (`<dir>/.git` suffix required — separate-gitdir layouts get an actionable unsupported-layout error), reads every `worktrees/*/gitdir` registration (strip exactly one record newline; CR/LF or unreadable → refusal), then strict `parseWorktreesLegacy` (blank-separated records only, no inline `worktree` lines, unknown lines rejected) plus a record-count cross-check against registration count.
- Tests: `TestWorktreesPreservesPathBytes` (real newline/tab/trailing-space/quote/UTF-8/embedded-fake-record worktree paths round-trip byte-exact), `TestParseWorktreesZ` + `TestParseWorktreesZMalformed`, `TestParseWorktreesLegacyMalformed` (incl. field-looking split fragments), `TestWorktreesLegacyFallback` + `TestWorktreesDoesNotFallbackOnError` + `TestWorktreesLegacyGuardRejectsUnsafeMetadata` via a PATH git-shim logging every call. Fuzz targets adapted to error-returning parsers; `FuzzParseWorktreesZ` added.
- e2e `TestWorktreeNewlinePathRoundTrip`: newline+tab+quote+UTF-8 worktree path round-trips exact bytes through `worktree ls --json` and the `log --json` ownership annotation; terminal output escapes the control bytes.
- Docs: AGENT.md's worktree section documents the compatibility boundary (newline-path worktrees need git ≥ 2.36; ordinary legacy worktrees keep working); CHANGELOG entry under `### Fixed`.
- Legacy-git runtime note: the 129/unknown-option fallback was verified through a deterministic PATH shim that reproduces the C-locale diagnostic byte-for-byte; no actual git 2.17–2.35 runtime was available locally — the guard's refusal path (not silent success) is what protects such repos, so worst case is an actionable error, not corrupted data.
- Verification: `go test ./internal/git -run 'Test.*(Worktree|ParseWorktrees)' -count=1` → ok; `go test ./e2e -run '^TestWorktreeNewlinePathRoundTrip$'` → ok; `make test-fast` → ok; `make test` → ok; `make e2e` → ok; `make build` → ok; `make fmt-check`/`make lint` (v2.12.2) → 0 issues.
- `make ci` on commit `0486ebf` in detached worktree `/private/tmp/st-ci-012` → exit 0 (lint clean, native/windows/plan9 vet, build, race tests, e2e, merged coverage 87.1% ≥ 75%, per-function floor holds).
- `git diff --check` → exit 0; files all in Scope (`internal/git/git.go`, `internal/git/git_test.go`, `internal/git/fuzz_test.go`, `e2e/e2e_journey_test.go`, `docs/AGENT.md`, `CHANGELOG.md`, this plan, `plans/README.md`).

## Why this matters

Worktree paths can contain newline characters on Unix. Line-based parsing can truncate such paths or interpret part of a path as another record. Use NUL framing on capable Git versions, and explicitly refuse ambiguous legacy cases while preserving the documented Git 2.17 floor.

## Current state

`internal/git/git.go:858` obtains trimmed, line-framed output:

```go
out, err := Run("worktree", "list", "--porcelain")
```

`parseWorktrees` at line 870 loops over `strings.Split(out, "\n")` and recognizes fields with `strings.Cut(line, " ")`. Existing callers trust Worktree.Path for ownership, navigation and removal. Use raw `run`, not Run, for byte-preserving output.

Compatibility evidence: [Git 2.17 worktree source](https://raw.githubusercontent.com/git/git/v2.17.0/builtin/worktree.c) writes raw paths followed by newline and has no -z list option. [Git 2.36 worktree source](https://raw.githubusercontent.com/git/git/v2.36.0/builtin/worktree.c) supports NUL field/record termination. A strict newline parser alone cannot prove that a newline-containing legacy path did not forge otherwise valid fields.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Keep the Worktree struct/JSON shape and branch-prefix stripping. Extend existing Worktrees/parseWorktrees tests in internal/git/git_test.go and parser fuzz seeds in internal/git/fuzz_test.go. Do not silently upgrade the Git floor or infer paths by shell unquoting.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./internal/git -run 'Test.*(Worktree\|ParseWorktrees)' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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
- `internal/git/fuzz_test.go`
- `e2e/e2e_journey_test.go`
- `docs/AGENT.md`
- `CHANGELOG.md`
- `plans/012-preserve-worktree-path-bytes.md`
- `plans/README.md`


**Out of scope:** Git minimum-version increase, changing public worktree JSON keys, automatic repair/prune, a general Git-directory registry replacement.

## Git workflow

Use a separate branch/worktree named `advisor/012-preserve-worktree-path-bytes` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: preserve worktree path bytes in porcelain parsing`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Add byte-preservation and capability tests

Add TestWorktreesPreservesPathBytes using a real linked worktree whose Unix path contains a newline, tab, trailing space, quote and non-ASCII character. Skip only filesystem/platform-incompatible path cases, recording why. Assert the returned path bytes exactly equal the canonical fixture path; do not TrimSpace in test helpers. Add TestParseWorktreesNUL with canned NUL parser records for detached/bare/locked/prunable/multiple worktrees and embedded field-looking text. Add TestWorktreesLegacyFallback using a Git wrapper that returns exit 129 with the C-locale unsupported -z diagnostic, and TestWorktreesDoesNotFallbackOnError for an unrelated failure that must never trigger fallback.

**Verify:** `go test ./internal/git -run '^TestWorktreesPreservesPathBytes$' -count=1` → the original parser fails exact path preservation on a Unix fixture

### Step 2: Implement NUL framing without trimming

Invoke raw `git worktree list --porcelain -z`; parse NUL-terminated attributes and the empty attribute between records. Preserve all bytes after `worktree ` and strip only protocol delimiters. Validate structural fields rather than silently accepting incomplete records; handle optional annotations without treating their contents as a new worktree. Return errors through Worktrees on malformed output. Adapt fuzz targets if the parser now returns errors. Separate stdout/stderr enough to recognize a specific unsupported-option failure with LC_ALL=C for the probe; ordinary command errors must propagate without fallback.

**Verify:** `go test ./internal/git -run '^Test(ParseWorktreesNUL|WorktreesPreservesPathBytes|WorktreesDoesNotFallbackOnError)$' -count=1` → NUL parser, real-path round trip and unrelated-error propagation pass; the legacy fallback regression is completed in Step 3

### Step 3: Guard the older-Git fallback against ambiguous paths

On the recognized unsupported -z error only, retain a strict legacy line parser for ordinary paths. Before trusting it, read the repository's common Git directory via raw rev-parse output, removing exactly its final output newline instead of TrimSpace, and inspect raw `worktrees/*/gitdir` registration files. These files store each linked path ending in `/.git` plus a single record newline; remove only that final record newline. If the common directory or any registered path contains CR/LF, or a registration cannot be read/validated, return an actionable unsupported-path/metadata error before exposing a parsed ownership map. This conservative guard intentionally refuses newline-path repositories on legacy Git. Reject duplicate/incomplete/unknown legacy structural lines; preserve ordinary spaces/tabs as actual path bytes. Do not attempt to reconstruct embedded newlines from the listing. Add legacy fixtures that contain field-looking newline path fragments and assert refusal even when the listing superficially resembles valid records.

**Verify:** `go test ./internal/git -run 'Test.*(Worktree|ParseWorktrees)' -count=1` → legacy ordinary-path listing succeeds; ambiguous registration paths, unreadable registrations and unrelated Git failures return explicit errors without mutation

### Step 4: Verify navigation and document the compatibility boundary

Add TestWorktreeNewlinePathRoundTrip to e2e/e2e_journey_test.go on supported Unix systems, exercising worktree list JSON and a read-only branch ownership/navigation result. Confirm raw JSON decodes to the correct path and terminal output still sanitizes control characters. Record a manual/CI compatibility check using actual Git 2.17 or a maintained older-Git environment; the wrapper is a deterministic test of fallback logic, not proof of old-Git runtime compatibility. Document that newline-path worktrees require NUL-capable Git, while ordinary legacy worktrees remain supported.

**Verify:** `go test ./e2e -run '^TestWorktreeNewlinePathRoundTrip$' -count=1` → PASS on NUL-capable Git; `make ci` passes and actual legacy-Git evidence or the precise unverified limitation is recorded

## Test plan

- NUL parser: arbitrary path whitespace/control delimiters, detached/bare/locked records, multiple worktrees, malformed/incomplete records and fuzz seeds.
- Capability fallback triggers only on the expected unsupported-option error, never permissions or repository errors.
- Legacy registry guard rejects CR/LF paths, unreadable registrations and field-looking injected path fragments.
- JSON and navigation use the exact path bytes; terminal rendering remains sanitized.

## Done criteria

- [ ] The modern Git round-trip regression preserves exact path bytes.
- [ ] No Worktrees path goes through strings.TrimSpace or newline splitting on the modern path.
- [ ] Legacy ordinary paths work, and ambiguous paths fail before a caller receives an ownership map.
- [ ] `make ci` passes; old-Git runtime verification is explicitly recorded rather than inferred from a shim.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The registry guard cannot establish the legacy path precondition for a supported layout such as separate Git directories; report that layout and revise the compatibility design instead of guessing.
- The plan would require silently dropping a worktree record or changing minimum Git version.
- A caller ignores a new Worktrees parse error and proceeds with destructive operations; identify the caller before broadening scope.

## Maintenance notes

Keep framing and path presentation separate. The legacy guard is a compatibility boundary, not a complete registry implementation; prefer Git's own NUL output whenever supported.

# Plan 013: Print safe copyable navigation commands for ordinary paths

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- cmd/shell.go cmd/sanitize.go cmd/shell_test.go cmd/sanitize_test.go cmd/commands_nav_test.go e2e/e2e_contract_test.go CHANGELOG.md plans/013-quote-manual-navigation-hints.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P3
- **Effort:** S
- **Risk:** LOW
- **Depends on:** 001 recommended; 012 is complementary but not required
- **Category:** dx
- **Audit item:** 13
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

Without a shell shim, stacked prints `run: cd <path>`. Spaces and shell metacharacters make that command incorrect or executable as unintended syntax when pasted. Quote ordinary paths once; for paths containing terminal controls, keep the display safe and avoid offering a misleading executable command.

## Current state

`cmd/shell.go:145` currently interpolates raw destination text:

```go
return fmt.Sprintf("%s is in worktree %s\nrun: cd %s", branch, dest, dest)
```

`cmd/sanitize.go:115`, teleportHintForTerminal, repeats this using sanitized path text. Sanitizing controls is a display operation, not shell quoting, and escaped display text may not name the original directory. Shell-shim CD directives already have their own mechanism and must remain unchanged.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Use one small private quoting/hint helper shared by terminal and raw-summary paths where appropriate. Match existing sanitize tests and navigation tests in cmd/shell_test.go, cmd/sanitize_test.go and cmd/commands_nav_test.go. Preserve raw structured path fields and JSON escaping.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./cmd -run 'Test.*(Shell\|Teleport\|Nav\|Sanitize)' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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

- `cmd/shell.go`
- `cmd/sanitize.go`
- `cmd/shell_test.go`
- `cmd/sanitize_test.go`
- `cmd/commands_nav_test.go`
- `e2e/e2e_contract_test.go`
- `CHANGELOG.md`
- `plans/013-quote-manual-navigation-hints.md`
- `plans/README.md`


**Out of scope:** shell-shim protocol, command execution by stacked, changing raw JSON path values, Windows shell integration, changing branch names or paths.

## Git workflow

Use a separate branch/worktree named `advisor/013-quote-manual-navigation-hints` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: quote manual worktree navigation hints`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Test pasted commands against actual directories

Add TestTeleportHintQuotedPath in cmd/shell_test.go. Create temporary directories containing spaces, apostrophes, dollar signs, semicolons, backticks and parentheses, using literal Go strings. Extract the offered command and run it in a disposable supported shell, then compare PWD bytes with the expected directory. Include a metacharacter sentinel and prove no extra command executes. Add control-character paths in sanitize_test.go; they must have safe display output and no copyable `run: cd` line. Existing simple-path exact-string assertions should be updated only after the expected quoting policy is implemented.

**Verify:** `go test ./cmd -run '^TestTeleportHintQuotedPath$' -count=1` → ordinary paths with spaces/metacharacters fail with the original unquoted hint

### Step 2: Separate display escaping from shell argument quoting

For paths without terminal control characters, emit `cd -- '<path>'`, replacing each literal apostrophe with the standard close-quote, escaped-apostrophe, reopen-quote sequence. Quote the original destination, not sanitizeForTerminal's escaped display string. Verify this spelling in bash, zsh and fish where available. For a path containing characters that terminal sanitization would escape, omit the executable suggestion and provide a brief direction to use the shell integration or the structured worktree path instead. Keep the branch/path display sanitized in terminal output, and preserve raw JSON fields. Share the command construction so teleportHint and teleportHintForTerminal cannot drift.

**Verify:** `go test ./cmd -run 'Test.*(Shell|Teleport|Nav|Sanitize)' -count=1` → ordinary path commands reach the exact directory with no sentinel execution; control-character cases display safely and offer no false command

### Step 3: Verify public navigation output and document the fix

Update affected exact navigation-summary expectations within the scoped tests. Add TestManualNavigationQuotedPath as a subprocess e2e contract case for manual navigation without the shell shim, keeping actual path fields unchanged. Run the existing shell integration tests to ensure CD directives are unaffected. Add a brief changelog entry.

**Verify:** `go test ./e2e -run '^(TestManualNavigationQuotedPath|TestNavigationEdges)$' -count=1` → navigation and shim contracts pass; then `make ci` passes

## Test plan

- Round-trip actual paths with spaces, apostrophes, UTF-8 and shell metacharacters through the offered command.
- A sentinel proves command substitutions/separators in paths are inert.
- CR/LF/tab/escape and other sanitized controls have no executable cd suggestion, while JSON path values remain exact.
- Bash is required where the shell tests already require it; optional zsh/fish cases skip explicitly when unavailable and must be exercised in a suitable CI/manual environment before claiming all-shell verification.

## Done criteria

- [ ] The ordinary-path hint executes only cd and reaches the intended path.
- [ ] Terminal-control paths never produce a sanitized-but-incorrect cd command.
- [ ] Shell-shim protocol tests pass unchanged in behavior.
- [ ] `make ci` passes; tested shell availability is recorded.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- A proposed quote sequence is not accepted by one of the supported shells; do not silently claim portability.
- Fixing manual hints appears to require altering CD directives or raw JSON fields.
- A regression actually executes user-provided shell text outside a disposable test fixture.

## Maintenance notes

Display sanitization and shell quoting solve different problems. Future navigation summaries should reuse the quoting helper and should not reintroduce raw path interpolation after `run:`.

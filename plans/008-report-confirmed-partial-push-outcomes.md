# Plan 008: Report every confirmed push outcome without retrying the batch

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- internal/git/git.go internal/git/git_test.go cmd/submit.go cmd/commands_json_test.go e2e/e2e_contract_test.go docs/AGENT.md CHANGELOG.md plans/008-report-confirmed-partial-push-outcomes.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P1
- **Effort:** M
- **Risk:** LOW
- **Depends on:** 001 recommended for remote fixture isolation
- **Category:** bug
- **Audit item:** 7
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

One non-atomic push can update A and C while rejecting B. Submit retries individually after the batch error, stops at B, and reports only A, even though C is already published. Read the first push's per-ref results and report confirmed outcomes in stack order without issuing another network mutation.

## Current state

`cmd/submit.go:110` retries the failed batch:

```go
if err := git.PushBranches(remote, stackBranches, true); err != nil {
    pushed, err = pushSubmitBranchesIndividually(remote, stackBranches, asJSON)
```

The fallback at line 159 stops on the first error. `internal/git/git.go:1357`, PushBranches, returns only error:

```go
args := []string{"push", "-u"}
// ... adds --force-with-lease and one explicit refspec per branch
_, err := Run(args...)
return err
```

The JSON contract is submitResult{remote,dryRun,pushed,failed?,repoURL?,prHints?,summary?}. Existing TestSubmitPartialFailureJSON uses a pre-receive hook, which does not establish independent per-ref acceptance.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Keep login-free operation, explicit refs/heads refspecs, upstream tracking, --force-with-lease and --remote. On errors JSON stdout may contain a partial submit result while the existing error envelope/exit code remains on stderr. Never mix raw push output into JSON stdout.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Focused regression | `go test ./cmd -run '^TestSubmit' -count=1` | exit 0 after the implementation; Step 1 may specify an intentional failing test |
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
- `cmd/submit.go`
- `cmd/commands_json_test.go`
- `e2e/e2e_contract_test.go`
- `docs/AGENT.md`
- `CHANGELOG.md`
- `plans/008-report-confirmed-partial-push-outcomes.md`
- `plans/README.md`


**Out of scope:** atomic push as a new policy, forge integration, submit/restack concurrency redesign, retrying uncertain pushes, changing public JSON field names.

## Git workflow

Use a separate branch/worktree named `advisor/008-report-confirmed-partial-push-outcomes` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `fix: report confirmed per-ref submit outcomes`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Create a genuine non-prefix partial push

Add TestSubmitReportsNonPrefixPartialPush in cmd/commands_json_test.go. Use a local bare remote with an update hook that accepts A and C but rejects B. Submit a three-branch stack. Assert remote A/C refs exist, B does not, JSON pushed equals [A,C] in input order, failed is B, and the command returns an error. Instrument the Git push invocation with a temporary wrapper or the existing subprocess-test approach and assert exactly one push. The hook must use only temporary local repositories and must never contact a real remote.

**Verify:** `go test ./cmd -run '^TestSubmitReportsNonPrefixPartialPush$' -count=1` → the original code fails outcome completeness and/or the single-push assertion

### Step 2: Capture machine-readable per-ref results from the single push

Change PushBranches to return a typed result plus error, with one status for each confirmed requested destination ref. Invoke `git push --porcelain -u` with existing force/remote/refspec arguments. Capture stdout separately from stderr using the existing Git environment/error conventions; do not use a whitespace-trimming wrapper because a successful status flag can be a space. Parse tab-delimited porcelain records, map full destination refs back to requested branch names, and accept documented success/up-to-date flags. A rejection is confirmed failure. Preserve parse/transport errors and mark missing or malformed results unconfirmed; never infer success just because the process exited, and never expose credentials from stderr. Unit tests should cover successful, up-to-date, rejected, unrelated, duplicate and incomplete status records. Adapt existing direct callers/tests to the new return value.

**Verify:** `go test ./internal/git -run 'Test.*(Push|PushPorcelain)' -count=1` → all existing push behavior and new status parser cases pass, preserving -u and --force-with-lease

### Step 3: Render complete confirmed results and remove fallback retries

Replace pushSubmitBranchesIndividually with rendering of the original typed result. Populate pushed with all confirmed successful/up-to-date requested branches, in stack order. Set optional failed to the first confirmed rejected branch in that order. If transport/parse failure leaves outcomes unconfirmed and none is a confirmed rejection, omit failed and explain the uncertainty in the returned error; do not falsely claim a particular ref failed or succeeded. Keep pushed a nonnil array and retain the established error exit/envelope. Preserve dry-run and successful PR hint behavior. Update the failed-field comment and docs to remove the assumption that successful pushes form a prefix. Add e2e contract coverage and changelog text.

**Verify:** `go test ./cmd -run '^TestSubmit' -count=1` → all submit cases pass, including A/C success around B rejection and single invocation; then `make ci` passes

## Test plan

- A/C accepted with B rejected, first branch rejected, multiple rejections, all up-to-date and all successful.
- Fatal transport failure before statuses and incomplete/malformed status streams report only confirmed outcomes and no fabricated failed branch.
- Assert remote refs directly, stdout JSON keys/arrays, stderr error envelope, selected remote, sanitized URLs and exactly one push.
- Keep all existing dry-run/PR-hint tests; do not loosen strict JSON checks to accommodate accidental extra fields.

## Done criteria

- [ ] TestSubmitReportsNonPrefixPartialPush proves remote A/C and JSON [A,C] agree.
- [ ] No submit fallback performs individual retry pushes.
- [ ] Missing status data is reported as uncertainty, never silently filled with guessed successes.
- [ ] `make ci` passes and documented JSON keys remain compatible.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The proposed parser relies on localized human summary text instead of porcelain status/ref fields.
- A subprocess helper combines or trims output in a way that destroys status records; adapt within the scoped Git file first.
- Correctness appears to require a second mutating push or pretending an indeterminate network outcome is known.

## Maintenance notes

Per-ref outcomes and transport success are different facts. Future retry policies must be explicit and must reconcile already-published refs before another mutation.


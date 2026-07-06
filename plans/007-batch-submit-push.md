# Plan 007: Batch successful `st submit` while preserving partial-failure JSON

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report -- do not improvise. When done, update the status row for this plan
> in `plans/README.md` -- unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 0a6bf83..HEAD -- cmd/submit.go internal/git/git.go internal/git/git_test.go cmd/commands_json_test.go e2e/e2e_contract_test.go`
> If any in-scope file changed since this plan was refreshed, compare the
> "Current state" excerpts against the live code before proceeding. In
> particular, the `submitResult.Failed` contract and its partial-failure tests
> must still exist; if they do not, STOP and report.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED (push failure semantics are user-visible)
- **Depends on**: none; refreshed after plans 004, 006, 009, and 022 landed
- **Category**: perf / correctness
- **Planned at**: commit `0a6bf83`, 2026-07-05

## Why this matters

On the successful path, `st submit` still runs one `git push` per branch in the
stack. A K-branch stack pays K network round trips to the remote, even though
`git push -u --force-with-lease <remote> <refspec>...` can publish the whole
stack in one invocation. The original plan only optimized for success and is
now stale: current `submit --json` deliberately reports partial push state via
`submitResult.Failed`, and a naive multi-ref push can break that contract when a
remote hook rejects the batch. This plan keeps the one-spawn success path, then
falls back to the existing per-branch push loop after a failed batch so JSON
consumers still see the branches pushed before the failing branch.

## Current state

- `cmd/submit.go` defines the JSON contract:

  ```go
  type submitResult struct {
      Remote  string   `json:"remote"`
      DryRun  bool     `json:"dryRun"`
      Pushed  []string `json:"pushed"`
      RepoURL string   `json:"repoURL,omitempty"`
      Summary string   `json:"summary,omitempty"`
      // Failed names the branch whose push failed; set only on a partial failure,
      // alongside the branches that were pushed before it (in Pushed).
      Failed string `json:"failed,omitempty"`
  }
  ```

- `cmd/submit.go` still pushes branches one at a time:

  ```go
  pushed := []string{}
  for _, name := range stackBranches {
      if dryRun {
          pushed = append(pushed, name)
          if !asJSON {
              out("would push %s\n", name)
          }
          continue
      }
      if err := git.PushRemote(remote, name, true); err != nil {
          if asJSON {
              _ = emit(true, submitResult{Remote: remote, Pushed: pushed, Failed: name}, func() {})
          }
          return fmt.Errorf("pushing %q (pushed %d of %d): %w", name, len(pushed), len(stackBranches), err)
      }
      pushed = append(pushed, name)
      if !asJSON {
          out("pushed %s\n", name)
      }
  }
  ```

- `internal/git/git.go` already has both the single-branch helper and the
  multi-branch helper. The missing piece is that `cmd/submit.go` still does
  not use `PushBranches` on the successful path.

  ```go
  func PushRemote(remote, branch string, force bool) error {
      if err := validRefArg("remote", remote); err != nil {
          return err
      }
      args := []string{"push", "-u"}
      if force {
          args = append(args, "--force-with-lease")
      }
      refspec := "refs/heads/" + branch + ":refs/heads/" + branch
      args = append(args, remote, refspec)
      _, err := Run(args...)
      return err
  }

  func PushBranches(remote string, branches []string, force bool) error {
      if err := validRefArg("remote", remote); err != nil {
          return err
      }
      if len(branches) == 0 {
          return nil
      }
      args := []string{"push", "-u"}
      if force {
          args = append(args, "--force-with-lease")
      }
      args = append(args, remote)
      for _, branch := range branches {
          if err := validRefArg("branch", branch); err != nil {
              return err
          }
          args = append(args, "refs/heads/"+branch+":refs/heads/"+branch)
      }
      _, err := Run(args...)
      return err
  }
  ```

- `cmd/commands_json_test.go` pins the partial-failure contract:
  `TestSubmitPartialFailureJSON` rejects `feat-b` and expects
  `Failed == "feat-b"` with `Pushed == []string{"feat-a"}`.
  `TestSubmitPartialFailureJSONFirstBranchKeepsPushedArray` rejects the first
  branch and expects `Failed == "feat-a"` with a present-but-empty `Pushed`.
- `e2e/e2e_contract_test.go` has `TestSubmitRealPushSetsUpstream`, which builds
  a two-branch stack, runs `st submit`, checks both remote refs exist, and
  checks both local branches have upstreams. Preserve that behavior.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Wrapper tests | `go test ./internal/git -run 'Push|FlagLike' -count=1 -v` | exit 0 |
| Submit JSON contract | `go test ./cmd -run TestSubmit -count=1 -v` | exit 0 |
| Submit e2e | `go test ./e2e -run TestSubmit -count=1 -v` | exit 0 |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**:

- `internal/git/git.go` / `internal/git/git_test.go` -- keep the existing
  multi-branch helper and its ref-arg validation intact; only adjust if live
  drift requires it.
- `cmd/submit.go` -- use the batch helper on the successful path and keep the
  per-branch fallback on failure.
- `cmd/commands_json_test.go` -- keep existing partial-failure tests passing;
  extend only if the new fallback needs a stronger assertion.
- `e2e/e2e_contract_test.go` -- keep or extend the two-branch upstream test.

**Out of scope**:

- Changing the `submitResult` struct or any JSON key.
- Parsing `git push --porcelain` output.
- Changing dry-run output or remote URL rendering.
- Moving submit into the stack engine or remote port.
- Adding retries, atomic push mode, or host API behavior.

## Git workflow

- Branch: `batch-submit-push`
- Commit message style: `perf: batch successful submit pushes`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Confirm `git.PushBranches` still matches the needed contract

Before changing submit, confirm the existing helper still:

- validates `remote` with `validRefArg("remote", remote)`
- validates every branch with `validRefArg("branch", branch)` before exec
- returns `nil` for an empty branch slice
- builds one command:
  `git push -u [--force-with-lease] <remote> refs/heads/<b1>:refs/heads/<b1> ...`
- uses the existing `Run(args...)` wrapper so stderr is preserved in errors

**Verify**: `go test ./internal/git -run 'Push|FlagLike' -count=1 -v` -> PASS.

### Step 2: Use one push on successful submit

In `cmd/submit.go`, keep the dry-run loop exactly as a no-push path. For the
non-dry-run path, call `git.PushBranches(remote, stackBranches, true)` once.
If it succeeds:

- set `pushed` to all `stackBranches`
- keep the existing per-branch text output (`pushed feat-a`, `pushed feat-b`)
- keep the final summary and repo URL behavior unchanged

Do not emit any JSON before the successful final `emit`.

**Verify**: `go test ./e2e -run TestSubmitRealPushSetsUpstream -count=1 -v` -> PASS.

### Step 3: Preserve partial-failure JSON with a fallback

If the batched push fails, run the existing single-branch push behavior as a
classification fallback:

- push `stackBranches` again one by one with `git.PushRemote(remote, name, true)`
- accumulate `pushed` in the same order as the current code
- on the first failing branch, emit the same partial JSON payload in `--json`
  mode: `submitResult{Remote: remote, Pushed: pushed, Failed: name}`
- return an error that still names the failing branch and includes the
  `pushed N of M` count

Why this fallback is intentional: a failed multi-ref push may reject the entire
batch (for example a `pre-receive` hook that exits non-zero after seeing
`feat-b`), while the previous contract reports that `feat-a` was already pushed
before `feat-b` failed. Retrying one-by-one on the failure path preserves that
contract. It is acceptable that the rare failure path does extra work; the
success path is the performance target.

**Verify**: `go test ./cmd -run TestSubmitPartialFailureJSON -count=1 -v` -> PASS.

### Step 4: Re-run the submit contract gates

Run:

- `go test ./cmd -run TestSubmit -count=1 -v`
- `go test ./e2e -run TestSubmit -count=1 -v`

Both must pass. Confirm `cmd/commands_json_test.go` still uses
`json.Decoder.DisallowUnknownFields()` for the submit JSON tests; do not loosen
schema checks.

### Step 5: Full gate

Run `make ci`.

**Verify**: `make ci` exits 0.

## Test plan

- `internal/git/git_test.go`: existing coverage for `PushBranches` must keep
  proving a single helper can push two refs and refuses flag-like remote/branch
  values before exec.
- Existing `cmd/commands_json_test.go` partial-failure tests must pass
  unchanged or with only stricter assertions. These tests are the guardrail
  against the stale "one push only" plan breaking `submitResult.Failed`.
- Existing `e2e/e2e_contract_test.go::TestSubmitRealPushSetsUpstream` must keep
  proving two remote refs exist and both local branches have upstreams.

## Done criteria

- [x] `internal/git/git.go` contains `PushBranches` and it validates every
  remote/branch argument with `validRefArg`.
- [ ] `cmd/submit.go` has no successful-path loop that calls
  `git.PushRemote` once per branch; `PushRemote` remains available for the
  failure fallback.
- [ ] `go test ./internal/git -run 'Push|FlagLike' -count=1 -v` passes.
- [ ] `go test ./cmd -run TestSubmit -count=1 -v` passes, including both
  partial-failure JSON tests.
- [ ] `go test ./e2e -run TestSubmit -count=1 -v` passes.
- [ ] `make ci` exits 0.
- [ ] No files outside the in-scope list are modified; `plans/README.md` row
  updated by the reviewer if this was dispatched.

## STOP conditions

- The existing partial-failure JSON tests cannot be preserved without changing
  `submitResult`, removing `Failed`, or loosening schema checks.
- `git push -u` with multiple refspecs does not set upstream for every branch
  on the local Git version or CI Git versions.
- The fix requires parsing `git push --porcelain` or introducing remote-specific
  behavior. That may be a valid future design, but it is outside this plan.
- A step's verification fails twice after a reasonable fix attempt.

## Maintenance notes

- The success path should be one network round trip. The failure path is
  deliberately conservative and may perform the old per-branch sequence after a
  failed batch so machine consumers retain accurate partial-state reporting.
- If future work wants exact status from a single failed multi-ref push, plan it
  separately around `git push --porcelain` and remote hook semantics; do not
  mix that parser into this small performance plan.

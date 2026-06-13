# Plan 007: Push the whole stack in one `git push` instead of one per branch

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- cmd/submit.go internal/git/git.go e2e/e2e_contract_test.go`
> PR #32 (ref-name hardening) also edits `internal/git/git.go`; if it has
> merged, expect a `validRefArg` guard in `PushRemote` — that is known drift,
> incorporate it (call the same guard in the new function). Any *other*
> mismatch with the excerpts below is a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW-MED (error attribution changes; push semantics per-refspec)
- **Depends on**: best after PR #32 merges (same file)
- **Category**: perf
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

`st submit` on a K-branch stack runs K separate `git push` invocations — K
full network round trips (auth + ref advertisement each time), the slowest
loop in the tool: typically 0.5–2s per push against a real remote. `git push`
accepts multiple refspecs in one invocation, with `--force-with-lease` and
`-u` applying to all of them, so one spawn does the whole stack.

## Current state

- `cmd/submit.go` (~lines 98–113) — the push loop:

  ```go
  for _, name := range stackBranches {
      if !dryRun {
          if err := git.PushRemote(remote, name, true); err != nil {
              return fmt.Errorf("pushing %q: %w", name, err)
          }
      }
      if !asJSON {
          if dryRun { out("would push %s\n", name) } else { out("pushed %s\n", name) }
      }
  }
  ```

  `stackBranches` is built bottom-up from `state.Ancestors(cur)` plus `cur`
  (lines ~84–97). Per-branch text output ("pushed X") is part of the current
  UX; the JSON shape is `submitResult` (`cmd/submit.go:24–30`, fields
  `Remote/DryRun/Pushed/Summary/URL`-ish — read the struct).
- `internal/git/git.go` (~lines 383–395) — `PushRemote`:

  ```go
  func PushRemote(remote, branch string, force bool) error {
      args := []string{"push", "-u"}
      if force { args = append(args, "--force-with-lease") }
      refspec := "refs/heads/" + branch + ":refs/heads/" + branch
      args = append(args, remote, refspec)
      _, err := Run(args...)
      return err
  }
  ```

- e2e coverage of submit: `e2e/e2e_contract_test.go:194`
  `TestSubmitDryRunAndURL` and `:220` `TestSubmitRealPushSetsUpstream` (pushes
  to a local bare repo and asserts upstream tracking). These pin the behavior
  the change must preserve: `-u` upstream set for **every** branch, force-with-
  lease semantics, dry-run pushes nothing.
- Behavioral note to preserve or knowingly change: today a failure reports
  the exact failing branch (`pushing "x": ...`) and earlier branches stay
  pushed. A single multi-refspec push is atomic per-refspec but reports
  errors in git's own message; the summary error should say the push failed
  and include git's stderr (which `Run` already wraps — check how `Run`
  surfaces stderr in `internal/git/git.go` top-of-file helpers).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Wrapper tests | `go test ./internal/git -count=1` | exit 0 |
| Submit e2e | `go test ./e2e -run TestSubmit -count=1 -v` | PASS |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/git/git.go` (add `PushBranches`), `cmd/submit.go`
(call it once), `internal/git/git_test.go` and/or `e2e/e2e_contract_test.go`
(coverage).

**Out of scope**: the `Remote` engine port (`internal/stack/git.go`) — submit
is a cmd-layer command and does not go through the engine; keep it that way.
The JSON result shape (`submitResult`) — must not change. `PushRemote` may
stay (delete it only if nothing else calls it — grep first).

## Git workflow

- Branch: `batch-submit-push`
- Commit message style: `perf: push the stack with one multi-refspec git push`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add `PushBranches(remote string, branches []string, force bool) error`

In `internal/git/git.go`, next to `PushRemote`, building
`push -u [--force-with-lease] <remote> refs/heads/b1:refs/heads/b1 ...` in one
`Run`. Doc comment in the file's style (why: one network round trip for the
stack). If PR #32's `validRefArg` exists in the file, validate `remote` the
same way `PushRemote` does after that PR.

**Verify**: `go build ./...` → exit 0.

### Step 2: Switch `cmd/submit.go` to one call

Replace the per-branch `git.PushRemote` with a single
`git.PushBranches(remote, stackBranches, true)` before the output loop (skip
entirely when `dryRun`). Keep the per-branch "pushed %s" text output by
printing after the one push succeeds; error message: `pushing stack: %w`.

**Verify**: `go test ./e2e -run TestSubmit -count=1 -v` → both submit tests PASS
(upstream is still set for every branch — `TestSubmitRealPushSetsUpstream`
asserts this; if it only asserts one branch, extend it to assert upstream on
at least two stack branches).

### Step 3: Cover the multi-branch shape

In `e2e/e2e_contract_test.go`, extend or add a test pushing a ≥2-branch stack
to the local bare remote and asserting both refs exist on the remote and both
locals have upstreams (`git rev-parse --abbrev-ref feat-a@{upstream}` →
`origin/feat-a`).

**Verify**: `go test ./e2e -run TestSubmit -count=1 -v` → PASS.

### Step 4: Full gate

**Verify**: `make ci` → exit 0.

## Test plan

- Extend `TestSubmitRealPushSetsUpstream` (or add a sibling) per Step 3 —
  multi-branch push, upstreams set, remote refs present.
- `TestSubmitDryRunAndURL` must pass unchanged (dry run spawns no push).

## Done criteria

- [ ] `grep -c "PushRemote(" cmd/submit.go` → 0
- [ ] `go test ./e2e -run TestSubmit -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] JSON output unchanged: `git diff cmd/submit.go` shows no `submitResult` struct edits
- [ ] Only in-scope files modified; `plans/README.md` row updated

## STOP conditions

- `-u` with multiple refspecs does not set upstream for all branches on the
  CI git version (verify with the e2e test; if git's behavior differs, report
  rather than working around).
- The fix appears to require changing the JSON shape or the engine port.

## Maintenance notes

- Error attribution coarsens deliberately (stack-level, with git's own
  per-refspec messages). If users need per-branch attribution back, parse
  git's `--porcelain` push output — deferred.
- Reviewer: check the dry-run path still spawns zero pushes.

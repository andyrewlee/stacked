# Plan 004: Reject flag-like ref names at the internal/git boundary

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- internal/git/ internal/stack/store.go e2e/`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (recommended after plan 001 so the new conflict tests guard the rebase path)
- **Category**: security
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

Branch names are passed to git as bare positional argv tokens with no `--`
separator and no leading-dash validation. A name that *looks like a flag* gets
parsed as one. The CLI-typed vector is incidentally closed (the flag
pre-scanner shunts `-`-leading tokens into flag parsing, and git itself refuses
to create `-`-leading branches), but names loaded from `.git/stacked/state.json`
and `.git/stacked/undo.json` flow to git argv unchecked. The worst site is
`git rebase`, which honors `--exec=<cmd>` — a planted state file turns
`st restack` into command execution. Reaching it requires `.git`-write access
(an attacker with that could plant hooks instead), so this is
**defense-in-depth/hardening**, matching git's own porcelain conventions — not
an open privilege break. The fix is one small validator applied at the
boundary.

## Current state

All in `internal/git/git.go` (every function builds an argv slice — there is
no shell interpolation anywhere; `Run`/`RunInteractive` use
`exec.Command("git", args...)`):

- `RebaseOnto` (line ~296): `RunInteractive("rebase", "--onto", newBase,
  oldBase, branch)` — `branch` is the RCE-class positional (`--exec`).
  `newBase`/`oldBase` are SHAs from `RevParse` (safe). `RebaseOntoQuiet`
  (line ~303) same shape.
- `Checkout` (line ~139): `Run("checkout", name)`. `CreateBranch` (~151):
  `Run("checkout", "-b", name)`. `DeleteBranch` (~157): `Run("branch", flag,
  name)`. `RenameBranch` (~476): `Run("branch", "-m", oldName, newName)`.
  `ForceBranch` (~484): `Run("branch", "-f", name, ref)`. `Fetch` (~377):
  `Run("fetch", remote)`.
- `localBranchRef` (~173–181): returns the name **unprefixed** when the branch
  doesn't exist, so `MergeBase`/`IsAncestor` can receive a raw token.
- Already-safe sites (for contrast — names embedded in a longer token can't be
  flag-parsed): `BranchExists` uses `refs/heads/<name>`; `PushRemote` builds
  the refspec `refs/heads/b:refs/heads/b` (the `remote` arg is still bare).
- Name sources that reach these functions: CLI args (guarded by
  `cmd/util.go:parseArgs`, which routes `len>1 && a[0]=='-'` tokens to flag
  parsing — see lines ~260–278), and **unguarded**: `state.json` /
  `undo.json` via the engine (`internal/stack/restack.go:51` RestackBranch →
  RebaseOnto; `internal/stack/undo_op.go` restore paths → ForceBranch /
  DeleteBranch / Checkout / RenameBranch).
- Git's own rule: no valid ref component may begin with `-`
  (`git check-ref-format` enforces this), so rejecting leading `-` cannot
  break any legitimately-created branch.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inner loop | `make test-fast` | exit 0 |
| Wrapper tests | `go test ./internal/git -count=1` | exit 0 |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope** (the only files you should modify):
- `internal/git/git.go` (the validator + call sites)
- `internal/git/git_test.go` (unit tests)
- `e2e/e2e_contract_test.go` (one black-box test)

**Out of scope**:
- `cmd/util.go` `parseArgs` — its `--` passthrough is deliberate UX; don't
  change CLI parsing.
- `internal/stack/` — the engine should stay ignorant of argv concerns; the
  guard belongs in the git wrapper it already calls.
- Adding `--` separators to every git invocation — rebase and several other
  subcommands don't accept `--` uniformly; the validator approach covers all
  sites without per-command argv research. Do not mix both approaches.
- `internal/git/remote.go` — its refs are built `refs/heads/`-prefixed or come
  from RevParse; leave it.

## Git workflow

- Branch: `git-argv-ref-hardening`
- Commit message style: `fix: reject flag-like ref names at the git boundary`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add the validator

In `internal/git/git.go`, add:

```go
// validRefArg guards branch/remote names that are passed to git as bare
// positional arguments. Git itself forbids ref components that begin with
// "-" (check-ref-format), so any such value here is either corrupt state or
// an attempt to smuggle a flag (e.g. a state.json branch named "--exec=...").
// Rejecting it before exec keeps git from parsing data as options.
func validRefArg(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if name[0] == '-' {
		return fmt.Errorf("%s name %q is not a valid git ref name", kind, name)
	}
	return nil
}
```

Match the file's comment style (the existing doc comments explain *why*, not
*what* — see `ForceBranch`'s comment as the exemplar).

**Verify**: `go build ./...` → exit 0

### Step 2: Apply it at every bare-positional site

Call `validRefArg` at the top of: `Checkout`, `CheckoutDetach` (ref),
`CreateBranch`, `DeleteBranch`, `RenameBranch` (both names), `ForceBranch`
(name), `RebaseOnto` and `RebaseOntoQuiet` (branch), `Fetch` (remote), and
`PushRemote` (remote — the branch is already refspec-embedded). In
`localBranchRef`, return an error-shaped sentinel is not possible (it returns
string), so instead validate in its two callers `MergeBase` and `IsAncestor`
before calling it. Use kind strings like `"branch"`, `"remote"`, `"ref"`.

**Verify**: `go test ./internal/git -count=1` → exit 0 (existing tests pass)
and `make test-fast` → exit 0 (engine fake unaffected).

### Step 3: Unit tests

In `internal/git/git_test.go`, following the file's existing table/temp-repo
test style, add `TestFlagLikeRefNamesRejected`: for each of
`Checkout("-x")`, `DeleteBranch("--exec=true", true)`,
`RebaseOnto("HEAD", "HEAD", "--exec=true")`, `Fetch("--upload-pack=true")`,
`ForceBranch("-b", "HEAD")`, assert an error is returned and that the error
message contains "not a valid git ref name". These must not require a remote
or network (the call must fail validation *before* exec).

**Verify**: `go test ./internal/git -run TestFlagLikeRefNamesRejected -count=1 -v` → PASS

### Step 4: Black-box test — hostile state.json

In `e2e/e2e_contract_test.go`, add `TestHostileStateBranchNameRefused`
(follow the harness conventions used by the other contract tests): build a
normal repo (`newRepo`, `initStack`, one `r.create(...)`), then overwrite
`.git/stacked/state.json` with a state whose tracked branch is named
`--exec=touch pwned` (write the JSON directly with `os.WriteFile`; copy the
shape from the README's state.json example: `{"trunk":"main","branches":{...}}`,
giving the hostile branch `"parent": "main"` and any 40-hex `parentSHA`).
Run `r.st("restack")` and assert: exit code is non-zero, stderr mentions the
invalid name, and the file `pwned` does NOT exist in the repo dir.

**Verify**: `go test ./e2e -run TestHostileStateBranchName -count=1 -v` → PASS

### Step 5: Full gate

**Verify**: `make ci` → exit 0

## Test plan

- `internal/git/git_test.go`: `TestFlagLikeRefNamesRejected` (5+ table cases,
  listed in Step 3). Pattern: the existing `git_test.go` tests.
- `e2e/e2e_contract_test.go`: `TestHostileStateBranchNameRefused` (Step 4) —
  the regression test for the actual threat shape (state-file-sourced name).
- All existing tests must pass unchanged — no legitimate branch name starts
  with `-`, so no fixture churn is expected. If a golden or fixture *does*
  change, treat it as a STOP condition.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -c "validRefArg" internal/git/git.go` ≥ 11 (1 definition + ≥10 call sites)
- [ ] `go test ./internal/git -count=1` exits 0
- [ ] `go test ./e2e -run TestHostileStateBranchName -count=1` exits 0
- [ ] `make ci` exits 0
- [ ] `git status --porcelain` shows changes only in the three in-scope files
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Any existing test fails after Step 2 — that means a legitimate caller passes
  a `-`-leading or empty value; report the caller instead of loosening the
  validator.
- The e2e hostile-state test shows the `pwned` file being created on the
  *unpatched* code and you are tempted to verify exploitability further — do
  not; the refusal test on patched code is the deliverable.
- The fix appears to require changes in `internal/stack/` or `cmd/`.

## Maintenance notes

- Any new `internal/git` function that passes an externally-sourced name as a
  bare argv token must call `validRefArg` — reviewers should check this in
  every PR touching that file.
- This validator deliberately checks only the dangerous property (leading
  dash, empty). Full `check-ref-format` validation was considered and rejected:
  git already enforces it at creation, and over-validating risks rejecting refs
  git accepts.
- Deferred follow-up: `--end-of-options` on rev-taking plumbing calls
  (`rev-parse`, `merge-base`) would harden SHA-position arguments too; not
  done here because those arguments are SHAs produced by `RevParse`, not names.

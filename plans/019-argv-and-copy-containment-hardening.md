# Plan 019: Close the two remaining input-hardening gaps (CommitSubjects ref guard, .worktreeinclude containment)

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 0a76742..HEAD -- internal/git/git.go internal/git/git_test.go cmd/worktree_copy.go cmd/worktree_test.go`
> If any of these changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: security (defensive hardening — no live exploit; see "Why")
- **Planned at**: commit `0a76742`, 2026-07-01

## Why this matters

A previous hardening pass (merged PR #32) added `validRefArg` guards so that a
corrupt or hand-edited `.git/stacked/state.json` value can never be parsed by
git as an option. That coverage is now complete except for exactly one
function: `CommitSubjects`. Separately, the `.worktreeinclude` copy machinery
relies on `git check-ignore` failing closed as its *only* path boundary — and
unlike `state.json`, `.worktreeinclude` is a tracked file that arrives with a
clone. Both gaps are honestly low-exploitability (the first needs local `.git`
write access; the second needs a locally-materialized gitignored symlink,
which a clone doesn't produce), but each is a one-guard fix that converts an
incidental boundary into an enforced one, keeping the "all external strings
are validated at the git/filesystem boundary" invariant uniform.

## Current state

### Part A — the missing ref guard

- `internal/git/git.go:820-829`:

```go
// CommitSubjects returns the subject lines of the commits in the local branch
// range base..branch, newest first.
func CommitSubjects(base, branch string) ([]string, error) {
	out, err := Run("log", "--format=%s", localBranchRef(base)+".."+localBranchNameRef(branch))
	...
}
```

- `localBranchRef` (git.go:373-381) returns `base` **verbatim** when it isn't
  `HEAD`/`refs/*` and isn't an existing branch — so a `base` beginning with
  `-` reaches `git log` as an option-shaped token. `localBranchNameRef`
  prefixes `refs/heads/`, which neutralizes `branch` as an option but still
  deserves the same validation its siblings have.
- Every sibling validates first — the convention to copy
  (`internal/git/git.go:366-371`):

```go
func RevParse(ref string) (string, error) {
	if err := validRefArg("ref", ref); err != nil {
		return "", err
	}
	return Run("rev-parse", ref)
}
```

- Sole caller: `internal/stack/engine.go:425-426` (`Squash`), passing
  `base := b.ParentSHA` — a value loaded from state.json.
- Test pattern for flag-like-ref rejection: `internal/git/git_test.go:352`
  (`t.Fatal("RevParse accepted a flag-like ref")`) — find that test and model
  the new one on it.

### Part B — copy containment

- `cmd/worktree_copy.go:62-72` — `parseIncludePatterns` accepts each
  non-comment line as a repo-root-relative path via `filepath.Clean(line)`;
  nothing rejects `..` segments or absolute paths:

```go
func parseIncludePatterns(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, filepath.Clean(line))
	}
	return out
}
```

- `cmd/worktree_copy.go:37-55` — the copy loop joins `rel` onto both roots;
  the only boundary is `isGitIgnored(srcRoot, rel)` (`git check-ignore`,
  line 77-80), which happens to fail closed for out-of-tree paths but
  evaluates *lexically*, so a path routed **through a gitignored symlinked
  directory inside the repo** can pass the gate while `os.Lstat`/`cp`
  resolve it to a file outside the repo:

```go
for _, rel := range patterns {
	src := filepath.Join(srcRoot, rel)
	if _, err := os.Lstat(src); err != nil {
		continue // listed but absent: skip rather than fail the whole create
	}
	if !isGitIgnored(srcRoot, rel) {
		continue // tracked (or not ignored): git worktree add already has it
	}
	dst := filepath.Join(dstRoot, rel)
	...
}
```

- Note: a top-level symlink entry is safe (`plainCopy` and `cp -R` recreate
  the link verbatim, git.go comment at worktree_copy.go:104-110); the gap is
  only a path whose *intermediate* component is a symlink.
- Existing copy tests to model after: `cmd/worktree_test.go:136-161`
  (writes a `.worktreeinclude`, calls `copyWorktreeIncludes(root, dst)`,
  asserts which paths copied).

Repo conventions: errors are lowercase `fmt.Errorf` with `%q` for names;
skipped manifest entries are silently `continue`d (see the two existing
`continue`s) — containment rejections should follow the same skip-don't-fail
style for consistency with "listed but absent: skip rather than fail the
whole create".

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Part A tests | `go test ./internal/git -count=1 -run CommitSubjects` | ok |
| Part B tests | `go test ./cmd -count=1 -run Worktree` | ok |
| Fast loop | `make test-fast` | ok, ~1s |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope** (the only files you should modify):
- `internal/git/git.go` (only the `CommitSubjects` function)
- `internal/git/git_test.go` (one new rejection test)
- `cmd/worktree_copy.go` (`parseIncludePatterns` + the copy loop)
- `cmd/worktree_test.go` (new containment tests)

**Out of scope** (do NOT touch):
- `validRefArg` itself and every other guarded wrapper — they are correct.
- `internal/stack/engine.go` (`Squash`) — the caller needs no change; the
  guard belongs at the git boundary.
- `reflinkCopy`/`plainCopy` — the symlink-verbatim copy behavior is
  deliberate (broken node_modules/.bin links must copy); do not dereference.
- The `.worktreeinclude` glob semantics — "full glob expansion is
  intentionally out of scope for the foundation" (worktree_copy.go:60-61).

## Git workflow

- Branch: `advisor/019-input-hardening`
- Commit style: conventional commits, e.g.
  `fix: guard CommitSubjects refs and contain .worktreeinclude copies`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Guard CommitSubjects

At the top of `CommitSubjects` (internal/git/git.go:820), add the same guards
its siblings use:

```go
func CommitSubjects(base, branch string) ([]string, error) {
	if err := validRefArg("ref", base); err != nil {
		return nil, err
	}
	if err := validRefArg("branch", branch); err != nil {
		return nil, err
	}
	out, err := Run("log", "--format=%s", localBranchRef(base)+".."+localBranchNameRef(branch))
	...
```

**Verify**: `go test ./internal/git -count=1` → ok (existing suite passes;
legitimate SHAs and branch names satisfy `validRefArg`).

### Step 2: Add the rejection test for Part A

In `internal/git/git_test.go`, find the existing flag-like-ref test around
line 352 (it asserts `RevParse` rejects such refs) and add, in the same test
or an adjacent one following the same fixture setup:

- `CommitSubjects("-x", "main")` → error, and no git process ran (the guard
  returns before `Run`).
- `CommitSubjects("abc123", "-x")` → error.

**Verify**: `go test ./internal/git -count=1 -run 'Flag|CommitSubjects'` → ok
(adjust `-run` to the actual test name you extended).

### Step 3: Reject escaping patterns in parseIncludePatterns

In `cmd/worktree_copy.go`, after `filepath.Clean(line)`, skip entries that are
absolute or contain a `..` path element:

```go
clean := filepath.Clean(line)
if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
	continue // outside the repo root: never a valid include
}
out = append(out, clean)
```

(`filepath.Clean` already collapses interior `..`, so a prefix check is
sufficient after cleaning.)

**Verify**: `go test ./cmd -count=1 -run Worktree` → ok.

### Step 4: Enforce real-path containment in the copy loop

In `copyWorktreeIncludes` (cmd/worktree_copy.go:38-55), after the `Lstat`
check succeeds and before the copy, resolve the source's parent directory and
require it to sit inside the resolved `srcRoot`:

```go
realParent, err := filepath.EvalSymlinks(filepath.Dir(src))
if err != nil {
	continue // unresolvable: skip like an absent entry
}
realRoot, err := filepath.EvalSymlinks(srcRoot)
if err != nil {
	return copied, err
}
if realParent != realRoot && !strings.HasPrefix(realParent, realRoot+string(filepath.Separator)) {
	continue // resolves outside the repo (e.g. through a symlinked dir): skip
}
```

Hoist the `realRoot` resolution above the loop (it's loop-invariant). The
top-level-symlink case still copies: for a manifest entry that *is* a symlink,
`filepath.Dir(src)` is a real in-repo directory, so containment passes and the
link is recreated verbatim as before.

**Verify**: `go test ./cmd -count=1 -run Worktree` → ok (existing
`.worktreeinclude` tests — including the broken-symlink copy test — still pass).

### Step 5: Add containment tests

In `cmd/worktree_test.go`, modeled on the existing test at lines 136-161:

1. Manifest containing `../outside.txt` (create `outside.txt` in the temp
   parent dir) → `copyWorktreeIncludes` returns without copying it; assert it
   is absent from both the returned list and `dst`.
2. Manifest containing `escape/secret.txt` where `escape` is a **gitignored
   symlink** inside the repo pointing at a directory outside it (create the
   outside dir with `secret.txt`; add `escape` to `.gitignore`; `os.Symlink`)
   → not copied.
3. Regression guard: a manifest entry that *is* a gitignored symlink (no
   traversal through it) still copies verbatim — assert the existing behavior
   the plainCopy comment promises.

**Verify**: `go test ./cmd -count=1 -run Worktree -v` → new tests listed and
passing.

### Step 6: Full gate

**Verify**: `make test-fast` → ok. Then `make ci` → exit 0.

## Test plan

- Part A: flag-like `base`/`branch` rejected before exec
  (internal/git/git_test.go, modeled on the RevParse rejection test ~line 352).
- Part B: `..` pattern skipped; through-symlink escape skipped; top-level
  symlink entry still copied (cmd/worktree_test.go, modeled on lines 136-161).
- Verification: the two package test commands above, then `make ci`.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `grep -A3 'func CommitSubjects' internal/git/git.go` shows a `validRefArg` call
- [ ] `go test ./internal/git ./cmd -count=1` exits 0, including the ≥5 new test cases
- [ ] `make ci` exits 0
- [ ] Only the four in-scope files are modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `CommitSubjects` or the copy loop no longer matches the excerpts (drifted).
- Adding the `validRefArg` guard breaks any existing test — that would mean a
  legitimate caller passes a value the guard rejects, which this plan
  believes impossible (`ParentSHA` is a hex SHA); report the failing input.
- The `EvalSymlinks` containment check breaks the existing broken-symlink
  copy test — the interaction was considered (Dir(src) is real), but if the
  test disagrees with the analysis, report rather than weaken the check.
- macOS `/tmp` symlinking (`/tmp` → `/private/tmp`) causes prefix-mismatch
  test failures: resolve **both** sides with `EvalSymlinks` in the test as the
  implementation does; if that still fails, report.

## Maintenance notes

- Any new `internal/git` wrapper that accepts a ref/branch string MUST call
  `validRefArg` (or build a `refs/…`-prefixed operand) — this plan closes the
  last known gap; reviewers should keep enforcing the pattern.
- If `.worktreeinclude` ever grows real glob support (explicitly deferred),
  the containment check in Step 4 must run on every *expanded* match, not the
  pattern.
- Follow-up deliberately not done here: `st worktree add/rm` run outside the
  advisory lock (see finding DEBT-02 in plans/README.md) — separate decision.

# Plan 018: Contain `.worktreeinclude` symlink entries — validate the entry's target, not just its parent

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/worktree_include.go cmd/worktree_copy.go internal/stack/worktree_include_fs_test.go`
> On mismatch, STOP.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW (a refusal path gets stricter; the legit symlinks that stay must keep working — pin those)
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`SelectWorktreeIncludes` (`internal/stack/worktree_include.go:245-251`) validates that a manifest entry's *parent directory* resolves inside the repo — but never `EvalSymlinks` the entry itself. An entry that IS a symlink passes: `cp -cR`/`cp --reflink=auto -R`/`plainCopy` all recreate links verbatim (`os.Readlink`+`os.Symlink`), so `.worktreeinclude` patterns like `.*` or `**` sweep the user's gitignored symlinks into the copy — absolute links keep pointing outside the new worktree, and `..`-relative links re-anchor against the destination tree to resolve to *different* files. Nothing is dereferenced at copy time (the link, not the content, is copied) so this is a containment-invariant violation rather than direct exfiltration — but the pipeline's stated goal is "resolve inside the repository", and file-level links defeat it. Directory entries additionally carry any nested symlinks inside them on both copy paths.

## Current state

- `internal/stack/worktree_include.go:231-255` — `SelectWorktreeIncludes`: `Lstat(src)` → `ignored[rel]` → `EvalSymlinks(filepath.Dir(src))` parent check — the ENTRY itself is never resolved.
- `internal/stack/worktree_include.go:341-370` — `ensureSafeDestinationDir` — destination-side containment already correct (parent chains resolved + checked).
- `cmd/worktree_copy.go:150-195` — the two copy paths: `cp` variants preserve links verbatim; `plainCopy` recreates them via `os.Readlink`+`os.Symlink` (deliberate — broken links must survive, comment at ~176-180).
- `internal/stack/worktree_include_fs_test.go` — tempdir tests; the outside-resolving-symlink arm is pinned for DESTINATION parents, not SOURCE entries.
- Convention: policy in `internal/stack` (pure/fs-bound, testable on tempdirs), byte copy in `cmd/worktree_copy.go`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Package tests | `go test ./internal/stack -run 'Include|Symlink'` | all pass |
| Copy tests | `go test ./cmd -run 'Worktree|Copy'` | all pass |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `internal/stack/worktree_include.go` (`SelectWorktreeIncludes` and a new member-walk for directory entries), `internal/stack/worktree_include_fs_test.go` + possibly a new fs-test file, `cmd/worktree_copy.go` (only if the copy paths need to consume a validated list — prefer keeping the gate in selection).

**Out of scope**: `RefuseWorktreeIncludeCollisions`/`PrepareIncludeDestination` (already correct), the manifest parser, `.gitignore` semantics.

## Git workflow

- Branch: `advisor/018-include-symlink-containment`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Validate each selected entry's own resolution

In `SelectWorktreeIncludes`, after the parent check: if `info.Mode()&os.ModeSymlink != 0`, `EvalSymlinks(src)` and require the resolved target `PathWithin(realRoot, resolved)` — OR accept a RELATIVE target that stays within the entry's subtree when re-anchored (a link `dir/x -> ./y` re-anchored at `dst/dir/x` still resolves inside — compute by joining and cleaning). Absolute-outside and escaping-relative both refuse (or skip — see Step 3 for the refuse-vs-skip decision).

```go
if info.Mode()&os.ModeSymlink != 0 {
    resolved, err := filepath.EvalSymlinks(src)
    if err != nil || !PathWithin(realRoot, resolved) {
        // refuse or skip — see Step 3
    }
}
```

**Verify**: `go build ./internal/stack` → exit 0.

### Step 2: Cover directory-entry members

A selected directory (`ignored` dir whose members get copied recursively) can contain symlinks — both `cp -R` and `plainCopy` preserve them. Add a member-walk: for each selected dir entry, `filepath.WalkDir` it; for each `ModeSymlink` member apply the same check. This is the piece both copy paths share — put it in `SelectWorktreeIncludes` (the fs-bound policy layer) so `cp -R` and `plainCopy` are covered by one check. If a nested link violates, refuse the whole entry (don't partially copy a dir).

**Verify**: `go test ./internal/stack -run 'Include'` → pass.

### Step 3: Decide refuse vs. skip

Two defensible semantics: (a) refuse the copy — loud, fails the whole `create --worktree`; (b) skip the violating link, copy the rest — friendlier but silently drops content. Match the pipeline's existing posture: `SelectWorktreeIncludes` skips unresolvable/absent entries silently ("skip, like an absent literal") while `RefuseWorktreeIncludeCollisions` refuses hazards loudly. A symlink escaping the repo is a hazard, not an absence — **refuse**, matching the collision policy's posture. If you find the pipeline deliberately skips symlinked *dirs* in `DropNestedIncludeDirs` ("keeping the conservative pre-existing behavior"), mirror that conservatism where consistent — but a file-level link escaping containment is a refusal.

**Verify**: tests assert refusal, not silent skip.

### Step 4: Tests

In `worktree_include_fs_test.go` (or a new `worktree_include_symlink_test.go`), tempdir fixtures:
- manifest selects a symlink entry → absolute target outside repo → refused (names the path).
- relative `../` escaping target → refused.
- relative in-subtree link → allowed.
- dir entry containing a nested escaping symlink → refused.
- broken symlink (target absent) — decide: `EvalSymlinks` fails on it; the copy deliberately preserves broken links (comment), so a broken link can't be proven safe — refuse it too, or treat as skip. Pick refuse-with-message (a broken link's destination semantics are unknowable).
- symlink → in-repo file → allowed and copied verbatim.

**Verify**: `go test ./internal/stack -run 'Include|Symlink'` → all pass.

### Step 5: Docs + gate

- `docs/AGENT.md`: the `.worktreeinclude` contract may already promise containment — align wording ("symlinks that resolve outside the repo are refused").
- `CHANGELOG.md` `[Unreleased]` → `Fixed`/`Security`.

**Verify**: `make ci` → exit 0 (coverage floor on the new arms).

## Test plan

- The new symlink fixtures (Step 4); pattern: existing `worktree_include_fs_test.go` tempdir tests.
- A `cmd`-level test if the refusal needs to surface in `create --worktree` output (check `cmd` worktree tests for the include-copy error path).
- Verification: `go test ./internal/stack ./cmd ./e2e` → all pass; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./internal/stack ./cmd ./e2e` exits 0
- [ ] A `.worktreeinclude` selecting a symlink with an outside-repo target is refused (manual repro or test)
- [ ] A `.worktreeinclude` selecting an in-repo symlink still copies it verbatim
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- `EvalSymlinks` on a broken link errors — decide refuse-vs-skip deliberately (recommend refuse); if the pipeline's "preserve broken links" comment is load-bearing for a real workflow, STOP and report the conflict.
- The dir-member walk becomes O(tree) expensive on huge gitignored dirs — bound it (skip nested dirs already covered by a parent selection) or STOP.
- A legit user pattern (e.g. symlinked `node_modules` into a shared cache dir outside the repo) turns out to be a documented feature — STOP and re-frame as opt-in rather than silent refuse.

## Maintenance notes

- The containment invariant now holds at three layers: manifest-path validation (parent+entry), selection (resolve check), destination (collision+symlink refuse). A reviewer should confirm all three stay aligned.
- `plainCopy`'s verbatim-link recreation is correct ONCE the selection gate exists — don't change the copier.
- Deferred: none — this closes the stated invariant.

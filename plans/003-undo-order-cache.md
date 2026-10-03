# Plan 003: Restore undo refs before saving state and refresh ownership after rebases

> **Executor instructions**: Read the whole plan, follow each step, and run its verification. Stop on the conditions below instead of extending scope. Update the index status on completion unless a dispatching reviewer owns it.
>
> **Drift check first**: `git diff --stat 159648a..HEAD -- internal/stack/undo_op.go internal/stack/undo_op_test.go internal/stack/failure_injection_test.go cmd/undo.go cmd/gitenv.go cmd/worktree_cache.go cmd/gitenv_test.go cmd/commands_mutation_test.go e2e/e2e_journey_worktree_test.go docs/AGENT.md CHANGELOG.md plans/README.md`
> Inspect uncommitted changes to these paths as well. Compare excerpts with live code before editing. This replaces the previous plan 003; do not follow its old atomic-recovery claims.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED — changes undo failure ordering and ownership-cache freshness
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `159648a`, 2026-10-02
- **Selection**: deep-audit F03, retaining the existing plan's F10 cache correction

## Why this matters

Undo currently saves the old metadata before restoring refs. A failed ref transaction therefore leaves old parentSHA values beside the newer branch tips. Separately, a rebase can move or detach HEAD while the command still uses a cached worktree owner; a later branch may be incorrectly treated as owned elsewhere. Restore the snapshot refs before saving its metadata, retain a failed undo entry for retry, and invalidate ownership after every rebase attempt.

## Current state

- internal/stack/undo_op.go:123 saves before the transaction at line 143:

```go
if s != nil {
    *s = *prev
    if s.Branches == nil {
        s.Branches = make(map[string]*Branch)
    }
}
if err := env.save(); err != nil {
    return nil, fmt.Errorf("restoring stack state: %w", err)
}
// updates and sorted names are built here
if err := g.UpdateRefs(updates); err != nil {
    return nil, fmt.Errorf("restoring branch refs: %w", err)
}
```

- Earlier Undo cleanup, lines 60–119, removes created worktrees/branches, may recreate a checkout target with UpdateRef, and moves HEAD out of the way. That cleanup is outside the atomic UpdateRefs batch. The entire operation is not transactional.
- cmd/undo.go:124 validates every selected snapshot before worktree preparation. At lines 140–152 it selects s.Save or RestoreState for a nil current state, calls Undo, and calls DropUndo **only on success**. Keep both schema barriers and this journal-retention rule.
- internal/stack/undo_op_test.go:645, TestUndoRefRestoreFailure, corrupts one recorded SHA and checks unchanged a/b tips, but never checks Save calls or metadata.
- internal/stack/engine_test.go:20, envWithSaveErr, demonstrates counted Save hooks. Engine tests use fakeGit and nil-safe Env hooks; no filesystem persistence belongs in engine logic.
- internal/stack/failure_injection_test.go:195, TestUndoSaveFailureStopsBeforeRefRestore, explicitly pins the old ordering with `f.calls["UpdateRefs"] != 0` as a failure. Replace this obsolete characterization while making the change; leaving it unchanged prevents even the focused TestUndo gate from passing.
- cmd/gitenv.go:73 provides the decorator pattern:

```go
func (c cachedPort) Checkout(name string) error {
    err := c.Git.Checkout(name)
    resetWorktreeCache()
    return err
}
```

- The actual stack.Git rebase methods in internal/stack/git.go are RebaseOnto(newBase, oldBase, branch), RebaseOntoIn(dir, newBase, oldBase, branch), RebaseContinue(), RebaseAbort(), and RebaseAbortIn(dir). There is no RebaseOntoQuiet method on this port; QuietShell implements the same port methods.
- cmd/worktree_cache.go:32 stores probed/wts behind a mutex; resetWorktreeCache clears both. cmd/worktree_test.go:21 supplies seedWorktreeCache(t, wts).
- cmd/undo.go:77 and docs/AGENT.md:278 overstate per-step atomicity. Narrow those comments/docs to the actual transaction and retry behavior.
- Constraints: standard-library-only Go 1.26, zero require entries/no go.sum; thin cmd, pure/fake-tested engine, real-Git e2e. Preserve state/journal schemas and JSON exit contracts.

## Commands you will need

- Engine regression: `go test ./internal/stack -run TestUndo -count=1` → all pass.
- Cache and real-Git adapter regression: `go test -timeout 20m ./cmd -run 'TestCachedPortRebase|TestUndo|TestWorktree' -count=1` → all pass.
- Binary ownership regression: `go test -timeout 20m ./e2e -run 'TestRestackCacheInvalidation|TestEveryE2ETestIsParallel' -count=1` → all pass.
- Inner loop: `make test-fast` → exit 0.
- Full local gate: `make ci` → exit 0.

The audit's fast/race/vet baseline passed. The full gate currently stops at absent golangci-lint v2.12.2; report missing tooling rather than claiming a full pass or changing pins. This plan does not authorize tool installation.

## Scope

**Only modify**:

- internal/stack/undo_op.go and internal/stack/undo_op_test.go
- internal/stack/failure_injection_test.go — replace the obsolete undo save-before-refs characterization only
- cmd/undo.go — inaccurate atomicity comment only
- cmd/gitenv.go
- cmd/worktree_cache.go — invalidation-site comment only
- cmd/gitenv_test.go (create)
- cmd/commands_mutation_test.go
- e2e/e2e_journey_worktree_test.go
- docs/AGENT.md, CHANGELOG.md
- plans/README.md, status only

**Do not modify**: ref CAS/OID validation, Git port signatures, undo preview algorithm/JSON fields, lock implementations, journal format, worktree removal policy, dirty-tree policy, broad subprocess caching, dependency/tool versions, or any hard-reset behavior. Recovery does not justify discarding working-tree edits.

## Git workflow

- Branch `advisor/003-undo-order-cache`; preserve unrelated changes.
- Commit by logical correction, using imperative messages such as “Restore undo refs before saving metadata” and “Invalidate worktree ownership after rebases”.
- No push, PR, merge, or release unless separately instructed.

## Steps

### Step 1: Correct undo ordering and make failures observable

Keep snapshot decoding, schema barriers, created-worktree/branch cleanup, and preparatory checkout in their existing order. Move building/sorting the UpdateRefs batch, the batch call, and its successful liveSet fold before assigning *s = *prev and env.save(). Keep final checkout after the save.

The resulting phases are: validate → cleanup/prepare → atomic snapshot-ref restore → replace in-memory metadata and save → final checkout. If the batch fails, do not assign snapshot state or call Save. Earlier cleanup may already have persisted; do not claim all refs/worktrees are unchanged.

If Save fails after the batch, leave in-memory s reflecting prev and return a wrapped error explicitly saying refs were restored but metadata saving failed and undo can be retried after fixing the save error. Preserve errors.Is for the Save sentinel. Do not roll back the successful ref transaction, pop the journal, or continue to final checkout. For s == nil, the caller's raw RestoreState hook must still run only after a successful batch.

Extend TestUndoRefRestoreFailure to capture post-modify metadata before Undo and assert byte/semantic equality afterward plus zero Save calls. The fixture contains no created-branch cleanup, so its unchanged-ref assertions are valid.

In failure_injection_test.go replace/rename TestUndoSaveFailureStopsBeforeRefRestore with TestUndoStateSaveFailureRefsAlreadyRestored. Strengthen its fixture so recorded tips actually differ from post-operation refs; the current fixture records the same refs and cannot observe restoration. Count Save, inject failure, assert UpdateRefs ran once before Save, every snapshot ref restored, in-memory metadata at the snapshot, simulated persisted metadata still at the post-operation value, then retry the same entry with a working Save and verify success. In undo_op_test.go include the nil-current-state hook case and a cleanup-plus-batch-failure case showing a created branch can already be gone while Save remains uncalled; retry skips the absent created branch. Do not retain contradictory old save-before-refs assertions or duplicate them under another name.

**Verify**: `go test ./internal/stack -run TestUndo -count=1` → all pass, with new ref-failure, save-failure, nil-state, and partial-cleanup retry assertions.

### Step 2: Prove journal retention and retry over real Git

In cmd/commands_mutation_test.go add TestUndoRefFailureRetainsEntryAndState. Follow the existing newRepo/mustInit/mustCreate/RecordUndo fixtures. Use a modify-style entry with no created branches, ensure a/b tips and metadata changed, and snapshot post-operation state.json plus undo.json bytes.

Resolve the temporary fixture's common Git directory and create refs/heads/<tracked-name>.lock exclusively, with cleanup registered, to force the UpdateRefs transaction to fail. Run undo in plain and JSON adapter subtests; assert an error, unchanged post-operation refs/state/journal, and no dropped entry. Remove only that fixture lock and retry; assert all snapshot refs/metadata restored and exactly that entry removed. Never create locks in the developer's repository. Preserve existing nil-state, multi-step, created-worktree, rename, and dirty-checkout tests.

**Verify**: `go test -timeout 20m ./cmd -run TestUndo -count=1` → all pass, including the new real-Git retry regression.

### Step 3: Invalidate after the five actual rebase methods

Add cachedPort wrappers for all five rebase methods listed in Current state, forwarding their exact arguments to c.Git, then resetWorktreeCache regardless of success/error and return the original error. Match the Checkout exemplar. This covers both gitShell and stackEnv's QuietShell-backed port without concrete-type assumptions.

Create cmd/gitenv_test.go with TestCachedPortRebaseInvalidatesOwnership. Use an embedded stack.Git test stub overriding the five methods, plus seedWorktreeCache. Table-drive each wrapper on success and a sentinel error; assert forwarded arguments, errors.Is preservation, and probed == false / wts == nil under the mutex afterward. Keep these package-global-cache tests serial. Update only the invalidation-site comment in worktree_cache.go.

**Verify**: `go test -timeout 20m ./cmd -run 'TestCachedPortRebase|TestWorktree' -count=1` → all pass; `go build ./cmd` → exit 0.

### Step 4: Pin the stale-owner consequence through the binary

Add TestRestackCacheInvalidationKeepsConflictPaused to e2e/e2e_journey_worktree_test.go, starting with t.Parallel and using per-repo subprocess helpers. Build main→a→b where main seeds a line, a adds an unrelated file, and b edits the seeded line. Create a linked worktree owning main; advance main there with a conflicting edit to that same line. Start st restack --all from b's original worktree.

Restacking a must succeed and move that worktree's HEAD; b's rebase must then conflict **in the caller's worktree**, remain paused, and return exit 2/code conflict (also cover --json in a fresh fixture). A stale cached owner of b incorrectly routes it as a foreign owner and aborts the conflict; assert rebase metadata remains in the caller's Git directory so the regression distinguishes the paths. Abort for fixture cleanup.

**Verify**: `go test -timeout 20m ./e2e -run 'TestRestackCacheInvalidation|TestEveryE2ETestIsParallel' -count=1` → all pass; the ownership regression must fail against the old wrappers.

### Step 5: Document the actual failure boundary and run the gate

Update Undo's phase comment, runUndoApply's “atomic unit” wording, and AGENT's per-step atomicity paragraph. Describe atomic snapshot-ref restoration, successful-step journal removal, retained entry on failure, possible earlier cleanup, and the refs-restored/state-save-failed retry case. Do not promise whole-operation rollback or recovery from arbitrary external Git changes. Add Unreleased Fixed entries for save ordering and rebase cache invalidation.

**Verify**: `make test-fast` and `make ci` → exit 0; `git diff --check` → no whitespace errors.

## Test plan

- fakeGit: unchanged metadata/zero Save on failed ref batch; restored refs before failed Save; retry; nil-state Save hook; earlier cleanup explicitly allowed.
- Real-Git cmd: deliberately locked ref causes atomic failure, retains exact state/journal bytes, then retry restores and pops once.
- Decorator: each of the five rebase methods invalidates on both success/error with exact argument forwarding.
- Binary: main-owner linked worktree, successful first restack, then caller-owned conflict remains paused in text/JSON modes.
- Reuse existing helpers; do not add test-only production hooks or alter the fake Git globally.

## Done criteria

- [ ] All focused commands above pass; the new TestUndoRefFailureRetainsEntryAndState, TestUndoStateSaveFailureRefsAlreadyRestored, TestCachedPortRebaseInvalidatesOwnership, and TestRestackCacheInvalidationKeepsConflictPaused exist.
- [ ] Failed UpdateRefs reaches neither state assignment nor Save.
- [ ] Failed Save reports already-restored refs, preserves the error cause, and a retry completes.
- [ ] All five rebase wrappers invalidate even on error; no nonexistent port method is added.
- [ ] `make ci` exits 0, `git diff --check` is clean, only scoped files changed, and the index is updated.

## STOP conditions

- Undo has acquired CAS validation or different cleanup phases since this HEAD; re-review rather than blindly moving blocks.
- Retrying the retained entry requires a destructive reset or changes to created-worktree policy.
- The real-Git ref-lock fixture changes no metadata/tips before undo, or the stale-owner binary fixture passes on the original wrappers; strengthen the fixture.
- A rebase wrapper needs a new Git interface method or edits outside scope.
- A verification fails twice after a reasonable correction, or required full-gate tooling is unavailable.

## Maintenance notes

Snapshot-ref transactions do not make worktree deletion, checkout, persistence, or journal trimming atomic. Review messages and tests for that distinction. Later undo CAS work must still restore refs before saving metadata and define its retry policy explicitly; preview unification and broader HEAD caching must follow those decisions. Any new HEAD-moving Git port method must invalidate ownership on error as well as success.

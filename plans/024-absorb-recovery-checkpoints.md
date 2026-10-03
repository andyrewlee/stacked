# Plan 024: Checkpoint absorb recovery commits before resets and cascades

> **Executor instructions**: Follow each step and its verification. Stop on the listed conditions rather than redesigning the journal. Update the index status when complete unless a reviewer owns it.
>
> **Drift check first**: `git diff --stat 159648a..HEAD -- internal/stack/git.go internal/stack/absorb.go internal/stack/undo.go internal/stack/absorb_test.go internal/stack/undo_test.go cmd/absorb.go cmd/commands_mutation_test.go e2e/e2e_absorb_test.go CHANGELOG.md plans/README.md`
> Inspect uncommitted changes and compare excerpts before editing.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED — introduces persistence checkpoints on partial absorb progress
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `159648a`, 2026-10-02
- **Selection**: deep-audit F06

## Why this matters

Absorb amends target commits before resetting staged copies and restacking descendants. If a later step fails, the undo entry is retained but its recovery commit map is empty, because the adapter writes that map only after complete success. Abort then undo can orphan the only committed copies of the staged edits without naming them. Record each successful amendment immediately and refresh those pointers after a successful cascade.

## Current state

- internal/stack/git.go:157 defines the nil-safe persistence seam:

```go
type Env struct {
    Git  Git
    Save func() error
}
func (e Env) save() error {
    if e.Save == nil {
        return nil
    }
    return e.Save()
}
```

- internal/stack/absorb.go:329 performs independent target amends before any resets:

```go
newTips := make(map[string]string, len(targets))
for _, target := range targets {
    newTip, err := g.AmendTipWithPatch(target, patches[target])
    if err != nil {
        return nil, fmt.Errorf("absorb into %q: %w", target, err)
    }
    newTips[target] = newTip
}
```

- It then resets foreign owners (line 344), drops caller staged copies (354), and cascades (361). Errors in any of those paths return before the adapter can annotate the entry.
- After a successful cascade, TipsFor(targets) at line 379 resolves rewritten higher targets. These final commits, rather than their earlier amend IDs, are what success should record.
- cmd/absorb.go:42 calls stack.Absorb and returns immediately on error; its SetLastUndoAbsorbed call at line 66 exists only on the success path.
- internal/stack/undo.go:216 currently silently succeeds with no journal entry:

```go
if len(entries) == 0 {
    return nil
}
entries[len(entries)-1].AbsorbedCommits = commits
return writeUndo(entries)
```

- loadUndo intentionally treats malformed JSON as an empty journal. Keep that general recovery policy, but an absorb checkpoint must not claim durability if its active entry has disappeared.
- mutateState records label “absorb” before invoking the closure; CleanupUndoOnError keeps entries when refs/state changed or a conflict remains paused. Existing annotation/trim helpers reread the journal, so they should preserve the map already checkpointed.
- Undo and UndoPreview already render absorbedCommitsNote(entry.AbsorbedCommits), including branch names, full SHAs, and cherry-pick instructions. Reuse that format; no new JSON fields or journal schema are needed.
- Test patterns: internal/stack/absorb_test.go:14 absorbEnv builds main→a→b→c; :857/:891 test owner/caller reset failures; :990 tests post-amend read/save/checkout failures. cmd/commands_mutation_test.go:834 and e2e/e2e_absorb_test.go:241 test success and abort→undo recovery respectively.
- Constraints: stdlib-only Go 1.26; no require entries/no go.sum. Engine receives persistence callbacks, never directly accesses journal files; cmd wires the existing storage helper.

## Commands you will need

- Engine and journal: `go test ./internal/stack -run 'TestAbsorb|TestSetLastUndoAbsorbed|TestUndoEntryAbsorbed' -count=1` → all pass.
- Real-Git adapters: `go test -timeout 20m ./cmd -run 'TestAbsorb|TestUndo' -count=1` → all pass.
- Binary journeys: `go test -timeout 20m ./e2e -run 'TestAbsorb|TestEveryE2ETestIsParallel' -count=1` → all pass.
- Inner loop: `make test-fast` → exit 0.
- Full local gate: `make ci` → exit 0.

The audit's fast/race/vet checks passed. make ci is currently blocked by absent golangci-lint v2.12.2; report tooling limits and do not alter pins or install a module dependency to bypass them.

## Scope

**Only modify**:

- internal/stack/git.go — optional Env callback
- internal/stack/absorb.go
- internal/stack/undo.go — SetLastUndoAbsorbed behavior/comment only
- internal/stack/absorb_test.go and internal/stack/undo_test.go
- cmd/absorb.go
- cmd/commands_mutation_test.go
- e2e/e2e_absorb_test.go
- CHANGELOG.md
- plans/README.md, status only

**Do not modify**: Git port methods, patch/blame attribution, reset policy, cascade algorithms, general mutateState/finalization helpers, loadUndo's corruption policy, undo apply/preview logic, OID/CAS handling, state/journal formats, JSON fields/exit mappings, lock policy, or dependency/tool configuration.

## Git workflow

- Branch `advisor/024-absorb-recovery-checkpoints`; preserve unrelated changes.
- Imperative messages such as “Checkpoint absorb recovery commits after each amend”.
- No push, PR, release, or remote repository mutations unless separately instructed.

## Steps

### Step 1: Add the optional engine checkpoint and failure tests

Add AbsorbCheckpoint func(map[string]string) error to Env with a comment explaining the absorb target→commit recovery map and nil-as-no-op behavior. All existing Env literals are keyed; leave unrelated constructors untouched.

In Absorb, after each successful AmendTipWithPatch and newTips update, call the optional checkpoint with a **fresh copy of the cumulative map**. A callback may retain its argument; later amends must not mutate an earlier snapshot. Complete this checkpoint before another amend, owner/caller ResetHardIn, or the cascade. On checkpoint error stop immediately, wrap the cause with %w, and include absorbedCommitsNote for the amendments that actually landed. This gives recoverable SHAs even if journal I/O failed. Do not call the hook during planning, refusals, or failures before an amendment.

After a successful cascade and final TipsFor resolution, checkpoint the resolved per-target map again, before the epilogue Save and restoreHEAD. This updates higher-target pointers rewritten by the cascade. If final tip reading fails, the earlier durable amend map remains useful. If the final checkpoint fails, keep the earlier journal annotation and report the newer known SHAs in the error; do not erase prior checkpoints.

Add TestAbsorbRecoveryCheckpoints in absorb_test.go. Use absorbEnv and the existing multi-target hunk/blame fixtures. For partial-amend failure, use a test-local embedded Git wrapper that fails the second AmendTipWithPatch; avoid editing global fake infrastructure. Cover:
1. Success with nil hook preserves existing behavior.
2. Two-target success supplies cumulative {a}, then {a,b}, then final live tips; retained earlier maps remain unchanged.
3. First/second checkpoint failure preserves errors.Is and names actual landed commit IDs; no subsequent amend/reset/cascade occurs.
4. Second amend failure retains the first successful target's checkpoint.
5. Existing owner-reset, caller-reset, cascade conflict/hard error, final-tip-read, epilogue-save, and HEAD-restore failure cases observe a nonempty last successful map containing the relevant committed edits.
6. Planning, refused plans, no staged edits, and pre-amend errors invoke the hook zero times.

For the pre-reset boundary, inspect resetHardDirs and original child ParentSHA inside the callback. For final refresh, include two targets where the cascade changes the higher target's commit ID; compare against live fake refs, not merely callback count.

**Verify**: `go test ./internal/stack -run TestAbsorb -count=1` → all pass, including nil-hook compatibility and immutable cumulative checkpoint cases.

### Step 2: Wire only absorb and require an active journal entry

Inside runAbsorb's mutateState closure set env.AbsorbCheckpoint = stack.SetLastUndoAbsorbed before stack.Absorb. The closure runs after RecordUndo under the existing repository lock. Do not add this callback globally in stackEnv or introduce journal access into the pure Absorb engine.

Remove the old post-success annotation loop; the engine now checkpoints both partial and final progress. Preserve res, emitAbsorb, successful/refused DryRun behavior, notes, and error result contracts. No checkpoint belongs in the --dry-run branch.

In SetLastUndoAbsorbed, return a descriptive error when entries is empty, or the latest entry's Label is not “absorb”. Keep other journal setters' semantics unchanged. Write the supplied cumulative map into that active entry using existing writeUndo/atomicWriteFile. Preserve other fields and earlier entries.

Add TestSetLastUndoAbsorbedRequiresActiveAbsorb in undo_test.go following initGitRepo/writeUndo/PeekUndo fixtures. Cover absent, malformed, empty, and wrong-label journals: return an error and preserve existing raw bytes/no file creation. A valid absorb entry updates first with one target, then cumulative targets, retaining snapshot/refs/created metadata and earlier entries. Keep TestLoadUndoRecoversFromCorruptJournal passing; this is a setter precondition, not a general corruption-policy change.

**Verify**: `go test ./internal/stack -run 'TestSetLastUndoAbsorbed|TestLoadUndoRecovers|TestUndoEntryAbsorbed' -count=1` and `go test -timeout 20m ./cmd -run TestAbsorb -count=1` → all pass.

### Step 3: Prove failed-absorb recovery survives cleanup, abort, and undo

Add TestAbsorbFailedUndoRecoveryPointer in cmd/commands_mutation_test.go. Follow TestAbsorbUndoRecoveryPointer but use an ancestor amendment followed by a genuine descendant conflict. Capture the amended ancestor SHA before abort. Assert the retained absorb entry already contains it despite runAbsorb returning an error; CleanupUndoOnError must preserve the annotation.

Abort the paused rebase, then assert undo --dry-run and real undo both name that SHA and recovery instructions while restoring all original refs. Cover text and JSON in fresh fixtures; conflict remains exit 2/code conflict.

Extend TestAbsorbConflictAbortUndoJourney in e2e/e2e_absorb_test.go using its existing absorbConflictFixture. It builds shared.txt with trunk A0/B0, feat-a A1/B0, and feat-b A1/B1; staged A2/B1 amends feat-a before conflicting on feat-b. Capture feat-a's amended SHA immediately after the fixture, require that SHA in undo preview and applied undo notes after abort, and verify original branch tips.

Then prove the pointer is usable: verify git cat-file -t <SHA> reports commit; in this disposable fixture only, clean/reset the working tree as needed and create a recovery branch at the recorded original main tip. Cherry-pick the saved amended commit and require shared.txt == A2/B0. Keep existing continue and multi-target successful journeys passing. New e2e top-level tests must start with t.Parallel and use per-repo subprocess environments.

**Verify**: `go test -timeout 20m ./cmd -run 'TestAbsorb|TestUndo' -count=1` and `go test -timeout 20m ./e2e -run 'TestAbsorb|TestEveryE2ETestIsParallel' -count=1` → all pass, with a recovered file-content assertion.

### Step 4: Describe the guarantee and run the gate

Add an Unreleased Fixed entry: failed absorb operations record commit pointers before dropping staged copies, so undo can name committed edits. Comment the per-amend and final-refresh boundaries near the engine calls.

This guarantees ordinary error-path recovery when a checkpoint succeeds. Git amendment plus journal I/O are not one crash-atomic transaction: a process crash between them remains possible. On checkpoint failure, halt before further resets/cascade and include recovery SHAs in the error; do not claim all staged content or every ref is rolled back.

**Verify**: `make test-fast`, `make ci`, and `git diff --check` → exit 0.

## Test plan

- Engine callback order and immutable cumulative maps, optional hook, failure short-circuit, and final rewritten-target refresh.
- Every existing post-amend error arm retains at least its last successful checkpoint; pre-amend/refusal arms never annotate.
- Journal setter cannot silently succeed without the active absorb entry; existing corruption recovery remains unchanged.
- Real-Git failed absorb → retained map → abort → preview → undo → cherry-pick recovers exact edit.
- Preserve healthy absorb output and existing recovery notes/schema.

## Done criteria

- [ ] Focused engine/cmd/e2e commands pass; TestAbsorbRecoveryCheckpoints, TestSetLastUndoAbsorbedRequiresActiveAbsorb, and TestAbsorbFailedUndoRecoveryPointer exist.
- [ ] A checkpoint occurs after each successful amend and before the next destructive/progress step.
- [ ] Callback errors preserve errors.Is, stop subsequent amends/resets/cascades, and include landed commit SHAs.
- [ ] Successful cascade refreshes the durable map to final target tips before Save/HEAD restoration.
- [ ] Failed-absorb undo preview/apply names a recoverable SHA, and the binary journey cherry-picks it to reproduce the edit.
- [ ] `make ci` exits 0, `git diff --check` is clean, only scoped files changed, and the index status is updated.

## STOP conditions

- Absorb's target/cascade order or journal lifetime differs from these excerpts.
- The latest active entry cannot be identified by label under the existing lock, or error cleanup discards a real partial mutation.
- The solution requires direct engine filesystem access, a journal schema/identity migration, broad mutation-protocol changes, or making Git+journal writes crash-atomic.
- An engine test passes despite checkpointing only after resets or complete success; strengthen its observation.
- A verification fails twice after a reasonable correction, required tooling is absent, or scope must expand.

## Maintenance notes

Keep checkpoints cumulative and copied. A new post-amend path must preserve the last durable pointer; a later rebase that changes target IDs must refresh success pointers. Setter absence/wrong-label errors prevent false durability claims. Broader crash recovery and undo CAS remain separate work; neither is required to fix the verified ordinary failure path here.

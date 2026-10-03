# Plan 021: Use the local trunk consistently for offline sync

> **Executor instructions**: Follow each step and its verification. Stop on the conditions below rather than expanding scope. Update this plan's row in plans/README.md when completed, unless a dispatching reviewer owns the index.
>
> **Drift check first**: `git diff --stat 159648a..HEAD -- internal/stack/ops_delete_sync.go internal/stack/sync_test.go cmd/sync.go cmd/flagsets.go cmd/commands_mutation_test.go e2e/e2e_journey_sync_test.go cmd/testdata/help.golden README.md docs/AGENT.md CHANGELOG.md plans/README.md`
> Also inspect uncommitted changes with `git diff -- <the same paths>`. Compare the excerpts with live code; do not overwrite unrelated work.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED — intentionally tightens which branches offline sync may prune
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `159648a`, 2026-10-02
- **Selection**: deep-audit F01

## Why this matters

Offline sync currently detects landed branches against a cached remote trunk, but rebases their surviving children onto the unchanged local trunk. With main=T, a=T+A, b=a+B, and cached origin/main containing A, sync can delete a and replay only B onto T, dropping A from b. Its preview substitutes the remote tip and predicts different behavior. This plan deliberately uses the local trunk for both pruning and restacking under --no-fetch; remote-only landed branches remain until local trunk advances.

## Current state

- internal/stack/ops_delete_sync.go:95 — Sync apply; its no-fetch arm changes only the prune basis:

```go
trunkRef := branchTipRef(s.Trunk)
switch {
case noFetch:
    ffResult = "skipped (--no-fetch)"
    if r.Exists(remote) {
        remoteRef := "refs/remotes/" + remote + "/" + s.Trunk
        if _, err := g.RevParse(remoteRef); err == nil {
            trunkRef = remoteRef
        }
    }
```

- The same function calls PruneMergedAgainst at line 165 and restackAll at line 178. restackAll resolves recorded parents from local branch tips.
- cmd/sync.go:47 ignores noFetch when selecting the preview basis:

```go
if dryRun {
    return preview(asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) {
        return stack.SyncPlanAgainst(env, s, noDelete, resolveTrunkRef(remote, s.Trunk))
    })
}
```

- SyncPlanAgainst, internal/stack/ops_delete_sync.go:232, substitutes a supplied nonlocal trunk into its tip map. Keep that capability for ordinary sync --dry-run.
- internal/stack/sync_test.go:800 and cmd/commands_mutation_test.go:1886 currently require deletion of a branch merged only into the cached remote. Their expectations must change explicitly.
- cmd/flagsets.go:212 describes no-fetch as using already-fetched refs. README.md:309 and docs/AGENT.md:20,269 document the same old policy.
- Convention: engine operations use Env.Git/Remote ports; command adapters choose flags and render. Qualified refs avoid tag shadowing. Match the existing branchTipRef usage and TestSyncNoFetchNotFooledByTrunkTag at cmd/commands_mutation_test.go:1929.
- Hard constraints: Go 1.26, standard library only, zero go.mod require entries, no go.sum. No new network calls or login requirements.

## Commands you will need

- Engine loop: `make test-fast` → exit 0.
- Focused engine: `go test ./internal/stack -run TestSync -count=1` → all pass.
- Adapter contracts: `go test -timeout 20m ./cmd -run 'TestSync|TestPruneRemoteBasis|TestJSONEnvelope' -count=1` → all pass.
- Binary proof: `go test -timeout 20m ./e2e -run TestSync -count=1` → all pass.
- Help golden, only if its output actually changes: `go test ./cmd -run '^TestGoldenHelp$' -update` → only help.golden changes.
- Full local gate: `make ci` → exit 0; no skipped required gate.

Audit baseline: fast/race tests and native/cross vet passed. make ci currently stops because pinned golangci-lint v2.12.2 is absent; GoReleaser is also absent. Report a tooling block if the full gate cannot run; do not call focused tests a full-gate pass.

## Scope

**Only modify**:
- internal/stack/ops_delete_sync.go
- internal/stack/sync_test.go
- cmd/sync.go
- cmd/flagsets.go
- cmd/commands_mutation_test.go
- e2e/e2e_journey_sync_test.go
- cmd/testdata/help.golden, only if changing no-fetch help affects this existing golden
- README.md, docs/AGENT.md, CHANGELOG.md
- plans/README.md, status only

**Do not modify**: standalone prune semantics, generic SyncPlanAgainst remote-tip support, restack algorithms, transport adapters, state/journal formats, dependency/tool pins, or other goldens. This is not plan 017's general remote-helper refactor.

## Git workflow

- Use a separate branch `advisor/021-offline-sync-local-basis`; preserve existing uncommitted work.
- Separate regression/policy change from documentation if useful; imperative messages such as “Keep offline sync on the local trunk”.
- No push, PR, release, or remote repository mutation unless separately instructed.

## Steps

### Step 1: Correct apply and preview basis together

In Sync's noFetch arm retain branchTipRef(s.Trunk) and the existing skipped note; remove selection of a cached remote for that arm. Do not fetch or fast-forward. In runSync's preview closure start with the qualified local trunk and use resolveTrunkRef only when noFetch is false. Leave explicit missing-remote validation at cmd/sync.go:43 in place; reading config is not transport.

Rename/update TestSyncNoFetchNeverTouchesRemote and TestSyncNoFetchUsesExistingRemoteRef to assert a remote-only landed branch remains tracked and local main stays unchanged. Preserve remote Fetch/FastForward counters and the skipped note. Preserve the existing local-merged pruning, tag-shadow, ordinary remote preview, and standalone remote-prune cases.

**Verify**: `go test ./internal/stack -run 'TestSync' -count=1` and `go test -timeout 20m ./cmd -run 'TestSync|TestPruneRemoteBasis' -count=1` → exit 0.

### Step 2: Prove surviving content and no transport through the binary

Add TestSyncNoFetchPreservesRemoteMergedAncestor to e2e/e2e_journey_sync_test.go using TestSyncPrunesMerged and the repo helpers as patterns. Use newRepo and t.Parallel; do not change process-global cwd/env.

Create a with a.txt and b with b.txt. Set cached origin/main to a's tip while main remains T, then configure origin to an unreachable **local path** so a real accidental fetch fails. In fresh subtest repos, cover default origin and explicit --remote origin:

1. Snapshot local/remote refs, metadata, and journal bytes. Run --no-fetch --dry-run --json; it predicts no deletion of a and changes none of those snapshots.
2. Run --no-fetch --json; a and b remain tracked, main remains T, origin/main remains a, b contains a.txt and b.txt, and the tree/current branch remain usable.
3. Advance only local main to a's tip and rewind the fixture's cached origin/main to T. Preview/apply now prune a, reparent b to main, and retain both files even with cached remote behind.

Also keep a normal fetched-sync fixture proving that advancing local trunk permits pruning a while preserving b. Do not replace actual content checks with topology-only assertions.

**Verify**: `go test -timeout 20m ./e2e -run 'TestSync' -count=1` → all pass, including the new no-fetch case.

### Step 3: Publish the narrowed offline contract and run the gate

Update the no-fetch flag help and README/AGENT text: both offline prune and restack use local trunk; --remote still selects/validates configuration but does not change the offline basis. Ordinary sync --dry-run may use cached remote; --no-fetch --dry-run uses local trunk. Explain that remote-only landed branches wait for local trunk to advance. Preserve JSON keys/exit codes and the exact skipped note.

Update engine comments that imply remote no-fetch pruning, including the PruneMergedAgainst comment if necessary. Add one Unreleased Fixed entry. TestGoldenHelp renders general help and may remain unchanged; regenerate that golden only if its actual output changes. There is no JSON-help golden to create.

**Verify**: `go test ./cmd -run Golden -count=1`, `make test-fast`, and `make ci` → exit 0.

## Test plan

- Preserve and strengthen the two existing offline tests identified above.
- Add real-git default/explicit-remote surviving-child content and preview parity cases.
- Include local-ahead/remote-behind, absent tracking ref, no-delete, and tag-shadow assertions using existing tests where possible.
- Do not assert that ordinary dry-run equals a future fetched run if the server changes; only the offline fixed-ref scenario has that guarantee.

## Done criteria

- [ ] `go test ./internal/stack -run TestSync -count=1` passes.
- [ ] `go test -timeout 20m ./cmd -run 'TestSync|TestPruneRemoteBasis|Golden' -count=1` passes.
- [ ] `go test -timeout 20m ./e2e -run TestSync -count=1` passes and TestSyncNoFetchPreservesRemoteMergedAncestor exists.
- [ ] New tests assert exact surviving file contents and unchanged local trunk under --no-fetch.
- [ ] `make ci` exits 0; go.mod/go.sum invariant remains intact.
- [ ] Changed files are only those in Scope; status row is updated.

## STOP conditions

- Live code differs materially from excerpts or another plan has changed sync policy.
- Preserving no-fetch requires moving local trunk, consulting a remote server, or changing generic restack/state semantics.
- A new regression passes on the original mixed-basis implementation; fix the fixture before claiming coverage.
- The full gate is unavailable, or a step fails twice after a reasonable correction; report the exact block.
- More goldens or source files need edits than listed; do not broaden the refactor silently.

## Maintenance notes

This intentionally trades earlier cached-remote pruning for coherent local history. Future offline policy must keep prune basis, rebase targets, recorded parentSHA, and preview aligned. Plans 008/017 may add tests or share helpers later; do not duplicate this content-loss regression in another plan.

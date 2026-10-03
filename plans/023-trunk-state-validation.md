# Plan 023: Reject trunk branch records and bound log traversal

> **Executor instructions**: Follow each step and verification. Honor STOP conditions rather than expanding the schema or graph refactor. Update the index status when complete unless a reviewer owns it.
>
> **Drift check first**: `git diff --stat 159648a..HEAD -- internal/stack/store.go internal/stack/store_test.go internal/stack/undo_op_test.go cmd/log.go cmd/log_integrity_test.go CHANGELOG.md plans/README.md`
> Inspect uncommitted changes to these paths and compare the excerpts before editing.

## Status

- **Priority**: P1
- **Effort**: S–M
- **Risk**: LOW–MED — refuses corrupted persisted state that was previously accepted
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `159648a`, 2026-10-02
- **Selection**: deep-audit F04

## Why this matters

The trunk is the root of the stack and must not also be a tracked branch record. Today a file with branches.main.parent == main loads successfully, creating a trunk-rooted cycle that recursive log rendering cannot terminate. Reject this structural corruption at the shared decoder, including undo snapshots, and make both renderers visit each name at most once as a defensive boundary.

## Current state

- internal/stack/store.go:144, decodeState, handles version compatibility and branch key/name integrity but not a trunk entry:

```go
for key, b := range s.Branches {
    if b == nil {
        return nil, fmt.Errorf("state file is corrupted: branch %q has no record", key)
    }
    if b.Name == "" {
        b.Name = key
        continue
    }
    if b.Name != key {
        return nil, fmt.Errorf("state file is corrupted: branch map key %q does not match its recorded name %q (run `st validate` / `st repair`, or fix and reload)", key, b.Name)
    }
}
```

- Load, ValidateUndoState (store.go:181), DecodeUndoState (line 191), and Undo's snapshot decode (internal/stack/undo_op.go:28) share this boundary. A check after the missing-name continue would leave a legacy-form bypass.
- cmd/log.go:169 and 201 recurse through d.index without a visited set:

```go
for _, child := range d.index[name] {
    node.Children = append(node.Children, build(child, name))
}
// text renderer:
for _, child := range d.index[name] {
    printBranch(child, depth+1)
}
```

- internal/stack/repair.go:172, cyclePath, stops when a parent is the trunk. Do not depend on Repair to remove an invalid root entry; the normal cmd load must refuse it first.
- internal/stack/store_test.go:474, TestDecodeStateBranchNameIntegrity, is the structural-decoder exemplar. It already tests mismatched names, missing-name backfill, null records, and valid trunk-parent relationships.
- cmd/commands_json_test.go:73, TestLogTextAndJSON, and :149, TestLogWarnsUnreachable, pin healthy output and unrelated orphan/cycle reporting. cmd/golden_test.go pins text/JSON tree output.
- internal/stack/undo_op_test.go's schema-barrier fixture snapshots main→a, creates b, then asserts refused undo leaves b/HEAD and Save untouched. Extend that pattern, not the undo algorithm.
- cmd/undo.go:111 intentionally permits a malformed **current** state to recover from a valid snapshot, while rejecting all invalid selected snapshots before worktree preparation. Preserve that recovery path.
- Constraints: Go 1.26, stdlib only, no require entries/no go.sum, schema v1 with legacy v0 acceptance. Trunk as a branch parent is valid; trunk as a branch-map key is invalid.

## Commands you will need

- Decoder/undo: `go test ./internal/stack -run 'TestDecodeState|TestLoad|TestUndo' -count=1` → all pass.
- Rendering/adapters: `go test -timeout 20m ./cmd -run 'TestLog|TestTrunkState|TestUndoTrunk|TestGoldenLog' -count=1` → all pass.
- Engine baseline: `make test-fast` → exit 0.
- Full local gate: `make ci` → exit 0.

Fast/race tests and vet passed during the audit. The full gate is currently blocked by absent pinned golangci-lint v2.12.2; do not change tool/dependency pins or call focused tests a full pass.

## Scope

**Only modify**:

- internal/stack/store.go
- internal/stack/store_test.go
- internal/stack/undo_op_test.go
- cmd/log.go
- cmd/log_integrity_test.go (create)
- CHANGELOG.md
- plans/README.md, status only

**Do not modify**: schema version or fields, graph/topology APIs, Repair's algorithm, undo apply/preview logic, ref restoration, JSON payload shapes, unrelated integrity rules, existing goldens, or dependency/tool configuration. Healthy golden output should remain identical.

## Git workflow

- Branch `advisor/023-trunk-state-validation`; preserve unrelated work.
- Imperative messages such as “Reject tracked trunk records” and “Bound stack log traversal”.
- No push, PR, release, or remote mutation unless separately instructed.

## Steps

### Step 1: Add the shared decoder guard and compatibility tests

In decodeState's branch loop reject key == s.Trunk before missing-name backfill/continue. Return a corruption error naming the trunk and saying it must not appear in tracked branches. A null trunk record is already invalid; preserve that refusal too. Do not reject ordinary branches whose Parent == s.Trunk, bump the version, or add broad new validation.

Extend TestDecodeStateBranchNameIntegrity with current v1 and legacy/no-version documents containing: a named trunk record with parent trunk; a trunk record without Name; a trunk record with a different parent; and a null trunk record. All must fail. Keep positive controls for missing-name ordinary branches, an empty tracked map, and ordinary trunk-parent records. Verify ValidateUndoState and DecodeUndoState also reject the same bytes.

In internal/stack/undo_op_test.go add TestUndoTrunkSnapshotRejected. Use a snapshot whose undo would delete a created branch; replace only its serialized State with a trunk-record document. Assert Undo errors before any cleanup: created branch and all tips survive, HEAD unchanged, zero Save calls, and current in-memory metadata unchanged. Cover nonnil and nil current state.

**Verify**: `go test ./internal/stack -run 'TestDecodeState|TestLoad|TestUndo' -count=1` → all pass, including the trunk-record negative and legacy positive controls.

### Step 2: Guard both log renderers without changing healthy output

Give each invocation of printLogJSON and printLogTree its own visited map. At the start of the recursive function, return immediately for an already-visited name and mark new names **before** traversing children. In JSON, duplicate visits return nil; append a child only when build returns nonnil. Keep the trunk root, healthy child ordering, empty children arrays, existing root.Unreachable field, and current signatures.

In cmd/log_integrity_test.go add TestLogCycleTraversalBounded, calling the renderers directly with synthetic logData rather than persisting invalid data. Cover main→main, main→a→b→a, and duplicate child references; use nonnil branch records and harmless empty annotation maps. Capture stdout, decode JSON, and count rendered names to prove each appears at most once and no null child is emitted. Compare a healthy main→a→b case with its expected existing shape/order. Do not add a new “cycle” field or silently repair persisted metadata.

**Verify**: `go test -timeout 20m ./cmd -run 'TestLog|TestGoldenLog' -count=1` → all pass with unchanged existing goldens.

### Step 3: Prove persisted refusal and preserve valid-snapshot recovery

In cmd/log_integrity_test.go add TestTrunkStateRefusesBeforeMutation. Follow newRepo/mustInit/mustCreate from existing cmd tests; do not t.Parallel because they change cwd/env. Write a corrupted state document into the temporary fixture's state.json, retaining ordinary a/b records but adding main as a branch. Snapshot exact state/journal bytes, all local refs, and HEAD.

Table-drive log text/JSON, prune, sync --no-fetch, validate, and repair through their adapters or Execute. Each must return a corruption error promptly; compare every snapshot afterward. An initialized lock file may be touched by an attempted mutation, but metadata, journal, HEAD, and refs must not change. Use fresh fixtures per subtest so a refusal cannot hide later writes.

Add TestUndoTrunkSnapshotAdapterBarrier: an invalid selected undo snapshot must refuse before dropping the entry, moving HEAD, or removing a recorded created worktree; use an ordinary created-branch fixture if no worktree is involved. Also add a positive TestUndoTrunkCorruptCurrentStateRecovery: leave a valid journal snapshot, corrupt only current state with a trunk record, and verify undo restores valid state/refs and drops the entry. The shared guard must protect snapshot bytes without disabling cmd's intentional nil-current-state recovery.

**Verify**: `go test -timeout 20m ./cmd -run 'TestTrunkState|TestUndoTrunk|TestLog|TestGoldenLog' -count=1` → all pass, with no hang or stack overflow.

### Step 4: Record the correction and run the full gate

Add an Unreleased Fixed entry describing refusal of a tracked trunk record and bounded log traversal. No schema or healthy-output documentation migration is needed.

**Verify**: `make test-fast`, `make ci`, and `git diff --check` → exit 0.

## Test plan

- Shared decoder: named/unnamed/null trunk records under v0/v1 reject; ordinary trunk-parent and legacy backfill still accept.
- Engine undo: invalid snapshot refuses before branch deletion, Save, HEAD/ref movement, or metadata replacement.
- Pure renderers: self-cycle, longer reachable cycle, duplicate edge, healthy tree; no duplicate node/null child and unchanged normal order.
- Real-Git adapters: persisted corruption refuses before mutation, including repair; invalid snapshot retained; valid snapshot repairs corrupt current state.
- Existing unreachable-node and golden tests remain the compatibility gate.

## Done criteria

- [ ] All focused commands pass; TestUndoTrunkSnapshotRejected, TestLogCycleTraversalBounded, TestTrunkStateRefusesBeforeMutation, and both adapter undo cases exist.
- [ ] Both named and legacy unnamed trunk records are rejected through Load/ValidateUndoState/DecodeUndoState.
- [ ] Invalid selected snapshots cause no branch/state/journal mutation; valid snapshots can recover a malformed current state.
- [ ] Healthy log goldens remain byte-identical and traversal visits each name at most once.
- [ ] `make ci` exits 0, `git diff --check` is clean, only scoped files changed, and the index status is updated.

## STOP conditions

- The decoder already has a structural barrier with different compatibility rules.
- A legitimate persisted state requires the trunk in Branches; report that model conflict rather than migrating it silently.
- A fix requires a schema bump, removal of nil-state undo recovery, changes to Repair, or changed healthy goldens.
- A verification fails twice after a reasonable correction, required tooling is missing, or scope must expand.

## Maintenance notes

Keep the trunk-record check ahead of legacy backfill. Every new serialized state ingress must use the shared decoder. Renderer visited sets are defensive bounds, not a substitute for rejecting bad persisted state or for existing unreachable-branch reporting.

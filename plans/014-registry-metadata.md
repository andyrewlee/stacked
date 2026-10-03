# Plan 014: Derive sub-verb/flag metadata from the registry; dedupe the JSON payloads and journal helpers

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- cmd/completion.go cmd/root.go cmd/undo.go cmd/worktree.go internal/stack/undo.go`
> On mismatch, STOP.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW (metadata-only; goldens pin behavior)
- **Depends on**: none
- **Category**: tech-debt
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Three small duplications that fail silently:

1. `subVerbs` (`cmd/completion.go:63-69`) hand-maintains each command's sub-verbs ("Keep in sync with each command's Usage string") — a new `worktree` verb or a `completion`/`shell` change lands without any test forcing the update; the generated scripts just omit the new token.
2. `commandFlags` (`cmd/root.go:196-199`) special-cases command names (`completion`/`shell`) to decide "takes no flags" — a second place knowing capabilities outside `Command` metadata, while names/aliases/flags are all registry-derived.
3. Literal payload/type duplication: `cmd/undo.go` declares the same `undone/count/restored/steps/notes` anonymous struct twice verbatim (187-193, 224-230) plus a near-duplicate n==1 variant (175-181); `cmd/worktree.go` duplicates a `{branch,path,copied,summary}` shape (`emitWorktree` 432-437 vs `worktreeAllEntry` 461-466); `internal/stack/undo.go:187-226` repeats the load→mutate-last→`writeUndo` template three times (`SetLastUndoCreatedBranches`/`SetLastUndoCreatedWorktrees`/`SetLastUndoAbsorbed`).

## Current state

- `cmd/root.go:31-53` — `Command` struct: `Name`, `Aliases`, `Summary`, `Usage`, `Run`, `NewFlagSet` (nil ⇒ only `--json`), `Completion` (nil ⇒ no dynamic candidates). Adding a `SubVerbs []string` field fits the established "declare-once-at-registration" pattern.
- `cmd/completion.go:63-69` — `subVerbs` map literal.
- `cmd/root.go:196-199` — `commandFlags` name-literal special case.
- `internal/stack/undo.go:187-226` — three `SetLastUndo*` helpers each ~10 lines of identical load/mutate/write.
- Convention: registry-driven metadata (the `Completion` field proved it — plan 022/034); generated scripts must read from the registry, never a side table; goldens pin help output.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| Completion tests | `go test ./cmd -run 'Complete|Completion'` | all pass |
| Goldens | `go test ./cmd -run 'Golden'` | all pass (regenerate ONLY if a Usage/field legitimately changes) |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `cmd/completion.go`, `cmd/root.go`, `cmd/undo.go`, `cmd/worktree.go`, `internal/stack/undo.go`, per-command registration files only where a `SubVerbs` field is set (`cmd/worktree.go`, `cmd/completion.go`, `cmd/shell.go`), tests.

**Out of scope**: `docs/AGENT.md` (the generated-script contract doesn't change), the completion *endpoint* logic (`complete.go`), `flagsets.go` (unless a `NoFlags`-style sentinel lands there).

## Git workflow

- Branch: `advisor/014-registry-metadata`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: `SubVerbs` on `Command`, drop the side map

Add `SubVerbs []string` to `Command` in `cmd/root.go` with a doc comment matching the field style ("when set, lists the literal sub-verbs the generated scripts complete after the command name"). Set it on `worktree` (`{"list","ls","remove","rm"}`), `shell` (`{"install"}`), `completion` (`{"bash","zsh","fish"}`). Delete the `subVerbs` map in `cmd/completion.go` and read `c.SubVerbs` in `commandCompletions`.

**Verify**: `go build ./cmd` → exit 0; `go test ./cmd -run 'Complete'` → pass; `grep -rn 'subVerbs' cmd/` → no match.

### Step 2: `commandFlags` — derive "no flags" instead of name-matching

The cleanest signal: `NewFlagSet == nil` already means "only `--json`" — but `completion`/`shell` take no flags at all. Options: (a) add a `NoFlags bool` field set on `completion`/`shell` registrations, or (b) make their `NewFlagSet` return an EMPTY flagset (no `--json`) and drop the name check — `commandFlags` then introspects an empty set and returns nil. Option (b) is more registry-native (one mechanism: "the flagset is the truth"). Pick (b): give `completion`/`shell` a `NewFlagSet` returning `flag.NewFlagSet(name, …)` with zero flags; `commandFlags` becomes `if c.NewFlagSet != nil { return flagList(c.NewFlagSet()) }; …` and the name check disappears. Verify `parsePlain`/`runCompletion`/`runShell` don't need the implicit `--json` set — check how they parse args today (they may not use flagsets at all).

**Verify**: `go test ./cmd -run 'Help|Completion|Shell'` → pass; `st help completion --json` shows `flags: []` or `null` (golden pins this).

### Step 3: Name the duplicated JSON payload types

- `cmd/undo.go`: hoist the shared anonymous struct (187-193/224-230) to a named `undoResult` type; the n==1 variant becomes the same type with `count`/`steps` `omitempty` (if fields differ only by omission, one type with `omitempty` serves both — verify against `docs/AGENT.md`'s documented undo shapes: single-entry emits `{undone,label,restored}` — n>1 adds `count`/`steps`. If `omitempty` on `count`/`steps` produces the same wire shape, unify; otherwise keep two named types but declare each ONCE).
- `cmd/worktree.go`: reuse `worktreeAllEntry` inside `emitWorktree` (or extract `worktreeResult` naming both).

**Verify**: `go test ./cmd -run 'Undo|Worktree|JSON'` → pass — goldens/contract tests pin the wire shape; a drift shows immediately.

### Step 4: Collapse the journal helpers

In `internal/stack/undo.go`, add:

```go
func mutateLastUndo(fn func(*UndoEntry)) error {
    entries, err := loadUndo()
    if err != nil { return err }
    if len(entries) == 0 { return nil }
    fn(entries[len(entries)-1])
    return writeUndo(entries)
}
```

Re-implement `SetLastUndoCreatedBranches`/`CreatedWorktrees`/`Absorbed` as one-line delegations. Keep the exported names (callers exist in `cmd/`).

**Verify**: `go test ./internal/stack -run 'Undo'` → pass.

### Step 5: Gate

**Verify**: `make ci` → exit 0.

## Test plan

- Existing contract/golden/completion tests are the net — they pin the wire shapes and the generated scripts byte-for-byte.
- Add one focused test if `SubVerbs` needs a pin: assert the generated bash script for `worktree` contains its sub-verbs (check if `completion_test.go` already asserts this — if it does, it just keeps passing).
- Verification: `go test ./cmd ./internal/stack ./e2e` → all pass; `make ci` → green.

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./...` exits 0
- [ ] `grep -n 'subVerbs' cmd/` → no match
- [ ] `grep -n 'c.Name == "completion"\|c.Name == "shell"' cmd/root.go` → no match
- [ ] `grep -c 'SetLastUndo' internal/stack/undo.go` → 3 funcs, each a one-line delegation
- [ ] No duplicated anonymous payload structs in `cmd/undo.go`
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- Option (b) in Step 2 breaks a command that relied on the implicit `--json` flagset — if `completion`/`shell`/`help`'s parsing depends on `newFlagSet(name, &asJSON)` existing, keep `NoFlags bool` (option a) instead; either is fine, don't force (b).
- The undo payload's wire shape can't be unified by `omitempty` without changing output — keep two named types; the point is each declared once.
- A golden shift appears that's NOT explained by the metadata move — STOP; goldens should only move if `Usage`/`flags` output changed on purpose.

## Maintenance notes

- After this, adding a sub-verb is a one-field registration change with a completion test pinning it — the silent-omission class is gone.
- Reviewer focus: `commandFlags`'s new path must still handle the `NewFlagSet == nil` case (commands whose only flag is `--json` leave it nil per `root.go:46`).
- Deferred: a `commandFlags`/`SubVerbs` consistency test could pin "registered command with sub-verbs ↔ generated script emits them" — cheap insurance if not already covered.

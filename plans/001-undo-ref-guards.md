# Plan 001: Guard `st undo` ref restoration — expected-old OIDs, OID validation, sanitized preview

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- internal/stack/undo.go internal/stack/undo_op.go internal/git/refs.go cmd/undo.go internal/stack/undo_preview.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED (undo is the recovery path; the change adds refusals, which can only strand an undo — mitigated by `--force` escape hatch and idempotent retry)
- **Depends on**: none
- **Category**: bug + security
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`st undo` restores every recorded branch tip via a single `git update-ref -z --stdin` batch whose old-oid field is **empty** — meaning "no verification". The journal only records *pre-op* tips, so undo cannot distinguish "the op moved this ref" from "someone else moved it after the op". A user who runs `git commit` on a stack branch between the op and `st undo` loses that commit to a silent rewind (reflog-only recovery); a branch deleted since the snapshot is resurrected. Separately, the journal's ref values are applied to `update-ref` with only whitespace/control-byte checks — a corrupt or hostile `undo.json` can carry `HEAD~3`, an all-zeros delete value, or another ref name and have `update-ref` resolve it. Finally, `st undo --dry-run` prints the journal's `From`/`To` values raw while sanitizing every neighboring field — a crafted journal emits terminal control sequences.

## Current state

- `internal/stack/undo.go` — journal schema and snapshot. `snapshotUndo` (lines 85–116) records `Refs` as **pre-op** tips only; there is no post-op tip field. `loadUndo` (lines ~41–63) unmarshals entries with no value validation.
- `internal/stack/undo_op.go` — `Undo` applies the entry. Lines 136–145 build `updates[branchTipRef(name)] = sha` and call `g.UpdateRefs(updates)`. Line 88 uses `g.UpdateRef(branchTipRef(target), sha)` for the intermediate checkout-target restore.
- `internal/git/refs.go` — `UpdateRefs` (lines 515–560). Lines 524–529 reject only whitespace/control bytes on ref and value. Line 541 emits `update %s\x00%s\x00\x00` — the comment at 535–540 documents the empty old-oid as "no verification". `UpdateRef` (line ~500) is `git update-ref -- <ref> <sha>` with no old-oid.
- `internal/git/diff.go:586` — `isHex40` helper already exists.
- `cmd/undo.go` — `renderUndoPreview` (lines ~445–481): `out("  restores: %s %s→%s (%s commits lost from ref)\n", sanitizeForTerminal(r.Branch), r.From, r.To, lost)` — `From`/`To` are raw journal bytes.
- `internal/stack/undo_preview.go:264-265` — `r.To` = `entry.Refs[name]` straight from the journal.
- `internal/stack/git.go` — the `Git` port interface; `UpdateRef`/`UpdateRefs` are the seam the engine uses. `git.Shell` is the production impl; `fakeGit` in `internal/stack/fakegit_test.go` is the test impl. **Both must be updated.**
- Conventions: engine functions are pure over `stack.Env`/`*stack.State`; per-function coverage floor is 50% (new code must be tested, not allowlisted); tests use `newEnvState`/`mkBranch`/`fakeGit` (see `internal/stack/engine_test.go`).
- `docs/AGENT.md` documents the `undo --json` result shapes — new refusal/blocker kinds must be added there and to the `undo --dry-run` blocker list in `CHANGELOG.md` `[Unreleased]`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Fast tests | `make test-fast` | exit 0 |
| Package tests | `go test ./internal/stack ./internal/git ./cmd` | all pass |
| Full gate | `make ci` | exit 0; coverage ≥ 75%, no per-function <50% |
| Real-git undo test | `go test ./cmd -run 'Undo'` | all pass |

## Scope

**In scope** (the only files you should modify):
- `internal/stack/undo.go` — `UndoEntry` gains a `PostRefs map[string]string` field (json `postRefs,omitempty`); `loadUndo` validates every `Refs` value with `git.IsHex40`-equivalent (see note below).
- `internal/stack/undo_op.go` — restore path uses expected-old values; new blocker/refusal path on mismatch.
- `internal/stack/undo_preview.go` — preview reports would-refuse mismatches as blockers; `From`/`To` already flow through — no logic change needed beyond optional blocker emission.
- `internal/stack/git.go` — port gains `UpdateRefsCas(updates map[string][2]string) error` (new→expectedOld) or an equivalent; pick the smallest signature change.
- `internal/git/refs.go` — implement the CAS batch (`update <ref>\0<new>\0<old>`), keep `UpdateRefs` for unconditional uses.
- `internal/stack/fakegit_test.go` — fake honors the CAS semantic.
- `cmd/undo.go` — wrap `r.From`/`r.To` in `sanitizeForTerminal`.
- `internal/stack/undo*_test.go`, `internal/git/refs_test.go` (or `git_test.go`), `cmd/undo_test.go` — new tests.
- `docs/AGENT.md`, `CHANGELOG.md` — document the new refusal/blocker.

**Out of scope** (do NOT touch):
- `internal/stack/undo_preview.go`'s gate-ordering twin-site structure — Plan 010 unifies it; do not refactor here.
- The `git update-ref` single-ref path's other callers (e.g. `internal/stack` ops that legitimately create/move refs) — only the undo restore path gains CAS semantics.
- `st undo`'s `--force` flag does not exist today; only add it if the plan below says so (it does — as the documented escape hatch).

## Git workflow

- Branch: `advisor/001-undo-ref-guards`
- One or two commits; message style follows history, e.g. `Guard undo ref restoration with expected-old OIDs` (imperative, no prefix tags).
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Record post-op tips in the journal

In `internal/stack/undo.go`, add `PostRefs map[string]string` to `UndoEntry` (json tag `postRefs,omitempty`). In `FinalizeUndo` (same file, ~line 300–370 — where `readUndoTips` already calls `g.Tips()`), populate `entry.PostRefs` for the same branch set as `Refs` (trunk + tracked), plus — important — the tips of any branches the op *created* (the `LocalBranches` diff already enumerates them; `PostRefs` should include created branches' tips so undo's delete path can distinguish "op-created" from "externally-moved-since").

**Verify**: `go test ./internal/stack -run 'Undo|Finalize' ` → all pass. `go build ./...` → exit 0.

### Step 2: Validate journal ref values at load

In `loadUndo` (`internal/stack/undo.go`), after unmarshal: for each entry, reject (skip + count as corrupt per existing malformed-entry handling) any `Refs`/`PostRefs` value that is not a 40-char lowercase hex string or the all-zeros SHA. `internal/git.IsHex40` is currently unexported — either export it or add a local `isSHA` helper in `undo.go` (match how the file already handles validation; do NOT import `internal/git` from `undo.go` if the file currently avoids it — check the file's imports first; `store.go` already imports `internal/git`, so a package-level helper is fine).

**Verify**: `go test ./internal/stack` → all pass.

### Step 3: Emit expected-old values in the restore batch

In `internal/git/refs.go`, add `UpdateRefsCas(updates map[string]RefUpdate)` where `RefUpdate{New, Old string}` emits `update <ref>\0<new>\0<old>\x00` (old = expected current OID; empty `""` means the ref must NOT exist — use that for refs that were absent pre-op so undo refuses to resurrect externally-recreated branches). Keep `UpdateRefs` unchanged for other callers. In `internal/stack/git.go` add the method to the port; implement it in `fakeGit` honoring the same semantics (mismatch → error naming the ref).

In `internal/stack/undo_op.go`, build the batch from `entry.PostRefs` as the expected-old values: `Old = postOp[branch]`; when a branch has no `PostRefs` entry (older journal — see Step 4), fall back to unconditional update. Handle the single-ref restore at line ~88 the same way via a `UpdateRefCas` or by folding it into the batch path.

### Step 4: Define the mismatch policy

Old journals lack `PostRefs`. Decide and implement: entries without `PostRefs` restore unconditionally (status quo) with a `note` in the result; entries with `PostRefs` run CAS. On CAS failure (git reports `cannot lock ref ... is at X but expected Y`): fail the undo with an error naming the diverged refs, exit 1, journal entry retained (existing behavior on failure). Add `--force` to `st undo` (flagset in `cmd/flagsets.go`: `undoOpts` gains `force bool`) that reverts to unconditional restore after printing the divergence list to stderr.

For the dry-run side: `UndoPreview` computes, per recorded ref, whether live tip ≠ recorded post-op tip (when `PostRefs` present) and emits a `blockers`/`notes` entry (extend the existing blocker strings list — e.g. `ref_moved_since:<branch>`). Keep preview read-only.

**Verify**: `go build ./...` → exit 0.

### Step 5: Sanitize preview From/To

In `cmd/undo.go` `renderUndoPreview`: wrap `r.From` and `r.To` in `sanitizeForTerminal`.

**Verify**: `go build ./cmd` → exit 0.

### Step 6: Tests

- `internal/stack/undo_op_test.go`: new test — record entry, externally move a branch tip via `f.ForceBranch`/tips mutation, `Undo` → error naming the ref, refs unchanged.
- `internal/git/git_test.go` or `refs_test.go`: `UpdateRefsCas` against a real temp repo — matching old succeeds; wrong old fails atomically (no ref moved); empty old refuses to create an existing ref.
- `internal/stack/undo_test.go` (loadUndo tests): poisoned `undo.json` with `Refs["main"]="HEAD~2"` → entry treated as corrupt/ignored.
- `cmd/undo_test.go`: `--dry-run` output contains no raw control bytes when journal values contain escapes (use a crafted journal via `os.WriteFile` on the fixture repo's `.git/stacked/undo.json`).
- Follow existing test conventions: `newEnvState`, `mkBranch`, table-driven.

**Verify**: `go test ./internal/stack ./internal/git ./cmd` → all pass, new tests included.

### Step 7: Docs

- `docs/AGENT.md`: add `ref_moved_since:<branch>` to the documented `undo --dry-run` blocker list; document `--force` on the `undo` bullet.
- `CHANGELOG.md` `[Unreleased]`: entry under `Changed`/`Fixed` — undo now refuses to clobber refs moved outside `st` unless `--force`.

**Verify**: `make ci` → exit 0.

## Test plan

- New tests listed in Step 6; structural pattern: `internal/stack/undo_op_test.go` (fake-git engine tests) and `internal/git/git_test.go` (real-git port tests).
- Verification: `go test ./internal/stack ./internal/git ./cmd ./e2e` → all pass; `make ci` → green including per-function floor (new CAS code must not need allowlisting).

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `go test ./internal/stack ./internal/git ./cmd` exits 0 with the new tests present
- [ ] `make ci` exits 0
- [ ] `grep -n 'update %s\\x00%s\\x00%s\\x00\|UpdateRefsCas' internal/git/refs.go` shows the CAS record emitted
- [ ] `grep -n 'sanitizeForTerminal(r.From)' cmd/undo.go` present
- [ ] `git status` shows no files outside the in-scope list
- [ ] `plans/README.md` status row updated

## STOP conditions

- The excerpts above don't match live code (drift since `159648a`).
- `fakeGit` can't express "ref moved externally" — check how existing tests move tips (e.g. `f.ForceBranch`); if no mechanism exists, add a minimal one rather than faking it.
- CAS refusal turns out to break the "idempotent retry" path (a second undo after a first successful restore sees `live == preOp` ≠ `postOp`): handle by treating `live == Old.New` (already-restored) as success, or STOP and report.
- Adding `--force` requires a `Command` registry or completion change beyond the flagset — STOP if it cascades.

## Maintenance notes

- Plan 010 (unify `Undo`/`UndoPreview`) depends on this landing — the preview's blocker list grows here.
- Reviewers: the `PostRefs` backward-compat path (old journals) is the subtle bit; confirm an entry written before this change still undoes correctly.
- Watch: `dropNoopUndo` (`undo.go:~361`) retains entries whose recorded tips differ from live — with CAS, those entries now *fail loudly* instead of silently rewinding, which is the intended behavior change.

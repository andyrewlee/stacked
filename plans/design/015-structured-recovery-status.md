# Design 015: Structured recovery and skip results for orchestrators

> Status: **proposed contract** — nothing in this document ships yet. Every
> field marked `recovery` or named inside it is invented here; the fixtures in
> `015-recovery-examples.json` are proposals, not current output. A future
> implementation plan must re-derive `Current contract` against then-current
> source before coding.

## Current contract

Mapped from source at the cb31f06 lineage (verified against this stack's HEAD,
which includes plans 006/007/012). Symbols cited are load-bearing.

### `st restack` / `st restack --all` / `st sync` (mutation, shared shape)

- Adapter: `runRestack` in `cmd/restack.go:16` → `mutate("restack", …)` →
  `stack.Restack` / `stack.RestackAllOp`; `runSync` (`cmd/sync.go:22`) funnels
  into the same cascade via `Sync` (`internal/stack/engine.go`).
- Success JSON (stdout, exit 0): the shared mutation shape —
  `{ "summary": "restacked" | "everything up to date", "branch"?, "restacked": [...], "notes": [...] }`
  built in `restackEpilogue` (`internal/stack/engine.go:340-344`) and rendered
  by `renderResult` in `cmd/mutate.go`.
- Skipped-branch collection: `s.skippedWorktrees` (`internal/stack/stack.go:70`,
  in-memory, `json:"-"`, populated by `restackInWorktree`
  `internal/stack/worktree.go:227` only when the OWNER worktree is dirty),
  drained into prose strings by `skippedWorktreeNote`
  (`internal/stack/engine.go:386`): `"skipped <name>: its worktree is dirty
  (commit/stash there, then re-run)"`. Notes land in the `notes` array; there
  is no machine-readable reason, path, or next action.
- Partial progress: `restacked` lists branches already rebased before a skip or
  conflict; ordering is stack order (cascade order in `restackUpstack`,
  `internal/stack/engine.go`).
- Conflict: `restackBranchWith` returns `*ConflictError{Action:"rebasing",
  Branch, Onto}` (`internal/stack/restack.go:141`) → `mutate` propagates →
  `renderError` (`cmd/root.go:405-419`) writes the stderr envelope
  `{ "error": { "code": "conflict", "message", "branch", "onto" } }`, exit 2.
  The rebase stays PAUSED in the current worktree; earlier `restacked` progress
  is kept (not rolled back) but is invisible to `--json` consumers because no
  success result is emitted — only the envelope.
- Cross-worktree conflict is different: `restackInWorktree`
  (`internal/stack/worktree.go:230-243`) aborts the paused rebase IN the owner
  worktree (`RebaseAbortIn`) and returns a plain `fmt.Errorf` ("rebasing … in
  its worktree … (resolve it there, then re-run)") → exit 1 envelope
  `{ "error": { "code": "error", "message" } }` with no `branch`/`onto` fields.
  Nothing is left paused; the branch is NOT locally resumable with
  `st continue` — that asymmetry is exactly what a structured result must
  express.

### `st continue` (recovery verb)

- `runContinue` (`cmd/continue.go:64`) → `mutate("continue", …, stack.Continue)`.
- Success JSON: `{ "summary": "continued restack", "restacked": [...],
  "notes": [...] }` where `notes` names the branch whose conflict just
  completed (`internal/stack/engine.go`, `Continue`). Exit 0.
- Re-stall: `Continue` returns `*ConflictError{Action:"continuing", Branch,
  Onto}` (`internal/stack/engine.go:950`) → same stderr envelope, exit 2. For
  a pending reparent the `onto` field comes from `PendingReparent`, not the
  live parent tip (plan 006).

### `st abort`

- `runAbort` (`cmd/abort.go:106`) → `stack.Abort` → JSON
  `{ "aborted": true, "summary": "..." }`, exit 0. Clears `PendingReparent`
  when the paused branch is the conflicted one (`internal/stack/engine.go:891`).
  Text mode already prints a second line telling the user previously-restacked
  branches keep their positions and the conflicted branch still needs a
  restack (`cmd/abort.go:142-143`); that guidance is prose-only.

### `st status` / `st log` (read-only observation)

- `status --json` already reports `rebaseInProgress`, `rebaseBranch`,
  `conflictedFiles` (`cmd/status.go:116-118`, fed by `git.RebaseInProgress` +
  `git.UnmergedFiles`) — observation, not an operation outcome.
- `log --json` carries per-node `worktree` (exact path bytes after plan 012)
  and `dirty`.

### `st worktree --all` / `st worktree rm --all` (already structured)

- Aggregate results `{ "created": [...], "skipped": [{ "branch", "reason" }],
  "failed": { "branch", "error" } }` (`cmd/worktree.go`) prove the codebase
  already ships a structured skip list — recovery reporting should reuse that
  precedent rather than invent a second shape for skips.

### Gaps an orchestrator hits today

1. A dirty-worktree skip surfaces only inside prose `notes` — no reason code,
   no worktree path, no instruction channel.
2. A paused conflict loses the earlier `restacked` progress from JSON entirely.
3. A rolled-back cross-worktree conflict is indistinguishable from a resumable
   one except by exit code (1 vs 2) — and only prose names the worktree.
4. Nothing states whether a rebase remains in progress after the command
   returned (exit 2 implies paused *in this worktree*; nothing on stdout).

## Proposed contract

One additive, optional field on the **existing** result and error surfaces —
no new commands, no renames:

```json
"recovery": [
  {
    "branch": "feat-b",
    "reason": "worktree_dirty",
    "worktree": "/abs/path",
    "state": "skipped",
    "action": { "kind": "manual",
                "detail": "commit or stash inside the worktree, then re-run st restack" }
  }
]
```

- `recovery` is an **array in outcome order** (cascade order for restack/sync;
  single-element for continue/abort conflict details). `omitempty`: absent when
  empty — never `"recovery": []` or `null`.
- Entry fields:
  - `branch` (string, required) — the branch the entry concerns.
  - `reason` (string, required) — a stable code from the registry below.
  - `worktree` (string, optional) — the exact path bytes of the owning worktree
    (byte-preserving per plan 012; JSON escapes control bytes itself).
  - `state` (string, required) — the operation state at return time:
    `skipped` (never attempted), `paused` (a rebase is in progress in the
    current worktree), `rolled_back` (a paused rebase was aborted where the
    main process could not drive it).
  - `action` (object, required) — the honest next step. Two kinds only:
    - `{ "kind": "argv", "argv": ["st","continue"], "cwd": "/abs/path" }` —
      structured argv, emitted ONLY when the next command is known to exist
      (`paused` in the current worktree → `st continue`/`st abort`; `cwd`
      omitted when it is the caller's cwd). `argv` is always a JSON array of
      strings; `cwd` is byte-exact. The descriptor is a *suggestion*, never
      executed by `st`.
    - `{ "kind": "manual", "detail": "<human instruction>" }` — required for
      `worktree_dirty` (no safe automatic stash/commit is guessed) and for
      `rolled_back` (the branch must be rebased in its own worktree or the
      conflict resolved there; `st continue` in the caller's worktree does NOT
      resume it).
- Reason-code registry (initial, closed set — extend only by new plan):
  - `worktree_dirty` — owner worktree unclean; source:
    `restackInWorktree` skip → `s.skippedWorktrees`.
  - `conflict_paused` — rebase stopped in the current worktree; source:
    `*ConflictError` → exit 2 envelope. Envelope placement: `recovery` joins
    the existing `error` object's siblings — i.e.
    `{ "error": {…}, "recovery": [...] }` on stderr, keeping
    `error.branch`/`error.onto` unchanged.
  - `conflict_rolled_back` — cross-worktree rebase aborted by
    `restackInWorktree`; source: the exit-1 plain-error path. With this field,
    that path also gains `error.branch` and a `recovery` entry with
    `state: "rolled_back"` — so an orchestrator never mistakes it for
    locally resumable work.
- Partial progress stays visible: on exit-2/exit-1 conflict returns, a
  `restacked` array MAY accompany the envelope on stderr inside `recovery`'s
  sibling `"restacked"` field — recommended but secondary; the essential
  contract is `reason`/`state`/`action`.
- `notes` remains the human prose channel and is unchanged in wording.

## Compatibility

- Additive optional field: existing consumers that ignore unknown keys are
  unaffected; every legacy field keeps its name, type, and position. The repo's
  own strict JSON test decoders (`decodeStrictJSON`, `cmd/testenv_test.go`)
  must allowlist `recovery`/`restacked`-on-envelope when implementation lands —
  an external strict consumer needs the same boundary: consumers decoding with
  `json.Decoder.DisallowUnknownFields` must permit `recovery`, or gate on a
  client capability. Recommended boundary: document `recovery` as part of the
  schema revision that adds it; no version negotiation is proposed — additive
  optional is the project's established policy (see `dirty`, `worktree`,
  `needsRestack` precedents), and strict consumers are expected to allowlist
  new optional keys.
- JSON path/branch values keep raw bytes (control bytes escaped by
  `encoding/json`); terminal text continues through `sanitizeForTerminal`.
- `worktree ls`/`status`/`log` shapes unchanged — `recovery` appears only on
  mutating commands' results and on conflict error envelopes.

## State and concurrency

- `recovery` describes the state **at command return** — a point-in-time
  observation, not a reservation. The advisory lock (`internal/stack/lock_*`)
  covers only the command's own critical section; a worktree can become dirty
  or a paused rebase resolved between the response and any follow-up. An
  orchestrator MUST re-observe (`st status --json`, `st log --json`) rather
  than cache entries.
- No repository-wide atomic snapshot is promised or needed: `restacked` +
  `recovery` together describe what *this* command did and left.
- `s.skippedWorktrees` is in-memory and drained once — `recovery` must be built
  at the same drain site (`skippedWorktreeNotes`) so the prose list and the
  structured list can never disagree.
- `state: "paused"` implies the caller's cwd still hosts the rebase; `argv`
  `cwd` therefore stays omit-able for same-directory continuation.

## Examples

`plans/design/015-recovery-examples.json` — eight scenarios: clean success,
dirty remote worktree skip, current-worktree paused conflict, continuation
conflicting again, cross-worktree conflict rolled back, partial earlier
progress, uninitialized repo, and a worktree path with escaped control bytes.
Each object: `scenario`, `command`, `stdout`, `stderr`, `exitCode`, plus
`_proposed` markers on every field that does not exist today.

## Acceptance

`plans/design/015-recovery-acceptance.md` maps each example to the future test
locations — engine (`internal/stack/engine_test.go`/`restack_test.go` next to
`TestOntoConflictRecordsPendingReparentWithoutChangingParent`), adapter
(`cmd/commands_json_test.go` next to `TestContinueJSONAfterConflict`,
`TestStatusJSONSurfacesConflict`, and the worktree aggregate tests), and e2e
(`e2e/e2e_contract_test.go` next to `TestSubmitPartialFailureJSON`) — with the
exact assertions, including the unchanged legacy fields.

## Deferred work

- Persisted operation state (a durable "recovery ledger") — explicitly out of
  scope; the contract stays derivable from live git/state per command.
- Automatic resolution (`st` running `git stash`/commit in a dirty worktree) —
  never silently; a future explicit `st rescue` verb could consume `action`
  descriptors, but this contract only *describes* next actions.
- Extending `recovery` to non-mutating commands or to `submit`'s per-ref push
  outcomes (plan 008 already structured those under `pushed`/`failed` — reuse
  rather than merge).
- Daemon/remote orchestration service.
- Estimated implementation split for a future plan: (1) `stack` — return
  structured skips instead of draining to prose only (still emitting `notes`);
  (2) `cmd` — render `recovery` on results and envelopes, allowlist in strict
  decoders; (3) tests per the acceptance map; (4) docs/AGENT.md + CHANGELOG.

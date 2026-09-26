# Design 015 acceptance: example → future test mapping

Each scenario in `015-recovery-examples.json` maps to concrete future test
locations. Legacy fields (`summary`, `restacked`, `notes`, `error.code`,
`error.branch`, `error.onto`, `aborted`) must stay byte-identical to today's
output; assertions below name the NEW fields only where `proposed` lists them.

Conventions: engine tests use the fake git (`newEnvState`, `mkBranch`,
`conflictOn`/worktree-owner fakes); adapter tests drive `runRestack`/`runSync`/
`runContinue` with `captureStdout`/strict JSON decoders (which must allowlist
`recovery` when implementation lands); e2e drives the real binary.

| scenario | future test (file · next to) | key assertions |
|---|---|---|
| clean_success | `cmd/commands_json_test.go` · `TestLogJSONTrunkOnly` family / a new `TestRestackJSONOmitsRecoveryWhenEmpty` | `recovery` key ABSENT on success (not `[]`/`null`); `summary`/`restacked` unchanged |
| dirty_remote_worktree_skip | engine: `internal/stack/engine_test.go` new `TestRestackReportsStructuredSkip` (fake owner worktree dirty) → `OpResult` carries a structured skip list parallel to `notes`; adapter: `TestRestackSkippedWorktreeRecoveryJSON` → `recovery[0]` = `{branch:"feat-c",reason:"worktree_dirty",worktree:<exact>,state:"skipped",action.kind:"manual"}` and `notes` still contains the prose string | |
| current_worktree_paused_conflict | adapter: `cmd/commands_json_test.go` new `TestRestackConflictEnvelopeCarriesRecovery` next to `TestContinueJSONAfterConflict` | stderr envelope keeps `error.{code:conflict,branch,onto}`; `stderr.recovery[0]` = `{branch,reason:"conflict_paused",state:"paused",action:{kind:"argv",argv:["st","continue"]}}`; `restacked` on envelope lists prior progress; exit 2 |
| continuation_conflicts_again | adapter: extend `TestContinueJSONAfterConflict` re-stall arm | envelope unchanged except `recovery[0]` with `action.argv == ["st","continue"]`; `error.branch` = re-stalled branch; exit 2 |
| cross_worktree_conflict_rolled_back | engine: `internal/stack/restack_test.go`/`worktree_test.go` fake `RebaseOntoIn` failure + `RebaseAbortIn` → error is non-ConflictError; adapter: new `TestRestackCrossWorktreeConflictEnvelope` | `error.code == "error"` (exit 1, NOT 2); `error.branch` present; `recovery[0]` = `{reason:"conflict_rolled_back",state:"rolled_back",worktree:<path>,action:{kind:"manual"}}` — `action.argv` MUST be absent (not locally resumable) |
| partial_earlier_progress | e2e: `e2e/e2e_contract_test.go` new `TestRestackPartialProgressEnvelope` next to `TestSubmitPartialFailureJSON` | real repo: feat-d dirty worktree + feat-c conflict → exit 2; stderr `restacked` lists [feat-a,feat-b]; `recovery` carries BOTH entries in cascade order (paused before skipped… exact order = outcome order); legacy `error` fields intact |
| not_initialized | adapter: extend existing `not_initialized` envelope tests | `recovery` absent; exit 3; `error.code == "not_initialized"` |
| escaped_path_bytes | adapter: worktree path with `\n` in cmd test (plan 012 keeps bytes exact) | `recovery[0].worktree` decodes to exact bytes incl. `\n`; `notes` text uses sanitized path; terminal run escapes control bytes |

## Invariants every implementation must hold

1. `recovery` is omitted entirely when empty — never emitted as `[]` or `null`.
2. `action.kind` is `argv` only when the named command is known resumable
   (`state:"paused"` in the caller's worktree). `state:"rolled_back"` and
   `state:"skipped"` always carry `kind:"manual"`.
3. `action.argv`/`cwd` are arrays/strings of raw bytes — JSON-escaped by
   `encoding/json`, never pre-sanitized; the `run:`-hint quoting rules from
   plan 013 apply only to terminal text, not these fields.
4. Reason codes come only from the registry in the design doc; new reasons
   require a new plan.
5. `notes` wording is unchanged — human prose may evolve independently.
6. Entries never instruct `st` to auto-execute anything; descriptors are data.

## Validator cross-references

`check-recovery.py` asserts: all eight scenarios exist; every `recovery[]`
entry uses a registered reason; `argv`/`cwd` shapes; `proposed` lists only
paths that actually appear in the fixture payload; acceptance table covers
every scenario name. It validates consistency — not runtime truth.

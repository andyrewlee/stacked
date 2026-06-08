# Next steps — `stacked` improvement plan

> Produced by a deep multi-agent code-quality audit of this repo: **6 dimensions, 45 findings, 43 confirmed** after a second adversarial-verification pass (each high-severity finding was re-checked against the source by an independent skeptic agent).

Every item is scoped to land as **one focused, reviewable increment in a stacked diff** that leaves `make ci` green. The plan is ordered: real robustness bugs first, then the agent-facing contract, then closing the self-verification loop, then test coverage, then maintainability refactors, then docs.

## How to read this

- **Stacked plan** is the work, in order. Each PR stacks on the one before it (`st create <branch>`), is small enough to review in one sitting, and must leave `make ci` green before it lands.
- **↳ Verifies** on each PR is the *exact* command an agent runs to prove the change works — the loop is closed before the PR is done.
- **Addresses** maps each PR to confirmed finding ids; the **Findings** appendix is the evidence behind every one.

## Overview

A prioritized sequence of 11 stacked PRs that each leave `make ci` green and build on the prior one. The sequence front-loads real robustness bugs (atomic state writes, panic recovery, fold ordering), then closes agent-contract gaps (stdout/stderr discipline, JSON envelope on unknown command, help/usage consistency), then plugs the self-verification holes (enforce the stdlib-only invariant, fix the inner-loop target, add a golden regen target), then adds the missing tests (dirty-guards, sync-conflict e2e, validate categories, quiet-rebase, amend-message), and finally lands the maintainability refactors (a shared mutateEnv so sync/repair stop copying the undo protocol, a finishUpstack tail helper, dead-code removal) plus the docs corrections. Foundational changes (the atomicWriteFile helper in PR1, the test-harness-only PRs) come before the PRs that depend on them. Every PR maps to confirmed finding ids and names the exact command an agent runs to prove it.

**Themes**

- Durability of on-disk state: route every metadata write (state.json, undo.json) through one atomic temp+rename helper and make a corrupt undo journal self-heal instead of bricking all mutations
- Agent-facing failure contract: never crash with the conflict exit code, never put human help/usage on stdout, always emit the JSON error envelope when --json is requested
- Close the self-verification loop: the explicitly-stated top invariant (standard-library only, zero module requires) and the documented JSON/exit-code contract must be checked by make ci, and the inner-loop target must match its own description
- Cover the destructive and agent-only paths that currently have zero tests: dirty-tree guards on reparent/reset ops, a real mid-sync conflict, quiet-rebase under --json, amend-message, and validate's drift categories
- De-duplicate the trickiest invariant in the tool (the undo/save protocol) and the repeated restack+restore-HEAD tail so a single fix can no longer silently miss a copy
- Make the documented discovery surface (help/usage strings and AGENT.md result shapes) match what the binary actually does

## Audit summary

| Dimension | Findings | State |
|---|---|---|
| **Engine correctness & bugs** | 8 | The stack engine is well-structured and notably defensive: mutations checkpoint via env.save() after each restack step, sentinel errors (ErrConflict/ErrDirty/ErrNotInitialized) are… |
| **CLI layer & agent contract** | 8 | The agent contract is mostly solid and genuinely thoughtful: all 27 subcommands (every one except the documented `completion`) accept and honor `--json`, success payloads are struc… |
| **Testability & coverage** | 10 | The suite is unusually well-layered: a fast pure-engine layer over an in-memory fake git (internal/stack/*_test.go), a real-git port suite (internal/git/git_test.go), thin-adapter … |
| **Maintainability & ease-of-change** | 7 | The codebase is in good maintainability shape overall: the port/engine/adapter split is real and disciplined, the engine carries zero CLI concerns (no printing/color/flag handling … |
| **Closed-loop self-verification** | 6 | The loop is in good shape on its spine: `make ci` (fmt-check + lint + vet + build + cover) is genuinely the single gate, the pre-push hook and CI both run exactly that target, the … |
| **Docs ↔ code drift** | 6 | The documentation set (README, CLAUDE, CONTRIBUTING, docs/AGENT, CHANGELOG, help.golden) is largely accurate and well-maintained: all 27 registered commands match the README table … |

**Severity after verification:** high 2 · medium 20 · low 21

## Stacked plan

### PR 1 — Make all metadata writes atomic and self-heal a corrupt undo journal
`atomic-state-writes-and-undo-self-heal` · effort **S — small** · base of the stack · addresses `ENG-1`, `ENG-2`

**Goal.** A crash or full disk mid-write can never truncate state.json or undo.json, and a corrupt undo.json no longer aborts every future mutating command.

**Why now.** Addresses the highest-impact durability bugs (ENG-1, ENG-2). ENG-1 is foundational: a truncated undo.json currently bricks create/modify/restack/fold/squash/onto/delete/sync/track/untrack/rename because mutate() calls RecordUndo->loadUndo first. The atomicWriteFile helper introduced here is reused by later PRs, so it must land first.

**Changes**
- In internal/stack/store.go, extract the temp-file+rename body of State.Save (lines 92-114) into a package helper func atomicWriteFile(path string, data []byte) error, and have Save call it.
- In internal/stack/undo.go writeUndo (line 66), replace os.WriteFile(path, append(data,'\n'), 0o644) with atomicWriteFile(path, append(data,'\n')).
- In internal/stack/undo.go RestoreState (line 183), replace os.WriteFile(path, raw, 0o644) with atomicWriteFile(path, raw).
- In internal/stack/undo.go loadUndo (lines 47-50), on json.Unmarshal failure do NOT return a hard error: discard the unparseable journal and return nil, nil (treat as empty) so RecordUndo and st undo recover automatically instead of aborting.
- Add a brief comment on loadUndo explaining that a corrupt journal is recoverable state, not a fatal error.

**Files:** `internal/stack/store.go`, `internal/stack/undo.go`, `internal/stack/undo_test.go`, `internal/stack/store_test.go`

**Tests**
- internal/stack/undo_test.go: write garbage bytes to undo.json, assert loadUndo returns nil entries and no error, and that a subsequent RecordUndo succeeds and produces a single valid entry.
- internal/stack/undo_test.go: after writeUndo, assert no leftover temp files remain in the stacked dir (atomic path cleans up).
- internal/stack/store_test.go: assert atomicWriteFile leaves the target unchanged on the normal path and that RestoreState round-trips bytes with a trailing newline.

**↳ Verifies (closes the loop):** make test-fast (runs go test ./internal/... -count=1) — the new undo_test/store_test cases must pass; then make ci stays green.

---

### PR 2 — Recover from panics into a structured error with an exit code that is not the conflict code
`panic-recovery-distinct-exit-code` · effort **S — small** · stacks on PR #1 · addresses `CLI-2`

**Goal.** Any panic in an engine or adapter path surfaces as a generic structured failure (JSON envelope when --json) with a distinct exit code, instead of a raw stack trace and exit 2 (which agents read as a resolvable conflict).

**Why now.** Addresses CLI-2 (the panic finding): Go's default panic exit code is 2, which exitCode() reserves for ErrConflict per docs/AGENT.md; an agent would run st continue in response to a crash. This is active mis-recovery in the core agent-driving contract, so it lands right after the durability fixes.

**Changes**
- In cmd/root.go Execute(), add at the top a deferred recover that, on a non-nil recover value, calls renderError(fmt.Errorf("internal error: %v", r), jsonRequested(os.Args[1:])) and sets the return code to a distinct value (e.g. 70).
- Convert Execute() to use a named return value (func Execute() (rc int)) so the deferred recover can override the exit code; adjust the existing explicit returns accordingly.
- Document the new exit code (70, unexpected internal error) in docs/AGENT.md's exit-code table.
- Ensure the panic path still emits the {error:{code:"error",message}} envelope on stderr in --json mode (renderError already handles this).

**Files:** `cmd/root.go`, `cmd/execute_test.go`, `docs/AGENT.md`

**Tests**
- cmd/execute_test.go: register a temporary command whose Run panics, drive dispatch, and assert the returned code is the panic code (not 2) and no panic escapes.
- cmd/execute_test.go: same panic with --json asserts a parseable {error:{code,message}} envelope on stderr.

**↳ Verifies (closes the loop):** go test ./cmd/... -run Panic -count=1 (and make ci) — the panic test asserts exit code != 2 and a clean envelope.

---

### PR 3 — Reorder Fold so the parent ref advances only after checkout and delete succeed
`fold-advance-parent-last` · effort **M — medium** · stacks on PR #2 · addresses `ENG-4`

**Goal.** A checkout or delete failure mid-fold can no longer leave git's parent silently advanced over cur's commits while state.json still tracks cur on the old parent.

**Why now.** Addresses ENG-4: ForceBranch(parent, curTip) currently runs first (engine.go:268) and is durable, but env.save() is last (line 281); a Checkout/DeleteBranch failure returns without rollback and mutate() never saves, so git and metadata diverge. Surgical reordering, no new harness needed.

**Changes**
- In internal/stack/engine.go Fold (lines 264-283), advance the parent ref as the LAST git mutation: run g.Checkout(parent) and g.DeleteBranch(cur,true) first, then g.ForceBranch(parent, curTip); reorder the in-memory re-parenting/Untrack so disk and git move together.
- If git ordering forbids checking out before the ref moves, instead roll back the original parent ref on the Checkout/DeleteBranch error paths; pick whichever keeps the clean precondition intact.
- Keep env.save() after the successful git mutations and before RestackUpstack.

**Files:** `internal/stack/engine.go`, `internal/stack/engine_test.go`, `internal/stack/fakegit_test.go`

**Tests**
- internal/stack/engine_test.go: drive Fold with a fake-git configured to fail DeleteBranch (add a delete-error hook to fakeGit if absent) and assert the parent ref is unchanged and the in-memory state still tracks cur — git and metadata agree on the error path.
- internal/stack/engine_test.go: happy-path Fold still advances parent to cur's tip, deletes cur, re-parents children, and persists.

**↳ Verifies (closes the loop):** make test-fast — the Fold failure-ordering test shows the parent ref unchanged on delete failure; make ci green.

---

### PR 4 — Keep diagnostics on stderr and always honor --json on the error paths
`stdout-stderr-and-json-error-discipline` · effort **M — medium** · stacks on PR #3 · addresses `CLI-1`, `CLI-4`, `CLI-7`, `CLI-8`

**Goal.** Unknown commands and flag errors never pollute stdout or skip the JSON envelope; status/bottom usage and completion's flag handling stop writing to stdout, and duplicate flag-error prints are removed.

**Why now.** Groups the agent-contract stdout/stderr defects (CLI-1, CLI-4, CLI-7, CLI-8). All four break the stdout=data / stderr=diagnostics split an agent relies on; they are small, localized, and share one theme.

**Changes**
- cmd/root.go Execute() unknown-command branch (lines 78-83): route through renderError(fmt.Errorf("unknown command %q", name), jsonRequested(args[1:])) and write a 'run st help' hint to os.Stderr only; do NOT call printHelp() (it writes the command table to stdout). Keep exit 1.
- cmd/status.go:28 and cmd/bottom.go:27-29: change fs.Usage from out(...) (stdout) to fmt.Fprintln(fs.Output(), "usage: ...") (stderr), matching the other commands.
- cmd/completion.go:25: replace fs.Parse(args) with parseFlagSet(fs, args) so flag/usage output is suppressed in JSON mode and not interleaved with the dispatcher's envelope.
- cmd/util.go parseFlagSet (or cmd/root.go): in non-JSON mode also discard the flag package's own error print (fs.SetOutput(io.Discard)) so renderError is the single stderr report, removing the duplicate 'flag provided but not defined' + 'st: ...' pair.

**Files:** `cmd/root.go`, `cmd/status.go`, `cmd/bottom.go`, `cmd/completion.go`, `cmd/util.go`, `cmd/execute_test.go`, `cmd/util_test.go`

**Tests**
- cmd/execute_test.go: unknown command with --json asserts empty stdout, exit 1, and a parseable {error:{code:"error"}} envelope on stderr.
- cmd/util_test.go: status/bottom flag-error asserts the usage line goes to stderr (fs.Output()) and stdout stays empty.
- cmd/execute_test.go: a bad-flag text-mode invocation asserts the error appears on stderr exactly once.

**↳ Verifies (closes the loop):** go test ./cmd/... -run 'Unknown|Usage|FlagError' -count=1 and make ci — assertions confirm clean stdout and a single stderr envelope/usage line.

---

### PR 5 — Make help/usage discovery work with --json and match the actual flag sets
`help-json-and-usage-string-consistency` · effort **M — medium** · stacks on PR #4 · addresses `CLI-3`, `CLI-6`, `DOC-1`, `DOC-2`, `DOC-3`

**Goal.** st help --json no longer errors, registry Usage strings list --json and missing optional args, sync's two help surfaces agree, and AGENT.md's claim about -h is accurate.

**Why now.** Groups the discovery-contract findings on the help surface (CLI-3, CLI-6, DOC-1, DOC-2, DOC-3). Together they make the documented discovery path (st help <cmd>, st <cmd> -h, AGENT.md) trustworthy for an agent that uniformly appends --json.

**Changes**
- cmd/root.go help branch (lines 67-72): strip a --json/-json token from args before treating args[1] as a command name, so st help --json and st help <cmd> --json do not look up '--json' as a command; never return exit 1 for st help --json (fall back to printHelp).
- Append ` [--json]` (and any missing optional positional such as [n]) to the registry Usage strings that omit it: cmd/abort.go:15, cmd/bottom.go:15, cmd/down.go:16, cmd/init.go:17, cmd/repair.go:16, cmd/submit.go:17, cmd/undo.go:18, cmd/validate.go:18 (CLI-6 / DOC-2).
- cmd/sync.go:16 (Command.Usage): add ` [--dry-run]` to match the fs.Usage closure at sync.go:32 (DOC-3); add a --dry-run mention to the README sync detail section.
- Resolve DOC-1 by softening docs/AGENT.md:84 to say st <command> -h prints the usage line (which names the flags) and st help <command> for the summary (surgical), rather than adding fs.PrintDefaults() to all 21 commands.
- Regenerate cmd/testdata/help.golden if printHelp output changed.

**Files:** `cmd/root.go`, `cmd/abort.go`, `cmd/bottom.go`, `cmd/down.go`, `cmd/init.go`, `cmd/repair.go`, `cmd/submit.go`, `cmd/undo.go`, `cmd/validate.go`, `cmd/sync.go`, `docs/AGENT.md`, `README.md`, `cmd/testdata/help.golden`

**Tests**
- cmd/execute_test.go: help --json dispatch asserts exit 0 and the command table on stdout (no 'unknown command').
- cmd integration test asserting helpForCommand output for down/bottom/sync now contains '--json' and (for sync) '--dry-run'.
- Update the help golden if changed.

**↳ Verifies (closes the loop):** go test ./cmd/... -run 'Help|Golden' -count=1 and make ci — help --json exits 0 and per-command usage strings include the flags.

---

### PR 6 — Enforce the stdlib-only invariant in CI and make the inner-loop/golden targets honest
`enforce-stdlib-only-and-fix-loop-targets` · effort **S — small** · stacks on PR #5 · addresses `LOOP-1`, `LOOP-2`, `LOOP-6`

**Goal.** make ci fails if go.mod gains a require entry or a go.sum appears; make test-fast is the genuinely fast fake-git-only loop its docs describe; and golden regeneration is a discoverable make target.

**Why now.** Addresses the loop/self-verification gaps (LOOP-1 high, LOOP-2, LOOP-6). LOOP-1 is the project's #1 invariant checked nowhere; wiring a tidy-check into ci makes the closed loop actually guard it. Independent of code behavior, so it slots before the test-coverage PRs.

**Changes**
- Makefile: add a tidy-check target asserting module purity, e.g. test ! -s go.sum && ! grep -qE '^[[:space:]]*require' go.mod (optionally copy go.mod aside, run go mod tidy, diff, restore). Wire tidy-check into the ci: chain (Makefile:16) so hooks and CI both inherit it.
- Makefile test-fast (line 44): point it at the pure engine only — go test ./internal/stack/... -count=1 — so it is sub-second with no real-git spawning, matching the comment at lines 41-42 and CLAUDE.md/CONTRIBUTING.md. Add a separate target (e.g. test-git: go test ./internal/git/... -count=1) or fold internal/git into test so its real-git wrapper tests still run in the full gate.
- Makefile: add golden: (and/or update-golden:) running go test ./cmd -run Golden -update; reference make golden in CONTRIBUTING.md and CLAUDE.md.
- Update the .PHONY list for the new targets.

**Files:** `Makefile`, `CLAUDE.md`, `CONTRIBUTING.md`

**Tests**
- No Go test; the verification is the make targets themselves.
- Optional: a short echo in tidy-check so the loop result is observable.

**↳ Verifies (closes the loop):** make tidy-check (passes on the clean tree, and must fail if a require line is added — verify by temporarily adding one); make test-fast completes sub-second over ./internal/stack; make ci stays green.

---

### PR 7 — Add fake-git tests for the untested dirty-tree guards and the random-model mutators
`dirty-guard-and-engine-coverage-tests` · effort **M — medium** · stacks on PR #6 · addresses `TEST-1`, `TEST-3`

**Goal.** A regression that removed or reordered requireClean in Fold/Squash/Onto/Sync, or that broke metadata bookkeeping in Squash/Rename/Track/Untrack/conflict-continue, is caught by the fast suite.

**Why now.** Addresses TEST-1 (four destructive guards untested) and TEST-3 (random model covers only 6 mutators). Pure additive engine tests over the existing fake git; lands after the bug fixes so the new tests assert the corrected behavior (e.g. Fold ordering from PR3).

**Changes**
- internal/stack/engine_test.go: add a table test over the requireClean mutators (Fold, Squash, Onto, Sync) that sets f.clean=false and asserts each returns stack.ErrDirty and mutates neither branch refs nor metadata, mirroring TestDeleteRequiresCleanTreeBeforeMutation.
- internal/stack/model_test.go: extend runModel's switch (6 cases at line 32) with Squash, Rename, Track/Untrack, and a conflict-then-Continue case (set f.conflictOn before a restack-inducing op, then drive Continue), plus a Sync-against-advanced-trunk case; keep checkInvariants after each step.
- If model_test lacks a Sync-capable Remote, add a minimal fake Remote so Sync can run in the model.

**Files:** `internal/stack/engine_test.go`, `internal/stack/model_test.go`, `internal/stack/fakegit_test.go`

**Tests**
- TestMutatorsRequireCleanTree (parametrized over Fold/Squash/Onto/Sync) asserting ErrDirty and no mutation.
- Extended runModel exercising Squash/Rename/Track/Untrack/Continue/Sync with invariant checks after each random op.

**↳ Verifies (closes the loop):** make test-fast — the dirty-guard table and extended model run thousands of ops with invariants holding; make ci green.

---

### PR 8 — Add e2e coverage for mid-sync conflicts, quiet-rebase under --json, amend-message, and validate categories
`e2e-sync-conflict-and-contract-coverage` · effort **M — medium** · stacks on PR #7 · addresses `TEST-2`, `TEST-5`, `TEST-6`, `TEST-7`, `LOOP-3`

**Goal.** The most dangerous real-git paths an agent hits — a conflict during sync, a real rebase in --json mode, amend changing the message, and validate's drift diagnostics — are driven end to end and their exit codes/JSON shapes asserted.

**Why now.** Groups the real-git/contract coverage gaps (TEST-2, TEST-5, TEST-6, TEST-7, LOOP-3). These can only be exercised against real git in e2e and cover paths (QuietShell rebase, AmendMessage, the sync ErrConflict branch, validate problems) that are currently 0% covered.

**Changes**
- e2e/e2e_test.go: add TestSyncConflictContinue — advance trunk so an upstack branch conflicts on restack during st sync; assert exit 2, a real rebase-merge dir exists, the merged branch was still pruned (prune persisted before the conflict), then st continue reconciles and st validate passes.
- e2e/e2e_test.go: add a --json rebase case — st modify -a --json over a 2-deep stack — asserting the JSON OpResult and that descendants actually rebased (covers QuietShell.RebaseOnto / RebaseOntoQuiet, TEST-6).
- e2e or cmd: add st modify -m "reworded" on a tracked branch with a staged change, asserting the top commit subject changed and descendants restacked (covers AmendMessage, TEST-7).
- cmd integration tests: construct each malformed state.json (cycle, missing trunk, untracked parent, ghost parent ref) and assert validate's specific problem string, non-zero exit, and the --json {ok:false, problems:[...]} shape (TEST-5).
- e2e/e2e_test.go: add a table mapping each documented exit code (0/1/2/3/4) to a triggering scenario and assert the code, plus assertions on validate --json and submit --json shapes so doc drift fails the gate (LOOP-3).

**Files:** `e2e/e2e_test.go`, `cmd/validate_test.go`, `cmd/commands_test.go`, `cmd/testdata`

**Tests**
- TestSyncConflictContinue, the --json modify-rebase e2e, the modify -m e2e/cmd test, the four validate-category cmd tests, and the exit-code-table e2e test.

**↳ Verifies (closes the loop):** make e2e (go test ./e2e/... -count=1) for the e2e cases and go test ./cmd/... -run Validate -count=1 for the validate cases; make cover confirms QuietShell.RebaseOnto / AmendMessage / the sync conflict branch are no longer 0%.

---

### PR 9 — Derive completeness checks from the registry and cover the lock-contention and per-command-help paths
`registry-derived-completeness-and-lock-help-tests` · effort **M — medium** · stacks on PR #8 · addresses `TEST-8`, `TEST-9`, `TEST-10`

**Goal.** The 'every command shows up in help' assertion can no longer silently miss a registered command, and the cross-process lock rejection plus per-command help are exercised by tests.

**Why now.** Groups the remaining testability gaps (TEST-8, TEST-9, TEST-10). TEST-8 found the e2e command slice already drifted (guide missing); deriving from the registry fixes it permanently. Small, additive.

**Changes**
- Expose the registered command names from cmd (an exported helper the tests can read) and rewrite e2e/e2e_test.go TestHelpListsAllCommands to assert the help output contains exactly the registry names plus help/version, instead of the hardcoded 28-item slice; add guide immediately as a stopgap.
- internal/stack: add a test (guarded to the flock build) that acquires stack.Lock() then asserts a second stack.Lock() returns the 'another st command is running' error (skip on the no-op lock_other build).
- cmd: add a test invoking one subcommand with -h and asserting helpForCommand/usage output, covering cmd/root.go helpForCommand (currently 0%).
- e2e/e2e_test.go: add a git worktree add scenario asserting both worktrees see the same stacked state and the lock serializes them; add an e2e that aborts a conflict then st undo restores the pre-mutation tip and st validate passes (TEST-10).

**Files:** `e2e/e2e_test.go`, `cmd/root.go`, `cmd/commands_test.go`, `internal/stack/lock_test.go`

**Tests**
- Registry-derived help-completeness test; lock double-acquire test; helpForCommand -h test; worktree-shared-state and abort-then-undo e2e tests.

**↳ Verifies (closes the loop):** make test (go test ./cmd/... ./internal/... -race) for the lock/help/registry tests and make e2e for the worktree/undo journeys; make ci green.

---

### PR 10 — Route sync and repair through one mutate path and extract the repeated restack-restore tail
`unify-undo-protocol-and-engine-tails` · effort **M — medium** · stacks on PR #9 · addresses `CLI-2`, `CLI-6`, `ENG-1`

**Goal.** The undo/save protocol lives in exactly one place (mutate), sync no longer copies it, and the four engine ops share one finishUpstack helper for the conflict-vs-success tail.

**Why now.** Addresses the maintainability findings now that behavior is locked by the new tests: CLI-2-quality (sync/repair duplicate mutate), CLI-6-quality (mutate cannot express remote ops), and ENG-1-quality (repeated RestackUpstack+restoreHEAD tail). The PRs 7-9 tests guard these refactors against regressions.

**Changes**
- cmd/util.go: add mutateEnv(label string, asJSON bool, env stack.Env, op func(stack.Env,*stack.State)(*stack.OpResult,error)) (or widen stack.Env in internal/stack/git.go to carry an optional Remote) so a remote-using op fits the shared path; implement mutate() in terms of it.
- cmd/sync.go: delete the verbatim RecordUndo/PeekUndo/cleanupNoopUndoOnError/Save/finalizeUndoOnSuccess body (lines 60-83) and call mutateEnv with a closure that closes over git.RemoteShell{} and calls stack.Sync.
- cmd/repair.go: optionally convert the repair body into an engine op stack.Repair(env, s) and call it via mutate('repair', ...); at minimum keep repair as the single other undo-primitive consumer and document why it uses the simpler Trim/Drop idiom.
- internal/stack/engine.go: extract func finishUpstack(env Env, s *State, anchor string) ([]string, error) that runs RestackUpstack, applies restoreHEADAfterNonConflict on error, env.save() and restoreHEAD on success; replace the four identical tails at engine.go ~184, ~285, ~356, ~430 with rebased, err := finishUpstack(env, s, anchor).

**Files:** `cmd/util.go`, `cmd/sync.go`, `cmd/repair.go`, `internal/stack/git.go`, `internal/stack/engine.go`, `internal/stack/engine_test.go`, `cmd/commands_test.go`

**Tests**
- Reuse the extended model test and sync conflict e2e from PRs 7-8 as the regression oracle; add a cmd test asserting sync still records and finalizes undo (st undo after a clean sync restores prior tips).
- internal/stack/engine_test.go: a focused finishUpstack test asserting the conflict path leaves HEAD un-restored and the success path restores it.

**↳ Verifies (closes the loop):** make ci — the full gate (model invariants, the sync conflict e2e, undo cmd tests from earlier PRs) proves the de-duplicated protocol and finishUpstack preserve behavior.

---

### PR 11 — Make inferParent deterministic, drop dead code, and correct the remaining doc mismatches
`deterministic-infer-parent-cleanup-and-doc-fixes` · effort **S — small** · stacks on PR #10 · addresses `ENG-5`, `CLI-4`, `DOC-4`, `DOC-5`, `DOC-6`

**Goal.** track infers a stable parent on non-linear histories, unused exports are gone, and AGENT.md/README accurately describe the checkout/result shapes and log glyphs.

**Why now.** Bundles the low-severity correctness, dead-code, and docs findings (ENG-5, CLI-4-quality, DOC-4, DOC-5, DOC-6) into one tidy closing PR after the structural work. Each is small and independently verifiable.

**Changes**
- internal/stack/engine.go inferParent (lines 816-853): iterate candidates in sorted order (sort the names slice before the loop) and/or add a topology-defined tie-breaker (prefer the ancestor with the latest merge-base / longest path) so the choice is no longer Go-map-iteration-order dependent (ENG-5).
- internal/git/git.go: delete the unused git.Push wrapper (lines 345-350) and update its tests to call PushRemote('origin', ...); cmd/color.go:27: remove the unused ansiCyan constant (CLI-4-quality).
- README.md:161: change the log glyph description from solid/hollow large circles to the small filled/hollow circles emitted by cmd/log.go, matching the README's own example (DOC-4).
- docs/AGENT.md:60: add the with-name checkout shape { "branch", "switched": bool } (DOC-5).
- docs/AGENT.md:50-52: note that restacked/deleted/notes/branch/dryRun are omitempty (absent when empty), not always-present [] (DOC-6).

**Files:** `internal/stack/engine.go`, `internal/git/git.go`, `internal/git/git_test.go`, `cmd/color.go`, `README.md`, `docs/AGENT.md`, `internal/stack/engine_test.go`

**Tests**
- internal/stack/engine_test.go: an inferParent determinism test — run it repeatedly over a fixed candidate set and assert a stable result (the single-parent fake cannot model two sibling ancestors, so assert stability of the chosen parent across runs).
- internal/git/git_test.go: switch the former Push tests to PushRemote and assert they still pass.
- go vet / the linter's unused check must pass with ansiCyan and git.Push removed.

**↳ Verifies (closes the loop):** make ci — go vet plus the linter's unused check confirm the dead code is gone and the build is green; the inferParent test asserts a stable parent across runs.

---

## Findings (confirmed, by dimension)

Severity shown as `claimed→adjusted` where the verification pass re-rated it.

### Engine correctness & bugs

- **ENG-1 · Undo journal uses non-atomic writes and a corrupt undo.json bricks every future mutation** — _high→medium · bug_  
  `internal/stack/undo.go:54-67, 35-52, 73-101; cmd/util.go:106-107`
  - **Impact:** A crash or full disk mid-write leaves a truncated undo.json. From then on RecordUndo fails, so EVERY mutating command (create/modify/restack/fold/squash/onto/delete/sync/track/untrack/rename) aborts before doing anything, with no automatic recovery. The user must know to manually delete .git/stacked/undo.json. The journal is the one piece of state that, when corrupt, takes down the whole tool.
  - **Fix:** Write undo.json through the same atomic temp+rename path as Save() (factor out an atomicWriteFile helper). In loadUndo, treat an unparseable journal as recoverable: log/warn and reset to an empty journal rather than returning a hard error that blocks all mutations.
- **ENG-2 · undo's RestoreState rewrites state.json non-atomically, risking corruption of the primary state file** — _medium · bug_  
  `internal/stack/undo.go:172-184; cmd/undo.go:130-132`
  - **Impact:** A crash during `st undo` while overwriting state.json can leave the primary state file truncated/half-written with no .tmp fallback, which Load() then fails to parse — a worse outcome than the operation undo was trying to reverse.
  - **Fix:** Route RestoreState through the same atomic temp+rename writer used by Save().
- **ENG-4 · Fold advances the parent ref before persisting, leaving a git-mutated/state-unsaved window** — _medium · correctness_  
  `internal/stack/engine.go:268-283`
  - **Impact:** On a checkout/delete failure mid-fold, the repository and the persisted metadata disagree: parent has silently absorbed cur's commits in git, but state.json still lists cur as a separate tracked branch on the old parent SHA. Recovery relies on the user running `st undo` (the snapshot does restore parent's ref), but nothing nudges them to; a naive retry of `st fold` would re-derive from inconsistent metadata.
  - **Fix:** Advance the parent ref as the LAST git mutation (after checking out parent and deleting cur), or persist state immediately after ForceBranch so disk and git move together; alternatively roll back the ForceBranch on the checkout/delete error path.
- **ENG-5 · inferParent is non-deterministic for branches with incomparable tracked ancestors** — _low · correctness_  
  `internal/stack/engine.go:816-853`
  - **Impact:** For non-linear histories (a branch whose two tracked ancestors are siblings of a merge), `st track` infers a parent that varies run-to-run, producing inconsistent stack metadata and an unstable ParentSHA base. Linear stacks (the common case) are unaffected because the chain comparison resolves deterministically.
  - **Fix:** Iterate candidates in sorted order for determinism, and/or pick the ancestor with the latest merge-base / longest path to make the choice topology-defined rather than iteration-order-defined.
- **ENG-6 · No cross-process serialization on non-flock platforms allows lost state updates** — _low · gap_  
  `internal/stack/lock_other.go:7-9; internal/stack/lock_unix.go:18-43`
  - **Impact:** On platforms without the flock implementation, two concurrent `st` mutations interleave their load→mutate→save with no serialization. state.json writes are individually atomic (temp+rename), so files are never torn, but a classic lost-update can silently discard one command's metadata changes; the undo journal (non-atomic, ENG-1) is even more exposed to interleaving.
  - **Fix:** Provide a fallback lock for non-flock platforms (e.g. an O_CREATE|O_EXCL lockfile with PID and staleness handling) instead of a pure no-op, or at least document the loss-of-serialization risk in the no-op path.
- **ENG-8 · RecordUndo bypasses the Git port and calls the concrete git package directly** — _low · quality_  
  `internal/stack/undo.go:73-101`
  - **Impact:** Violates the stated pure-engine-behind-ports architecture: the snapshot's ref set is taken from real git rather than the port, so the undo layer cannot be exercised against the in-memory fake and would capture wrong refs if the engine were ever driven against a non-default git. Works correctly today only because RecordUndo is reached solely from the CLI where the real git is present.
  - **Fix:** Thread the Git port (and a LocalBranches method) into RecordUndo so the undo snapshot is taken through the same port as the rest of the engine.

### CLI layer & agent contract

- **CLI-1 · Unknown command emits human help to stdout and no JSON envelope, even with --json** — _high→medium · agent-ux_  
  `cmd/root.go:78-83`
  - **Impact:** An agent that always passes --json and parses stdout as JSON gets a wall of human help text and a JSON-parse failure on a simple typo, instead of the documented stderr envelope it gets for every other error. The unknown-command path bypasses renderError() entirely.
  - **Fix:** Route unknown commands through renderError(err, jsonRequested(args[1:])) with errorCode "error" (exit 1), and write the help suggestion to stderr only. Do not print the command table to stdout on the error path.
- **CLI-2 · No panic recovery: a panic crashes to stderr with a stack trace and exit code 2, colliding with the conflict code** — _high · correctness_  
  `cmd/root.go:58-93 (Execute), cmd/st/main.go:13-15`
  - **Impact:** Two contract violations at once: (a) no `{error:{code,message}}` envelope is emitted on a panic, despite the agent contract promising structured failures; (b) Go's panic exit code is 2, which `exitCode()` reserves for `ErrConflict` ("rebase conflict in progress") per AGENT.md — so an agent will misinterpret a crash as a resolvable conflict and run `st continue`, compounding the failure.
  - **Fix:** Add `defer func(){ if r := recover(); r != nil { renderError(fmt.Errorf("internal error: %v", r), jsonRequested(os.Args[1:])); os.Exit(70) } }()` at the top of Execute() (or in main before os.Exit), using a distinct exit code that is NOT 2/3/4, so panics surface as a structured generic error.
- **CLI-5 · `submit --json` returns two incompatible result shapes (trunk vs non-trunk)** — _medium→low · correctness_  
  `cmd/submit.go:65-72 vs 115-120`
  - **Impact:** An agent must conditionally branch on which keys are present to read the same command's output; a fixed struct unmarshal will silently drop fields. The result schema is not stable across the command's own outcomes.
  - **Fix:** Unify into one struct that always contains remote, dryRun, pushed, repoURL (omitempty), and an optional summary, so a single shape covers both the trunk and non-trunk cases.
- **CLI-3 · `st help --json` and `st --help --json` are unusable for headless help discovery** — _medium · agent-ux_  
  `cmd/root.go:66-76,97-110`
  - **Impact:** An agent that uniformly appends --json cannot discover commands or per-command help in machine form. Help/guide are the documented discovery surface (AGENT.md 'Discoverability'), but they have no JSON mode and actively error when --json is appended.
  - **Fix:** Either make help/version accept and ignore --json gracefully (strip it before treating remaining args as a command name) or emit a JSON help payload when --json is present. At minimum, do not return exit 1 for `st help --json`.
- **CLI-4 · `status` and `bottom` write their usage line to stdout on flag errors, polluting the data stream** — _medium · agent-ux_  
  `cmd/status.go:28, cmd/bottom.go:27-29`
  - **Impact:** For an agent in non-JSON mode that reads stdout for data and stderr for diagnostics, a flag typo injects a usage line into the data stream — exactly the stdout/stderr split the contract relies on. Inconsistent with all 25 other commands.
  - **Fix:** Change both to `fmt.Fprintln(fs.Output(), "usage: ...")` so diagnostics stay on stderr.
- **CLI-6 · 8 commands' registry Usage strings omit --json, so `st help <cmd>` under-documents the flag** — _low · docs_  
  `cmd/abort.go:16, cmd/bottom.go:15, cmd/down.go:16, cmd/init.go:17, cmd/repair.go:16, cmd/submit.go:17, cmd/undo.go:18, cmd/validate.go:18`
  - **Impact:** An agent (or human) using the documented discovery path `st help <command>` to learn the flags will not see that --json is accepted on these commands, and sees inconsistent usage between `st help <cmd>` and `st <cmd> -h`.
  - **Fix:** Append `[--json]` (and any missing optional args) to the registry Usage strings for these 8 commands so they match their flag sets and their fs.Usage closures.
- **CLI-7 · `completion --json` mixes a plain flag error, usage, and a JSON envelope on stderr** — _low · agent-ux_  
  `cmd/completion.go:23-32`
  - **Impact:** completion is documented as non-JSON, but appending --json produces three interleaved diagnostic formats on stderr, which is hard to parse. Minor because completion output is for shells, not agents.
  - **Fix:** Route completion's parse through parseFlagSet so flag/usage output is suppressed in JSON mode, or explicitly reject --json with a single clean error.
- **CLI-8 · Flag errors print to stderr twice in non-JSON mode** — _low · quality_  
  `cmd/root.go:85-91 + flag package default output`
  - **Impact:** Duplicated diagnostics on stderr for any bad-flag invocation in text mode. Parseable but noisy; an agent counting/parsing stderr lines sees the same error twice. (JSON mode is unaffected because parseFlagSet discards the flag package's own output.)
  - **Fix:** In non-JSON mode, suppress the flag package's own error print (SetOutput(io.Discard) symmetric to parseFlagSet) and rely solely on renderError, or vice-versa, so each error is reported once.

### Testability & coverage

- **TEST-1 · Dirty-tree guard untested for Fold, Squash, Onto, and Sync** — _high→medium · gap_  
  `internal/stack/engine.go:241,304,375,529 (requireClean callers); tests in internal/stack/engine_test.go`
  - **Impact:** Four state-mutating commands could lose a user's uncommitted changes if the guard regresses, with no test to catch it. These are exactly the operations (rebase/reset) where a dirty tree is most destructive.
  - **Fix:** Add fast fake-git table tests asserting Fold/Squash/Onto/Sync return ErrDirty when f.clean=false and do not mutate branch/metadata (mirror TestDeleteRequiresCleanTreeBeforeMutation). One parametrized test over all requireClean mutators keeps it cheap.
- **TEST-2 · No e2e (or any) test for a conflict that occurs mid-sync** — _high→medium · gap_  
  `internal/stack/engine.go:569-577 (Sync -> RestackAll conflict branch); e2e/e2e_test.go`
  - **Impact:** The most dangerous sync state (partial prune saved + active rebase + altered HEAD) is unverified end to end. A bug in continue/abort recovery after a sync conflict, or in the 'persist prune before restack' ordering, would ship silently.
  - **Fix:** Add an e2e test: create a stack where the trunk advances such that an upstack branch conflicts on restack during sync; assert exit code 2, a real rebase-merge dir exists, then `st continue` reconciles and `st validate` passes; also assert the merged branch was still pruned (prune persisted before the conflict).
- **TEST-3 · Random model test exercises only 6 of the mutators; Sync/Continue/Squash/Rename/conflict-continue never asserted against invariants** — _high→low · testability_  
  `internal/stack/model_test.go:30-99`
  - **Impact:** The strongest correctness oracle in the codebase (invariants over thousands of random ops) has blind spots for half the mutators, including the conflict-resume path where metadata/topology bookkeeping is most error-prone.
  - **Fix:** Extend the model switch with Squash, Rename, Track/Untrack, and a conflict-then-Continue case (set conflictOn before a restack-inducing op, then drive Continue); add a Sync-against-advanced-trunk case. Keep invariant checks after each.
- **TEST-4 · Fake git models conflicts as a manual flag and a single-parent DAG, hiding conflict-classification and merge-base-over-merges bugs** — _medium · testability_  
  `internal/stack/fakegit_test.go:11-15,60,217-252,291-302`
  - **Impact:** A regression in conflict detection (e.g. misreading rebase output, mishandling RebaseInProgress/RebaseHeadName, or merge-base on a branch containing a merge) cannot be caught by the fast suite — only by the slow e2e, if a matching scenario exists. The fake can give false confidence that conflict handling is well-covered.
  - **Fix:** Document this limitation prominently (the header partly does) and compensate by ensuring every conflict-classification branch has a dedicated e2e case (see TEST-2). Optionally let the fake model a content conflict by subject collision so 'unexpected' conflicts (not pre-flagged) flow through the engine too.
- **TEST-5 · validate's problem categories and cyclePath are almost entirely untested** — _medium · gap_  
  `cmd/validate.go:47-126 (5 problem branches + warnings), cmd/validate.go:134 cyclePath`
  - **Impact:** validate is the user's drift-diagnosis tool; four of its five diagnostics and its JSON contract could regress (wrong message, false negative on a cycle) without any failing test.
  - **Fix:** Add cmd integration tests that construct each malformed state.json (cycle, missing trunk, untracked parent, ghost parent ref) and assert the specific problem string, the non-zero exit, and the --json {ok:false, problems:[...]} shape. A cycle case is especially valuable since cyclePath has bespoke logic.
- **TEST-6 · --json mode never runs a rebase-triggering mutation: QuietShell rebase path is 0% covered** — _medium · gap_  
  `internal/git/shell.go:45-48 (QuietShell.RebaseOnto/RebaseContinue), internal/git/git.go:264 RebaseOntoQuiet; selected in cmd/util.go:37-39`
  - **Impact:** Agent/automation users run with --json; the quiet rebase code path they exclusively hit is untested. A bug in RebaseOntoQuiet (wrong flags, lost output suppression, or a divergence from the non-quiet form) would only surface in the field.
  - **Fix:** Add an e2e case that performs a real restack-inducing mutation in --json mode (e.g. amend the bottom branch with `st modify -a --json` over a 2-deep stack) and assert the JSON result plus that descendants actually rebased.
- **TEST-7 · modify -m (amend changing the commit message) and the real-git AmendMessage path are untested** — _medium · gap_  
  `internal/stack/engine.go:171 (g.AmendMessage); internal/git/git.go:247 + shell.go:25 AmendMessage (0% covered)`
  - **Impact:** Amending a commit's message in place — a core modify mode — has zero coverage at both engine and real-git layers; a regression (e.g. amend dropping staged content, or wrong -m handling) would not be caught.
  - **Fix:** Add a cmd or e2e test for `st modify -m "reworded"` on a tracked branch with a staged change, asserting the top commit subject changed and descendants restacked.
- **TEST-8 · Completeness checks (e2e command list, help golden) are hand-maintained copies of the registry and already drift** — _low · testability_  
  `e2e/e2e_test.go:376-381 (hardcoded command slice), cmd/golden_test.go:50 + cmd/testdata/help.golden`
  - **Impact:** The test meant to guarantee 'every command shows up in help' does not actually cover all commands, and the duplication makes adding a command error-prone (three places to update: code, e2e slice, golden).
  - **Fix:** Derive the expected command set from the exported registry (or add a test that asserts the e2e/help list equals the registry names) so new commands are covered automatically; at minimum add guide to the slice.
- **TEST-9 · Lock contention path and per-command help (helpForCommand) are uncovered** — _low · gap_  
  `internal/stack/lock_unix.go:32-34 (EWOULDBLOCK 'another st command is running'); cmd/root.go:97 helpForCommand (0%)`
  - **Impact:** The cross-process serialization guarantee — a headline feature — has no test proving a second concurrent st is rejected. Per-command help text can regress unnoticed.
  - **Fix:** Add a test that acquires stack.Lock() then asserts a second Lock() returns the 'another st command is running' error (skip on the no-op lock_other build). Add a cmd test invoking one command's -h and asserting its usage string.
- **TEST-10 · No end-to-end undo-after-conflict and no worktree state-sharing scenario** — _low · gap_  
  `e2e/e2e_test.go (TestUndo:788, TestConflictContinue:597); state under .git/stacked shared across worktrees`
  - **Impact:** Two documented guarantees — undo recoverability around conflicts, and one stacked state shared across worktrees with the lock serializing them — are unverified at the integration level.
  - **Fix:** Add an e2e test that adds a second worktree (`git worktree add`), runs st from both, and asserts they share state and the lock prevents concurrent mutation; add an e2e that aborts a conflict then undoes back to the pre-mutation tip and validates.

### Maintainability & ease-of-change

- **CLI-1 · runUndo is a 222-line git-graph algorithm living in a 'thin adapter'** — _high→medium · quality_  
  `cmd/undo.go:27-223`
  - **Impact:** The single hardest piece of code to change in the repo sits where it can't be unit-tested against the in-memory fake git (fakegit_test.go) — only against real git in e2e tests. Any change to undo semantics (a new mutating op, a new label like 'rename') requires editing this branch-heavy cmd function and re-reasoning about HEAD/ref restoration by hand. The package-boundary claim ('cmd are THIN adapters') is violated most severely here.
  - **Fix:** Move the reversal into the engine as stack.Undo(env, s, entry) operating through the Git port (UpdateRef/CheckoutDetach would be added to the port), returning an OpResult. Leave cmd/undo.go as a ~30-line adapter that loads the entry, calls the engine op under the lock, and renders. This makes undo testable with the fake and removes the label string-matching from the CLI layer.
- **CLI-2 · sync and repair duplicate the mutate() undo/save protocol instead of reusing it** — _high→medium · quality_  
  `cmd/sync.go:60-83, cmd/repair.go:38-114, cmd/util.go:100-124`
  - **Impact:** The undo-journal protocol — the trickiest invariant in the tool (drop no-op entries, finalize created branches, trim) — now lives in 3 places. A bug fix or change to undo semantics must be applied to mutate(), runSync, and runRepair consistently or they silently diverge. This is the single biggest source of copy-paste risk in the adapters.
  - **Fix:** Generalize mutate to carry the env (e.g. accept an op closure that already closes over the Remote, or add a mutateWith(label, asJSON, buildEnv, op) variant) so Sync can run through it. Convert repair into an engine operation stack.Repair(env, s) and call it via mutate('repair', ...). After this, every mutating command goes through one undo path.
- **CLI-3 · Stack logic (cycle detection, re-parenting, traversal) implemented in cmd instead of the engine** — _medium · gap_  
  `cmd/repair.go:64-103, cmd/validate.go:134-150, cmd/up.go:70-101, cmd/down.go:64-77, cmd/top.go:48-66, cmd/bottom.go:55-62`
  - **Impact:** These behaviors can't be exercised through the engine's in-memory fake git; only via real-git e2e tests, which are slower and rarer. The 'pure engine behind a port' design is partially bypassed: a meaningful fraction of the product's graph logic is in adapters, so reuse across commands and testability both suffer.
  - **Fix:** Add engine helpers/ops where logic is non-trivial: stack.CyclePath(s, name) (shared by validate and repair), and engine navigation ops like stack.NavUp/NavDown(env, s, cur, n) that return the target branch + checkout it through the port. Leave one-liner checkouts (checkout.go) as-is. Prioritize repair and cyclePath since they carry mutation/correctness weight.
- **ENG-1 · Repeated post-operation 'restack-upstack + restore-HEAD' tail across 4-5 engine ops** — _medium · quality_  
  `internal/stack/engine.go:184-192, 285-296, 356-367, 430-441, 502-514`
  - **Impact:** Each of these ~9-line tails encodes the same conflict-vs-success branching contract. A change to how a conflict leaves HEAD (or when to persist) must be edited in 4-5 spots, with the risk that one is missed — exactly the kind of subtle divergence that produces a HEAD-restore bug only on the conflict path, which is the least-tested path.
  - **Fix:** Extract a helper, e.g. func finishUpstack(env Env, s *State, anchor string) (restacked []string, err error) that runs RestackUpstack, applies restoreHEADAfterNonConflict on error, env.save, and restoreHEAD on success. Each op then ends with `rebased, err := finishUpstack(env, s, cur)`.
- **CLI-4 · Dead code: git.Push wrapper and ansiCyan are defined but never used in production** — _low · quality_  
  `internal/git/git.go:345-350, cmd/color.go:27`
  - **Impact:** Minor, but dead exports invite confusion ('which push do I call?') and the unused color constant suggests an intended-but-dropped styling. Both are trivially removable surface area.
  - **Fix:** Delete git.Push and update its tests to call PushRemote('origin', ...), or keep it only if you intend a documented default-remote convenience. Remove ansiCyan (or use it where a third color was intended).
- **CLI-5 · Inconsistent positional-arg rejection and flag-parsing idioms across adapters** — _low · quality_  
  `cmd/util.go:45-50 (rejectArgs), cmd/sync.go:37-39, cmd/up.go:34-36, cmd/down.go:38-40`
  - **Impact:** A maintainer adding a command must reverse-engineer which parse helper and which rejection style to copy; the sync.go inline duplication is a latent inconsistency (its message could drift from rejectArgs). Low severity because behavior is currently correct, but it raises the cost of every new adapter.
  - **Fix:** Make sync.go call rejectArgs('sync', fs.Args()). Add a one-line doc comment on parseArgs vs parseFlagSet ('use parseArgs when the command takes positional names that may be followed by flags; parseFlagSet otherwise') and consider a tiny parsePositional(fs, args, min, max, usage) helper to standardize the 'wrong arg count' commands (create/delete/onto/up/down/rename/untrack/checkout).
- **CLI-6 · mutate() cannot express remote-dependent or custom-undo ops — the main friction for new mutating commands** — _low · agent-ux_  
  `cmd/util.go:100-124, internal/stack/git.go:33-50`
  - **Impact:** Enumerated steps to add a new mutating command today: (a) write engine func taking (Env, *State, ...) -> (*OpResult, error); (b) add a cmd/*.go with init()/register + flag set + parseArgs/parseFlagSet + arg-rejection + a one-line mutate() call. That is genuinely thin and easy. BUT if the new op needs a remote or a non-standard undo, the author must instead clone the 24-line sync.go boilerplate, which is the documented sharp edge.
  - **Fix:** Either widen Env to optionally carry a Remote (nil for most ops) so every mutation fits mutate(), or add mutateEnv(label, asJSON, env, op) that takes a pre-built Env. Then port sync onto it (closes CLI-2 too) and document 'all mutating commands go through mutate*'.

### Closed-loop self-verification

- **LOOP-1 · Stdlib-only / zero-go.mod-requires constraint is enforced nowhere** — _high · gap_  
  `Makefile:16 (ci target); .github/workflows/ci.yml:44; .githooks/pre-commit:21; .githooks/pre-push:20`
  - **Impact:** The single most important project constraint is unverifiable by the closed loop. An agent following 'if make ci is green you can commit' (CLAUDE.md:27) can silently break stdlib-only purity. This is the textbook 'CI checks something local does not, or vice versa' trap — except here NEITHER checks it.
  - **Fix:** Add a `tidy-check` step to the `ci` chain. e.g. a target that runs `go mod tidy` against a copy and diffs, or simply asserts go.mod has no require block and go.sum is absent: `test ! -s go.sum && ! grep -q '^require' go.mod`. Wire it into Makefile:16 `ci:` so the hook and CI both inherit it.
- **LOOP-2 · `make test-fast` is not sub-second and does spawn real git, contradicting its own docs** — _medium · loop_  
  `Makefile:43-44 (test-fast); CLAUDE.md:22; CONTRIBUTING.md:9; Makefile:41-42 (comment)`
  - **Impact:** The advertised tight inner loop is ~4x slower than claimed and is NOT the pure-fake-git loop the agent is told it is. An agent iterating on engine logic pays a real-git tax on every run and may distrust the loop when timings don't match the docs.
  - **Fix:** Point test-fast at the pure engine only: `go test ./internal/stack/... -count=1` (genuinely sub-second, fake-git only), and add a separate target (or fold internal/git into `test`) for the real-git wrapper tests. Then the comment, CLAUDE.md:22, and CONTRIBUTING.md:9 become true.
- **LOOP-3 · No automated check that the JSON / exit-code contract in docs/AGENT.md hasn't drifted** — _medium · docs_  
  `docs/AGENT.md:19-64; e2e/e2e_test.go; cmd/testdata/*.golden`
  - **Impact:** An agent's entire integration is 'branch on exit code, parse JSON shape' (docs/AGENT.md:21 'do not parse messages'). If a refactor changes a field name or an exit-code mapping, make ci stays green and the doc silently lies — the worst failure mode for an agent-facing contract.
  - **Fix:** Add golden coverage for every `--json` result shape listed in docs/AGENT.md (extend cmd/golden_test.go / cmd/testdata) and one table-driven e2e test mapping each documented exit code to a triggering scenario. Optionally export the exit-code constants and assert the doc table against them. Keep these in the `cover`/`ci` path so drift fails the gate.
- **LOOP-4 · No `st doctor`-style tooling/environment health command; the `doctor` alias checks stack state, not the toolchain** — _low · agent-ux_  
  `cmd/validate.go:13-21 (Aliases: ["doctor"]); Makefile:38-39 (lint)`
  - **Impact:** When an agent's environment is missing the lint binary or has a mismatched version, `make ci` fails with a low-level 'command not found' rather than an actionable 'run X to install the pinned linter'. There's no one-shot 'is my environment set up to close the loop' check.
  - **Fix:** Add a `make doctor` (or `st`-independent) target that checks: go version >= 1.26, golangci-lint present and == the CI-pinned v2.12.2, git present, hooks installed (core.hooksPath == .githooks). Emit actionable fix hints. This makes the loop self-bootstrapping for a fresh agent.
- **LOOP-5 · golangci-lint version pinned in CI but not locally — silent lint drift** — _low · loop_  
  `.github/workflows/ci.yml:33-39 (version: v2.12.2); Makefile:38-39`
  - **Impact:** An agent on a machine with a different (older/newer) linter can get green locally and red in CI, or vice versa — breaking the 'make ci == CI' parity promise (CLAUDE.md:27, ci.yml:41-43). Non-reproducible across environments.
  - **Fix:** Have `make lint` (or a `make doctor` prereq) assert the installed linter version equals the CI-pinned value, failing with the install command if not. Define the pinned version once (Makefile variable) and reference it from both the Makefile and a comment in ci.yml so they can't drift independently.
- **LOOP-6 · Golden files have an `-update` flag but no Makefile regen target** — _low · agent-ux_  
  `cmd/golden_test.go:12,32-39; CONTRIBUTING.md:41-44; Makefile (no golden target)`
  - **Impact:** When an agent legitimately changes user-visible output (e.g. adds a command, which CONTRIBUTING.md:44 notes shifts the --help golden), it must recall a bespoke `go test` incantation instead of a discoverable `make` target. Minor friction, but it's an undiscoverable step in the documented add-a-command recipe.
  - **Fix:** Add `golden:` / `update-golden:` to the Makefile running `go test ./cmd -run Golden -update`, and reference `make golden` in CONTRIBUTING.md:41-44 and CLAUDE.md:73. Makes the regen step discoverable via `make`.

### Docs ↔ code drift

- **DOC-1 · AGENT.md claims `st <command> -h` prints flags, but 21 of 27 commands print only a one-line usage** — _high→low · docs_  
  `docs/AGENT.md:83-84 vs cmd/*.go fs.Usage funcs`
  - **Impact:** An agent told (in the canonical machine-interface doc) to run `st <cmd> -h` to learn a command's flags will get only a usage line for ~78% of commands, and may conclude flags like --json/--dry-run don't exist. Undermines the doc's core promise of discoverability.
  - **Fix:** Either make the claim accurate by adding `fs.PrintDefaults()` to every command's fs.Usage (cheap, uniform), or soften AGENT.md to say `-h` prints the usage line and direct agents to `st help <command>` / the usage string for flags.
- **DOC-2 · `st help down` and `st help bottom` omit --json from their registered Usage although both accept it** — _medium · agent-ux_  
  `cmd/down.go:16 and cmd/bottom.go:15 vs docs/AGENT.md:62,83`
  - **Impact:** The documented discovery path (AGENT.md:83 'st help <command> prints its summary, usage, and aliases') hides --json on these two navigation commands, so an agent cannot learn they emit machine-readable output without reading source.
  - **Fix:** Append ` [--json]` to down.go:16 and bottom.go:15 Usage strings to match up/top.
- **DOC-3 · `st help sync` omits --dry-run; the `-h` and `help` paths disagree for sync** — _medium · agent-ux_  
  `cmd/sync.go:16 (Command.Usage) vs cmd/sync.go:32 (fs.Usage) and README.md:131,208`
  - **Impact:** Two official help surfaces for the same command contradict each other, and the registered Usage (the one AGENT.md:83 tells agents to read via `st help`) drops a real, documented-elsewhere feature. Inconsistent signal about whether sync supports preview.
  - **Fix:** Add ` [--dry-run]` to sync.go:16 Command.Usage and mention --dry-run in the README sync detail section (around line 208) for parity with restack.
- **DOC-4 · README documents log glyphs as ●/◯ but the code emits ◉/○ (and the README's own example uses ◉/○)** — _low · docs_  
  `README.md:161 vs cmd/log.go:103,105 (and README.md:283-287)`
  - **Impact:** Cosmetic, but it is a literal doc-vs-code mismatch a reader can spot, and the README is internally inconsistent (line 161 vs line 283).
  - **Fix:** Change README.md:161 to `◉`/`○` to match cmd/log.go and the example block below it.
- **DOC-5 · AGENT.md does not document the JSON shape of `checkout <name> --json`** — _low · gap_  
  `docs/AGENT.md:60 vs cmd/checkout.go:52-56`
  - **Impact:** An agent that runs `st checkout feat-a --json` to confirm a switch gets a shape AGENT.md never lists, so it cannot rely on the documented contract for the most common checkout invocation (with a target).
  - **Fix:** Add the with-name shape `{ "branch", "switched": bool }` to the checkout bullet in AGENT.md:60.
- **DOC-6 · AGENT.md shows the mutating-command arrays as `[]`, but they are omitempty and absent when empty** — _low · docs_  
  `docs/AGENT.md:50-52 vs internal/stack/engine.go:12-21`
  - **Impact:** An agent that keys off `result.deleted` or `result.notes` always being present (e.g. `len(result.deleted)`) may hit a missing field rather than an empty array. Also AGENT.md's shared-shape example omits the `dryRun` field that OpResult includes for restack/sync.
  - **Fix:** Note in AGENT.md that these fields are omitted when empty (omitempty) and that `dryRun` is part of the same shape for preview-capable commands; or drop the empty `[]` placeholders from the example.


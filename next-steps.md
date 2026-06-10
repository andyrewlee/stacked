# Next steps — `stacked` improvement plan (round 2)

> Produced by a full-codebase review (June 2026) after all 11 PRs of the
> previous plan landed (PRs #6–#16). The remaining work falls into four arcs:
> a latent locale bug, the undo subsystem (the last real violation of the
> port/engine/adapter architecture), mechanical engine simplifications, and
> CLI-contract consistency — plus perf and housekeeping at the tail.

Every PR lands as **one focused, reviewable increment in a stacked diff**
(`st create <branch>`) and leaves `make ci` green. The order matters: the
undo arc (PRs 2–4) is sequenced so the existing e2e undo tests act as the
regression oracle *before* the code moves, and the engine cleanups (PRs 5–7)
come after so they cover the newly engine-resident undo code too.

## How to read this

- **Stacked plan** is the work, in order. Each PR stacks on the one before it,
  is small enough to review in one sitting, and must leave `make ci` green.
- **↳ Verifies** is the exact command that proves the change works — close the
  loop before the PR is done.

## Overview

| # | Branch | Theme | Effort |
|---|--------|-------|--------|
| 1 | `pin-git-locale-and-plumbing-ff` | Fix locale-fragile parsing of git output | S |
| 2 | `undo-snapshot-through-port` | Widen the Git port; snapshot capture via the port | S |
| 3 | `engine-undo-op` | Move undo reversal into the engine | M |
| 4 | `undo-finalize-in-stack` | Move the no-op/finalize undo protocol into `stack` | M |
| 5 | `join-rollback-errors` | One helper for the rollback double-error pattern | S |
| 6 | `restack-branch-reports` | `RestackBranch` reports whether it rebased | S |
| 7 | `remove-branch-helper` | One topology helper for branch removal; Delete reports restacked | M |
| 8 | `submit-single-json-shape` | Unify `submit --json` into one result shape | S |
| 9 | `cli-parse-dedup-and-nav-consistency` | Dedup flag scanners; consistent nav parsing/wording | M |
| 10 | `batch-ref-reads` | One `for-each-ref` spawn instead of 2 per branch in log/validate | M |
| 11 | `portable-lock-fallback` | Real lock on non-flock platforms | S |
| 12 | `retire-stale-plan-and-split-tests` | Delete this file; split test megafiles | S |

---

### PR 1 — Pin git's locale and decide fast-forward by plumbing, not message text
`pin-git-locale-and-plumbing-ff` · effort **S** · base of the stack

**Goal.** No code path makes a decision by parsing git's localized human
output. On a machine with `LANG=de_DE` (etc.), sync no longer misclassifies
an "already up to date" fast-forward as an error.

**Why now.** This is the one latent *bug* in the findings, it is small, and
PR 3 wants a reliable foundation for the checkout-failure fallback it
relocates.

**Changes**
- `internal/git/git.go`: add a `gitEnv()` helper returning
  `append(os.Environ(), "LC_ALL=C")` and set it on the `exec.Command` in
  `run()`, `ok()`, and the direct `exec.Command` call sites
  (`HasStagedChanges`, `HasUnstagedChanges`, `IsAncestor`,
  `RebaseContinueQuiet`). Leave `RunInteractive`/`RebaseContinue` stdio and
  env inherited — their output goes to the user and is never parsed.
- `internal/git/remote.go` `FastForward`: decide "already up to date" with
  plumbing — resolve the upstream SHA and check
  `IsAncestor(upstream, localTrunk)` *before* merging; only run
  `merge --ff-only` when there is something to advance. Delete
  `isAlreadyUpToDate` (the string sniff) entirely.
- `cmd/undo.go` `checkoutBlockedByLocalChanges` keeps its sniff for now
  (locale-pinned makes it reliable); PR 3 replaces it with a plumbing check
  when the logic moves to the engine.

**Tests**
- `internal/git/git_test.go`: `FastForward` table — up-to-date (no merge run,
  message says "already up to date"), fast-forwardable (trunk advances),
  diverged (returns error). Assert by SHA, not message text.

**↳ Verifies:** `make test` (real-git port suite) then `make ci`.

---

### PR 2 — Capture undo snapshots through the Git port
`undo-snapshot-through-port` · effort **S** · stacks on PR 1

**Goal.** The undo snapshot is taken through the same port as the rest of the
engine, so PRs 3–4 can be tested against the in-memory fake. This closes the
old ENG-8 finding (`RecordUndo` bypasses the port).

**Changes**
- `internal/stack/git.go`: add `LocalBranches() ([]string, error)` and
  `CheckoutDetach(ref string) error` to the `Git` interface (both already
  exist as package functions in `internal/git`).
- `internal/git/shell.go`: add the two `Shell` methods.
- `internal/stack/fakegit_test.go`: implement both on the fake
  (`LocalBranches` from its branch map; `CheckoutDetach` records a detached
  HEAD).
- `internal/stack/undo.go`: split `RecordUndo` into a **pure capture** —
  `func (s *State) SnapshotUndo(g Git, label string) *UndoEntry` — that reads
  refs/branches/current-branch via the port, plus a thin
  `RecordUndo(g Git, label string) error` that captures and appends to the
  journal. Branches whose tip cannot be resolved are still omitted, not
  fatal.
- `cmd/util.go` `mutateState`: pass `gitShell` to `RecordUndo`.

**Tests**
- `internal/stack/undo_test.go`: drive `SnapshotUndo` against the fake and
  assert the refs map, local-branch list, and current branch match the fake's
  state — no real git, no disk.

**↳ Verifies:** `make test-fast` then `make ci`.

---

### PR 3 — Move undo reversal into the engine
`engine-undo-op` · effort **M** · stacks on PR 2

**Goal.** The hardest code in the repo — the 200-line reversal algorithm in
`cmd/undo.go` (created-branch deletion ordering, checkout-target selection,
detached-HEAD fallback, rename handling) — becomes an engine op exercised by
the fast fake-git suite. `cmd/undo.go` shrinks to a ~40-line adapter.

**Why now.** The e2e undo tests added by the previous plan (undo-after-abort,
worktree journeys) lock current behavior, so this is a safe relocation, and
it must precede PR 4 (protocol move) and benefit from PR 5 (error helper).

**Changes**
- `internal/stack/undo_op.go` (new): `func Undo(env Env, s *State, entry
  *UndoEntry) (*OpResult, error)` performing, through the port, exactly what
  `runUndo` does today: unmarshal `entry.State` into a prev-state, delete
  branches the undone command created (with the checkout-target dance),
  replace `*s` with the prev state, restore every recorded ref via
  `UpdateRef`, re-checkout `entry.CurrentBranch` when possible, and
  `env.save()` to persist. Returns an `OpResult` with the restored branch
  names in `Restacked`-style fields (`Notes` for the label).
- Replace `checkoutBlockedByLocalChanges` (message sniff) with plumbing: on a
  checkout failure, consult `g.IsClean()`; if the tree is dirty, take the
  `CheckoutDetach(RevParse("HEAD"))` path, else propagate the error.
- Keep the `entry.Label == "rename"` target-selection logic *behaviorally
  identical* but inside the engine, with a comment noting the follow-up
  (derive the target from the state diff instead of the label).
- `cmd/undo.go`: adapter only — lock, `git.RebaseInProgress()` guard,
  `stack.PeekUndo`, call `stack.Undo`, `stack.DropUndo`, render. The helpers
  `restoredRenameTarget` / `missingRestoredRef` / `branchCreatedByEntry` move
  to the engine with the op.

**Tests**
- `internal/stack/engine_test.go` (or `undo_op_test.go`): fake-git tests —
  create→undo deletes the created branch and restores HEAD; modify+restack→
  undo restores every ref; rename→undo restores the old name and checks it
  out; delete→undo resurrects the branch from the snapshot ref.
- `internal/stack/model_test.go`: the new strong oracle — after each random
  op, capture `SnapshotUndo` (in-memory, via the fake), apply the op, run
  `stack.Undo` with the captured entry, and assert state + refs equal the
  snapshot; then re-apply the op and continue the run. This makes undo
  correctness hold over thousands of random sequences instead of four e2e
  spot checks.

**↳ Verifies:** `make test-fast` (new fake undo tests + extended model), then
`make e2e` (existing undo e2e unchanged), then `make ci`.

---

### PR 4 — Move the undo no-op/finalize protocol into the stack package
`undo-finalize-in-stack` · effort **M** · stacks on PR 3

**Goal.** The trickiest invariant in the tool — when a tentative undo entry
is dropped (no-op), trimmed, or annotated with created branches — lives next
to the journal it manages, behind the port, with fast unit tests. After this
PR no undo logic remains in `cmd`.

**Changes**
- Move `sameState`, `refsUnchanged`, `createdBranchesSince`, `dropNoopUndo`,
  `finalizeUndoOnSuccess`, and `cleanupNoopUndoOnError` from `cmd/util.go`
  into `internal/stack/undo.go`, taking a `Git` port parameter instead of
  calling the concrete `git` package.
- Expose two entry points consumed by `mutateState`:
  `stack.FinalizeUndo(g Git, s *State, entry *UndoEntry) error` (success
  path) and `stack.CleanupUndoOnError(g Git, s *State, opErr error) error`.
  `mutateState` shrinks to: lock → load → record → run op → save → finalize.
- The rebase-in-progress check inside the old `dropNoopUndo` goes through the
  port (`g.RebaseInProgress()`), not `git.RebaseInProgress()`.

**Tests**
- `internal/stack/undo_test.go` fake-git tables, all previously only
  integration-covered: failed op with nothing changed → entry dropped;
  failed op that moved a ref → entry kept and trimmed; successful no-op →
  dropped; success that created a branch → `CreatedBranches` recorded;
  conflict with a rebase in progress → entry kept.
- Existing `cmd` undo tests are the integration oracle; they should not
  change.

**↳ Verifies:** `make test-fast`, then `make ci`.

---

### PR 5 — One helper for the rollback double-error pattern
`join-rollback-errors` · effort **S** · stacks on PR 4

**Goal.** The `fmt.Errorf("%w; additionally failed to X: %v", err,
rollbackErr)` pattern (~10 sites) becomes one helper, and the secondary error
stays matchable with `errors.Is`.

**Changes**
- `internal/stack/engine.go`: add
  `func alsoFailed(primary error, what string, secondary error) error`
  implemented with two `%w` verbs
  (`fmt.Errorf("%w; additionally failed to %s: %w", ...)` — supported since
  Go 1.20), so both errors survive `errors.Is`/`errors.As`.
- Replace the pattern at its sites in `engine.go` (`Create`, `Fold`,
  `Squash`, `Onto`, `Delete`, `Sync`, `restoreHEADAfterNonConflict`),
  `restack.go` (`RestackBranch`), the relocated undo op (PR 3), and
  `cmd/util.go` (`mutateState`).

**Tests**
- One unit test asserting `errors.Is` matches both the primary and the
  rollback error through the helper. Existing message-content tests may need
  small string updates (messages stay human-equivalent).

**↳ Verifies:** `make test-fast`, then `make ci`.

---

### PR 6 — `RestackBranch` reports whether it rebased
`restack-branch-reports` · effort **S** · stacks on PR 5

**Goal.** Callers stop doing a `NeedsRestack` pre-check that
`RestackBranch` immediately repeats, removing duplicated bookkeeping at
three sites and halving the `rev-parse` subprocess spawns per branch during
restacks.

**Changes**
- `internal/stack/restack.go`: `RestackBranch` returns
  `(rebased bool, err error)` — it already resolves the parent tip and knows
  whether it was a no-op.
- Drop the `NeedsRestack` pre-checks and rebased-list bookkeeping in
  `RestackUpstack` (`restack.go:131-147`), `Restack` (`engine.go:227-238`),
  `RestackAll` (`engine.go:737-757`), and the child loop in `Delete`
  (`engine.go:520-529`).
- `NeedsRestack` stays — `RestackPlan`, `SyncPlanAgainst`, `validate`, and
  `log` are read-only consumers.

**Tests**
- Existing engine + model tests are the oracle (they assert *which* branches
  restacked); adjust call signatures only.

**↳ Verifies:** `make test-fast`, then `make ci`.

---

### PR 7 — One topology helper for branch removal; Delete reports restacked
`remove-branch-helper` · effort **M** · stacks on PR 6

**Goal.** The "re-parent children, then untrack" invariant exists exactly
once instead of hand-rolled in five places, and `st delete --json` reports
the children it restacked like every other restacking op.

**Changes**
- `internal/stack/stack.go`: add
  `func (s *State) RemoveBranch(name string) (formerChildren []string)` —
  re-parents every child onto the removed branch's parent **preserving each
  child's ParentSHA** (the invariant `Delete` documents), untracks, returns
  former child names sorted.
- Use it in `Fold` (`engine.go:316-319`), `Delete` (`engine.go:501-515`),
  `PruneMerged` (`engine.go:781-784`), and the prune simulation in
  `SyncPlanAgainst` (`engine.go:632-637`).
- `UntrackBranch` and `cmd/repair.go` intentionally set different ParentSHAs;
  leave them, with a one-line comment pointing at `RemoveBranch` and why they
  differ.
- `Delete`: collect the rebased branches from its child-restack loop into
  `OpResult.Restacked`. Update `docs/AGENT.md` if it enumerates the delete
  shape; regenerate goldens if text output changes (`make golden`).

**Tests**
- `internal/stack/stack_test.go`: `RemoveBranch` unit test (multi-child,
  ParentSHA preserved).
- `internal/stack/engine_test.go`: delete-with-children asserts
  `res.Restacked` lists the re-parented children that moved.

**↳ Verifies:** `make test-fast`, `make golden` if output shifted, then
`make ci`.

---

### PR 8 — Unify `submit --json` into one result shape
`submit-single-json-shape` · effort **S** · stacks on PR 7

**Goal.** An agent parsing `st submit --json` unmarshals one struct
regardless of outcome. This is the last open finding from the previous
audit's agent-contract dimension (its CLI-5).

**Changes**
- `cmd/submit.go`: one payload struct
  `{remote, dryRun, pushed, repoURL omitempty, summary omitempty}` used by
  both the trunk early-return (`submit.go:65-72`) and the normal path
  (`submit.go:115-120`). Trunk case: `pushed: []`,
  `summary: "at trunk; nothing to submit"`.
- `docs/AGENT.md`: document the single shape.

**Tests**
- `cmd/commands_test.go`: assert the trunk-case and pushed-case JSON
  unmarshal into the same struct with no unknown/missing keys
  (`json.Decoder.DisallowUnknownFields`).

**↳ Verifies:** `go test ./cmd/... -run Submit -count=1`, then `make ci`.

---

### PR 9 — Dedup the flag scanners and make navigation commands consistent
`cli-parse-dedup-and-nav-consistency` · effort **M** · stacks on PR 8

**Goal.** One implementation each for "--json detection", "built-in command
arg parsing", and "positional count parsing", and one phrasing/quoting style
across the navigation commands.

**Changes**
- `cmd/root.go`: implement `jsonRequested` (`root.go:308-324`) in terms of
  `parseJSONFlag` (`root.go:186-205`) so the two cannot drift; merge
  `parseHelpArgs`/`parseVersionArgs` (`root.go:134-184`) into one scanner
  with an `allowTopic bool`.
- `cmd/util.go`: add `parseCount(fs *flag.FlagSet, args []string, what
  string) (int, error)` and use it in `up.go:33-45` and `down.go:36-48`,
  standardizing on one term ("step count") and one message set.
- Pick one output style for navigation/checkout and apply it everywhere:
  `switched to %s` (down currently uses `%q`, checkout capitalizes).
- `cmd/util.go`: `stackEnv(s *stack.State, asJSON bool)` — drop the variadic
  bool; update all call sites.
- Regenerate goldens / adjust e2e string assertions for the wording changes.

**Tests**
- Existing `util_test.go`/`execute_test.go` parsing tables extend to the
  shared scanners; nav e2e assertions updated alongside.

**↳ Verifies:** `make test`, `make golden`, then `make ci`.

---

### PR 10 — Batch ref reads: one git spawn for log/validate drift checks
`batch-ref-reads` · effort **M** · stacks on PR 9

**Goal.** `st log` and `st validate` stop spawning ~2 git processes per
tracked branch for tip/exists checks; a 30-branch forest does one
`for-each-ref` instead of 60 spawns. (`topSubject`'s per-branch `git log`
stays — it cannot be batched into one ref read.)

**Changes**
- `internal/git/git.go`: `func Tips() (map[string]string, error)` via
  `for-each-ref --format='%(refname:short) %(objectname)' refs/heads`.
- `internal/stack/git.go` + `shell.go` + `fakegit_test.go`: add `Tips()` to
  the port, shell, and fake.
- `internal/stack/restack.go`: `func (s *State) DriftAgainst(tips
  map[string]string) map[string]bool` — pure needs-restack computation from
  a tip map (a branch missing from the map ≙ missing git branch).
- `cmd/log.go` and `cmd/validate.go`: call `Tips()` once, derive
  `needsRestack` and branch-exists from the map; per-branch `RevParse`/
  `BranchExists` calls for drift checks go away. Engine mutation paths keep
  reading live tips per step (correctness during restacks depends on it).

**Tests**
- Fake-git test for `DriftAgainst`; real-git test for `Tips()`; existing
  log/validate integration tests are the behavior oracle.

**↳ Verifies:** `make test`, then `make ci`.

---

### PR 11 — A real lock on non-flock platforms
`portable-lock-fallback` · effort **S** · stacks on PR 10

**Goal.** Two concurrent `st` mutations on Windows can no longer silently
interleave load→mutate→save and lose an update (the old ENG-6 finding —
`lock_other.go` is currently a pure no-op).

**Changes**
- `internal/stack/lock_other.go`: replace the no-op with an
  `O_CREATE|O_EXCL` lock file containing PID + RFC3339 timestamp; on
  conflict, return the same "another st command is running" error; treat a
  lock file older than a conservative threshold (e.g. 10 minutes) as stale
  and reclaim it. Release removes the file.
- Extract the stale-decision into a small pure function shared by a unit
  test that runs on all platforms (the syscalls stay build-tagged).
- `README.md`/`docs/AGENT.md`: one line on locking semantics per platform.

**Tests**
- Pure unit test for the staleness decision; `GOOS=windows go build ./...`
  in the PR to prove the tagged file compiles (CI runs unix, so the build
  check is the gate).

**↳ Verifies:** `GOOS=windows go build ./...` and `make ci`.

---

### PR 12 — Retire this plan and split the test megafiles
`retire-stale-plan-and-split-tests` · effort **S** · stacks on PR 11

**Goal.** No stale planning document for a future agent to mistake for
current truth, and the two 1,300+-line test files become navigable.

**Changes**
- Delete `next-steps.md` (this file — everything in it has landed by now).
- Split `cmd/commands_test.go` (~1,450 lines) by theme: mutations,
  navigation, JSON-contract; split `e2e/e2e_test.go` (~1,294 lines)
  similarly. Pure moves — no logic edits, no renamed tests.
- `CLAUDE.md`: update the test-layer file references if any are named.

**Tests**
- None new. Assert nothing was lost: `go test ./cmd/... ./e2e/... -list '.*'`
  before/after produces the same set.

**↳ Verifies:** `make ci` (full gate, identical test inventory).

---

## Deliberately deferred

- **Always-quiet rebases** (single rebase code path for text and JSON modes,
  with captured output replayed in text mode): a UX change for interactive
  conflict resolution, not just a refactor — revisit after the undo arc.
- **Deriving undo's rename handling from the state diff** instead of
  `entry.Label`: noted in PR 3; do it once `stack.Undo` has fake-git
  coverage proving equivalence.
- **`absorb`** remains out of scope (see CLAUDE.md).

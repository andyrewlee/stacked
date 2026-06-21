# 017 — Stacked diffs across git worktrees (parallel agents)

Make stacked diffs first-class across git worktrees so multiple agents can work
different branches of one stack in parallel while the stack stays restacked.

## Design decisions (converged)

1. **One user-facing concept**: a "branch" is "a place you can be". The worktree
   is invisible plumbing — users never think "branch vs worktree".
2. **Worktrees are lazy + explicitly triggered.** The common case is plain
   single-tree stacked diff with zero overhead; a worktree is materialized only
   when the user explicitly asks (e.g. `st worktree <branch>`), never by inferred
   contention.
3. **The single-tree flow stays byte-for-byte unchanged.** Everything new is
   gated behind `stack.IsMultiWorktree(...)`, which short-circuits to today's
   behavior when only the main worktree exists. The `internal/stack/model_test.go`
   invariant model preserves its original single-tree op menu and assertions
   unchanged (the single-tree baseline still runs and passes every step); a
   worktree dimension is layered ON TOP (`maybeReconcileWithWorktrees`): on a
   random subset of steps it parks some tracked branches in their own worktrees
   (some dirty), drives the reconcile through the real `Restack`/`RestackAll` so the
   owner-driven cross-worktree cascade runs, asserts the worktree-aware invariants
   (`checkWorktreeInvariants` — see the Tier 3 status), then tears the worktrees
   back down so the next step is single-tree-coherent.
4. **On-disk location**: `~/.stacked/worktrees/<repo>/<branch>` (full project
   name, central per-repo dir under `$HOME`). Matches the `.git/stacked/` naming
   and keeps linked worktrees out of the repo tree so runners/linters/watchers
   never walk into them.
5. **Cross-worktree restack is owner-driven**: a branch is only rebased by the
   worktree that owns it; the tidy is command-triggered and skips dirty
   dependents with a clear message (never clobbers). **Lifecycle removals
   (`delete`/`fold`/prune-merged) tear down a CLEAN owning worktree before
   deleting the branch** (git refuses otherwise) and **refuse on a DIRTY one**
   (clear error, nothing deleted) — in-progress work is never silently discarded.
6. **New-worktree setup** adopts Claude Code's `.worktreeinclude` mechanism
   (gitignore syntax; copy only files that match AND are gitignored), using
   copy-on-write reflink (`cp -c` / `cp --reflink=auto`) with a plain-copy
   fallback. An optional post-create hook is the escape hatch, not the default.
7. **Navigation teleports**: `st checkout/up/down/top/bottom` cd into a branch's
   worktree if it has one, else checkout in place. `st shell install` emits a tiny
   shell shim so a CLI process can change the parent shell's cwd; without the shim
   the command prints the path instead of failing.

## Status

- **Foundation tier — LANDED (branch `stacked-worktrees-foundation`).**
  - `stack.Git` port gains `Worktrees() ([]git.Worktree, error)`; the production
    `Shell` parses `git worktree list --porcelain` (`internal/git/git.go`,
    `Worktrees`/`IsCleanAt`); the in-memory fake models a worktree set.
  - Pure helpers in `internal/stack/worktree.go`: `WorktreesRoot`, `WorktreePath`
    (canonical `~/.stacked/worktrees/<repo>/<branch>`, sanitized, does not require
    the worktree to exist), `OwnerOf`, `IsMultiWorktree`. All unit-tested.
  - `st log` / `st status` annotate worktree location + dirty + stale, gated on
    `IsMultiWorktree` so single-tree output (and the goldens) are unchanged.
    `--json` shapes gain `worktree`/`dirty` (log) and `worktree` (status), all
    `omitempty`. Documented in README, `docs/AGENT.md`, CHANGELOG.
  - Real-git tests in `internal/git` and a black-box e2e
    (`TestWorktreeAnnotationsInLog`) cover the new paths.

- **Tier 2 (materialize / navigate) — LANDED.**
  - `st worktree <branch>` (alias `wt`) / `st worktree ls` / `st worktree rm
    <branch>`: materialize at the resolved path (`git.WorktreeAdd`/`WorktreeRemove`),
    copying `.worktreeinclude` matches (gitignore syntax, gitignored-only,
    reflink via `cp -c` / `cp --reflink=auto` with a stdlib fallback).
  - `st shell install [bash|zsh|fish]` emits a cd shim; `st checkout`/`up`/`down`/
    `top`/`bottom` teleport into a branch's worktree via a `$ST_CD_FILE` directive
    (`teleportCheckout`), printing the path without the shim. Gated on
    `IsMultiWorktree` so single-tree navigation is unchanged.
  - Real-git e2e: `TestWorktreeCommand`, `TestShellInstallEmitsShim`,
    `TestCheckoutTeleportsToWorktree`.

- **Tier 3 (owner-driven cross-worktree restack cascade) — LANDED.**
  - `RestackBranch` routes a branch owned by ANOTHER worktree to a `git -C <path>`
    rebase (`RebaseOntoIn` on the port), gated on `IsMultiWorktree` + a clean owner
    tree (`IsCleanIn`). Single-tree behavior — and the unmodified `model_test.go`
    invariant, whose fake reports a single worktree — is unchanged.
  - A dirty dependent worktree is skipped (recorded in transient
    `State.skippedWorktrees`, surfaced as `restack`/`sync` notes), never clobbered.
  - A conflict during the cross-worktree rebase is rolled back in that worktree
    (`RebaseAbortIn`) and surfaced as an error, rather than left paused where the
    main process's `continue`/`abort` (cwd-scoped) could not finish it. This is a
    deliberately conservative semantic: full cross-worktree paused-conflict
    resume is left as future work.
  - Fast engine tests (`cascade_test.go`) and real-git e2e
    (`TestCrossWorktreeRestackCascade`, `…SkipsDirty`, `…ConflictRollsBack`).
  - **Multi-level / mixed-ownership cascade** (`main -> a -> b -> c` with the
    intermediate `b` owned elsewhere, `a`/`c` local): the fast engine test
    `TestRestackCascadeMultiLevelMixedOwnership` drives the real `Restack` and pins
    topological reconcile across the worktree boundary; the e2e
    `TestCrossWorktreeRestackMultiLevel` proves the same with real git.
  - **Lifecycle teardown.** `Delete`/`Fold`/`PruneMerged` reach git only via the
    engine port's new `WorktreeRemove`: a branch they remove that owns a CLEAN
    linked worktree has that worktree torn down first (else git refuses the branch
    delete); a DIRTY owner errors with nothing changed (`releaseOwnedWorktree` in
    `internal/stack/worktree.go`). Fast engine tests
    (`worktree_lifecycle_test.go`) and real-git e2e (`TestDeleteTearsDownClean…`,
    `TestDeleteRefusesDirty…`, `TestFoldCascadesIntoCleanChildWorktree`).
  - **Invariant-model worktree coverage.** The crown-jewel random model
    (`model_test.go`) now layers a worktree dimension over its reconcile step
    (`maybeReconcileWithWorktrees` + `checkWorktreeInvariants`): clean owners
    reconcile through the cascade, a dirty owner is the only kind left needing a
    restack (and is reported in `SkippedWorktrees`), and the main worktree's HEAD
    never moves — over thousands of random sequences, mixed with pure single-tree
    runs.

## Deferred / future work

These are deliberate, reviewed decisions — recorded so they are not mistaken for
oversights. Each is "accepted as-is" for the foundation, with the path forward noted.

- **Cross-worktree *paused* conflict resume (R5) — ACCEPTED as-is.** A
  cross-worktree rebase conflict is aborted + rolled back (`RebaseAbortIn`) rather
  than left paused, because `st continue`/`st abort` operate on the process cwd and
  cannot drive a rebase paused in another worktree. The branch is simply left
  needing a restack (metadata stays consistent; `validate` is clean). This is a
  deliberately conservative semantic, not a partial feature. Consequences kept on
  purpose:
  - `State.PendingReparent` stays a SINGLE slot (one in-flight reparent for the
    cwd's rebase). A per-branch `PendingReparent` map for concurrent paused
    cross-worktree rebases was considered and rejected here — it only pays off
    alongside cross-worktree paused-resume, which is itself deferred.
  - A full solution would teach `continue`/`abort` to target the OWNING worktree
    (run `git -C <owner>` and resume that branch's reparent), and only then is a
    per-branch pending-reparent map warranted. Left as future work.
- **Worktree path collision on sibling branch names (R2) — ACCEPTED as-is.**
  `feat/foo` and `feat-foo` both sanitize to the path segment `feat-foo`
  (`sanitizeSegment` collapses `/` to `-`), so the second `st worktree` would
  collide. This is low impact and fails LOUDLY at `git worktree add` (git refuses
  to reuse an occupied path) rather than silently corrupting state. A future fix
  would disambiguate colliding segments (e.g. a short hash suffix).
- **Reconcile-on-entry (a would-be phase 3) — intentionally command-triggered
  only.** Restacking is driven by explicit commands (`st restack`/`sync`), never
  implicitly on navigating into a worktree. Auto-reconciling on entry was
  considered and deliberately NOT done: it would make read/nav commands mutate the
  repo as a side effect, violating decision 5 (cross-worktree restack is
  command-triggered) and the "single-tree flow stays byte-for-byte unchanged"
  contract. Stays explicit by design.
- **`.worktreeinclude` glob expansion.** The foundation treats each line as a
  repo-root-relative path (plus comments/blanks); full gitignore glob semantics
  are out of scope for now.
- **Optional post-create hook (one command) as an escape hatch:** not yet wired
  (decision 6 names it as the escape hatch, not the default).

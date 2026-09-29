# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **`st undo --list` previews the journal without reverting.** It prints the
  undo entries newest-first (index 1 is what a bare `st undo` would revert);
  `--json` emits `{ "entries": [ { "index", "label", "currentBranch",
  "createdBranches": [], "createdWorktrees": {}, "refs": {} } ] }` where
  `refs` maps each recorded branch to the tip undo would restore.
- **Branch-aware shell completion via a hidden `st __complete` endpoint.**
  `st completion <bash|zsh|fish>` now emits dynamic hooks for the four
  branch-taking commands — `checkout`/`co`, `onto`/`move`, `track`, and
  `worktree`/`wt` — that call `st __complete <cmd> <index> -- <words>` at
  completion time. Candidates are read-only and cheap (state file plus at
  most two flat git probes — `for-each-ref` or `worktree list`, never a
  fetch or history walk), silent-empty outside a repo or on unreadable
  state, and passed to the shell as data: a refname-legal name like
  `$(touch_pwned)` can never be evaluated. `checkout` offers trunk plus
  tracked branches; `onto` offers everything except the moving subtree;
  `track` offers untracked locals (`--parent`'s value completes tracked
  branches); `worktree` offers tracked branches lacking a worktree, and
  `worktree rm` the branches owning one. `__complete` is hidden from help
  and word-1 candidates and is exempt from the git-version floor so the
  per-keystroke cost stays flat; `ST_COMPLETE_BIN` overrides the binary
  the hooks call.
- **`st undo --dry-run` previews the next undo.** Unlike `--list` (recorded
  data only), it diffs the journal against live refs and worktrees:
  `wouldRestore` carries each ref's live→recorded SHA pair plus a per-ref
  `commitsLostFromRef` count (`"unknown"` when either object is missing),
  `wouldDelete` lists branches the op created with their live owning
  worktrees (even when none was recorded) and dirtiness, and `blockers`
  names — in the real undo's gate order — everything a real run would
  refuse on (`rebase_in_progress`, `state_too_new`, `malformed_snapshot`,
  `cwd_inside_created_worktree`, `worktree_dirty`,
  `recorded_worktree_mismatch`). The preview holds the advisory lock but
  mutates nothing: state, journal, refs, worktrees, and cwd are byte-for-
  byte unchanged, and a later real undo revalidates everything. `--dry-run`
  and `--list` are mutually exclusive; blockers are data, exit is still 0.
- **`st prune` is sync's prune step as a standalone command.** It deletes
  every tracked branch already merged into the local trunk — or into
  `refs/remotes/<remote>/<trunk>` with `--remote` (no fetch; a missing
  tracking ref fails loudly) — and reports them in `deleted`. It never
  moves HEAD and requires no clean tree: pruning the current branch is
  refused with "check out another branch or run st sync". `--dry-run`
  lists the same set under `"dryRun": true`.
- **`st sync --no-fetch` skips every remote call.** No fetch, no trunk
  fast-forward; prune and restack run against the already-fetched
  `refs/remotes/<remote>/<trunk>` when it exists, else the local trunk.
  The result note reports `trunk: skipped (--no-fetch)`.
- **`st track --all` adopts an existing branch stack in one command.**
  Every untracked local branch is adopted with its parent inferred from
  the full local-branch set — `a→b→c` becomes a real chain, not three
  trunk-parented orphans. Cyclic proposals are refused naming the members;
  orphan branches sharing no history with the trunk are skipped with a
  note. The JSON result adds a `tracked` name→parent map.
- **`st submit --all` pushes the whole tracked forest.** In dependency
  order (parents before children), from any branch — including trunk and
  detached HEAD — with the same confirmed-per-ref partial-failure contract
  as a single-path submit. `st submit` without the flag is unchanged.
- **`ST_LOCK_WAIT` waits out lock contention.** Set it to a Go duration
  (`10s`) or bare seconds (`10`) and lock acquisition retries every 100ms
  up to the budget; exhaustion still exits 5. Malformed or negative values
  fail fast naming the variable; the abandoned `lock.reclaim` guard is
  never retried.
- **`st absorb` applies multi-target plans.** A staged set whose hunks belong
  to different stack branches now lands as one amend per owning tip (each
  with only its own hunks, post-image line numbers corrected for same-file
  splits) plus a single cascade restack; one `st undo` reverts everything.
  All-or-nothing: any refusal or dirty target worktree leaves the whole plan
  unapplied.

### Fixed
- **Documentation corrections.** The 0.0.1 notes now list all seven shipped
  exit codes (`5` lock-held and `70` internal existed from the start) and name
  `shell` alongside `completion` as the `--json` exceptions; `worktree
  rm --all` is described as tracked-branches-only everywhere; the README's
  `state.json` example carries the always-written `"version": 1`, and its
  `st modify` example shows the current `restacked: <names>` rendering.
- **`st absorb` can no longer drop unaccounted staged changes.** The staged
  diff is now captured with pinned flags (`--no-color --no-ext-diff
  --no-textconv --src-prefix=a/ --dst-prefix=b/`) so user diff configuration
  cannot reshape it; metadata-only sections such as empty added or deleted
  files produce explicit refusals instead of being silently omitted, and a
  raw staged-path inventory cross-check refuses anything the parser cannot
  classify — a zero-refusal plan now provably covers the whole index before
  any reset runs.
- **`st absorb` refuses hunks whose coordinates moved since the owning
  commit.** Blame attribution now keeps each line's original line number and
  path (`git blame --line-porcelain`), and a hunk is accepted only when every
  removed line maps to the same line number and path at its owning stack
  commit. Lines shifted by a descendant's insert/delete and lines whose file
  was renamed are refused — the apply lands a zero-context patch at HEAD line
  numbers on the ancestor's tree, where shifted coordinates could hit the
  wrong occurrence of repeated text.
- **`st continue` records the rebase target that was actually incorporated.**
  When a child's rebase was paused on a conflict and the parent ref moved
  before `st continue`, the child now records the target captured from the
  paused rebase's own metadata (`rebase-merge/onto`) — not the parent's new
  tip — so the follow-up restack still recognizes the move and lands the
  parent's new commits on the child. A missing or unreadable target is an
  actionable error before the rebase is continued, never a silent guess.
- **An abandoned `lock.reclaim` is no longer reclaimed automatically.** On
  platforms without `flock`, `st` used to read/compare/unlink a stale
  `lock.reclaim` guard — a non-atomic sequence that could admit two
  contenders into stale `lock.excl` reclamation at once. Acquisition is now
  exclusive-create only: a live or freshly-written guard stays ordinary
  contention (exit 5), while a provably abandoned guard is a maintenance
  error naming its path (exit 5, code `"locked_guard"`). Stop every `st` process, verify no writer
  is active, then remove the named file — see CONTRIBUTING's troubleshooting
  section. Stale `lock.excl` reclamation under a freshly owned guard is
  unchanged.
- **Manual worktree navigation hints are paste-safe shell commands.** Without
  the `st shell install` integration, teleport output used to print
  `run: cd <path>` with the raw path — a path containing spaces pasted wrong,
  and one containing `;`/`$(...)`/backticks executed as shell syntax. The hint
  now emits `run: cd -- '<path>'` (POSIX single-quoting, so embedded
  apostrophes are handled too); when the path contains terminal control bytes
  no executable command is offered — the output points at the shell
  integration or the `--json` `worktree` field instead, since an escaped
  spelling would not name the real directory.
- **Worktree paths are parsed byte-exactly.** `git worktree list` now uses
  the NUL-framed `--porcelain -z` grammar on git ≥ 2.36, so a path containing
  a newline (or text that would look like a record field) can no longer
  split into a truncated path plus a forged worktree record. Older git keeps
  the line-based listing, but only after the worktree registration metadata
  (`worktrees/*/gitdir`) proves no path carries CR/LF bytes — an ambiguous
  repo gets an actionable error naming git ≥ 2.36 instead of a corrupted
  ownership map.
- **`.worktreeinclude` copies can no longer overwrite or merge into occupied
  destination paths.** Before the first file is copied, every selected entry
  is preflighted against the destination worktree: a path that is tracked
  there (`git ls-files` — including tracked-but-missing worktree files),
  that has a tracked descendant or ancestor, or that already exists on disk
  in any form (file, symlink, or directory — even an empty one) now fails the
  whole copy loudly. A later colliding entry prevents even earlier
  candidates from being copied, a copy failure still removes a worktree that
  was just materialized for it, and when the native `cp` fails after
  creating partial output the plain-copy fallback no longer merges into it.
- **`st submit` reports every confirmed push outcome instead of retrying the
  batch.** The single `git push --porcelain` invocation's own per-ref
  statuses now drive the result: when the remote accepts A and C but rejects
  B in the same push, `--json` reports `pushed` `[A,C]` in stack order and
  `failed` `B` — the old fallback's per-branch retries stopped at B and hid
  C's confirmed update. A ref whose outcome the push did not report is
  counted in neither field; the error describes the uncertainty instead of
  guessing.
- **`st undo` refuses state written by a newer `st` before mutating anything.**
  Both the current `state.json` and the newest undo journal snapshot are
  schema-checked up front — a file or snapshot with a schema version newer
  than this binary understands now fails with "state file written by newer
  st" before any branch deletion, worktree removal, ref restore, checkout,
  or save, leaving state, journal, refs, index, and cwd untouched. Legacy
  (version-less) and current snapshots still undo, and the supported
  malformed-current-state recovery path is unchanged.
- **Squash-merge detection keeps both sides of a rename.** `st sync`'s
  containment check now enumerates rename sources as well as destinations, so
  a branch that renames A to B is no longer pruned when upstream merely copied
  A to B while keeping A — the branch's deletion of A has not landed.
- **`st log` no longer materializes the whole reachable history.** The
  topCommit visibility question — "does this branch's tip hold commits its
  parent's tip cannot reach" — is now answered by one bounded
  `merge-base --is-ancestor` probe per distinct tip pair, cached for the
  render, instead of a `git rev-list --parents` walk plus an in-memory graph
  over every commit. Go-side memory and subprocess output no longer scale
  with total commit count (git still walks internally, and a trunk-only log
  asks no ancestry question at all). Output is unchanged.
- **`st absorb` only attributes hunks to on-path tips.** A hunk whose owning
  commit is tipped only by a tracked branch *off* the current stack's path is
  now refused, naming that branch — previously it could be attributed to the
  off-path tip, silently landing the staged change in a different stack.
- **0.0.1 notes clarifications.** The `[0.0.1]` absorb entry describes the
  initial single-target slice; the multi-target apply is under `Added` above.
  `st restack --all` (whole-forest restack from anywhere) and `st worktree
  --all` (materialize every tracked branch's worktree) also shipped in 0.0.1
  but were omitted from its notes.

## [0.0.1] - 2026-07-12

### Added
- **`st absorb` (v1).** `st absorb --dry-run` maps each staged hunk to the
  stack commit that owns its lines (blame-based, refusing everything ambiguous
  with a reason); bare `st absorb` applies a single-target plan — the owning
  branch tip is amended via a checkout-free temp-index commit, descendants are
  restacked, and one `st undo` reverts both.
- **`st worktree rm --all`** tears down the clean linked worktree of every
  tracked branch that has one in a single command (dirty ones and the main
  worktree's branch are skipped loudly; untracked branches' worktrees are
  left alone).
- Shell completion now completes flags and sub-verbs per command
  (bash/zsh/fish), not just command names.
- Compare URLs from `st submit` for self-hosted GitLab and GitHub Enterprise
  remotes (forge detection by host label).
- `st version` reports the module build version for `go install` builds
  instead of always printing the compiled-in default.
- Initial `st` CLI: a login-free, dependency-free tool for stacked
  diffs — `init`, `create`, navigation (`up`/`down`/`top`/`bottom`/`checkout`),
  `log`, `status`, `track`/`untrack`, `modify`, `restack`, `continue`, `abort`,
  `fold`, `squash`, `onto`, `rename`, `delete`, `sync`, `submit`, `undo`,
  `validate`, `repair`, `completion`.
- **`st create <name> --worktree`** creates the branch, tracks it, and
  materializes its own linked worktree in one command; the main worktree's HEAD
  does not move, and with the shell shim installed the shell teleports into the
  new worktree. `-m`/`-a` are rejected in this mode — commit inside the new
  worktree instead.
- **Per-branch PR compare URLs from `st submit`.** After pushing, text mode
  prints one `head -> base  <compare URL>` line per branch for github.com
  (`/compare/base...head`) and gitlab.com (`/-/compare/base...head`) remotes;
  `--json` carries the same data as `prHints` (documented in `docs/AGENT.md`).
- **Windows CI test job.** The engine and lock paths now run on
  `windows-latest` in CI, exercising the windows-only lock classifiers for real.
- **Owner-driven cross-worktree restack cascade.** `st restack` / `st sync` now
  rebase a dependent branch that lives in another worktree *inside that worktree*
  (git forbids rebasing a branch checked out elsewhere), gated on a clean tree: a
  dirty dependent worktree is skipped with a clear note rather than clobbered, and
  a conflict during the cross-worktree rebase is rolled back in that worktree
  rather than left paused. Single-tree restack/sync behavior is unchanged.
- **Worktree-aware lifecycle ops.** `st delete`, `st fold`, and `st sync`'s
  prune-merged step now tear down a *clean* linked worktree that owns a branch they
  remove (git refuses to delete a branch checked out elsewhere) before deleting the
  branch. A *dirty* owning worktree is refused with a clear error — nothing is
  deleted and no in-progress work is discarded; commit/stash or `st worktree rm`
  first. Single-tree behavior is unchanged.
- `--dry-run` on `onto`, `fold`, `squash`, and `delete` previews the branches
  that would be moved, folded, squashed, deleted, or restacked (a
  `{"dryRun":true,...}` result) without changing stack metadata or branch refs.
- **`st worktree` (alias `wt`)** materializes, lists, and removes a branch's own
  git worktree under `~/.stacked/worktrees/` using a collision-resistant repo key
  and encoded branch segment, copying `.worktreeinclude` matches (literal
  paths and shell-glob patterns incl. `**`, gitignored-only, copy-on-write
  reflink) into it — so multiple agents
  can work different branches of one stack in parallel.
- **`st shell install [bash|zsh|fish]`** prints a shell shim so `st checkout`/`up`/
  `down`/`top`/`bottom` can teleport (`cd`) into a branch's worktree; without the
  shim the destination path is printed.
- **Worktree-aware stack views (foundation).** `st log` and `st status` annotate,
  in a multi-worktree repo, which linked worktree each branch lives in and whether
  it is dirty; `log --json` gains `worktree`/`dirty` and `status --json` gains
  `worktree` (all `omitempty`, so single-tree output is byte-for-byte unchanged).
  Backed by a new `Worktrees()` git port method (parsing `git worktree list
  --porcelain`) and pure worktree-path/ownership helpers in `internal/stack`.
- `--dry-run` on `restack` and `sync` previews the branches that would be rebased
  or pruned (a `{"dryRun":true,...}` result) without changing anything.
- `st guide` prints the recommended end-to-end workflow (text or `--json`).
- **Agent-native interface.** Every command (except `completion` and `shell`)
  accepts `--json` with stable schemas; failures emit a structured
  `{"error":{"code","message"}}` envelope on stderr. Documented in `docs/AGENT.md`.
- **Semantic exit codes**: `0` ok, `1` usage/generic, `2` conflict (run
  `st continue`), `3` not initialized, `4` dirty working tree, `5` repo lock
  held (retry after it clears), `70` internal error (a bug in `st`).
- `submit --json` reports a partial-push failure through a `failed` field naming
  the branch whose push failed (the branches in `pushed` were already pushed);
  documented in `docs/AGENT.md`.
- `st help <command>` prints a command's summary, usage, and aliases.
- Conflict and sync logic is exercised by millisecond fake-git tests, including a
  property/invariant model test over thousands of random operation sequences.

### Security
- **Ref-update injection hardening.** Transactional ref restores
  (`st undo`) use NUL-framed `git update-ref -z --stdin`, so branch names can
  never be misparsed as record framing.
- **Terminal output sanitization.** Git-controlled strings (commit subjects,
  branch names, worktree paths) are control-byte-escaped before rendering, on
  stdout and on the stderr error path, closing a terminal escape-injection
  vector. JSON output is unaffected (encoding/json already escapes).

### Fixed
- **`st sync` from a linked worktree no longer deletes that worktree.** The
  worktree cache is invalidated on checkout/detach, so prune sees the true
  layout.
- Nested `.worktreeinclude` selections are no longer copied twice into a new
  worktree.
- Sync's "HEAD left detached" note reflects where HEAD actually landed.
- **`st sync` works from inside a linked worktree.** The trunk fast-forward now
  runs in the trunk's own worktree (or moves the ref directly, fast-forward
  only, when the trunk is checked out nowhere), and pruning no longer requires
  checking out the trunk locally. A dirty trunk worktree blocks sync with an
  error naming its path.
- **`st undo` releases a created branch's worktree even when unrecorded.** The
  recovery sequence after a failed `st create --worktree` (retry with
  `st worktree <name>`, then undo) no longer fails on git's refusal to delete a
  branch checked out in a linked worktree; a clean worktree is released first,
  a dirty one still refuses.
- **Windows lock access-denied classification.** Transient
  `ERROR_ACCESS_DENIED`/`ERROR_SHARING_VIOLATION` during lock-file races are
  treated as contention and retried, while a stale lock that cannot be
  reclaimed for permission reasons now reports a permission error instead of
  "another st command is running".
- Non-flock platforms use a real lock file with stale-owner reclamation instead
  of a no-op lock.
- Git output parsing is locale-pinned, and fast-forward detection uses plumbing
  instead of message text.
- Parent inference (`st track`) is deterministic.

### Changed
- `st submit` pushes the whole stack in a single `git push` invocation; on a
  failure it falls back to per-branch pushes so partial results are still
  reported.
- The stack operations were extracted into a pure **engine** (`internal/stack`)
  behind a small git **port**; `cmd/` commands are now thin adapters. `sync` and
  `continue` moved behind a `Remote` port and are fully fake-testable.
- The feedback loop runs the suite once (race + merged in-process/e2e coverage)
  instead of three times; `make test-fast` is about a second.
- `submit --json` emits one unified result shape instead of per-mode shapes.
  `delete` results include the `restacked` list. `log --json` always includes
  `children` (empty array on leaves).

### Removed
- Redundant slow `cmd` integration tests now covered by the engine and e2e suites;
  duplicated `restoreHEAD`/clean-check/fast-forward helpers.

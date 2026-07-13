# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **`st absorb` applies multi-target plans.** A staged set whose hunks belong
  to different stack branches now lands as one amend per owning tip (each
  with only its own hunks, post-image line numbers corrected for same-file
  splits) plus a single cascade restack; one `st undo` reverts everything.
  All-or-nothing: any refusal or dirty target worktree leaves the whole plan
  unapplied.


## [0.0.1] - 2026-07-12

### Added
- **`st absorb` (v1).** `st absorb --dry-run` maps each staged hunk to the
  stack commit that owns its lines (blame-based, refusing everything ambiguous
  with a reason); bare `st absorb` applies a single-target plan — the owning
  branch tip is amended via a checkout-free temp-index commit, descendants are
  restacked, and one `st undo` reverts both.
- **`st worktree rm --all`** tears down every clean stacked-owned linked
  worktree in one command (dirty ones are skipped loudly).
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
- **Agent-native interface.** Every command (except `completion`) accepts `--json`
  with stable schemas; failures emit a structured `{"error":{"code","message"}}`
  envelope on stderr. Documented in `docs/AGENT.md`.
- **Semantic exit codes**: `0` ok, `1` usage/generic, `2` conflict (run
  `st continue`), `3` not initialized, `4` dirty working tree.
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

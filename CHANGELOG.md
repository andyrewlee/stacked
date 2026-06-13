# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `--dry-run` on `restack` and `sync` previews the branches that would be rebased
  or pruned (a `{"dryRun":true,...}` result) without changing anything.
- `st guide` prints the recommended end-to-end workflow (text or `--json`).
- **Agent-native interface.** Every command (except `completion`) accepts `--json`
  with stable schemas; failures emit a structured `{"error":{"code","message"}}`
  envelope on stderr. Documented in `docs/AGENT.md`.
- **Semantic exit codes**: `0` ok, `1` usage/generic, `2` conflict (run
  `st continue`), `3` not initialized, `4` dirty working tree.
- `st help <command>` prints a command's summary, usage, and aliases.
- Conflict and sync logic is exercised by millisecond fake-git tests, including a
  property/invariant model test over thousands of random operation sequences.

### Changed
- The stack operations were extracted into a pure **engine** (`internal/stack`)
  behind a small git **port**; `cmd/` commands are now thin adapters. `sync` and
  `continue` moved behind a `Remote` port and are fully fake-testable.
- The feedback loop runs the suite once (race + merged in-process/e2e coverage)
  instead of three times; `make test-fast` is about a second.
- `submit --json` emits one unified result shape instead of per-mode shapes.
  `delete` results include the `restacked` list. `log --json` always includes
  `children` (empty array on leaves).

### Fixed
- Non-flock platforms use a real lock file with stale-owner reclamation instead
  of a no-op lock.
- Git output parsing is locale-pinned, and fast-forward detection uses plumbing
  instead of message text.
- Parent inference (`st track`) is deterministic.

### Removed
- Redundant slow `cmd` integration tests now covered by the engine and e2e suites;
  duplicated `restoreHEAD`/clean-check/fast-forward helpers.

## [0.1.0]

### Added
- Initial `st` CLI: a login-free, dependency-free tool for stacked
  diffs — `init`, `create`, navigation (`up`/`down`/`top`/`bottom`/`checkout`),
  `log`, `status`, `track`/`untrack`, `modify`, `restack`, `continue`, `abort`,
  `fold`, `squash`, `onto`, `rename`, `delete`, `sync`, `submit`, `undo`,
  `validate`, `repair`, `completion`.

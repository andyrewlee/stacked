# AGENTS.md

`st` (`stacked`) — a standard-library-only Go 1.26 CLI for stacked git diffs with
native GitHub stacked-PR support via `gh`.

## Loop

- `make test-fast` — sub-second pure engine tests (`./internal/stack`) over fake git/GitHub.
- `make ci` — the only gate (pin checks, `gofumpt`, strict `golangci-lint`, `vet` + windows/plan9 cross-vet, `-race` tests, `e2e`, merged coverage ≥75% / per-function ≥50%, installer checks).
- `make golden` — regenerate `cmd/testdata/*.golden` when help/log/status output changes.

## Hard constraints

- **Zero dependencies:** `go.mod` must keep zero `require` entries; no `go.sum`.
- **Untracked-file safe:** `IsClean`/`HasUnstagedChanges` ignore untracked files (`--untracked-files=no`), `git.Add()` stages tracked files only (`git add -u`), and `st modify` defaults to staged-only.
- **Worktree safe:** cooperate with external `git worktree`s (shared metadata, cross-worktree restack, paused-rebase guards), never create or delete worktrees on disk.
- **Probes, not stderr:** classify git state via port probes, not error-message parsing; keep concurrent read probes safe under `-race`.

## Layout

- `internal/git/` — `git` wrapper + `git.Shell` (`ST_DEBUG=1` traces spawns on stderr).
- `internal/gh/` — `gh` CLI wrapper (`ST_GH_BIN` override; disabled by default in `cmd`/`e2e` tests).
- `internal/stack/` — pure engine (`ops_*.go`, `github.go`, `restack.go`, `undo*.go`, `repair.go`, `worktree.go`); tested against in-memory fakes and `conformance_test.go`. Checkpoint mid-op with `env.save()` so conflicts never lose prior progress.
- `cmd/` — thin adapters (`flagsets.go` is the single flag source for `Run` + `help --json`; `mutate()` handles lock → undo → op → save).
- `e2e/` — black-box subprocess tests against the built `st` binary.

See `docs/AGENT.md` for the CLI JSON/exit-code contract (enforced by `TestAgentDoc*`) and `CONTRIBUTING.md` for adding commands and releasing.

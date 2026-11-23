# CLAUDE.md

Notes for agents working on **stacked** (the `st` CLI). This repo is built to be
refactored with confidence: close the loop with tests + lint, don't hand-test.

## What this is

A sharp, dependency-free CLI for stacked git diffs with native GitHub stacked-PR
support via the `gh` CLI. The Go module/project is `stacked`; the binary/CLI
command is `st`. It shells out to system `git` (and optionally `gh`) and stores
stack topology locally under `.git/stacked/`.

## Hard constraints

- **Standard library only.** `go.mod` must keep **zero require entries** (verify:
  `go mod tidy` leaves it empty). `golangci-lint` is an external binary, never a dep.
- **Go 1.26.** Keep it `gofmt`/`gofumpt`-clean and pass strict `golangci-lint`.
- **Untracked-file safe.** `git.IsClean` / `HasUnstagedChanges` ignore untracked
  files (`--untracked-files=no`), `git.Add()` stages tracked files only
  (`git add -u`), and `st modify` defaults to `-a=false` (staged only).
- **Never delete foreign worktrees.** `st` cooperates with externally created
  `git worktree`s (cross-worktree restack, shared metadata, paused-rebase guards)
  but never creates or deletes worktrees on disk.

## The feedback loop (this is the point)

```
make test-fast   # about a second: the stack engine package over the fake git (./internal/stack)
make ci          # THE gate — there is no remote CI: check-deps + pin checks +
                 #   fmt-check + lint + vet + vet-cross + build + race tests +
                 #   e2e + merged-coverage gate (>=75%) + installer checks
make hooks       # install pre-commit (fast loop) + pre-push (make ci)
```

`make ci` is the single source of truth — the pre-push hook runs the exact same
target and nothing else does. If `make ci` is green, you can commit/refactor
without manual testing. The inner loop you hit constantly is `make test-fast`.

## Architecture (why the loop is fast)

The tricky logic is a **pure engine** decoupled from git and GitHub, so it tests
in milliseconds against in-memory fakes instead of spawning subprocesses.

```
internal/git/        the git wrapper + git.Shell (the production port impl),
                     split by domain: index/rebase/refs/remote/worktree.go;
                     debug.go is the ST_DEBUG spawn trace
internal/gh/         thin zero-dependency `gh` CLI wrapper + gh.Shell
                     (ST_GH_BIN override for tests)
internal/stack/
  git.go             the Git PORT interface + Env{Git, Save}
  stack.go           State/Branch types + topology helpers (Children/Descendants/…)
  restack.go         restack primitives (NeedsRestack/restackBranch/restackUpstack)
                     + restack dry-run planners (RestackPlan/RestackAllPlan)
  plan.go            dry-run planners for other mutating ops
  engine.go          shared op machinery (OpResult, env.save, error taxonomy)
  ops_*.go           operations by domain: ops_lifecycle (Create/Modify),
                     ops_restack (Restack), ops_combine (Squash),
                     ops_onto (Onto/Continue/Abort),
                     ops_delete_sync (Delete/SyncWithOptions),
                     ops_track (TrackBranch/UntrackBranch/Rename) → *OpResult
  submit.go          submit push-order planning + PR hint/URL derivation
  github.go          GitHub port + RetargetStackPRs/SyncStackPRs/MergedGitHubBranches
                     + idempotent `<!-- st:stack:start -->` block rendering
  repair.go          Repair + the problem kinds `st doctor` reports
  worktree.go        read-only worktree ownership & paused-rebase helpers
  store.go undo*.go lock_*.go   persistence, undo journal + Undo op, flock
cmd/                 thin adapters: parse flags → mutate(label, json, engineFn) → render
cmd/st/main.go       package main → os.Exit(cmd.Execute())
e2e/                 black-box tests driving the real binary as a subprocess
```

- **Engine functions** take `(stack.Env, *stack.State, params)` and return
  `(*stack.OpResult, error)`. They never lock, print, or load — pure transforms
  over the State + git port. `Env.Git` is the port; `Env.Save` is a persistence
  hook the engine calls at safe checkpoints (nil in tests = no-op).
- **`cmd.mutate(label, asJSON, fn)`** wraps every mutation: lock → load → record
  undo → run `fn(env, s)` → save → render (text or `--json`).

## How to add a command (recipe)

1. Add the operation to the matching `internal/stack/ops_*.go` file (or a new
   one for a new domain):
   ```go
   func Frobnicate(env Env, s *State, arg string) (*OpResult, error) {
       // mutate s and call env.Git.* ; checkpoint with env.save() if needed
       return &OpResult{Summary: "...", Branch: "..."}, nil
   }
   ```
2. Add a unit test in `internal/stack/engine_test.go` using `newEnvState()` + the
   fake git (`mkBranch` helper). Runs in microseconds.
3. Add `cmd/frob.go` — a thin adapter that parses flags and calls
   `mutate("frob", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) { return stack.Frobnicate(env, s, arg) })`,
   self-registering via `init()` → `register(&Command{...})`. Add a `--json` flag.
   If `frob` parses any flag beyond `--json`, declare those flags once in
   `cmd/flagsets.go` (`frobOpts`, `newFrobFlags`, `frobFlagSet`) and wire
   `NewFlagSet: frobFlagSet` so `st help --json` never drifts.
4. Update `docs/AGENT.md` with any new JSON fields (`TestAgentDoc*` enforces parity).
5. If the command takes a positional branch name, set `Completion` on the
   `Command` registration so `st __complete` offers branch names.
6. Run `make golden` if help/log/status output changed, then `make ci`.

## Invariants the tests enforce

`internal/stack/model_test.go` applies thousands of random op sequences and, after
every step, asserts: the forest is **acyclic** with valid parents, every branch
**contains its recorded base** (`parentSHA` is an ancestor of its tip), a full
restack **reconciles** everything, and restack is **idempotent**.

## Test layers

- `internal/stack/*_test.go` — pure engine: topology, store, fake-git engine
  unit tests, GitHub PR sync tests (`github_test.go`), model/invariant tests,
  fuzz, and `conformance_test.go` (fake vs `git.Shell` parity + port coverage guard).
- `internal/gh/*_test.go` — `gh` CLI wrapper tests using mock `ST_GH_BIN`.
- `cmd/*_test.go` — adapters over real git (integration), mock `gh` lifecycle
  (`github_test.go`), dispatcher, parseArgs, golden output, `docs/AGENT.md` contract.
- `e2e/*_test.go` — black-box: builds the real binary (harness in `e2e_test.go`)
  and drives it as a hermetic subprocess (isolated HOME/git config, `ST_GH_BIN`
  disabled by default, mock `gh` in `e2e_github_test.go`). Contributes to
  coverage via `GOCOVERDIR`.

## Conflicts & gotchas

- A restack conflict leaves a real git rebase in progress; the op returns an error
  pointing at `st continue` (finishes + resumes) or `st abort` (rolls back).
- `Onto` changes `Parent`/`ParentSHA` only after a successful rebase. A paused
  conflict preserves the old parent and records `PendingReparent`; `st continue`
  promotes that intent after the rebase completes, while `st abort` clears it.
- Mutators take an advisory lock: flock on unix-like platforms
  (`internal/stack/lock_unix.go`), an exclusive lock file with stale-owner
  reclamation elsewhere (`internal/stack/lock_other.go`, `lock_stale.go`).
- **Decisions run on state probes, never on git's stderr** — classify with port
  probes (`OwnerOf`/`LinkedOwnerOf`, `RebaseInProgressIn`, `IsCleanIn`).
- **Independent read-only probes fan out via `git.ParallelProbes`** (bounded
  workers, deterministic index order). Port implementations must be safe under
  concurrent reads (`-race` is load-bearing).
- **GitHub activation rule (`cmd/submit.go`)**: GitHub PR sync auto-activates
  when `!noPR && gh.Available() && (ST_GH_FORCE=1 || stack.IsGitHubRemote(url))`.
  In tests, `cmd/testenv_test.go` and `e2e/e2e_test.go` point `ST_GH_BIN` at a
  nonexistent sentinel by default so non-GitHub tests never hit real `gh`.

# CLAUDE.md

Notes for agents working on **stacked** (the `st` CLI). This repo is built to be
refactored with confidence: close the loop with tests + lint, don't hand-test.

## What this is

A login-free, dependency-free CLI for stacked git diffs. The Go module/project is
`stacked`; the binary/CLI command is `st`. It shells out to the system `git` and
stores stack topology locally — no host API.

## Hard constraints

- **Standard library only.** `go.mod` must keep **zero require entries** (verify:
  `go mod tidy` leaves it empty). golangci-lint is an external binary, never a dep.
- **Go 1.26.** Keep it `gofmt`/`gofumpt`-clean and pass strict `golangci-lint`.

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

The tricky logic is a **pure engine** decoupled from git, so it tests in
milliseconds against an in-memory fake instead of spawning git.

```
internal/git/        the git wrapper + git.Shell (the production port impl)
internal/stack/
  git.go             the Git PORT interface + Env{Git, Save}
  stack.go           State/Branch types + topology helpers (Children/Descendants/…)
  restack.go         restack primitives (NeedsRestack/restackBranch/restackUpstack)
                     + the restack dry-run planners (RestackPlan/RestackAllPlan)
  plan.go            dry-run planners for the other mutating ops (FoldPlan/…)
  engine.go          the operations: Create/Modify/Restack/Fold/Squash/Onto/Delete/
                     Sync/Abort/Continue/TrackBranch/UntrackBranch/Rename → *OpResult
  absorb.go          staged-hunk attribution + tip-amend apply (AbsorbPlan/Absorb)
  repair.go          Repair + the problem kinds `st validate` reports
  worktree*.go       worktree paths/ownership + .worktreeinclude validation
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
  undo → run `fn(env, s)` → save → render (text or `--json`). That is why each
  command file is ~15 lines.

## How to add a command (recipe)

1. Add the operation to `internal/stack/engine.go`:
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
   `cmd/flagsets.go`: a `frobOpts` struct and `newFrobFlags(o *frobOpts)` (the single
   declaration site), plus a no-arg `frobFlagSet()` wrapper. `runFrob` calls
   `fs := newFrobFlags(&o)` and reads `o.<field>`; `register` sets
   `NewFlagSet: frobFlagSet`. `Run` and `st help --json` then build the flags from
   the same constructor, so the declared-flags contract in `docs/AGENT.md` can't drift.
4. If it has interesting CLI output, add a golden test (`cmd/golden_test.go`,
   regenerate with `go test ./cmd -run Golden -update`).
5. If the command takes a positional branch name, register a completion policy
   in `cmd/complete.go`'s `branchCompletionCommands`/`completeCandidates` switch
   so `st __complete` (and the generated shell hooks) offer branch names; the
   positional classifier picks up new value-taking flags automatically from the
   same flagset.
6. `make ci`. Adding the command shifts the help golden — regenerate it deliberately.

## Invariants the tests enforce

`internal/stack/model_test.go` applies thousands of random op sequences and, after
every step, asserts: the forest is **acyclic** with valid parents, every branch
**contains its recorded base** (`parentSHA` is an ancestor of its tip), a full
restack **reconciles** everything, and restack is **idempotent**. If you change the
engine and these hold, the topology bookkeeping is sound.

## Test layers

- `internal/stack/*_test.go` — pure engine: topology, store, **fake-git engine
  unit tests**, the **model/invariant** test, fuzz. Fast; the inner loop.
- `cmd/*_test.go` — adapters over real git (integration), dispatcher, parseArgs,
  golden output.
- `e2e/*_test.go` — black-box: builds the real binary (harness in `e2e_test.go`)
  and drives it as a hermetic subprocess (isolated HOME/git config); journeys in
  `e2e_journey_test.go`, CLI-contract in `e2e_contract_test.go`. Contributes to
  coverage via `GOCOVERDIR` (built `-cover -covermode=atomic` so it merges with
  the race-instrumented run).

## Conflicts & gotchas

- A restack conflict leaves a real git rebase in progress; the op returns an error
  pointing at `st continue` (finishes + resumes) or `st abort` (rolls back). The
  fake git can model paused rebase conflicts (`conflictOn`) for fast engine tests;
  use real-git integration/e2e for worktree, index, and conflict-marker behavior.
- `Onto` changes `Parent`/`ParentSHA` only after a successful rebase. A paused
  conflict preserves the old parent and records `PendingReparent`; `st continue`
  promotes that intent after the rebase completes, while `st abort` clears it
  and keeps the old parent. See `TestOntoConflictRecordsPendingReparentWithoutChangingParent`
  in `internal/stack/engine_test.go`.
- Mutators take an advisory lock: flock on unix-like platforms
  (`internal/stack/lock_unix.go`), an exclusive lock file with stale-owner
  reclamation elsewhere (`internal/stack/lock_other.go`, `lock_stale.go`).

## Absorb refuses everything ambiguous

`st absorb --dry-run` maps staged hunks to the stack commits that own their
lines; bare `st absorb` applies any ZERO-REFUSAL plan, which may span several
target branches — each target branch's tip is amended with only its own hunks
via a temp-index (no checkout), then one cascade restack from the lowest
target; one undo entry reverts all amends plus the cascade. Attribution is
restricted to the current stack's path (cur plus its ancestors): a commit
tipped only by an off-path tracked branch is refused naming that branch, and
a tip shared by several on-path branches goes to the lowest sharer. Everything
ambiguous is refused loudly: hunks spanning commits, pure additions, lines
owned by trunk/history, non-tip or off-path targets, hunks whose blame
provenance does not map each removed line to the same line number and path
at the owning commit (a descendant's insert/delete shifts the line; a rename
changes the path — either would land the -U0 patch at the wrong position in
the ancestor's tree), and unclassifiable
staged records (binary/mode/rename/quoted paths) — splitting hunks or
rewriting mid-branch commits is out of scope. The staged diff itself is
captured with pinned flags (`--no-color --no-ext-diff --no-textconv
--src-prefix=a/ --dst-prefix=b/`), so user diff config cannot reshape it, and
every section must yield hunks or a refusal — metadata-only changes like
empty added/deleted files are refused, and a rename-disabled path inventory
cross-check refuses anything the parser could not account for. The decision
table lives in the doc comments in `internal/stack/absorb.go`.

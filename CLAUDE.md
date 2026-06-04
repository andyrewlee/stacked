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
make test-fast   # sub-second: pure engine logic over the fake git (./internal/...)
make ci          # full gate = pre-push hook = CI: fmt-check + lint + vet + build
                 #   + race tests + e2e + merged-coverage gate (>=75%)
make hooks       # install pre-commit (fast loop) + pre-push (make ci)
```

`make ci` is the single source of truth — the pre-push hook and CI run the exact
same target. If `make ci` is green, you can commit/refactor without
manual testing. The inner loop you hit constantly is `make test-fast`.

## Architecture (why the loop is fast)

The tricky logic is a **pure engine** decoupled from git, so it tests in
milliseconds against an in-memory fake instead of spawning git.

```
internal/git/        the git wrapper + git.Shell (the production port impl)
internal/stack/
  git.go             the Git PORT interface + Env{Git, Save}
  stack.go           State/Branch types + topology helpers (Children/Descendants/…)
  restack.go         restack primitives (NeedsRestack/RestackBranch/RestackUpstack)
  engine.go          the operations: Create/Modify/Restack/Fold/Squash/Onto/Delete/
                     Track/Untrack/Rename/RestackAll/PruneMerged → return *OpResult
  store.go undo.go lock_*.go   persistence, undo journal, flock
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
4. If it has interesting CLI output, add a golden test (`cmd/golden_test.go`,
   regenerate with `go test ./cmd -run Golden -update`).
5. `make ci`. Adding the command shifts the help golden — regenerate it deliberately.

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
- `e2e/e2e_test.go` — black-box: builds the real binary and drives it as a hermetic
  subprocess (isolated HOME/git config). Contributes to coverage via `GOCOVERDIR`
  (built `-cover -covermode=atomic` so it merges with the race-instrumented run).

## Conflicts & gotchas

- A restack conflict leaves a real git rebase in progress; the op returns an error
  pointing at `st continue` (finishes + resumes) or `st abort` (rolls back). In the
  fake git, rebases never conflict — conflict handling is covered by integration/e2e.
- `Onto` records the new parent in state **before** rebasing so `st continue`
  computes the right base after a conflict.
- Mutators take the flock lock (`internal/stack/lock_unix.go`); no-op off unix.

## Deliberately not implemented

`absorb` (auto-distributing staged hunks to ancestor commits) — a large blame/fixup
subsystem; left as a future item rather than shipped partial.

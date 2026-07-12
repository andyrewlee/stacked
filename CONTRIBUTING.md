# Contributing to stacked

The repo is built so you can change it with confidence: one command closes the
loop, and the engine you'll touch most is tested in milliseconds.

## The loop

```sh
make test-fast   # about a second: pure engine logic over the fake git
make ci          # the full gate (= the pre-push hook = CI)
make hooks       # install pre-commit (fast loop) + pre-push (make ci)
```

`make ci` is the single source of truth: `fmt-check` + strict `golangci-lint` +
`vet` + `build` + race tests + black-box e2e + a merged-coverage gate (≥75%). If
it's green, you can commit.

The coverage gate also enforces a **per-function floor** (default 50%,
`COVERAGE_FUNC_MIN` to override): a new function below the floor fails the
build and is listed as `<path>	<func>`. Either add tests, or — only for
platform stubs and production-overridden port methods — add a justified entry
to `scripts/cover-allow.txt` (matched on path + function, each line carrying a
`# why` comment). The allowlist is a ratchet: entries should only be removed;
allowlisting new feature code defeats the gate's purpose. Keep the tool **standard-library only** — `go.mod` must
have zero `require` entries (`go mod tidy` stays a no-op).

The lint step needs **golangci-lint v2** on your `PATH` (an external binary, never
a module dependency); `make lint` preflights for it and prints the install line:
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.
Development also requires **Go 1.26+** and **Git 2.17+** on your `PATH`; **Git
2.31+** is recommended for native common-dir path resolution, while older
supported Git versions use a fallback.

## Architecture in one breath

The tricky logic is a pure **engine** (`internal/stack`) that talks to git through
a small **port** interface, so it tests against an in-memory fake instead of
spawning git. Commands in `cmd/` are thin adapters: parse flags → `mutate()` →
render. See `CLAUDE.md` for the full map and `docs/AGENT.md` for the machine
interface (JSON, exit codes).

## Adding a command (recipe)

1. **Engine** — add the operation to `internal/stack/engine.go`:
   ```go
   func Frobnicate(env Env, s *State, arg string) (*OpResult, error) {
       // mutate s, call env.Git.* (and env.save() at safe checkpoints)
       return &OpResult{Summary: "...", Branch: "..."}, nil
   }
   ```
2. **Test it fast** — `internal/stack/engine_test.go`, using `newEnvState()` and
   the fake git (`mkBranch`). Microseconds, no real git.
3. **Adapter** — `cmd/frob.go`, a thin wrapper that self-registers and calls
   `mutate("frob", asJSON, func(env stack.Env, s *stack.State) (*stack.OpResult, error) { return stack.Frobnicate(env, s, arg) })`.
   Add a `--json` flag (see `docs/AGENT.md` — every command speaks JSON). For any
   flag beyond `--json`, declare it once in `cmd/flagsets.go` (a `frobOpts` struct +
   `newFrobFlags(o)` constructor + a no-arg `frobFlagSet()` wrapper); `runFrob` reads
   `o.<field>` and `register` sets `NewFlagSet: frobFlagSet`, so `help --json` reports
   exactly what `Run` parses, from the same declaration.
4. **Golden output** (optional) — `cmd/golden_test.go`; regenerate with
   `go test ./cmd -run Golden -update`.
5. `make ci`. Adding a command shifts the `--help` golden — regenerate it
   deliberately.

A command that needs the remote goes through the `Remote` port (see `Sync`); a
read-only command skips `mutate()` and renders with `emit()`.

## Invariants

`internal/stack/model_test.go` runs thousands of random op sequences and asserts,
after every step: the forest is acyclic with valid parents, every branch contains
its recorded base, a full restack reconciles, and restack is idempotent. If you
change the engine and these still hold, the topology bookkeeping is sound.

## Test layers

- `internal/stack/*_test.go` — engine over the fake git (incl. conflicts + sync).
  **The inner loop.**
- `cmd/*_test.go` — the adapter layer: dispatch, flag parsing, output rendering.
- `e2e/e2e_test.go` — the real binary as a hermetic subprocess; contributes to
  coverage via `GOCOVERDIR`.

## Releasing

Releases are cut from a tag:

```sh
git tag v0.2.0
make release          # build and publish the release (needs a publish token)
# or dry-run locally:
make snapshot         # build the release artifacts without publishing
```

Update `CHANGELOG.md` before tagging.

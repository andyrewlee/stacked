# Contributing to stacked

The repo is built so you can change it with confidence: one command closes the
loop, and the engine you'll touch most is tested in milliseconds.

## The loop

```sh
make test-fast   # about a second: pure engine logic over the fake git
make ci          # the full gate (= the pre-push hook; there is no remote CI)
make hooks       # install pre-commit (fast loop) + pre-push (make ci)
```

`make ci` is the single source of truth — and the ONLY gate: version-pin
agreement checks, `fmt-check`, strict `golangci-lint`, `vet` (+ windows/plan9
cross-vet), `build`, race tests, black-box e2e, a merged-coverage gate (≥75%),
and the installer checks (`sh -n install.sh`, plus the goreleaser asset-parity
and minisign signature-matrix scripts when those tools are installed —
`CI_STRICT=1` makes them mandatory, e.g. before a release). If it's green, you
can commit.

The Makefile, `scripts/cover.sh`, and the git hooks assume a POSIX shell — on
Windows run them under git-bash or WSL (the engine and tests themselves are
cross-platform; `vet-cross` type-checks the windows/plan9 lock code, but with
no remote CI there is no windows RUNTIME leg — verify windows-specific changes
by hand on a Windows machine).

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
  The cmd suite silences command stdout; when debugging a failure, run with
  `ST_TEST_DEBUG=1 go test ./cmd/...` to un-silence it.
- `e2e/e2e_test.go` — the real binary as a hermetic subprocess; contributes to
  coverage via `GOCOVERDIR`.

A historical manual-QA snapshot lives at `docs/qa/FEATURE_STORIES.csv`
(frozen, not maintained; see `docs/qa/README.md`).

## Releasing

Releases are cut locally from a tag:

```sh
git tag vX.Y.Z            # must match defaultVersion in cmd/root.go
git push origin vX.Y.Z    # the GitHub release attaches to this tag
make release              # build, sign, and publish (needs GITHUB_TOKEN and
                          #  MINISIGN_KEY_FILE; check-release-ready gates the
                          #  embedded pubkey and the key file first)
make snapshot             # build the release artifacts without publishing
```

Before tagging: fold `CHANGELOG.md`'s `[Unreleased]` into the new `[x.y.z]`
heading, and bump `defaultVersion` in `cmd/root.go` to match the tag —
`make check-release-version RELEASE_TAG=vX.Y.Z` verifies the pin (`make
release` runs it). `make release`/`make snapshot` need the external
`goreleaser` binary — match `GORELEASER_VERSION` in the Makefile (currently
`v2.17.0`; `brew install goreleaser` tracks latest, so check
`goreleaser --version`, or pin exactly with
`go install github.com/goreleaser/goreleaser/v2@v2.17.0`). It is not a Go
dependency.

`bash scripts/check-install-signatures.sh` exercises the installer's
signature/checksum decisions end-to-end against a disposable signed fixture —
valid install, missing signature, empty embedded key, absent minisign, invalid
signature (refused even under `ST_ALLOW_UNVERIFIED=1`), and tampered
archive/checksum-entry cases, each verified against a pre-placed sentinel. It
needs `minisign` plus `curl`/`tar`/the platform sha256 tool locally, no release
secrets (keys are generated per run), and skips cleanly when minisign is
absent. `make ci` runs it when minisign is on PATH; `CI_STRICT=1 make
check-install` makes it (and the goreleaser checks) mandatory.

### Signing runbook (minisign)

Every release signs `checksums.txt` with minisign (the `signs:` pipe in
`.goreleaser.yaml`), producing `checksums.txt.minisig` as a release asset.
`install.sh` downloads that signature and verifies it against the public key
embedded in the script (`MINISIGN_PUBKEY`), **failing closed** when it cannot
verify — so a release must never go out unsigned, and `make release`
hard-fails when the embedded pubkey is still a placeholder or the signing key
file is absent (`check-release-ready`).

**One-time provisioning (operator; the key is never committed):**

```sh
# Unencrypted key: `make release` cannot answer a password prompt (the signs
# pipe passes -W).
minisign -G -W -p stacked.pub -s stacked.key

# Embed the public key: copy the "RW…" line from stacked.pub into install.sh's
# MINISIGN_PUBKEY and commit. Optionally commit stacked.pub itself so the key
# can be cross-checked out-of-band.

# Store stacked.key OUTSIDE the repo, chmod 600 — e.g. ~/.config/stacked/
# or a password manager attachment. It is never committed.
mv stacked.key ~/.config/stacked/stacked.key && chmod 600 ~/.config/stacked/stacked.key
```

`make release` reads the key file path from `MINISIGN_KEY_FILE`
(`export MINISIGN_KEY_FILE=~/.config/stacked/stacked.key`), which
`.goreleaser.yaml` passes to `minisign -s`. `make snapshot`
(`goreleaser build`) never reaches the sign pipe, so it needs neither.

**Rotation:** generate a new pair, replace the local key file, update
`MINISIGN_PUBKEY` (and `stacked.pub` if committed) in the same commit, then cut
a new release. Caveat: `install.sh` always embeds the *current* public key, so
after rotation it can no longer verify signatures of releases signed with the
old key — rotate only alongside a new release, and note it in the changelog.

`ST_ALLOW_UNVERIFIED=1` remains the escape hatch — post-signing it is for
snapshot/dev installs only, and it never bypasses an actual signature mismatch.

## Troubleshooting

### `stale-lock reclaim guard … is abandoned`

On platforms without `flock` (Windows, plan9, js/wasm) `st` serializes mutating
commands with two lock files in the stack metadata directory: `lock.excl` (the
exclusive lock) and `lock.reclaim` (a short-lived guard that serializes
reclaiming a stale `lock.excl`). A stale `lock.excl` is still reclaimed
automatically — but only under a freshly acquired `lock.reclaim`. If the guard
itself is left behind by a dead process (`lock.reclaim` exists and its recorded
owner is provably gone, or its contents are malformed and older than the
conservative 10-minute bound), `st` refuses to remove it: a
read/compare/unlink against a file another process may replace is not atomic,
and getting it wrong can admit two writers. Availability is traded for
integrity — the condition needs an operator.

The error is a maintenance condition, not contention: it exits 5 like
contention but carries `"code": "locked_guard"` rather than `"locked"`, so a
retry loop can tell it apart. To recover:

1. Stop every `st` process against the repository.
2. Verify no writer is active — a `lock.excl` whose recorded owner pid is alive
   means a command is really running; let it finish rather than deleting locks
   underneath it.
3. Only then remove the named `lock.reclaim` file and retry.

The error names the exact path; do not delete it while any `st` command could
still be running. A live or freshly-written malformed guard stays ordinary
contention (exit 5) — that case means another `st` is mid-reclaim and the retry
idiom applies.

Coverage note: the stale-lock code paths are exercised by the native test
suite on every platform (`internal/stack/lock_stale_test.go` runs the real
file operations in temp dirs regardless of `GOOS`). There is no Windows runtime
leg anymore — exercise the composed `Lock`/`Unlock` pair on a real Windows
machine before shipping lock changes. Timing races are deterministic in tests —
synchronized contenders, not sleeps.

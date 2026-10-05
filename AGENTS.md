# AGENTS.md

`st` — a login-free, standard-library-only Go 1.26 CLI for stacked git diffs.

- Inner loop: `make test-fast`. Full gate: `make ci` (no remote CI — it is the
  only gate). `make release` runs `make ci` plus the `CI_STRICT=1` release
  legs itself, so the publish path can never ship an ungated tree.
- Hard constraint: `go.mod` keeps zero `require` entries; no `go.sum`.
- Engine logic lives in `internal/stack` (pure, fake-git tested); `cmd/` stays
  thin adapters; real-git behavior is proven in `e2e/`.

See `CLAUDE.md` for architecture and `docs/AGENT.md` for the machine-agent
contract (exit codes, `--json`, worktree orchestration).

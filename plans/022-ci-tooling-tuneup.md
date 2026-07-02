# Plan 022: CI/tooling tune-up — enforce the lint-version pin, cancel superseded runs, fix the stale build cache, ignore dist/

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 0a76742..HEAD -- Makefile .github/workflows/ci.yml .gitignore README.md CONTRIBUTING.md`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: dx
- **Planned at**: commit `0a76742`, 2026-07-01

## Why this matters

Four small tooling gaps, bundled because they touch the same few files:

1. **Lint-version drift is unguarded.** golangci-lint `v2.12.2` is hardcoded
   in four hand-synced places; comments say "keep in sync" but nothing checks.
   The repo's core promise is "local `make ci` == CI == pre-push hook"; a
   missed bump silently breaks it. The repo already solves this class of
   problem mechanically (`check-deps` enforces stdlib-only) — copy that
   pattern.
2. **No concurrency-cancel in CI.** Stacked-diff workflows re-push constantly
   (amend → restack → push); every superseded push keeps burning two runners
   to completion.
3. **The Go build cache never re-saves.** `setup-go`'s cache key derives from
   `go.mod`, which — being stdlib-only, 3 lines — essentially never changes.
   `actions/cache` only saves on a key *miss*, so the cache froze at
   first-run contents: stdlib artifacts help, but the project's own packages
   recompile from scratch every run.
4. **`make snapshot` drops an untracked, unignored, uncleaned `dist/`** —
   goreleaser's default output dir is neither in `.gitignore` nor removed by
   `make clean`, so a contributor following CONTRIBUTING can accidentally
   `git add` release binaries.

## Current state

- `Makefile:8-11`:

```make
# golangci-lint is an external binary, never a go.mod dependency. v2 is required:
# .golangci.yml uses the v2 schema and its bundled gofumpt formatter. Keep this
# in sync with the version pinned in .github/workflows/ci.yml.
GOLANGCI_VERSION := v2.12.2
```

- The four pin sites: `Makefile:11`, `.github/workflows/ci.yml:40`
  (`version: v2.12.2`), `README.md:72` and `CONTRIBUTING.md:21` (both inside
  a `go install …golangci-lint@v2.12.2` command).
- The enforcement pattern to copy, `Makefile:57-66`:

```make
check-deps:
	@if grep -qE '^require' go.mod; then \
		echo "go.mod declares a require directive; this project must stay standard-library only"; \
		exit 1; \
	fi
	...
```

- `Makefile:23`: `ci: check-deps fmt-check vet vet-cross build lint cover`
- `.github/workflows/ci.yml` (full relevant excerpt):

```yaml
on:
  push:
  pull_request:

permissions:
  contents: read

jobs:
  ci:
    name: ci (${{ matrix.os }})
    runs-on: ${{ matrix.os }}
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0 # full history so `git describe` can produce a version

      # The cache key must come from go.mod because stdlib-only means go.sum can never exist.
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.26'
          cache-dependency-path: go.mod
```

- `.gitignore` has no `dist` entry (verified `grep -n dist .gitignore` →
  empty). `.goreleaser.yaml` has no `dist:` override, so the default `./dist`
  applies. `Makefile:109-110`: `clean:` removes only `$(BINARY)` and
  `cover.out`.
- Note: `on: push` + `on: pull_request` means PR branches already run twice
  (push + PR event); the concurrency group below also collapses that
  duplication for PRs.

Repo conventions: Makefile targets are commented with *why*, `.PHONY` is
maintained (line 16), and CI comments explain non-obvious choices — match
that (comments state constraints, not narration).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| New check | `make check-lint-version` | prints ok-style line, exit 0 |
| Negative test | (temporarily edit one pin) `make check-lint-version` | exit 1, names the drifted file |
| Full gate | `make ci` | exit 0 |
| YAML sanity | `git diff .github/workflows/ci.yml` then visual check | valid YAML (CI proves it on push) |

## Scope

**In scope** (the only files you should modify):
- `Makefile` (new `check-lint-version` target; `ci` prerequisites; `clean`;
  `.PHONY`)
- `.github/workflows/ci.yml` (concurrency block; cache steps)
- `.gitignore` (one line)

**Out of scope** (do NOT touch):
- `README.md` / `CONTRIBUTING.md` — the checker *reads* them; their current
  pins are correct today.
- `.goreleaser.yaml` — keep goreleaser's default `dist/`; we ignore it, not
  relocate it.
- The `golangci-lint-action` step itself and `GOLANGCI_VERSION`'s value — no
  version bump in this plan.
- The `git tag` / release-versioning question (recorded separately in
  plans/README.md as a maintainer decision).

## Git workflow

- Branch: `advisor/022-ci-tooling-tuneup`
- Commit style: conventional commits, e.g. `build: enforce the lint pin, cancel superseded CI, fix the build cache`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: `check-lint-version` target

Add to the Makefile (near `check-deps`, matching its style), and add it to
both `.PHONY` and the `ci:` prerequisite list (before `lint`):

```make
# The lint version is pinned in four hand-synced places (Makefile, ci.yml,
# README, CONTRIBUTING). Enforce agreement so "local make ci == CI" cannot
# silently break — the same mechanical guard check-deps gives stdlib-only.
check-lint-version:
	@ok=1; \
	for f in .github/workflows/ci.yml README.md CONTRIBUTING.md; do \
		if ! grep -q "$(GOLANGCI_VERSION)" $$f; then \
			echo "$$f does not pin golangci-lint $(GOLANGCI_VERSION) (drifted from Makefile)"; \
			ok=0; \
		fi; \
	done; \
	[ $$ok -eq 1 ] || exit 1; \
	echo "lint pin: $(GOLANGCI_VERSION) consistent across Makefile, ci.yml, README, CONTRIBUTING"
```

Update `ci:` to `ci: check-deps check-lint-version fmt-check vet vet-cross build lint cover`.

**Verify**: `make check-lint-version` → the "consistent" line, exit 0.
**Verify (negative)**: change `v2.12.2` to `v2.12.3` in `README.md`, run
`make check-lint-version` → exit 1 naming README.md; **revert the edit**
(`git checkout -- README.md`).

### Step 2: Concurrency cancel

At the top level of `.github/workflows/ci.yml` (after `permissions:`):

```yaml
# A re-push supersedes the previous run of the same ref; don't keep burning
# two runners on it. github.ref alone would collide all PRs on the merge ref,
# so include the event name and PR number.
concurrency:
  group: ci-${{ github.workflow }}-${{ github.event_name }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true
```

**Verify**: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml'))"`
→ no error (if PyYAML is unavailable, any YAML validator; final proof is the
CI run itself).

### Step 3: Make the build cache actually rotate

In `ci.yml`, disable `setup-go`'s built-in cache and manage `GOCACHE`
explicitly so every run *saves* a fresh cache that the next run restores by
prefix:

```yaml
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.26'
          cache: false # stdlib-only: go.mod never changes, so setup-go's key is static and never re-saves; see the explicit cache below

      - name: Resolve Go build cache dir
        id: gocache
        run: echo "dir=$(go env GOCACHE)" >> "$GITHUB_OUTPUT"

      # Keyed on the commit so every run saves; restored by prefix so the
      # newest previous cache seeds the next run (project packages included,
      # not just the frozen first-run stdlib artifacts).
      - name: Go build cache
        uses: actions/cache@v4
        with:
          path: ${{ steps.gocache.outputs.dir }}
          key: gobuild-${{ runner.os }}-go1.26-${{ github.sha }}
          restore-keys: |
            gobuild-${{ runner.os }}-go1.26-
```

Remove the now-inaccurate `cache-dependency-path: go.mod` line and the
comment above it ("The cache key must come from go.mod…"), replacing it with
the new rationale.

**Verify**: YAML parses (as Step 2). Semantics are proven on the first two
CI runs after merge: run 2's "Go build cache" step should report a
restore-key hit and its `make ci` build/test phase should be faster; note
this in your report rather than asserting it locally.

### Step 4: Ignore and clean dist/

- Append `/dist/` to `.gitignore` (with a one-line comment: goreleaser output).
- In the Makefile `clean:` target, add `rm -rf dist`.

**Verify**: `make snapshot` (only if goreleaser is installed — otherwise skip
and note it) → `git status --porcelain` shows no `dist` entries;
`make clean` → `test ! -d dist` → exit 0.

### Step 5: Full gate

**Verify**: `make ci` → exit 0 (now includes `check-lint-version`).

## Test plan

- The negative test in Step 1 (temporary drift → red, then revert) is the
  behavioral test for the checker; the repo's Makefile guards have no unit
  tests (check-deps doesn't either) — CI running `make ci` is the harness.
- Steps 2–3 are proven by the first post-merge CI runs (cancellation on a
  double-push; cache restore on the second run). Record both observations in
  the PR/report if you have CI visibility; otherwise state that they need a
  maintainer's eye on the next runs.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `make check-lint-version` exits 0; with a deliberately drifted pin it exits 1 (tested and reverted)
- [ ] `grep -n "check-lint-version" Makefile` shows it in `.PHONY` and in the `ci:` prerequisites
- [ ] `grep -n "concurrency:" .github/workflows/ci.yml` → present at top level with `cancel-in-progress: true`
- [ ] `grep -n "cache: false" .github/workflows/ci.yml` and `grep -n "actions/cache@v4" .github/workflows/ci.yml` → both present
- [ ] `grep -n "dist" .gitignore Makefile` → `/dist/` ignored and `rm -rf dist` in clean
- [ ] `make ci` exits 0
- [ ] Only `Makefile`, `.github/workflows/ci.yml`, `.gitignore` modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The four pin sites don't all read `v2.12.2` at execution time (someone
  bumped one — reconcile *first* is a human decision; report which drifted).
- `make ci` fails on the lint step: try `PATH=/opt/homebrew/bin:$PATH make ci`
  (this machine's default golangci-lint resolves to v1; the repo needs v2).
  If still red, report.
- The `golangci-lint-action` step turns out to *depend on* `setup-go`'s cache
  being enabled (it should not; it has its own caching) — if the workflow
  errors on that interaction, report rather than re-enabling both caches.

## Maintenance notes

- Bumping golangci-lint now requires touching all four files in one commit —
  `make check-lint-version` tells you which were missed; consider that the
  feature, not friction.
- If a Windows runner is ever added (known open question, see
  plans/README.md "CI-02"), extend the cache key's `runner.os` coverage
  automatically works; the concurrency group needs no change.
- The `go1.26` literal in the cache key must be bumped with the toolchain
  (cheap staleness, self-corrects via restore-keys; a comment in ci.yml
  noting it is enough).

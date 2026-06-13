# Plan 003: Compile-gate the non-flock lock code in CI and enable the Go build cache

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat ed9f2f7..HEAD -- Makefile .github/workflows/ci.yml internal/stack/lock_other.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: dx
- **Planned at**: commit `ed9f2f7`, 2026-06-12

## Why this matters

The trickiest concurrency code in the repo — the non-flock lock fallback with
stale-owner reclamation — is excluded from every CI build by its build tags.
`internal/stack/lock_other.go` builds only when GOOS is **not**
darwin/dragonfly/freebsd/illumos/linux/netbsd/openbsd, and the CI matrix is
`[ubuntu-latest, macos-latest]`. A typo in the Windows or Plan 9 lock path
would not even fail to compile in `make ci`. Cross-GOOS `go vet` closes that
hole for the cost of seconds. Separately, CI runs the most rebuild-heavy
configurations (race + coverage instrumentation) from a cold `GOCACHE` every
run: `actions/setup-go` keys its cache on `go.sum`, which this repo can never
have (stdlib-only is enforced), so caching silently never engages. Pointing it
at `go.mod` fixes that with one line.

## Current state

- `internal/stack/lock_other.go:1` — build tag:
  `//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd`
- `internal/stack/lock_owner_windows.go:1` — `//go:build windows`;
  `internal/stack/lock_owner_plan9.go:1` — `//go:build plan9`.
- `.github/workflows/ci.yml:17` — `os: [ubuntu-latest, macos-latest]`.
- `.github/workflows/ci.yml:24–27` — the setup step, currently with no cache
  configuration:

  ```yaml
  - name: Set up Go
    uses: actions/setup-go@v5
    with:
      go-version: '1.26'
  ```

- `Makefile` — `ci: check-deps fmt-check lint vet build cover`. The `vet`
  target is `go vet ./...` (host GOOS only). CLAUDE.md documents `make ci` as
  the single source of truth mirrored by the pre-push hook, so the new check
  must live inside the `ci` target, not only in the workflow.
- Repo convention: Makefile targets are commented with *why*, declared in
  `.PHONY`, and `ci` lists them in fail-fast order. Match that style (see the
  existing `check-deps` target as the exemplar).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Cross-vet (new) | `GOOS=windows GOARCH=amd64 go vet ./...` and `GOOS=plan9 GOARCH=amd64 go vet ./...` | exit 0, no output |
| Full gate | `make ci` | exit 0 |
| Workflow lint (optional, if installed) | `actionlint .github/workflows/ci.yml` | exit 0 |

## Scope

**In scope** (the only files you should modify):
- `Makefile`
- `.github/workflows/ci.yml`

**Out of scope**:
- `internal/stack/lock_*.go` — if cross-vet reveals an error in them, that is
  a STOP condition (a real bug find), not something to fix here.
- `.githooks/*` — the pre-push hook runs `make ci` and picks the change up
  automatically.
- Adding a `windows-latest` runner to the matrix — considered and deferred
  (it roughly +50% CI cost for a platform goreleaser doesn't ship; see
  plans/README.md). Do not add it in this plan.

## Git workflow

- Branch: `ci-cross-goos-vet-and-cache`
- Commit message style: `build: cross-GOOS vet the lock fallbacks; enable the Go build cache in CI`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add a `vet-cross` target to the Makefile and wire it into `ci`

Add, in the Makefile's style (comment explaining why, `.PHONY` entry):

```make
# The non-flock lock fallback (lock_other.go, lock_owner_windows.go,
# lock_owner_plan9.go) is excluded from every native build by its build tags,
# so a plain `go vet` never compiles it. Vet the two GOOSes that select those
# files so a breakage cannot land green.
vet-cross:
	GOOS=windows GOARCH=amd64 go vet ./...
	GOOS=plan9 GOARCH=amd64 go vet ./...
```

Add `vet-cross` to the `ci` target's list right after `vet`, and to `.PHONY`.

**Verify**: `make vet-cross` → exit 0. Then
`GOOS=windows GOARCH=amd64 go vet ./internal/stack` → exit 0 (sanity: the
package that owns the tagged files vets cleanly).

### Step 2: Prove the gate actually gates

Temporarily introduce a syntax error in `internal/stack/lock_owner_windows.go`
(e.g. an undefined identifier), run `make vet-cross`, confirm it FAILS, then
revert the file exactly (`git checkout -- internal/stack/lock_owner_windows.go`).

**Verify**: `make vet-cross` fails with the error, and after revert
`git status --porcelain internal/stack/` is empty.

### Step 3: Enable the build cache in CI

In `.github/workflows/ci.yml`, change the setup-go step to:

```yaml
- name: Set up Go
  uses: actions/setup-go@v5
  with:
    go-version: '1.26'
    cache-dependency-path: go.mod
```

Add a one-line comment above it noting that the cache key must come from
`go.mod` because the stdlib-only invariant means `go.sum` (the action's
default key source) can never exist.

**Verify**: `grep -n "cache-dependency-path" .github/workflows/ci.yml` → 1 match.

### Step 4: Full gate

**Verify**: `make ci` → exit 0.

## Test plan

No Go tests change. The verification gates are Step 2 (the negative test that
`vet-cross` catches a broken windows file) and `make ci` green.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `make vet-cross` exits 0
- [ ] `grep -n "vet-cross" Makefile` shows the target, a `.PHONY` entry, and its presence in the `ci:` prerequisite list
- [ ] `grep -n "cache-dependency-path: go.mod" .github/workflows/ci.yml` → 1 match
- [ ] `make ci` exits 0
- [ ] `git status --porcelain` shows only `Makefile` and `.github/workflows/ci.yml` modified
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- `GOOS=windows GOARCH=amd64 go vet ./...` or
  `GOOS=plan9 GOARCH=amd64 go vet ./...` fails on the
  *current* code — that is a live bug in the lock fallbacks; the report is the
  vet output.
- `go vet` with a foreign GOOS errors for environmental reasons (toolchain
  missing cross support) rather than code reasons.

## Maintenance notes

- If a `windows-latest` CI runner is ever added, `vet-cross`'s windows leg
  becomes redundant (but harmless); the plan9 leg remains the only check for
  that file.
- The first CI run after this lands populates the cache; speedup shows from
  the second run. If CI time does not drop, check the setup-go log for a
  "cache restored" line before assuming the key is wrong.
- Follow-up explicitly deferred: restructuring `lock_other.go` so its
  create/reclaim loop is unit-testable on unix (effort M, see README findings
  list).

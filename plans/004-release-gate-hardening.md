# Plan 004: Harden the release gate — pin enforcement on release/snapshot, encrypted-key + pubkey-match preflight, misc Makefile/script fixes

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- Makefile scripts/check-release-ready.sh scripts/check-install-signatures.sh .gitignore scripts/check-install-assets.sh`
> On mismatch between "Current state" and live code, STOP.

## Status

- **Priority**: P1 (cheap, and it gates the release path the whole security posture depends on)
- **Effort**: S
- **Risk**: LOW (preflight-only; worst case a stricter refusal)
- **Depends on**: none; **blocks Plan 006** (key provisioning must land on the hardened gate)
- **Category**: dx + security-adjacent release plumbing
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

The release chain has real gaps despite looking guarded:

1. `make release` requires `check-release-version` + `check-release-ready` but **not** `check-goreleaser-version` — a developer with a newer goreleaser than the v2.17.0 pin (e.g. brew's v2.18.x) ships a release built by an unpinned tool. `make snapshot` has zero prerequisites.
2. `check-release-ready.sh:28` greps `'secret key'` — which **also matches** "minisign encrypted secret key", yet `.goreleaser.yaml` signs with `minisign -S -W` (never prompts) → an encrypted key passes preflight and fails mid-release. Nothing verifies the embedded `MINISIGN_PUBKEY` in `install.sh` corresponds to `$MINISIGN_KEY_FILE` — the rotation runbook warns exactly this pairing desyncs.
3. `check-shell` is missing from `.PHONY` — a stray `./check-shell` file would silently disable the shellcheck leg of `make ci`.
4. `.gitignore`'s comment claims the signing key lives in a "MINISIGN_KEY GitHub secret" — there is no CI; the design is a local key file. The comment misleads.
5. `check-install-signatures.sh` prints "(9 cases)" but runs 11 assertions — a dropped case goes unnoticed.
6. `check-deps` greps `^require` but never proves `go mod tidy` is a no-op (the actual invariant CLAUDE.md states).

## Current state

- `Makefile:25` — `.PHONY` lists all targets except `check-shell` (defined at ~194–203).
- `Makefile` — `release: check-release-version check-release-ready` then `goreleaser release --clean`; `snapshot:` has no prerequisites. `check-goreleaser-version` (~149–165) exists but only runs under `ci`.
- `scripts/check-release-ready.sh:14-22` — refuses empty/placeholder `MINISIGN_PUBKEY` in install.sh. `:26` — requires `$MINISIGN_KEY_FILE` env. `:28` — `grep -q 'secret key' "$MINISIGN_KEY_FILE"` (matches encrypted too).
- `install.sh:22` — `MINISIGN_PUBKEY=""` (placeholder).
- `.gitignore:17-19` — "it lives in the MINISIGN_KEY GitHub secret (see CONTRIBUTING.md's Releasing section)" — stale; CONTRIBUTING documents a local `MINISIGN_KEY_FILE` path.
- `scripts/check-install-signatures.sh:242` — `echo "OK: installer signature/checksum decision matrix passed (9 cases)"` — 11 `pass`/`expect_refuse` calls exist in the file.
- `scripts/check-install-assets.sh:27-28,36` — comments/skip message claim "CI installs it via goreleaser-action on the ubuntu leg" — no CI exists.
- `Makefile` `check-deps` (~71-80) — greps `^require`/`go.sum` only.
- Conventions: Makefile targets echo a one-line result; optional tools skip loudly unless `CI_STRICT=1`; scripts are `sh`/`bash` with `shellcheck` enforced — keep them clean.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Makefile parse | `make -n release` | shows `check-goreleaser-version` in prereqs after fix |
| Preflight run | `MINISIGN_PUBKEY=x MINISIGN_KEY_FILE=/dev/null sh scripts/check-release-ready.sh` | fails with actionable message |
| Shellcheck | `make check-shell` | `shellcheck: N scripts clean` or documented skip |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `Makefile`, `scripts/check-release-ready.sh`, `scripts/check-install-signatures.sh`, `scripts/check-install-assets.sh`, `.gitignore`, `CONTRIBUTING.md` (only if a check message needs a doc pointer).

**Out of scope**: `.goreleaser.yaml` signing config, `install.sh` internals (Plan 005), the actual key provisioning (Plan 006), `cover.sh`.

## Git workflow

- Branch: `advisor/004-release-gate-hardening`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Enforce the goreleaser pin on release paths

In `Makefile`: `release: check-release-version check-release-ready check-goreleaser-version` and `snapshot: check-goreleaser-version`. Also add `check-install` to `release`'s prerequisites so the asset/signature contract is proven on the exact toolchain doing the publishing.

**Verify**: `make -n release` prints `check-goreleaser-version` and `check-install` among prerequisites; `make check-goreleaser-version` → prints pin line (skips cleanly if goreleaser absent).

### Step 2: Tighten `check-release-ready.sh`

- Reject encrypted keys: after the existing `secret key` grep, add `if grep -q 'encrypted' "$MINISIGN_KEY_FILE"; then echo "...is passphrase-encrypted; the goreleaser signs: pipe runs minisign -W (non-interactive) and will fail mid-release — generate an unencrypted key per CONTRIBUTING's runbook" >&2; exit 1; fi`.
- Prove pubkey↔keyfile pairing: sign a temp file non-interactively and verify with the embedded pubkey:
  ```sh
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  echo probe > "$tmp/probe"
  if command -v minisign >/dev/null 2>&1; then
      minisign -S -W -s "$MINISIGN_KEY_FILE" -m "$tmp/probe" -x "$tmp/probe.minisig" &&
      minisign -V -P "$key" -m "$tmp/probe" -x "$tmp/probe.minisig" ||
      { echo "MINISIGN_PUBKEY in install.sh does not match $MINISIGN_KEY_FILE" >&2; exit 1; }
  else
      echo "note: minisign not installed; key-pairing rehearsal skipped" >&2
  fi
  ```
  (Extract `$key` — the script already does at line 14.) Keep it POSIX `sh` and shellcheck-clean.

**Verify**: `MINISIGN_PUBKEY=... sh scripts/check-release-ready.sh` against a real unencrypted keypair passes; against an encrypted key fails at the new check; with mismatched pubkey fails the rehearsal. Test encrypted-vs-unencrypted by generating both: `minisign -G -W -s plain.key -p plain.pub` (unencrypted) vs `minisign -G -s enc.key -p enc.pub` (will prompt — run interactively or pipe a passphrase; if impractical, verify the comment wording on a generated encrypted key's first line).

### Step 3: `.PHONY`, `.gitignore`, case count, CI-leg comments

- `Makefile`: add `check-shell` to `.PHONY`.
- `.gitignore`: change the comment to reflect local-key reality, e.g. `# Release signing key — never committed; it lives outside the repo at the path named by MINISIGN_KEY_FILE (see CONTRIBUTING.md's Releasing section). The public key (stacked.pub) is deliberately NOT ignored.`
- `check-install-signatures.sh`: replace the literal `9` with a counter — increment a `cases` variable inside `pass`/`expect_refuse` and print `"($cases cases)"`.
- `check-install-assets.sh:27-28` and `:36`: drop the "CI … ubuntu leg" claims — state the truth: "skips cleanly when goreleaser isn't installed; `make release`/`CI_STRICT=1` runs require it" (match actual gating — check where the script runs from: `check-install` in `ci`, plus any release-path call).

**Verify**: `sh -n` each edited script; `make check-shell` → clean; `make -n ci` unchanged behaviorally.

### Step 4: `check-deps` — prove `go mod tidy` is a no-op

Append to the `check-deps` recipe:
```make
	@if ! go mod tidy -diff >/dev/null 2>&1; then \
		echo "go.mod is not tidy (go mod tidy -diff non-empty)"; \
		exit 1; \
	fi
```
(`-diff` prints the diff without writing; needs Go ≥1.23 — the repo pins 1.26.) Note: `-diff` exits non-zero when a diff exists AND writes it to stdout — capture output and echo it on failure for a better message:
```make
	@if ! out=$$(go mod tidy -diff 2>&1); then echo "$$out"; echo "go.mod/go.sum not tidy"; exit 1; fi
```

**Verify**: `make check-deps` → `deps: standard library only` + tidy pass; `echo 'require x v1' >> go.mod; make check-deps` fails → `git checkout go.mod`.

### Step 5: Gate

**Verify**: `make ci` → exit 0 (all legs, including the fixed `check-shell` in .PHONY and stricter preflight).

## Test plan

- These are build-script changes; verification is the commands in each step plus `make ci`.
- Negative tests to run manually: wrong-pin goreleaser on PATH → `make release` fails before goreleaser runs; encrypted key file → `check-release-ready` fails with the new message; `check-shell` phony — `touch check-shell && make check-shell` still runs the recipe.

## Done criteria

- [ ] `make -n release` lists `check-goreleaser-version` and `check-install`
- [ ] `grep -c 'check-shell' Makefile` on the `.PHONY` line — `check-shell` appears in `.PHONY`
- [ ] `scripts/check-release-ready.sh` rejects an encrypted key and verifies the pubkey↔keyfile pairing when minisign is installed
- [ ] `grep -n 'GitHub secret' .gitignore` → no match
- [ ] `check-install-signatures.sh` prints the real case count (no literal `9`)
- [ ] `make check-deps` includes a `go mod tidy -diff` assertion
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- `minisign` unavailable and Step 2's rehearsal can't be verified end-to-end — implement it, verify the grep-arms and message paths, and mark the rehearsal as exercised-by-`check-install-signatures`-style fixture only if you can generate a throwaway keypair; otherwise STOP and note the gap.
- `go mod tidy -diff` behaves unexpectedly on a zero-require module (older Go in the toolchain path) — confirm `go version` ≥1.26 first.
- Makefile recipe syntax (tabs) breaks — run `make -n <target>` to parse-check before continuing.

## Maintenance notes

- Plan 006 depends on the pairing rehearsal from Step 2.
- Reviewer focus: the `-W` flag is what makes the minisign rehearsal non-interactive; a missing `-W` reintroduces the prompt-mid-release failure.
- If the project later adopts a second signing tool, the rehearsal block is the thing to extend, not the greps.

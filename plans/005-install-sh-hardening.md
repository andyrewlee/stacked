# Plan 005: Harden `install.sh` — tarball member validation, env-var scoping, anchored version parse

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update your row in `plans/README.md`.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- install.sh scripts/check-install-signatures.sh scripts/check-install-assets.sh Makefile`
> On mismatch between "Current state" and live code, STOP.

## Status

- **Priority**: P2
- **Effort**: S–M
- **Risk**: LOW (installer-only; gated by its own test matrix)
- **Depends on**: none. Composes with Plan 004 (release gate) and 006 (key provisioning) but does not require them.
- **Category**: security
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

`install.sh` trusts three things it shouldn't:

1. The tarball's `st` member — `tar -xzf` at line 156 extracts whatever the archive carries and `mv` installs it verbatim: a symlink member lands as a symlink at `${INSTALL_DIR}/st`, and `chmod +x` (line 166) then operates on the link's target. Archive-recorded mode bits (e.g. setuid) also survive `mv`.
2. The ambient environment — `VERSION` (line 82) and `ST_INSTALL_BASE` (line 98) silently override the release tag and the download origin. Combined with `ST_ALLOW_UNVERIFIED=1` that is a complete arbitrary-binary install from a poisoned env; even signed, `VERSION=v<old>` performs a silent downgrade.
3. The GitHub API parse — `get_latest_version` (`install.sh:76-80`) greps `"tag_name":` without `head -1`; a `tag_name` string inside a release body would emit a multi-line `VERSION` and corrupt the URL.

## Current state

- `install.sh:76-80` — `get_latest_version` pipes `curl … | grep '"tag_name":' | sed -E '…'`.
- `install.sh:82` — `VERSION="${VERSION:-$(get_latest_version)}"`.
- `install.sh:95-98` — `BASE="${ST_INSTALL_BASE:-https://github.com/${REPO}/releases/download/${VERSION}}"`; comment admits it's a test seam.
- `install.sh:156` — `tar -xzf "${TMP_DIR}/${FILENAME}" -C "$TMP_DIR"`.
- `install.sh:160-166` — `mv`/`sudo mv` + `chmod +x "${INSTALL_DIR}/${BINARY}"`.
- `scripts/check-install-signatures.sh` — the test harness driving `install.sh` against `file://` fixtures (uses `ST_INSTALL_BASE`); extending its matrix is how you test this plan.
- `scripts/check-install-assets.sh` — asserts artifact names exist for the OS/arch matrix.
- Convention: `sh` POSIX dialect, `set`-guarded, `shellcheck -s sh` clean; failure messages go to stderr with exit 1; `skip_or_die` is the fail-closed/bypass helper (do not weaken it here — Plan 006 addresses the bypass policy).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Syntax | `sh -n install.sh` | exit 0 |
| Shell lint | `shellcheck -s sh install.sh` | no findings |
| Signature matrix | `sh scripts/check-install-signatures.sh` | `OK: … (N cases)` |
| Full gate | `make ci` | exit 0 |

## Scope

**In scope**: `install.sh`, `scripts/check-install-signatures.sh` (new fixture cases), `scripts/check-install-assets.sh` (only if the matrix derivation change below is taken), `CHANGELOG.md`, `README.md`/`docs/AGENT.md` only if an env-var contract changes.

**Out of scope**: `.goreleaser.yaml`, `Makefile` targets, `check-release-ready.sh` (Plan 004), the `ST_ALLOW_UNVERIFIED` policy itself (Plan 006 decides).

## Git workflow

- Branch: `advisor/005-install-sh-hardening`; imperative commit; no push/PR unless instructed.

## Steps

### Step 1: Validate the extracted member before installing

After `tar -xzf` (line 156), before the `mv` block:

```sh
if [ ! -f "${TMP_DIR}/${BINARY}" ] || [ -L "${TMP_DIR}/${BINARY}" ]; then
  echo "Error: archive member ${BINARY} is missing or not a regular file" >&2
  exit 1
fi
```

Then normalize the mode on the *source* file so `mv` carries a known mode: `chmod 0755 "${TMP_DIR}/${BINARY}"`. Keep the existing `chmod +x` at the destination as a harmless belt.

Optionally harden extraction for exotic tars: list members first (`tar -tzf`) and refuse if any member is absolute or contains `..` — do this ONLY if it's portable across bsdtar/gnutar without a new dependency; a `grep -E '^/|(^|/)\.\.(/|$)'` on the member list is portable enough. If it complicates, skip it — goreleaser produces flat tarballs and signature verification is the outer gate.

**Verify**: `sh -n install.sh` → exit 0; run the installer against a real snapshot tarball (`make snapshot` if goreleaser is installed) → installs.

### Step 2: Scope the env overrides

- `ST_INSTALL_BASE`: restrict to `file://` URLs (its documented purpose is the test seam):
  ```sh
  case "$ST_INSTALL_BASE" in
    "" ) BASE="https://github.com/${REPO}/releases/download/${VERSION}" ;;
    file://* ) BASE="$ST_INSTALL_BASE" ;;
    * ) echo "Error: ST_INSTALL_BASE only supports file:// (test seam)" >&2; exit 1 ;;
  esac
  ```
  Check how `check-install-signatures.sh` passes it — it must keep working.
- `VERSION`: keep accepting it (pinning a version is legitimate) but add a loud stderr note when it doesn't equal the fetched latest: `if [ -n "${VERSION_OVERRIDE:-}" ]…` — simplest: capture `latest=$(get_latest_version)` once; when `VERSION` env was set and differs, `echo "note: installing requested ${VERSION} (latest is ${latest})" >&2`. Do NOT hard-refuse downgrades — that's a product call; the warn is the fix.

**Verify**: `ST_INSTALL_BASE=http://evil sh install.sh` → refusal; `ST_INSTALL_BASE=file:///tmp/x` path still works for the test script.

### Step 3: Anchor the version parse

`get_latest_version`: add `| head -1` after the grep so a body-embedded `tag_name` can't produce multi-line output. Better still if tolerable: also anchor to the first quoted value only — current sed already does; the `head -1` is the needed bit.

**Verify**: `get_latest_version` against a fixture containing two `tag_name` lines returns the first only.

### Step 4: Extend the signature matrix

In `scripts/check-install-signatures.sh`, add fixture cases:
- archive whose `st` member is a symlink → expect `expect_refuse` (script exits 1 naming the member).
- `ST_INSTALL_BASE=http://…` (non-file) → expect refuse.
- `VERSION` env different from fixture's latest → install still succeeds but stderr carries the note (assert the note text, `expect_note`-style if the harness supports it — check the script's `pass`/`expect_refuse` helpers and extend minimally).
Update the `(N cases)` count mechanism — if Plan 004 already landed, it self-counts; otherwise update the literal.

**Verify**: `sh scripts/check-install-signatures.sh` → all cases pass.

### Step 5: Changelog + gate

`CHANGELOG.md` `[Unreleased]` → `Fixed`: installer validates the tarball member type/mode and scopes `ST_INSTALL_BASE` to `file://`; `VERSION` override now notes when it differs from latest.

**Verify**: `make ci` → exit 0 (the `check-install` leg runs the updated scripts).

## Test plan

- The signature-matrix script IS the test suite for this file — extend it (Step 4) rather than adding Go tests.
- Verification: `sh scripts/check-install-signatures.sh` → all pass; `shellcheck -s sh install.sh` → clean; `make ci` → green.

## Done criteria

- [ ] `sh -n install.sh` and `shellcheck -s sh install.sh` clean
- [ ] `grep -n 'L.*TMP_DIR.*BINARY\|-f.*TMP_DIR.*BINARY' install.sh` shows the member check
- [ ] `ST_INSTALL_BASE=http://x sh install.sh` (against a stubbed curl or fixture) refuses
- [ ] `grep -n 'head -1' install.sh` near the `tag_name` parse
- [ ] `sh scripts/check-install-signatures.sh` passes with the new cases
- [ ] `make ci` exits 0
- [ ] `plans/README.md` row updated

## STOP conditions

- `check-install-signatures.sh`'s fixture builder can't express a symlink member or env override — extend the fixture builder minimally; if it resists, STOP.
- bsdtar/gnutar flag divergence on member-list validation — prefer the portable `tar -tzf | grep` form; if none portable exists, skip that sub-check (documented in the file) rather than shipping platform-conditional tar flags.
- `ST_INSTALL_BASE` is used by something other than the test script (grep the repo) — if a real consumer needs `https://`, STOP and reconsider the scoping.

## Maintenance notes

- Plan 006 (key provisioning) will revisit `ST_ALLOW_UNVERIFIED` policy — keep the seam intact here.
- Reviewer focus: `mv` semantics under `sudo` — the member check must run BEFORE `mv`, in `TMP_DIR`, where ownership is the invoking user's.
- Deferred: absolute/`..` member rejection if skipped in Step 1.

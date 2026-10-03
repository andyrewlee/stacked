# Plan 006: Provision the release signing key and ship the first signature-verified release

> **Executor instructions**: This is an OPERATIONAL plan — the "code change" is embedding a public key and cutting a release. The secret key must never be committed, echoed, or written into the repo. Follow the steps; on any doubt, STOP.
>
> **Drift check (run first)**: `git diff --stat 159648a..HEAD -- install.sh .goreleaser.yaml scripts/check-release-ready.sh scripts/check-install-signatures.sh CHANGELOG.md`

## Status

- **Priority**: P1 (the whole verification pipeline is built and dead — one keypair unblocks it)
- **Effort**: S (operational, not code)
- **Risk**: LOW — `check-release-ready` already hard-fails while the placeholder remains; the risk is only operator error on key custody
- **Depends on**: plans/004-release-gate-hardening.md (the hardened preflight proves the pairing)
- **Category**: security / ops
- **Planned at**: commit `159648a`, 2026-10-02

## Why this matters

Every layer of release verification is implemented and tested — except the key. `install.sh:22` has `MINISIGN_PUBKEY=""`, which means the canonical `curl | sh` install path either refuses outright or (under `ST_ALLOW_UNVERIFIED=1`) runs checksum-only — a checksum fetched from the same origin as the tarball, proving download integrity but not authenticity. The README documents the bypass, normalizing it. Until this lands, the install story is "trust GitHub's asset hosting" — a weaker posture than the codebase's own standards.

## Current state

- `install.sh:16-22` — `MINISIGN_PUBKEY=""` marked `PLACEHOLDER`; empty means signature verification cannot run and the script fails closed.
- `.goreleaser.yaml:44-56` — `signs:` pipe producing `checksums.txt.minisig` via `minisign -S -W` (non-interactive — the secret key must be UNencrypted).
- `scripts/check-release-ready.sh` — `make release` preflight: refuses placeholder pubkey, requires `MINISIGN_KEY_FILE` pointing at a minisign secret key. (Plan 004 adds encrypted-key rejection and a pubkey↔keyfile rehearsal.)
- `scripts/check-install-signatures.sh` — 11-case decision matrix exercising the installer's verify/refuse/bypass arms against `file://` fixtures.
- `CONTRIBUTING.md:138-176` — the signing runbook (key generation, custody, rotation).
- `Makefile` — `release: check-release-version check-release-ready` then `goreleaser release --clean` (post-Plan-004 also `check-goreleaser-version` + `check-install`).
- `README.md:75-81` — documents `curl | sh` and the `ST_ALLOW_UNVERIFIED=1` waiver.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Generate keypair | `minisign -G -W -p stacked.pub -s stacked.key` | creates files, no passphrase prompt (unencrypted for `-W` non-interactive use) |
| Preflight | `MINISIGN_KEY_FILE=$PWD/stacked.key make check-release-ready` | `release gate: …` |
| Signature matrix | `sh scripts/check-install-signatures.sh` | all cases pass |
| Release (operator) | `MINISIGN_KEY_FILE=<path> GITHUB_TOKEN=… make release` | publishes signed release |

## Scope

**In scope**: `install.sh` (embed pubkey), `CHANGELOG.md`, `README.md` (drop the "cannot verify today" caveat if present), `stacked.pub` (commit — it's already deliberately NOT gitignored, per `.gitignore:19`).

**Out of scope**: anything that changes the signing tool or scheme; rotating `ST_ALLOW_UNVERIFIED` policy (see deferred note); the secret key file — it must NEVER enter the repo.

## Steps

### Step 1: Generate the keypair OUTSIDE the repo

```sh
mkdir -p ~/.config/stacked-signing && cd ~/.config/stacked-signing
minisign -G -W -p stacked.pub -s stacked.key
```

`-W` skips the passphrase — required because goreleaser's `signs:` pipe runs `minisign -S -W` non-interactively. Record custody: `stacked.key` stays at this path (or a manager's vault), `MINISIGN_KEY_FILE` points at it.

**Verify**: `minisign -V -P "$(cat stacked.pub | tail -1)" -m /etc/hostname` — no wait, simpler: sign+verify a probe per CONTRIBUTING's runbook. `grep -c 'secret key' stacked.key` → 1; `head -1 stacked.key` shows `untrusted comment: minisign encrypted secret key`? It must NOT — for `-W` keys the comment is `minisign secret key` (no "encrypted"). If "encrypted" appears, regenerate with `-W`.

### Step 2: Embed the public key

Read the SECOND line of `stacked.pub` (the base64 key — first line is an `untrusted comment`). Set `MINISIGN_PUBKEY="<that line>"` in `install.sh:22`. Commit `stacked.pub` to the repo root too (the runbook may already intend this — `.gitignore` deliberately does NOT ignore it; check CONTRIBUTING for whether it's meant to be tracked).

**Verify**: `grep -n 'MINISIGN_PUBKEY="[A-Za-z0-9+/=]' install.sh` → non-empty. `make check-release-ready` with `MINISIGN_KEY_FILE` set → passes (including the Plan-004 pairing rehearsal if landed).

### Step 3: Verify the full loop locally

`make snapshot` then run `install.sh` against the artifacts via `ST_INSTALL_BASE=file://…` (this is exactly what `check-install-signatures.sh` does — run it and confirm the "valid signature" arm passes with the REAL embedded pubkey + real minisig produced by signing `checksums.txt` with the new key — the script may generate its own fixtures; check how it provisions keys and whether a "real key" run is already parameterized).

**Verify**: `sh scripts/check-install-signatures.sh` → all pass.

### Step 4: Docs + changelog

- `CHANGELOG.md` `[Unreleased]` → `Security`/`Added`: release signing is live — installs verify `checksums.txt.minisig` with the embedded public key; `ST_ALLOW_UNVERIFIED` remains as the documented escape.
- `README.md`: update the install section's "refuses unverifiable downloads" wording if it currently says verification can't run (check lines ~75–90).
- Optionally tighten `ST_ALLOW_UNVERIFIED`: keep it (needed when minisign isn't installed — that's a real usability need) but ensure the README frames it as exceptional, not expected. Consider refusing it in combination with `ST_INSTALL_BASE` — defer to Plan 005 if that plan added the scoping already.

### Step 5: Cut the signed release (operator)

`MINISIGN_KEY_FILE=~/.config/stacked-signing/stacked.key GITHUB_TOKEN=… make release` from a tagged commit. Verify on a clean machine: `curl -fsSL <install.sh URL> | sh` installs WITHOUT `ST_ALLOW_UNVERIFIED` and prints `Signature verified.`

## Test plan

- `check-install-signatures.sh` — all cases green.
- `check-release-ready.sh` green with the real key file; fails with it unset.
- Post-release: `install.sh` on a clean PATH verifies the signature.

## Done criteria

- [ ] `install.sh` `MINISIGN_PUBKEY` is a real base64 key (no placeholder)
- [ ] `stacked.pub` committed; `stacked.key` NOT in the repo (`git ls-files | grep stacked.key` → empty)
- [ ] `MINISIGN_KEY_FILE=<key> make check-release-ready` → pass
- [ ] `make ci` → exit 0
- [ ] A published release exists whose `checksums.txt.minisig` verifies with the embedded key
- [ ] `plans/README.md` row updated

## STOP conditions

- minisign not installed → install it first or hand back to operator.
- `stacked.pub`'s second line isn't base64 → keyfile format drifted; STOP.
- Releasing is the maintainer's call — if you're the advisor/executor without release authority, STOP after Step 4 and hand the release step back.

## Maintenance notes

- Key rotation runbook is in CONTRIBUTING.md — the Plan-004 pairing rehearsal is what catches a desynced pubkey after rotation.
- If `ST_ALLOW_UNVERIFIED` is ever removed, update `check-install-signatures.sh`'s matrix accordingly (it has bypass-arm cases).
- The `stacked.key` custody location should be recorded somewhere durable (1Password/vault) — that's a human step.

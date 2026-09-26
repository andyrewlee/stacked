# Plan 011: Exercise installer signature acceptance and refusal in CI

> **Executor instructions:** Read this entire file, then execute its steps in order. Run each verification command and check its stated result. Stop under the conditions below instead of widening the task. Update this plan's status row in `plans/README.md` when finished.
>
> **Drift check — run first:** `git diff --stat cb31f06..HEAD -- scripts/check-install-signatures.sh .github/workflows/ci.yml CONTRIBUTING.md plans/011-test-installer-signature-decisions.md plans/README.md`
> Compare changed source with the excerpts below. Documented predecessor changes are expected; verify that the specific assumptions used here still hold. Stop on unexplained drift. Also inspect `git status --short`; preserve unrelated work.

## Status

- **Priority:** P2
- **Effort:** M
- **Risk:** LOW
- **Depends on:** none
- **Category:** tests
- **Audit item:** 11
- **Planned at:** commit `cb31f06`, 2026-09-26
- **Implementation status:** TODO

## Why this matters

The installer has important fail-closed signature behavior, but the asset smoke test always enables ST_ALLOW_UNVERIFIED. That verifies packaging without covering the security decisions. Add a local behavioral matrix using disposable keys and archives so missing verification and invalid signatures cannot silently become equivalent.

## Current state

`install.sh:121` routes a missing signature, empty embedded key or missing minisign through skip_or_die. An invalid signature is always fatal:

```sh
elif ! minisign -V -P "$MINISIGN_PUBKEY" -m "${TMP_DIR}/checksums.txt" -x "${TMP_DIR}/checksums.txt.minisig" >/dev/null 2>&1; then
  echo "Error: signature verification FAILED for checksums.txt — the release may have been tampered with." >&2
  exit 1
```

The production embedded public key is intentionally empty pending provisioning. `scripts/check-install-assets.sh:137` runs the installer with ST_ALLOW_UNVERIFIED=1; .github/workflows/ci.yml runs that smoke test on Ubuntu after installing GoReleaser.

Repository conventions: Go 1.26, standard library only, system Git, no forge API or new module dependencies. Commands in `cmd` parse/render; engine operations in `internal/stack` call the `Git` port and checkpoint through `Env.Save`. Keep Git subprocesses in `internal/git` (command-side adapters already have selected direct probes). The documented Git floor is 2.17; do not silently require a newer version. Existing fake-Git tests model engine behavior; real-Git and e2e tests prove filesystem/index/ref behavior. Use argument arrays, retain JSON output shapes unless this plan explicitly says otherwise, and keep errors on the existing channel.

Keep the production installer and release signing setup unchanged. Existing scripts use temporary local file:// release assets and cleanup traps. New tests must use an ephemeral test key injected only into a temporary copy of install.sh, never a production key or a new runtime key-override feature.

## Commands you will need

Run from the repository root.

| Purpose | Command | Expected result |
| --- | --- | --- |
| Shell syntax | `bash -n scripts/check-install-signatures.sh` | exit 0 |
| Installer syntax | `sh -n install.sh` | exit 0 |
| Behavior matrix | `bash scripts/check-install-signatures.sh` | every named case passes; no release secrets needed |
| Repository contributor gate | `make ci` | exit 0 with pinned existing tools |
| Whitespace | `git diff --check` | exit 0 |

The new matrix requires minisign, curl, tar and the installer's checksum utilities. CI should install the test verifier on its Ubuntu leg. Historical make ci/coverage results do not validate this new shell test, and the Go gate does not replace it.

## Scope

**Only modify:**

- `scripts/check-install-signatures.sh`
- `.github/workflows/ci.yml`
- `CONTRIBUTING.md`
- `plans/011-test-installer-signature-decisions.md`
- `plans/README.md`


**Out of scope:** install.sh production logic/key, release secrets, .goreleaser.yaml, real downloads/publishing, adding installer bypasses, replacing the existing artifact smoke test.

## Git workflow

Use a separate branch/worktree named `advisor/011-test-installer-signature-decisions` if the operator's execution workflow provides one; do not change or discard unrelated work. Suggested conventional commit: `test: cover installer signature decisions in ci`. Do not commit, push, merge, or open a PR unless the execution request authorizes that action.

## Steps

### Step 1: Build a disposable signed release fixture

Create scripts/check-install-signatures.sh using Bash with strict error handling and a trap removing its own mktemp directory. Generate an unencrypted ephemeral minisign key pair with `minisign -G -W -p <temporary-public-key> -s <temporary-secret-key>` and sign with `minisign -S -s <temporary-secret-key> -m <checksums-file> -x <signature-file>`; all paths must be inside the fixture directory. Build a tiny executable st fixture that prints a known version, archive it with the filename install.sh expects for the current OS/arch, write checksums.txt, and sign that file. Copy install.sh into the fixture directory; assert there is exactly one MINISIGN_PUBKEY assignment before substituting the ephemeral public key there. Invoke the temporary installer with explicit VERSION=v0.0.0-test, ST_INSTALL_BASE pointing at the fixture's file:// location, and INSTALL_DIR pointing at a temporary writable directory. Build the matching stacked_0.0.0-test_<os>_<arch>.tar.gz name. Supplying VERSION is required to prevent the latest-release API call. Never echo secret key material or edit the checkout's installer. Add one positive signed-install case first.

**Verify:** `bash -n scripts/check-install-signatures.sh && bash scripts/check-install-signatures.sh` → syntax succeeds and the valid signature installs the expected fixture executable into the temporary target

### Step 2: Add a fail-closed decision matrix

Extend the script with isolated named cases: valid signature succeeds; missing signature denies by default and succeeds with ST_ALLOW_UNVERIFIED=1; empty embedded key follows that same missing-verification policy; absent minisign follows it too; invalid signature denies even with ST_ALLOW_UNVERIFIED=1; a changed archive with correctly signed checksums fails checksum validation; missing checksum entry fails. For the absent-tool case construct a controlled PATH with the installer's required utilities but no minisign, rather than assuming the machine lacks it. Each refusal must leave an existing target sentinel executable unchanged. Check the expected diagnostic and exit category so an unrelated curl/tar failure cannot count as a passing refusal. Restore independent fixture assets between cases.

**Verify:** `bash scripts/check-install-signatures.sh` → all cases explicitly report PASS; invalid signatures and checksum mismatches never install or overwrite the sentinel

### Step 3: Run the matrix in ordinary CI

Add a dedicated Ubuntu CI step installing minisign for this test, then invoke the new script with bash. Keep pinned existing actions and the GoReleaser packaging smoke test intact. Document the local command and test dependency in CONTRIBUTING.md; do not make production release secrets a prerequisite for CI. Keep make ci's existing local prerequisites unchanged unless separately approved.

**Verify:** `bash -n scripts/check-install-signatures.sh && sh -n install.sh && bash scripts/check-install-signatures.sh` → all local shell gates pass; the new Ubuntu CI step and existing `make ci` job also pass

## Test plan

- Positive authenticated install; each unavailable-verification reason with override off/on; invalid signature with override off/on.
- Correctly signed checksum file with mismatched archive, and signed checksum file missing the expected archive entry.
- Every refused installation preserves the existing destination executable; fixtures use no network service or production key.
- Run the matrix in the normal PR CI job, not only a release workflow.

## Done criteria

- [ ] The complete named signature/checksum matrix passes locally with minisign installed.
- [ ] Ordinary Ubuntu CI runs the matrix independently of release signing secrets.
- [ ] Production install.sh and its embedded key are unchanged.
- [ ] Shell syntax checks and existing contributor checks pass.
- [ ] `git diff --check` exits 0.
- [ ] Compare `git diff --name-only` and `git ls-files --others --exclude-standard` with the initial inventory; every task-created or task-modified file is in Scope.
- [ ] Record executed commands and results in this plan; update its index row to DONE only when all required gates pass, otherwise BLOCKED with the concrete reason.

## STOP conditions

- Source assumptions no longer match, except for understood changes from the named dependencies.
- A verification fails twice after a reasonable correction, or the regression fails for an unrelated fixture/setup reason.
- The solution needs an out-of-scope file, dependency, public contract change, or a Git/toolchain floor increase.
- The local fixture does not use the existing VERSION, ST_INSTALL_BASE and INSTALL_DIR seams exactly; correct the harness rather than adding a production bypass.
- Generating the test key requires exposing or reusing a production credential.
- A case passes because required tools are missing instead of exercising the intended signature/checksum decision.

## Maintenance notes

This tests policy, not production key provisioning. When the embedded production key is eventually filled, the fixture must still replace only its temporary installer copy and keep invalid-signature refusal unconditional.

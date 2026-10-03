#!/bin/sh
# Preflight for `make release`: refuses to publish artifacts install.sh
# cannot verify. Releases are cut locally (no CI job gates this), so this is
# the only thing standing between a placeholder key and an unverifiable
# release.
#
# Snapshots are NOT gated here: `goreleaser build --snapshot` never reaches
# the .goreleaser.yaml `signs:` pipe (check-install-assets.sh passes
# --skip=sign), and unpublished dev artifacts do not need an embedded pubkey.
set -eu

cd "$(dirname "$0")/.."

key=$(sed -nE 's/^MINISIGN_PUBKEY="?([^"]*)"?.*/\1/p' install.sh | head -1)
case "$key" in
	"" | *PLACEHOLDER* | *TODO*)
		echo "install.sh MINISIGN_PUBKEY is empty/placeholder — embed the" >&2
		echo "release signing public key before publishing" >&2
		echo "(CONTRIBUTING.md, Signing runbook)." >&2
		exit 1
		;;
esac

# The signs: pipe expands {{ .Env.MINISIGN_KEY_FILE }} — fail here with an
# actionable message instead of inside goreleaser's template expansion.
: "${MINISIGN_KEY_FILE:?MINISIGN_KEY_FILE must point at the minisign secret key file the .goreleaser.yaml signs: pipe reads}"

# minisign is mandatory on the publish path: the signs: pipe shells out to it,
# and the pairing rehearsal below is the only proof the key is usable — a
# skip here is a skipped proof, not a missing optional check.
if ! command -v minisign >/dev/null 2>&1; then
	echo "minisign not installed; the signing rehearsal cannot run — the" >&2
	echo "release must prove the key pairs with the embedded pubkey" >&2
	exit 1
fi

# Bounded, noninteractive sign/verify rehearsal — the audit-corrected check.
# Do NOT detect passphrase-encryption by grepping the key's comment line:
# minisign writes the same default comment for both key forms. The sign
# attempt itself is the detector: `-W` (no password) makes an encrypted or
# malformed key fail fast, and stdin is /dev/null so nothing can block on a
# prompt mid-release. Verify against the embedded pubkey to prove the
# secret key pairs with what install.sh will check signatures against.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "release-gate probe" >"$tmp/probe"

if ! minisign -S -W -s "$MINISIGN_KEY_FILE" -m "$tmp/probe" -x "$tmp/probe.minisig" </dev/null 2>"$tmp/sign.err"; then
	cat "$tmp/sign.err" >&2
	echo "MINISIGN_KEY_FILE ($MINISIGN_KEY_FILE) cannot sign non-interactively:" >&2
	echo "it is passphrase-encrypted or malformed — the signs: pipe runs" >&2
	echo "minisign -S -W and would fail mid-release; generate an unencrypted" >&2
	echo "key per CONTRIBUTING.md's Signing runbook" >&2
	exit 1
fi

if ! minisign -V -P "$key" -m "$tmp/probe" -x "$tmp/probe.minisig" >/dev/null 2>&1; then
	echo "MINISIGN_PUBKEY in install.sh does not match $MINISIGN_KEY_FILE:" >&2
	echo "a release signed by this key is unverifiable by the installer" >&2
	exit 1
fi

echo "release gate: embedded MINISIGN_PUBKEY verified against $MINISIGN_KEY_FILE"

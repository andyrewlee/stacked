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

if ! grep -q 'secret key' "$MINISIGN_KEY_FILE"; then
	echo "MINISIGN_KEY_FILE ($MINISIGN_KEY_FILE) does not look like a minisign secret key" >&2
	exit 1
fi

echo "release gate: embedded MINISIGN_PUBKEY set; signing key at $MINISIGN_KEY_FILE"

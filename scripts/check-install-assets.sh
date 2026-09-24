#!/usr/bin/env bash
#
# check-install-assets.sh — prove install.sh's asset-name contract still matches
# what goreleaser actually produces.
#
# install.sh reconstructs release asset URLs client-side:
# stacked_<version>_<os>_<arch>.tar.gz + checksums.txt from the GitHub release.
# A .goreleaser.yaml archives.name_template change would silently break every
# curl-install. This script:
#
#   1. sh -n install.sh                        (syntax gate)
#   2. goreleaser build --snapshot --clean     (local artifacts only; nothing
#      is published — output lands in dist/)
#   3. for each shipped os/arch: the tarball exists under the expected name,
#      checksums.txt lists it, and the tarball has `st` at its root
#   4. pins install.sh's side of the contract (ARCHIVE/BINARY/FILENAME
#      template/checksums.txt)
#   5. smoke: runs install.sh end-to-end against the snapshot artifacts over
#      ST_INSTALL_BASE=file://<dist> into a temp INSTALL_DIR
#
# Skips cleanly when goreleaser isn't installed — CI installs it via
# goreleaser-action on the ubuntu leg; a local dev box may not have it.
#
# TODO(signing): once release signing lands, also assert dist/checksums.txt.minisig.
set -euo pipefail

cd "$(dirname "$0")/.."

sh -n install.sh

if ! command -v goreleaser >/dev/null 2>&1; then
	echo "check-install-assets: goreleaser not installed; skipping artifact check (CI installs it via goreleaser-action)"
	exit 0
fi

echo "==> goreleaser build --snapshot --clean"
goreleaser build --snapshot --clean

meta=dist/metadata.json
if [ ! -f "$meta" ]; then
	echo "FAIL: $meta missing after snapshot build" >&2
	exit 1
fi
version="$(sed -nE 's/.*"version":[[:space:]]*"([^"]+)".*/\1/p' "$meta" | head -1)"
if [ -z "$version" ]; then
	echo "FAIL: could not parse \"version\" from $meta" >&2
	exit 1
fi
echo "==> snapshot version: $version"

# A snapshot names its version segment differently than a release tag (e.g.
# 0.0.2-SNAPSHOT-<sha>); what must hold is the SHAPE install.sh reconstructs —
# ${ARCHIVE}_${VERSION_NUM}_${OS}_${ARCH}.tar.gz — so expected names are built
# from the snapshot's own version, never a literal.
sums=dist/checksums.txt
if [ ! -f "$sums" ]; then
	echo "FAIL: $sums missing — install.sh fetches it by that exact name" >&2
	exit 1
fi

fail=0
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
	name="stacked_${version}_${target}.tar.gz"
	if [ ! -f "dist/$name" ]; then
		echo "FAIL: expected artifact dist/$name not produced" >&2
		fail=1
		continue
	fi
	# Same pattern install.sh greps checksums.txt with.
	if ! grep -q " ${name}\$" "$sums"; then
		echo "FAIL: checksums.txt has no ' ${name}' line (install.sh greps for it)" >&2
		fail=1
	fi
	if ! tar -tzf "dist/$name" | grep -qE '^\./?st$'; then
		echo "FAIL: $name has no st binary at archive root; contents:" >&2
		tar -tzf "dist/$name" | sed 's/^/      /' >&2
		fail=1
	fi
done
if [ "$fail" -ne 0 ]; then
	echo "dist/ tarballs actually produced:" >&2
	ls dist/*.tar.gz 2>/dev/null | sed 's/^/  /' >&2 || true
	exit 1
fi

# install.sh's side of the contract: the constants validated above must be
# the ones install.sh actually uses to build its URLs.
grep -q '^ARCHIVE="stacked"$' install.sh ||
	{ echo 'FAIL: install.sh ARCHIVE is no longer "stacked"' >&2; exit 1; }
grep -q '^BINARY="st"$' install.sh ||
	{ echo 'FAIL: install.sh BINARY is no longer "st"' >&2; exit 1; }
grep -q 'ARCHIVE}_\${VERSION_NUM}_\${OS}_\${ARCH}\.tar\.gz' install.sh ||
	{ echo 'FAIL: install.sh FILENAME template changed' >&2; exit 1; }
grep -q 'checksums\.txt' install.sh ||
	{ echo 'FAIL: install.sh no longer fetches checksums.txt' >&2; exit 1; }

# End-to-end smoke: install.sh against the snapshot artifacts over file://.
# No checksums.txt.minisig exists in a snapshot build, so signature
# verification cannot run — ST_ALLOW_UNVERIFIED=1 exercises the checksum-only
# path; the fail-closed default itself is untouched.
bin="$(mktemp -d)"
trap 'rm -rf "$bin"' EXIT
echo "==> smoke: install.sh against file://$PWD/dist"
ST_ALLOW_UNVERIFIED=1 INSTALL_DIR="$bin" ST_INSTALL_BASE="file://$PWD/dist" VERSION="$version" \
	sh install.sh
[ -x "$bin/st" ] || { echo "FAIL: install.sh did not leave an executable $bin/st" >&2; exit 1; }
"$bin/st" version >/dev/null

echo "OK: goreleaser artifacts match install.sh's naming contract; installer smoke-passed"

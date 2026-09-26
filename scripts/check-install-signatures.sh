#!/usr/bin/env bash
#
# check-install-signatures.sh — exercise install.sh's fail-closed signature
# and checksum decisions against a disposable signed release fixture.
#
# check-install-assets.sh proves the asset-name contract but always runs the
# installer with ST_ALLOW_UNVERIFIED=1 — the security decisions this script
# covers are exactly what that smoke test skips. Everything here is local:
# an ephemeral minisign keypair signs a fixture checksums.txt, the installer's
# own VERSION / ST_INSTALL_BASE / INSTALL_DIR seams point at it over file://,
# and the ephemeral public key is substituted into a *copy* of install.sh —
# the checkout's installer and production key provisioning are never touched.
#
#   case                          expected
#   valid signature               installs the fixture st
#   missing signature             refuses by default; installs with override
#   empty embedded key            refuses by default; installs with override
#   minisign absent               refuses by default; installs with override
#   invalid signature             refuses even with ST_ALLOW_UNVERIFIED=1
#   archive changed after signing checksum mismatch
#   checksum entry missing        "no checksum entry" failure
#
# Every refusal must leave a pre-placed sentinel executable byte-identical,
# and each is matched on its specific diagnostic so an unrelated curl/tar
# failure cannot count as a pass.
#
# Requires minisign, curl, tar and the platform sha256sum/shasum — CI installs
# minisign on the Ubuntu leg; a dev box without it skips cleanly (the sibling
# asset check skips the same way without goreleaser).
set -euo pipefail

cd "$(dirname "$0")/.."

for tool in minisign curl tar; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "check-install-signatures: $tool not installed; skipping signature matrix (CI installs minisign on the ubuntu leg)"
		exit 0
	fi
done

FIXT="$(mktemp -d)"
trap 'rm -rf "$FIXT"' EXIT

# --- fixture ---------------------------------------------------------------
# install.sh's own OS/ARCH naming, reproduced for the asset name it expects.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in x86_64 | amd64) ARCH="amd64" ;; arm64 | aarch64) ARCH="arm64" ;; esac

VERSION="v0.0.0-test"
FILENAME="stacked_0.0.0-test_${OS}_${ARCH}.tar.gz"

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# build_dist <sig-mode>: (re)populate $FIXT/dist with the tarball, a signed
# checksums.txt, and optionally a detached signature. Modes:
#   signed   — tarball + checksums.txt + valid checksums.txt.minisig
#   nosig    — tarball + checksums.txt, signature file absent
#   badsig   — tarball + checksums.txt + signature made by a DIFFERENT key
#   noentry  — tarball + signed checksums.txt that omits the FILENAME line
build_dist() {
	local mode="$1"
	rm -rf "$FIXT/dist"
	mkdir -p "$FIXT/dist/pkg"
	printf '#!/bin/sh\necho st 0.0.0-test fixture\n' >"$FIXT/dist/pkg/st"
	chmod +x "$FIXT/dist/pkg/st"
	tar -czf "$FIXT/dist/$FILENAME" -C "$FIXT/dist/pkg" st
	rm -rf "$FIXT/dist/pkg"

	if [ "$mode" = "noentry" ]; then
		printf '%s  other-artifact.tar.gz\n' "$(sha256_of "$FIXT/dist/$FILENAME")" >"$FIXT/dist/checksums.txt"
	else
		printf '%s  %s\n' "$(sha256_of "$FIXT/dist/$FILENAME")" "$FILENAME" >"$FIXT/dist/checksums.txt"
	fi

	case "$mode" in
	signed | noentry)
		minisign -Sq -s "$FIXT/sec.key" -m "$FIXT/dist/checksums.txt" -x "$FIXT/dist/checksums.txt.minisig"
		;;
	badsig)
		# A validly formed signature from a different key: -V against the
		# fixture key fails.
		minisign -Sq -s "$FIXT/sec-other.key" -m "$FIXT/dist/checksums.txt" -x "$FIXT/dist/checksums.txt.minisig"
		;;
	nosig) ;;
	esac
}

# build_installer <with-key|no-key>: fresh installer copy per case.
build_installer() {
	cp install.sh "$FIXT/install.sh"
	if [ "$1" = "with-key" ]; then
		if [ "$(grep -c '^MINISIGN_PUBKEY=' "$FIXT/install.sh")" -ne 1 ]; then
			echo "FAIL: install.sh no longer has exactly one MINISIGN_PUBKEY assignment" >&2
			exit 1
		fi
		# Substitute the ephemeral public key into the COPY only.
		pub="$(grep '^RW' "$FIXT/pub.key")"
		sed -i.bak "s|^MINISIGN_PUBKEY=\"\"|MINISIGN_PUBKEY=\"$pub\"|" "$FIXT/install.sh"
		rm -f "$FIXT/install.sh.bak"
	fi
}

# run_install <extra-env...>: invoke the fixture installer; output captured in
# $FIXT/out.log; returns the installer's exit status. /bin/sh is spelled out so
# a controlled PATH= (the absent-minisign case) doesn't have to provide it.
run_install() {
	set +e
	env "$@" VERSION="$VERSION" ST_INSTALL_BASE="file://$FIXT/dist" INSTALL_DIR="$FIXT/bin" \
		/bin/sh "$FIXT/install.sh" >"$FIXT/out.log" 2>&1
	echo $?
	set -e
}

# no_minisign_path: a PATH dir with the installer's utilities but no minisign.
no_minisign_path() {
	rm -rf "$FIXT/tools"
	mkdir -p "$FIXT/tools"
	for t in uname tr curl mktemp grep awk sed tar mv chmod rm ls mkdir cp cat head shasum sha256sum; do
		src="$(command -v "$t" 2>/dev/null || true)"
		[ -n "$src" ] && ln -sf "$src" "$FIXT/tools/$t"
	done
	echo "$FIXT/tools"
}

place_sentinel() {
	mkdir -p "$FIXT/bin"
	printf '#!/bin/sh\necho sentinel\n' >"$FIXT/bin/st"
	chmod +x "$FIXT/bin/st"
}

sentinel_intact() {
	[ -f "$FIXT/bin/st" ] && grep -q sentinel "$FIXT/bin/st"
}

installed_ok() {
	[ -x "$FIXT/bin/st" ] && [ "$("$FIXT/bin/st")" = "st 0.0.0-test fixture" ]
}

pass() { echo "PASS: $1"; }
fails=0
fail() {
	echo "FAIL: $1" >&2
	sed 's/^/      | /' "$FIXT/out.log" >&2 || true
	fails=$((fails + 1))
}

# expect_refuse <name> <grep-pattern> <env...>: installer must exit nonzero
# with the named diagnostic AND preserve the sentinel.
expect_refuse() {
	local name="$1" pattern="$2"
	shift 2
	place_sentinel
	if [ "$(run_install "$@")" -eq 0 ]; then
		fail "$name: installer unexpectedly succeeded"
		return
	fi
	if ! grep -q "$pattern" "$FIXT/out.log"; then
		fail "$name: expected diagnostic /$pattern/ not found"
		return
	fi
	if ! sentinel_intact; then
		fail "$name: sentinel executable was modified or removed"
		return
	fi
	pass "$name"
}

# --- ephemeral keys ---------------------------------------------------------
minisign -G -Wq -p "$FIXT/pub.key" -s "$FIXT/sec.key" >/dev/null
minisign -G -Wq -p "$FIXT/pub-other.key" -s "$FIXT/sec-other.key" >/dev/null

# --- cases ------------------------------------------------------------------

build_dist signed
build_installer with-key
place_sentinel
if [ "$(run_install)" -eq 0 ] && installed_ok && grep -q "Signature verified" "$FIXT/out.log"; then
	pass "valid signature installs"
else
	fail "valid signature: expected successful verified install"
fi

build_dist nosig
build_installer with-key
expect_refuse "missing signature refuses by default" "could not download release signature"
place_sentinel
if [ "$(run_install ST_ALLOW_UNVERIFIED=1)" -eq 0 ] && installed_ok &&
	grep -q "continuing with checksum-only" "$FIXT/out.log"; then
	pass "missing signature installs with ST_ALLOW_UNVERIFIED=1"
else
	fail "missing signature + override: expected warned install"
fi

build_dist signed
build_installer no-key # empty MINISIGN_PUBKEY, signature file present
expect_refuse "empty embedded key refuses by default" "no release signing public key embedded"
place_sentinel
if [ "$(run_install ST_ALLOW_UNVERIFIED=1)" -eq 0 ] && installed_ok; then
	pass "empty embedded key installs with ST_ALLOW_UNVERIFIED=1"
else
	fail "empty key + override: expected warned install"
fi

build_dist signed
build_installer with-key
TOOLPATH="$(no_minisign_path)"
expect_refuse "absent minisign refuses by default" "minisign not found" "PATH=$TOOLPATH"
place_sentinel
if [ "$(run_install "PATH=$TOOLPATH" ST_ALLOW_UNVERIFIED=1)" -eq 0 ] && installed_ok; then
	pass "absent minisign installs with ST_ALLOW_UNVERIFIED=1"
else
	fail "no-minisign + override: expected warned install"
fi

build_dist badsig
build_installer with-key
expect_refuse "invalid signature refuses by default" "signature verification FAILED"
expect_refuse "invalid signature refuses even with ST_ALLOW_UNVERIFIED=1" "signature verification FAILED" ST_ALLOW_UNVERIFIED=1

# Re-sign the ORIGINAL checksums but swap in a changed archive: signature is
# valid, the checksum is not.
build_dist signed
printf 'tampered\n' >>"$FIXT/dist/$FILENAME"
build_installer with-key
expect_refuse "changed archive fails checksum" "checksum mismatch"

build_dist noentry
build_installer with-key
expect_refuse "missing checksum entry refuses" "no checksum entry"

if [ "$fails" -ne 0 ]; then
	echo "check-install-signatures: $fails case(s) failed" >&2
	exit 1
fi
echo "OK: installer signature/checksum decision matrix passed (9 cases)"

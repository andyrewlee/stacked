#!/usr/bin/env bash
#
# cover.sh — run the whole test suite ONCE and enforce a coverage threshold.
#
# The in-process unit/integration tests run with -race and emit coverage in
# GOCOVERDIR (covdata) format; the black-box e2e suite runs a coverage-
# instrumented `st` binary that emits its own covdata. The two are merged with
# `go tool covdata` so the reported number reflects BOTH the in-process tests and
# real end-to-end usage. Exits non-zero if total coverage is below THRESHOLD.
#
# THRESHOLD defaults to 75 and is overridable:  COVERAGE_MIN=80 ./scripts/cover.sh
set -euo pipefail

cd "$(dirname "$0")/.."

THRESHOLD="${COVERAGE_MIN:-75}"
PKGS="./cmd/...,./internal/..."

unitdir="$(mktemp -d)"
e2edir="$(mktemp -d)"
trap 'rm -rf "$unitdir" "$e2edir"' EXIT

echo "==> running unit/integration tests (race + coverage)"
go test ./cmd/... ./internal/... \
	-race -coverpkg="$PKGS" -count=1 \
	-args -test.gocoverdir="$unitdir"

echo "==> running black-box e2e tests (coverage-instrumented binary)"
GOCOVERDIR="$e2edir" go test ./e2e/... -count=1

echo "==> merging coverage (in-process + e2e)"
go tool covdata textfmt -i="$unitdir,$e2edir" -o=cover.out

SUMMARY="$(go tool cover -func=cover.out | tail -1)"
echo "$SUMMARY"
TOTAL="$(echo "$SUMMARY" | awk '{print $NF}' | tr -d '%')"
if [ -z "$TOTAL" ]; then
	echo "cover.sh: could not parse total coverage" >&2
	exit 1
fi

if [ "${TOTAL%.*}" -lt "$THRESHOLD" ]; then
	echo "FAIL: total coverage ${TOTAL}% is below threshold ${THRESHOLD}%" >&2
	exit 1
fi
echo "OK: total coverage ${TOTAL}% meets threshold ${THRESHOLD}% (in-process + e2e)"

# Per-function floor: a new under-tested function must not hide under a green
# total. Functions below COVERAGE_FUNC_MIN% (default 50) fail the gate unless
# justified in scripts/cover-allow.txt (matched on "<path>	<func>", never on
# line numbers, so entries survive unrelated edits).
FUNC_MIN="${COVERAGE_FUNC_MIN:-50}"
ALLOW="scripts/cover-allow.txt"
allowpats="$(mktemp)"
trap 'rm -rf "$unitdir" "$e2edir" "$allowpats"' EXIT
awk -F'\t+' '$1 !~ /^#/ && NF >= 2 { print $1 "\t" $2 }' "$ALLOW" >"$allowpats"
fails="$(go tool cover -func=cover.out | awk -v min="$FUNC_MIN" '
	$1 == "total:" { next }
	{ p = $NF; sub(/%/, "", p); if (p + 0 < min) { f = $1; sub(/:[0-9]+:$/, "", f); print f "\t" $2 } }
')"
unallowed=""
if [ -n "$fails" ]; then
	unallowed="$(echo "$fails" | grep -v -x -F -f "$allowpats" || true)"
fi
if [ -n "$unallowed" ]; then
	echo "FAIL: functions below the ${FUNC_MIN}% per-function floor (add tests, or justify in $ALLOW):" >&2
	echo "$unallowed" >&2
	exit 1
fi
echo "OK: per-function floor ${FUNC_MIN}% holds ($(wc -l <"$allowpats" | tr -d ' ') allowlisted)"

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

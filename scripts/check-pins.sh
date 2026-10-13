#!/bin/sh
# check-pins.sh LABEL EXPECTED FILE GREP-ERE SED-EXPR [FILE GREP-ERE SED-EXPR ...]
# — verify every file's declared version pin agrees with the expected value.
# Extraction is two-stage, matching the inline checks this replaces: grep -oE
# yields each match in file order (e.g. "Git 2.17+"), the sed expr then strips
# it to the bare pin (e.g. "2.17"); EVERY declaration must agree — a second,
# stale pin later in the file is the drift this check exists to catch. Every
# check-*-version doc-agreement leg runs through here so a new pin is a spec
# row, not a fifth skeleton.
set -eu

label=$1
expected=$2
shift 2

# A spec row is three args; a partial row or no rows at all must fail loudly —
# silently passing is the failure direction this script exists to prevent.
if [ $# -eq 0 ] || [ $(( $# % 3 )) -ne 0 ]; then
	echo "usage: check-pins.sh LABEL EXPECTED FILE GREP-ERE SED-EXPR [FILE GREP-ERE SED-EXPR ...]" >&2
	exit 2
fi

ok=1
files=""
while [ $# -ge 3 ]; do
	file=$1
	gre=$2
	sedexpr=$3
	shift 3
	# grep exit 1 (no match) must not trip set -e: a vanished declaration is a
	# failure WE report, not a signal the script swallowed. grep -oE output is
	# newline-delimited and a match cannot itself contain a newline, so the
	# per-match loop splits on newlines only (a match may contain spaces).
	matches=$(grep -oE "$gre" "$file" || true)
	if [ -z "$matches" ]; then
		echo "$file declares '<none>' (want $label $expected)" >&2
		ok=0
	else
		oldifs=$IFS
		IFS='
'
		for m in $matches; do
			IFS=$oldifs
			pin=$(printf '%s\n' "$m" | sed -E "$sedexpr")
			if [ "$pin" != "$expected" ]; then
				echo "$file declares '$pin' (want $label $expected)" >&2
				ok=0
			fi
		done
		IFS=$oldifs
	fi
	files="$files $file"
done
[ "$ok" -eq 1 ] || exit 1
echo "$label: $expected consistent across$files"

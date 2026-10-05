#!/bin/sh
# check-pins.sh LABEL EXPECTED FILE GREP-ERE SED-EXPR [FILE GREP-ERE SED-EXPR ...]
# — verify every file's declared version pin agrees with the expected value.
# Extraction is two-stage, matching the inline checks this replaces: grep -oE
# yields the first match in file order (e.g. "Git 2.17+"), the sed expr then
# strips it to the bare pin (e.g. "2.17"). Every check-*-version doc-agreement
# leg runs through here so a new pin is a spec row, not a fifth skeleton.
set -eu

label=$1
expected=$2
shift 2

ok=1
files=""
while [ $# -ge 3 ]; do
	file=$1
	gre=$2
	sedexpr=$3
	shift 3
	pin=$(grep -oE "$gre" "$file" | head -1 | sed -E "$sedexpr")
	if [ "$pin" != "$expected" ]; then
		echo "$file declares '${pin:-<none>}' (want $label $expected)" >&2
		ok=0
	fi
	files="$files $file"
done
[ "$ok" -eq 1 ] || exit 1
echo "$label: $expected consistent across$files"

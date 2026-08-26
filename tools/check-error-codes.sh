#!/usr/bin/env bash
# Asserts every DPG-Ennn code listed in Appendix C of rfc/dpg-1.md has at
# least one occurrence in core/, except codes explicitly marked retracted
# in the RFC (their whole point is that core/ must NOT implement them).
#
# Guards against the class of drift Phase 2 of the launch-readiness audit
# found: 22 codes documented in the RFC with no matching implementation.
set -euo pipefail

cd "$(dirname "$0")/.."

RFC=rfc/dpg-1.md
CORE=core

status=0

while IFS= read -r line; do
	code=$(printf '%s' "$line" | grep -oE 'DPG-E[0-9]{3}')
	if printf '%s' "$line" | grep -q 'retracted'; then
		if grep -rq "$code" "$CORE" --include='*.go'; then
			echo "error: $code is marked retracted in $RFC but still appears in $CORE/" >&2
			status=1
		fi
		continue
	fi
	if ! grep -rq "$code" "$CORE" --include='*.go'; then
		echo "error: $code is documented in $RFC Appendix C but has no occurrence in $CORE/" >&2
		status=1
	fi
done < <(grep -oE '\| DPG-E[0-9]{3} \|.*' "$RFC")

if [ "$status" -eq 0 ]; then
	echo "ok: every DPG-Ennn code in $RFC Appendix C is accounted for in $CORE/"
fi

exit "$status"

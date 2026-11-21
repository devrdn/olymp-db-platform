#!/usr/bin/env bash
# Replaces the run's generated secrets with *** in every text file under the
# directory given, so a failure artifact can be published: the workflow's
# ::add-mask:: protects only its own log, never an uploaded file.
#
#   frontend/e2e/redact.sh <artifact dir> <stack dir>
#
# The secrets are the *PASSWORD, *SECRET and *TOKEN values of <stack dir>/stack.env
# and E2E_ADMIN_PASSWORD of <stack dir>/smoke.env; either file may be missing.
# Prints no value. stack.sh writes every one with `openssl rand -hex 32`, so each
# is safe inside a sed pattern; do not feed it a value that is not hex.
set -euo pipefail

dir="${1:?usage: redact.sh <artifact dir> <stack dir>}"
stack="${2:?usage: redact.sh <artifact dir> <stack dir>}"

secrets=()
if [ -f "$stack/stack.env" ]; then
	while IFS='=' read -r key value; do
		case "$key" in
		*PASSWORD | *SECRET | *TOKEN) [ -z "$value" ] || secrets+=("$value") ;;
		esac
	done <"$stack/stack.env"
fi
if [ -f "$stack/smoke.env" ]; then
	value="$(sed -n 's/^E2E_ADMIN_PASSWORD=//p' "$stack/smoke.env")"
	[ -z "$value" ] || secrets+=("$value")
fi
[ "${#secrets[@]}" -gt 0 ] || exit 0

script=""
for value in "${secrets[@]}"; do
	script+="s/$value/***/g;"
done

# grep -I skips binary files (the screenshots), which hold no log text.
while IFS= read -r -d '' file; do
	sed "$script" "$file" >"$file.redacted"
	mv "$file.redacted" "$file"
done < <(grep -rIl --null . "$dir" || true)

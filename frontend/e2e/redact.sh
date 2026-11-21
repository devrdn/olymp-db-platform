#!/usr/bin/env bash
# Replaces the run's generated secrets with *** in every text file under the
# directory given, so a failure artifact can be published: the workflow's
# ::add-mask:: protects only its own log, never an uploaded file.
#
#   frontend/e2e/redact.sh <artifact dir> <stack dir>
#
# The secrets are the *PASSWORD, *SECRET and *TOKEN values of <stack dir>/stack.env
# and E2E_ADMIN_PASSWORD of <stack dir>/smoke.env; either file may be missing.
# Prints no value.
#
# It fails closed: the upload step runs after a failed one, so if redaction
# stops for any reason the whole artifact directory is deleted here, where the
# failure is seen, rather than trusting every caller to remember to.
set -euo pipefail

dir="${1:?usage: redact.sh <artifact dir> <stack dir>}"
stack="${2:?usage: redact.sh <artifact dir> <stack dir>}"

done_ok=0
list=""
cleanup() {
	[ -z "$list" ] || rm -f "$list"
	[ "$done_ok" -eq 1 ] || rm -rf "$dir"
}
trap cleanup EXIT

values=()
if [ -f "$stack/stack.env" ]; then
	while IFS='=' read -r key value; do
		case "$key" in
		*PASSWORD | *SECRET | *TOKEN) [ -z "$value" ] || values+=("$value") ;;
		esac
	done <"$stack/stack.env"
fi
if [ -f "$stack/smoke.env" ]; then
	value="$(sed -n 's/^E2E_ADMIN_PASSWORD=//p' "$stack/smoke.env")"
	[ -z "$value" ] || values+=("$value")
fi

if [ "${#values[@]}" -gt 0 ]; then
	# Longest first, so a value that contains another is not left with a tail;
	# each is escaped for sed, as the admin password is base64url, not hex.
	script=""
	while read -r _ value; do
		script+="s/$(printf '%s' "$value" | sed 's/[][\.*^$/&]/\\&/g')/***/g;"
	done < <(for value in "${values[@]}"; do echo "${#value} $value"; done | sort -rn)

	list="$(mktemp)"
	# Exit 1 is "no text file", fine; 2 is an error, which must not pass.
	status=0
	grep -rIl --null . "$dir" >"$list" || status=$?
	[ "$status" -le 1 ] || exit 1

	tmp="$(mktemp)"
	while IFS= read -r -d '' file; do
		sed "$script" "$file" >"$tmp"
		# Written through the file, not renamed over it, to keep its mode.
		cat "$tmp" >"$file"
	done <"$list"
	rm -f "$tmp"
fi
done_ok=1

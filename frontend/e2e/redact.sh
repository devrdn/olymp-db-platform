#!/usr/bin/env bash
# Replaces the run's generated secrets with *** in every text file under the
# failure artifact, so it can be published: the workflow's ::add-mask::
# protects only its own log, never an uploaded file.
#
#   frontend/e2e/redact.sh <path>/e2e-artifact <stack dir>
#
# The secrets are the *PASSWORD, *SECRET and *TOKEN values of <stack dir>/stack.env
# and E2E_ADMIN_PASSWORD of <stack dir>/smoke.env. Prints no value.
#
# It fails closed: the upload step runs after a failed one, so if redaction
# stops for any reason the whole artifact directory is deleted here, where the
# failure is seen, rather than trusting every caller to remember to. Container
# logs in e2e-logs/ exist only if a stack did, and so its stack.env: staged
# logs without a secret read from it are such a failure. A run that stopped
# before stack.env was written has no secret, and its artifact is left as is.
set -euo pipefail

# Bytes, not characters: under a UTF-8 locale one invalid byte can make grep
# skip a file as binary or sed refuse it. Only a NUL marks a file binary here.
export LC_ALL=C

usage="usage: redact.sh <path>/e2e-artifact <stack dir>"
arg="${1:?$usage}"
stack="${2:?$usage}"

# The directory is deleted on failure, so only the one the workflow stages
# qualifies: named e2e-artifact, which rules out /, the checkout and $HOME,
# and not the directory this runs in, which rules out `.`.
dir="$(cd -P -- "$arg" 2>/dev/null && pwd)" || {
	echo "redact: $arg is not a directory" >&2
	exit 2
}
if [ "${dir##*/}" != e2e-artifact ] || [ "$dir" = "$(pwd -P)" ]; then
	echo "redact: refusing $arg; $usage" >&2
	exit 2
fi

done_ok=0
list=""
tmp=""
cleanup() {
	[ -z "$list" ] || rm -f "$list"
	[ -z "$tmp" ] || rm -f "$tmp"
	[ "$done_ok" -eq 1 ] || rm -rf "$dir"
}
trap cleanup EXIT

values=()
from_stack=0
if [ -e "$stack/stack.env" ]; then
	[ -r "$stack/stack.env" ] || {
		echo "redact: $stack/stack.env cannot be read; artifact removed" >&2
		exit 1
	}
	while IFS='=' read -r key value; do
		case "$key" in
		*PASSWORD | *SECRET | *TOKEN)
			[ -z "$value" ] || {
				values+=("$value")
				from_stack=$((from_stack + 1))
			}
			;;
		esac
	done <"$stack/stack.env"
fi
if [ -f "$stack/smoke.env" ]; then
	value="$(sed -n 's/^E2E_ADMIN_PASSWORD=//p' "$stack/smoke.env")"
	[ -z "$value" ] || values+=("$value")
fi

logs=""
[ ! -d "$dir/e2e-logs" ] || logs="$(ls -A "$dir/e2e-logs")"
if [ -n "$logs" ] && [ "$from_stack" -eq 0 ]; then
	echo "redact: container logs are staged but $stack/stack.env gave no secret; artifact removed" >&2
	exit 1
fi

if [ "${#values[@]}" -gt 0 ]; then
	# Longest first, so a value that contains another is not left with a tail;
	# each is escaped for sed, as the admin password is base64url, not hex.
	script=""
	while read -r _ value; do
		script+="s/$(printf '%s' "$value" | sed 's/[][\.*^$/&]/\\&/g')/***/g;"
	done < <(for value in "${values[@]}"; do echo "${#value} $value"; done | sort -rn)

	list="$(mktemp "${TMPDIR:-/tmp}/redact.XXXXXX")"
	tmp="$(mktemp "${TMPDIR:-/tmp}/redact.XXXXXX")"
	# Exit 1 is "no text file", fine; 2 is an error, which must not pass.
	status=0
	grep -rIl --null . "$dir" >"$list" || status=$?
	[ "$status" -le 1 ] || exit 1

	while IFS= read -r -d '' file; do
		sed "$script" "$file" >"$tmp"
		# Written through the file, not renamed over it, to keep its mode.
		cat "$tmp" >"$file"
	done <"$list"
fi
done_ok=1

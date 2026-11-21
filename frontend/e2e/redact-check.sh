#!/usr/bin/env bash
# Proves what frontend/e2e/redact.sh guarantees for the public failure
# artifact, with the real script on fake env files and a fake artifact in a
# throwaway directory:
#
#   - every generated secret in every text file becomes ***, longest value
#     first and escaped for sed, under a UTF-8 locale and with an invalid
#     UTF-8 byte in the file; a binary file is left alone and a file keeps
#     its mode;
#   - staged container logs without a secret from stack.env (missing, empty
#     or unreadable) fail and delete the artifact; without logs and without
#     stack.env nothing happens;
#   - an unwritable file fails, deletes the artifact and leaves no temporary
#     file;
#   - any directory other than an existing <path>/e2e-artifact, or the one it
#     runs in, is refused before anything can be deleted.
#
# Run it as a user whom file modes stop, not root. REDACT_CHECK_ROOT=1 adds
# the case that passes / itself; CI sets it, as its runner is thrown away.
set -euo pipefail

redact="$(cd "$(dirname "$0")" && pwd)/redact.sh"

if [ "$(id -u)" -eq 0 ]; then
	echo "redact-check: run as a user other than root, whom file modes do not stop" >&2
	exit 2
fi
# The C.UTF-8 of Linux or the en_US.UTF-8 of macOS, set in the environment
# for redact.sh to override.
utf8="$(locale -a 2>/dev/null | grep -iE '^(c|en_us)\.utf-?8$' | sed -n 1p)" || true
[ -n "$utf8" ] || {
	echo "redact-check: no UTF-8 locale to run under" >&2
	exit 2
}

work="$(mktemp -d)"
# Some cases leave a file or a directory without permissions.
trap 'chmod -R u+rwX "$work" 2>/dev/null || true; rm -rf "$work"' EXIT

failed=0
pass() { echo "ok   $1"; }
fail() {
	echo "FAIL $1"
	sed 's/^/     /' "$c/out"
	failed=1
}

S1=aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888
S2=bbbb1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888
# S2SUB is S2's head: replaced first, it would leave S2's tail behind.
S2SUB=bbbb1111bbbb2222
# Unlike the hex values, this one carries characters sed reads as syntax, so
# it is found only if redact.sh escapes it; the $ is meant literally.
# shellcheck disable=SC2016
ADMIN='a+b/c.d*e[f]g$h&i-_xyz'

# A fresh case: $c/stack, $c/e2e-artifact and $c/tmp, the case's TMPDIR.
new_case() {
	c="$work/$1"
	mkdir -p "$c/stack" "$c/e2e-artifact" "$c/tmp"
	A="$c/e2e-artifact" ST="$c/stack" T="$c/tmp"
	: >"$c/out"
}
write_env() {
	printf 'REGISTRY=x\nCORE_DB_PASSWORD=%s\nINGRESS_SECRET=%s\nQUERY_RUNNER_TOKEN=%s\nREDIS_PASSWORD=\n' \
		"$S1" "$S2" "$S2SUB" >"$ST/stack.env"
	printf 'E2E_BASE_URL=https://localhost:18443\nE2E_ADMIN_PASSWORD=%s\n' "$ADMIN" >"$ST/smoke.env"
}
stage() {
	mkdir -p "$A/e2e-logs" "$A/test-results"
	printf 'ERROR: CREATE ROLE x PASSWORD %s\nsecret=%s token=%s admin=%s\n' \
		"$S1" "$S2" "$S2SUB" "$ADMIN" >"$A/e2e-logs/game-db.log"
	chmod 640 "$A/e2e-logs/game-db.log"
	printf 'admin %s\n' "$ADMIN" >"$A/test-results/error-context.md"
	printf '\x89PNG\r\n\x1a\n\x00\x00%s' "$S1" >"$A/test-results/shot.png"
}
run() { # <redact.sh arguments>: sets rc
	rc=0
	TMPDIR="$T" LC_ALL="$utf8" "$redact" "$@" >"$c/out" 2>&1 || rc=$?
}
leaks() {
	grep -raF -e "$S1" -e "$S2SUB" -e "$ADMIN" --exclude=shot.png "$A" >/dev/null 2>&1
}
tmp_left() { find "$T" -mindepth 1 | tr '\n' ' '; }
tmp_empty() { [ -z "$(tmp_left)" ]; }

new_case normal
write_env
stage
run "$A" "$ST"
if [ "$rc" -eq 0 ] && ! leaks && grep -q 'PASSWORD \*\*\*$' "$A/e2e-logs/game-db.log" &&
	grep -qx 'secret=\*\*\* token=\*\*\* admin=\*\*\*' "$A/e2e-logs/game-db.log" &&
	[ -n "$(find "$A/e2e-logs/game-db.log" -perm 0640)" ] &&
	grep -aqF "$S1" "$A/test-results/shot.png" && tmp_empty; then
	pass "every secret becomes ***, longest first, the mode is kept, a binary file is left alone"
else fail "normal path: rc=$rc"; fi

new_case invalid-byte
write_env
stage
printf 'PASSWORD %s \xff\n' "$S1" >"$A/e2e-logs/odd.log"
run "$A" "$ST"
if [ "$rc" -eq 0 ] && ! leaks && grep -aq 'PASSWORD \*\*\* ' "$A/e2e-logs/odd.log"; then
	pass "a file with an invalid UTF-8 byte is redacted under LC_ALL=$utf8"
else fail "invalid UTF-8 byte: rc=$rc, artifact $([ -d "$A" ] && echo kept || echo removed)"; fi

new_case trailing-slash
write_env
stage
run "$A/" "$ST/"
if [ "$rc" -eq 0 ] && ! leaks; then pass "arguments with a trailing slash are accepted"
else fail "trailing slashes: rc=$rc"; fi

new_case no-stack-env
stage
run "$A" "$ST"
if [ "$rc" -eq 1 ] && [ ! -e "$A" ]; then pass "staged logs without stack.env: exit 1, artifact removed"
else fail "logs without stack.env: rc=$rc, artifact $([ -e "$A" ] && echo kept || echo removed)"; fi

new_case no-secret
write_env
stage
printf 'REGISTRY=x\nREDIS_PASSWORD=\n' >"$ST/stack.env"
run "$A" "$ST"
if [ "$rc" -eq 1 ] && [ ! -e "$A" ]; then pass "staged logs with a stack.env holding no secret: exit 1, artifact removed"
else fail "logs with a stack.env holding no secret: rc=$rc"; fi

new_case unreadable-env
write_env
stage
chmod 000 "$ST/stack.env"
run "$A" "$ST"
if [ "$rc" -eq 1 ] && [ ! -e "$A" ]; then pass "an unreadable stack.env: exit 1, artifact removed"
else fail "unreadable stack.env: rc=$rc"; fi

new_case before-stack
mkdir -p "$A/e2e-logs" "$A/test-results"
printf 'nothing\n' >"$A/test-results/note.txt"
run "$A" "$ST"
if [ "$rc" -eq 0 ] && [ -f "$A/test-results/note.txt" ]; then pass "no logs and no stack.env: exit 0, artifact kept"
else fail "no logs, no stack.env: rc=$rc"; fi

new_case unwritable
write_env
stage
chmod 444 "$A/e2e-logs/game-db.log"
run "$A" "$ST"
if [ "$rc" -eq 1 ] && [ ! -e "$A" ] && tmp_empty; then pass "an unwritable file: exit 1, artifact removed, no temporary file left"
else fail "unwritable file: rc=$rc, left in TMPDIR: $(tmp_left)"; fi

# The refused arguments carry logs and no stack.env, so a script that took
# them would delete them.
new_case wrong-name
mkdir -p "$c/artifact/e2e-logs"
printf 'keep\n' >"$c/artifact/e2e-logs/x.log"
run "$c/artifact" "$ST"
if [ "$rc" -eq 2 ] && [ -f "$c/artifact/e2e-logs/x.log" ]; then pass "a directory not named e2e-artifact is refused"
else fail "a directory not named e2e-artifact: rc=$rc"; fi

new_case symlink
mkdir -p "$c/real/e2e-logs"
printf 'keep\n' >"$c/real/e2e-logs/x.log"
rmdir "$A"
ln -s "$c/real" "$A"
run "$A" "$ST"
if [ "$rc" -eq 2 ] && [ -f "$c/real/e2e-logs/x.log" ]; then pass "a symlink named e2e-artifact to another name is refused"
else fail "symlink: rc=$rc"; fi

new_case missing
run "$c/none/e2e-artifact" "$ST"
if [ "$rc" -eq 2 ]; then pass "a directory that does not exist is refused"
else fail "missing directory: rc=$rc"; fi

new_case cwd
mkdir -p "$A/e2e-logs"
printf 'keep\n' >"$A/e2e-logs/x.log"
for arg in . "$A"; do
	rc=0
	(cd "$A" && TMPDIR="$T" "$redact" "$arg" "$ST") >"$c/out" 2>&1 || rc=$?
	how="$([ "$arg" = . ] && echo . || echo 'its path')"
	if [ "$rc" -eq 2 ] && [ -f "$A/e2e-logs/x.log" ]; then pass "the working directory is refused, given as $how"
	else fail "the working directory given as $how: rc=$rc"; fi
done

if [ "${REDACT_CHECK_ROOT:-}" = 1 ]; then
	new_case root
	run / "$ST"
	if [ "$rc" -eq 2 ]; then pass "/ is refused"; else fail "/: rc=$rc"; fi
fi

if [ "$failed" -ne 0 ]; then
	echo "redact-check: FAILED" >&2
	exit 1
fi
echo "redact-check: all checks passed"

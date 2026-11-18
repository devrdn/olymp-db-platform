#!/usr/bin/env bash
# Proves what the host targets take from deploy/.env, with the real Makefile and
# a throwaway env file:
#
#   - `make run` and `make runner` hand their processes the settings the file
#     sets, through the environment and never on the echoed command line, while
#     the container-only values (REDIS_ADDR, QUERY_RUNNER_ADDR, GAME_UPLOAD_DIR,
#     ENV) are the host's;
#   - a value on the make command line beats the file, and the shell does not;
#   - an empty API_PORT still gives the API and the interface a port;
#   - a value make cannot expand, an open `$(` or `${` such as a provider may
#     put in a secret, changes nothing in `make backup`, `make deploy` or
#     `make deployed`: the scheduled backup runs on servers that never run
#     `make run`.
#
# `go` is a stub on PATH that records its environment, so nothing is compiled
# or started, and the other targets are dry runs. deploy/.env is never read.
#
# Usage: deploy/host-targets-check.sh   (or `make host-targets-check`).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
MAKE_BIN="$(command -v make)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
umask 077

failures=0
pass() { echo "ok   $*"; }
fail() { echo "FAIL $*"; failures=$((failures + 1)); }

mkdir "$WORK/bin"
cat > "$WORK/bin/go" <<'STUB'
#!/bin/sh
env > "$HOST_CHECK_DUMP/$(basename "$2").env"
STUB
chmod 700 "$WORK/bin/go"

# Nothing from the caller's environment reaches make, so a variable the caller
# happens to export cannot stand in for one the file is meant to give.
mk() {
	local file="$1"
	shift
	env -i PATH="$WORK/bin:/usr/bin:/bin" HOST_CHECK_DUMP="$WORK" \
		"$MAKE_BIN" --no-print-directory -C "$ROOT" ENV_FILE="$file" "$@"
}

# The shipped example with its placeholders filled, then the lines under test;
# a later assignment wins, as it does in an operator's file.
fixture() {
	local file="$WORK/$1.env"
	shift
	sed 's/change-me-before-first-run/0123456789abcdef/' "$ROOT/deploy/.env.example" > "$file"
	printf '%s\n' "$@" >> "$file"
	chmod 600 "$file"
	echo "$file"
}

has() { # file name value
	if grep -qxF "$2=$3" "$1"; then pass "$(basename "$1" .env) gets $2=$3"
	else fail "$(basename "$1" .env) gets $2=$3, has: $(grep "^$2=" "$1" || echo nothing)"; fi
}
lacks() { # file name
	if grep -q "^$2=" "$1"; then fail "$(basename "$1" .env) has no $2, has: $(grep "^$2=" "$1")"
	else pass "$(basename "$1" .env) has no $2"; fi
}

SECRETS=(check-github-secret check-author-password check-device-secret check-runner-token)
normal="$(fixture normal \
	API_PORT=8085 \
	QUERY_RUNNER_ADDR=queryrunner:9300 \
	GITHUB_CLIENT_SECRET=check-github-secret \
	GAME_AUTHOR_PASSWORD=check-author-password \
	DEVICE_COOKIE_SECRET=check-device-secret \
	QUERY_RUNNER_TOKEN=check-runner-token \
	SMTP_PORT=2525 \
	QUERY_PER_MINUTE=77)"

echo "== make run and make runner"
mk "$normal" run > "$WORK/run.out" 2>&1 || { cat "$WORK/run.out"; fail "make run exited non-zero"; }
mk "$normal" runner > "$WORK/runner.out" 2>&1 || { cat "$WORK/runner.out"; fail "make runner exited non-zero"; }
api="$WORK/api.env"
runner="$WORK/queryrunner.env"
for f in "$api" "$runner"; do [ -f "$f" ] || { fail "$(basename "$f") was not started"; : > "$f"; }; done

has "$api" GITHUB_CLIENT_SECRET check-github-secret
has "$api" GAME_AUTHOR_PASSWORD check-author-password
has "$api" DEVICE_COOKIE_SECRET check-device-secret
has "$api" QUERY_RUNNER_TOKEN check-runner-token
has "$api" SMTP_PORT 2525
has "$api" QUERY_PER_MINUTE 77
has "$api" HTTP_ADDR :8085
has "$api" QUERY_RUNNER_ADDR localhost:9300
has "$api" REDIS_ADDR "redis://:0123456789abcdef@localhost:6379/0"
has "$api" GAME_UPLOAD_DIR "$ROOT/deploy/game-uploads"
has "$api" ENV development
lacks "$api" SMTP_HOST
has "$runner" QUERY_RUNNER_TOKEN check-runner-token
has "$runner" QUERY_PER_MINUTE 77
has "$runner" QUERY_RUNNER_ADDR 127.0.0.1:9300
lacks "$runner" GITHUB_CLIENT_SECRET
for s in "${SECRETS[@]}"; do
	if grep -qF "$s" "$WORK/run.out" "$WORK/runner.out"; then fail "$s is on an echoed command line"
	else pass "$s is on no echoed command line"; fi
done

echo "== a value on the make command line"
# What the troubleshooting page tells a developer to type: the command line
# beats the file, an empty value included, and the shell does not.
rm -f "$api"
env -i PATH="$WORK/bin:/usr/bin:/bin" HOST_CHECK_DUMP="$WORK" SMTP_PORT=9999 \
	"$MAKE_BIN" --no-print-directory -C "$ROOT" ENV_FILE="$normal" \
	SMTP_HOST=localhost SMTP_TLS=none QUERY_PER_MINUTE=120 run > "$WORK/cli.out" 2>&1 ||
	{ cat "$WORK/cli.out"; fail "make run exited non-zero"; }
[ -f "$api" ] || { fail "api was not started"; : > "$api"; }
has "$api" SMTP_HOST localhost
has "$api" SMTP_TLS none
has "$api" QUERY_PER_MINUTE 120
has "$api" SMTP_PORT 2525
rm -f "$api"
mk "$normal" SMTP_PORT= run > "$WORK/blank.out" 2>&1 || { cat "$WORK/blank.out"; fail "make run exited non-zero"; }
[ -f "$api" ] || { fail "api was not started"; : > "$api"; }
has "$api" SMTP_PORT ""

echo "== an empty API_PORT"
noport="$(fixture noport API_PORT=)"
rm -f "$api"
mk "$noport" run > "$WORK/noport.out" 2>&1 || { cat "$WORK/noport.out"; fail "make run exited non-zero"; }
[ -f "$api" ] || { fail "api was not started"; : > "$api"; }
has "$api" HTTP_ADDR :8080
if mk "$noport" -n front-build 2>&1 | grep -qF 'API_ORIGIN="http://localhost:8080"'; then
	pass "front-build gets API_ORIGIN=http://localhost:8080"
else fail "front-build gets API_ORIGIN=http://localhost:8080"; fi

echo "== a value make cannot expand"
hostile="$(fixture hostile "GITHUB_CLIENT_SECRET='ab\$(cd'" "SMTP_PASSWORD='x\${y'")"
for goal in backup "deploy VERSION=v1.2.3" "deploy-api VERSION=v1.2.3" deployed; do
	# The dry runs name their env file, so each is compared with the other's
	# path written in.
	# shellcheck disable=SC2086 # a goal and its assignment are two words
	want="$(mk "$normal" -n $goal 2>&1)"
	want="${want//$normal/$hostile}"
	# shellcheck disable=SC2086 # as above
	if got="$(mk "$hostile" -n $goal 2>&1)" && [ "$got" = "$want" ]; then
		pass "make -n $goal is unchanged"
	else
		fail "make -n $goal changed: $(echo "$got" | tail -1)"
	fi
done

if [ "$failures" -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all host-target checks passed"

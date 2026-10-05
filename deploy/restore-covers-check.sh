#!/usr/bin/env bash
# Proves that `make restore-covers` gives back covers the API can read.
#
# A backup is only as good as its restore, and this half of it failed
# silently: the files came back owned by whoever ran make, with the 0600 mode
# the API writes them with, and the API — uid 65532 — could not open one of
# them (issue #56). Only a real container and a real volume show who owns a
# restored file, so this runs the real Makefile target against a throwaway
# compose project: the API image built from this tree, its covers volume, and
# an archive shaped like the one `make backup` writes.
#
# Nothing here touches deploy/.env or a running stack: the project has its own
# name and compose file, and is removed with its volume at the end.
#
# Usage: deploy/restore-covers-check.sh   (or `make restore-covers-check`).
# Needs Docker. API_IMAGE=<tag> skips the build and checks that image instead.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROJECT="restorecheck$$"
WORK="$(mktemp -d)"
COVERS=/var/lib/dbcontest/covers
READER="${READER_IMAGE:-caddy:2-alpine}"
API_IMAGE="${API_IMAGE:-}"

compose() { docker compose -f "$WORK/compose.yml" -p "$PROJECT" "$@"; }

cleanup() {
	compose down -v >/dev/null 2>&1 || true
	rm -rf "$WORK"
}
trap cleanup EXIT

failures=0
pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n        got: %s\n' "$1" "$2"; failures=$((failures + 1)); }
expect() { # name, got, want
	if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "$2 (want $3)"; fi
}

if [ -z "$API_IMAGE" ]; then
	API_IMAGE="dbcontest-api:restore-check"
	echo "building the API image"
	docker build -q --target runtime -t "$API_IMAGE" "$ROOT/backend" >/dev/null
fi

cat >"$WORK/compose.yml" <<EOF
services:
  api:
    image: $API_IMAGE
    volumes:
      - contest-covers-data:$COVERS
volumes:
  contest-covers-data:
EOF
: >"$WORK/env"

# The archive as `make backup` writes it: the files copied out of the api
# container onto the host, owned by the host user, keeping the API's 0600.
mkdir -p "$WORK/backup"
printf 'first cover' >"$WORK/backup/cover-a"
printf 'second cover' >"$WORK/backup/cover-b"
chmod 600 "$WORK/backup/cover-a" "$WORK/backup/cover-b"
tar -czf "$WORK/covers.tar.gz" -C "$WORK/backup" .

restore() { # into the api container of this project, through the real target
	make -s -C "$ROOT" restore-covers \
		COMPOSE="docker compose -f $WORK/compose.yml -p $PROJECT" \
		ENV_FILE="$WORK/env" CORE_DB_PASSWORD=unused \
		FILE="$WORK/covers.tar.gz" CONFIRM=yes >"$WORK/restore.log" 2>&1
}

as_api() { # command, run as the API's user against the covers volume
	docker run --rm --user 65532:65532 -v "${PROJECT}_contest-covers-data:$COVERS" "$READER" sh -c "$1" 2>&1
}

echo "Restoring into a fresh install"
compose create api >/dev/null 2>&1
if restore; then pass "make restore-covers succeeds"; else fail "make restore-covers succeeds" "$(cat "$WORK/restore.log")"; fi
expect "the API's user reads every restored cover" \
	"$(as_api "cat $COVERS/cover-a; echo; cat $COVERS/cover-b")" "first cover
second cover"
expect "the restored covers belong to the API's user" \
	"$(as_api "stat -c '%u:%g' $COVERS/cover-a $COVERS/cover-b | sort -u")" "65532:65532"
expect "and keep the mode the API wrote them with" \
	"$(as_api "stat -c '%a' $COVERS/cover-a")" "600"
expect "the API's user can still add a cover beside them" \
	"$(as_api "printf x > $COVERS/cover-new && echo written")" "written"

echo "Restoring again over the same covers"
if restore; then pass "a second restore succeeds"; else fail "a second restore succeeds" "$(cat "$WORK/restore.log")"; fi
expect "and leaves them readable" "$(as_api "cat $COVERS/cover-a")" "first cover"

if [ "$failures" -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"

#!/usr/bin/env bash
# Proves that `make backup` takes the game uploads, and that
# `make restore-game-uploads` gives them back to the API's user.
#
# The uploads are organisers' SQL dumps and the table builder's CSV files on
# the game-uploads-data volume; a game built from a file cannot be built again
# without them. Who owns a restored file, and what a real backup archive
# holds, only show on a real container and a real volume, so this runs the
# real Makefile targets against a throwaway compose project: the API image
# built from this tree with its two volumes, and a pg-core for the dump that
# `make backup` takes before it reaches the volumes.
#
# Nothing here touches deploy/.env, deploy/backups or a running stack: the
# project has its own name and compose file, every file lands in a temporary
# directory, and the project is removed with its volumes at the end.
#
# Usage: deploy/game-uploads-check.sh   (or `make game-uploads-check`).
# Needs Docker. API_IMAGE=<tag> skips the build and checks that image instead.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROJECT="uploadscheck$$"
WORK="$(mktemp -d)"
UPLOADS=/var/lib/dbcontest/uploads
READER="${READER_IMAGE:-caddy:2-alpine}"
PG_IMAGE="${PG_IMAGE:-postgres:16-alpine}"
API_IMAGE="${API_IMAGE:-}"
DUMP_ID=0f8e2c1a-4b7d-4e09-9c3a-5d6e7f809a1b
TABLE_ID=7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d

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
      - game-uploads-data:$UPLOADS
      - contest-covers-data:/var/lib/dbcontest/covers
  pg-core:
    image: $PG_IMAGE
    environment:
      POSTGRES_USER: dbcontest
      POSTGRES_PASSWORD: unused
      POSTGRES_DB: dbcontest_core
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "dbcontest", "-d", "dbcontest_core"]
      interval: 1s
      retries: 30
volumes:
  game-uploads-data:
  contest-covers-data:
EOF
: >"$WORK/env"

run_make() { # log, target, assignments...: the real target, against this project only
	log="$1"
	shift
	make -s -C "$ROOT" "$@" \
		COMPOSE="docker compose -f $WORK/compose.yml -p $PROJECT" \
		ENV_FILE="$WORK/env" CORE_DB_PASSWORD=unused >"$log" 2>&1
}

as_api() { # command, run as the API's user against the uploads volume
	docker run --rm --user 65532:65532 -v "${PROJECT}_game-uploads-data:$UPLOADS" "$READER" sh -c "$1" 2>&1
}

# The volume's contents as the API leaves them: a dump and its index at the
# root, a table's CSV under tables/, every file 0600 and tables/ 0750.
seed_as_api() {
	docker run --rm -v "${PROJECT}_game-uploads-data:$UPLOADS" "$READER" sh -c "
		cd $UPLOADS && mkdir -p tables && chmod 750 tables &&
		printf 'dump body' >$DUMP_ID.data && printf 'dump index' >$DUMP_ID.index &&
		printf 'id,name' >tables/$TABLE_ID.data &&
		chmod 600 $DUMP_ID.data $DUMP_ID.index tables/$TABLE_ID.data &&
		chown -R 65532:65532 $UPLOADS" >/dev/null
}

files_in() { # archive: its regular files, without the leading ./
	tar -tzvf "$1" | awk '$1 ~ /^-/ {print $NF}' | sed 's|^\./||' | sort
}

expected_files="$DUMP_ID.data
$DUMP_ID.index
tables/$TABLE_ID.data"

echo "Backing up an installation that holds game uploads"
compose up -d --wait pg-core >"$WORK/pg.log" 2>&1 ||
	{ echo "pg-core did not become healthy, so make backup cannot dump it:"; cat "$WORK/pg.log"; exit 1; }
compose create api >/dev/null 2>&1
seed_as_api
if run_make "$WORK/backup.log" backup BACKUP_DIR="$WORK/backups"; then
	pass "make backup succeeds"
else
	fail "make backup succeeds" "$(cat "$WORK/backup.log")"
fi
dump="$(ls "$WORK/backups" 2>/dev/null | grep '\.dump$' || true)"
uploads_archive="$(ls "$WORK/backups" 2>/dev/null | grep '^game-uploads-.*\.tar\.gz$' || true)"
stamp="$(echo "${dump:-no dump}" | sed 's/^dbcontest_core-//; s/\.dump$//')"
expect "it writes a game uploads archive beside the dump under the same timestamp" \
	"${uploads_archive:-no archive}" "game-uploads-$stamp.tar.gz"
if [ -n "$uploads_archive" ]; then
	expect "the archive holds every upload, the table builder's files included" \
		"$(files_in "$WORK/backups/$uploads_archive")" "$expected_files"
else
	fail "the archive holds every upload, the table builder's files included" "no archive"
fi

echo "Backing up with DB_ONLY=1"
if run_make "$WORK/dbonly.log" backup BACKUP_DIR="$WORK/dbonly" DB_ONLY=1; then
	pass "make backup DB_ONLY=1 succeeds"
else
	fail "make backup DB_ONLY=1 succeeds" "$(cat "$WORK/dbonly.log")"
fi
expect "it takes no game uploads archive" \
	"$(ls "$WORK/dbonly" 2>/dev/null | grep -c '^game-uploads-' || true)" "0"
expect "and says the game uploads were not taken" \
	"$(grep -c 'the game uploads were not taken' "$WORK/dbonly.log" || true)" "1"

# The archive as `make backup` writes it: the files copied out of the api
# container onto the host, owned by the host user, keeping the API's modes.
mkdir -p "$WORK/backup/tables"
printf 'restored dump' >"$WORK/backup/$DUMP_ID.data"
printf 'restored index' >"$WORK/backup/$DUMP_ID.index"
printf 'restored csv' >"$WORK/backup/tables/$TABLE_ID.data"
chmod 600 "$WORK/backup/$DUMP_ID.data" "$WORK/backup/$DUMP_ID.index" "$WORK/backup/tables/$TABLE_ID.data"
chmod 750 "$WORK/backup/tables"
tar -czf "$WORK/game-uploads.tar.gz" -C "$WORK/backup" .

restore() {
	run_make "$WORK/restore.log" restore-game-uploads FILE="$WORK/game-uploads.tar.gz" CONFIRM=yes
}

echo "Restoring into a fresh install"
compose down -v >/dev/null 2>&1
compose create api >/dev/null 2>&1
if run_make "$WORK/refused.log" restore-game-uploads FILE="$WORK/game-uploads.tar.gz"; then
	fail "without CONFIRM=yes the restore refuses" "$(cat "$WORK/refused.log")"
else
	pass "without CONFIRM=yes the restore refuses"
fi
if restore; then pass "make restore-game-uploads succeeds"; else fail "make restore-game-uploads succeeds" "$(cat "$WORK/restore.log")"; fi
expect "the API's user reads every restored upload" \
	"$(as_api "cd $UPLOADS && cat $DUMP_ID.data; echo; cat $DUMP_ID.index; echo; cat tables/$TABLE_ID.data")" "restored dump
restored index
restored csv"
expect "every restored file and directory belongs to the API's user" \
	"$(as_api "find $UPLOADS -exec stat -c '%u:%g' {} + | sort -u")" "65532:65532"
expect "the files keep the mode the API wrote them with" \
	"$(as_api "stat -c '%a' $UPLOADS/$DUMP_ID.data $UPLOADS/tables/$TABLE_ID.data | sort -u")" "600"
expect "and tables/ keeps its own" "$(as_api "stat -c '%a' $UPLOADS/tables")" "750"
expect "the API's user can still add an upload and a table beside them" \
	"$(as_api "printf x > $UPLOADS/new.data && printf x > $UPLOADS/tables/new.data && echo written")" "written"

echo "Restoring again over the same uploads"
if restore; then pass "a second restore succeeds"; else fail "a second restore succeeds" "$(cat "$WORK/restore.log")"; fi
expect "and leaves them readable" "$(as_api "cat $UPLOADS/tables/$TABLE_ID.data")" "restored csv"

if [ "$failures" -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"

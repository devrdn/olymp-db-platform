#!/usr/bin/env bash
# Proves the scheduled backup: the rotation, the wrapper the timer runs, and
# the schedule's units, on synthetic backup sets in a throwaway container.
#
# Rotation deletes files, so what it may delete is checked against a
# directory built to tempt it: incomplete sets, another database's dump, a
# directory and a symlink named like a set, and files that only look like
# one. The real `make backup` target runs against a fake Compose that hands
# it a synthetic dump and two synthetic volumes, and is killed mid-run to show
# that a stopped backup leaves nothing that counts as a set. Elsewhere it is
# replaced by a stub; nothing here dumps or restores a database. The units are
# rendered and given to `systemd-analyze verify`, and the two schedules to
# `systemd-analyze calendar`, in the same container.
#
# Only the Makefile, the scripts and the unit files are copied into the
# container, never deploy/backups itself, so the backups on this machine are
# out of reach. The container has no network and is removed at the end.
#
# Usage: deploy/backups/scheduled-backup-check.sh   (or `make scheduled-backup-check`).
# Needs Docker; the first run builds a small Debian image with systemd, rsync
# and rclone (CHECK_IMAGE=<tag> uses one already built).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

outside() {
	local root work name image
	root="$(cd "$HERE/../.." && pwd)"
	work="$(mktemp -d)"
	name="backupcheck$$"
	image="${CHECK_IMAGE:-dbcontest-backup-check:bookworm}"
	# shellcheck disable=SC2064 # expanded now: the locals are gone when it runs
	trap "docker rm -f '$name' >/dev/null 2>&1 || true; rm -rf '$work'" EXIT

	if [ -z "${CHECK_IMAGE:-}" ]; then
		echo "building the check image"
		docker build -q -t "$image" - >/dev/null <<-'EOF'
			FROM debian:12-slim
			RUN apt-get update \
			 && apt-get install -y --no-install-recommends systemd rsync rclone make \
			 && rm -rf /var/lib/apt/lists/*
		EOF
	fi

	mkdir -p "$work/srv/dbcontest/deploy/backups" "$work/srv/dbcontest/deploy/systemd"
	cp "$root/Makefile" "$work/srv/dbcontest/"
	cp "$root"/deploy/backups/*.sh "$work/srv/dbcontest/deploy/backups/"
	cp -R "$root"/deploy/systemd/. "$work/srv/dbcontest/deploy/systemd/"

	docker create --name "$name" --network none "$image" \
		bash /srv/dbcontest/deploy/backups/scheduled-backup-check.sh --inside >/dev/null
	docker cp "$work/srv" "$name:/" >/dev/null
	docker start -a "$name"
}

failures=0
pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n        got: %s\n' "$1" "$2"; failures=$((failures + 1)); }
expect() { # name, got, want
	if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "$2 (want $3)"; fi
}
expect_contains() { # name, haystack, needle
	case "$2" in *"$3"*) pass "$1" ;; *) fail "$1" "$2 (want it to contain: $3)" ;; esac
}
expect_lacks() { # name, haystack, needle
	case "$2" in *"$3"*) fail "$1" "$2 (it must not contain: $3)" ;; *) pass "$1" ;; esac
}
refused() { # name, message, command...: the command fails, saying message
	local name=$1 message=$2 out
	shift 2
	if out=$("$@" 2>&1); then fail "$name" "exit 0: $out"; else expect_contains "$name" "$out" "$message"; fi
}

ROOT=/srv/dbcontest
B="$ROOT/deploy/backups"
DB=dbcontest_core

mkset() { # dir stamp: a complete set as `make backup` names it
	printf dump >"$1/$DB-$2.dump"
	printf covers >"$1/covers-$2.tar.gz"
	printf uploads >"$1/game-uploads-$2.tar.gz"
}
setfiles() { # stamp...: the file names of those sets, one per line
	local s
	for s in "$@"; do printf '%s\n' "$DB-$s.dump" "covers-$s.tar.gz" "game-uploads-$s.tar.gz"; done
}
listing() { (cd "$1" && find . -mindepth 1 -maxdepth 1 ! -name '.*.lock' -printf '%f\n' | LC_ALL=C sort) }
sorted() { LC_ALL=C sort; }
complete_count() { # dir: complete sets in it
	local d n=0
	for d in "$1/$DB"-*.dump; do
		d=${d##*/"$DB"-}
		if [ -n "$(is_kept "$1" "${d%.dump}")" ]; then n=$((n + 1)); fi
	done
	echo "$n"
}
metric() { sed -n "s/^$1 //p" "$2" 2>/dev/null || true; }
newest_dump_stamp() { # dir: the stamp of the newest dump by name
	local d
	d=$(find "$1" -maxdepth 1 -name "$DB-*.dump" -printf '%f\n' | LC_ALL=C sort | tail -n1)
	d=${d#"$DB-"}
	printf '%s' "${d%.dump}"
}
stamp_of_run() { # the BACKUP_STAMP the stub make was last given
	sed -n 's/^make .* BACKUP_STAMP=\([^ ]*\)$/\1/p' "$STUB_LOG" | tail -n1
}

# Every configuration value the scripts read, cleared, so each case states
# its own and nothing leaks from one case into the next.
clean_env() {
	unset BACKUP_DIR BACKUP_KEEP BACKUP_KEEP_DAILY BACKUP_MIN_FREE_MB BACKUP_TMPDIR \
		BACKUP_OFFHOST BACKUP_OFFHOST_TARGET BACKUP_OFFHOST_SSH_KEY BACKUP_RCLONE_CONFIG \
		BACKUP_TEXTFILE_DIR CORE_DB_NAME STUB_MAKE
	: >"$ROOT/deploy/.env"
}

# The tempting directory: eleven complete sets over six days, and everything
# rotation must leave alone however old it is, a stopped run's .partial too.
tempting() {
	local d=$1 s
	mkdir -p "$d"
	for s in 20261028-033000 20261029-033000 20261030-033000 20261031-033000 \
		20261101-090000 20261101-091500 20261101-093000 \
		20261102-090000 20261102-091500 20261102-093000 20261102-094500; do
		mkset "$d" "$s"
	done
	printf dump >"$d/$DB-20261020-000000.dump"
	printf dump >"$d/$DB-20261021-120000.dump"
	printf covers >"$d/covers-20261021-120000.tar.gz"
	printf notes >"$d/notes.txt"
	printf dump >"$d/$DB-latest.dump"
	printf part >"$d/covers-20261022-000000.tar.gz.part"
	printf dump >"$d/otherdb-20261023-000000.dump"
	printf covers >"$d/covers-20261023-000000.tar.gz"
	printf uploads >"$d/game-uploads-20261023-000000.tar.gz"
	printf dump >"$d/$DB-20261024-000000.dump"
	printf covers >"$d/covers-20261024-000000.tar.gz"
	mkdir "$d/game-uploads-20261024-000000.tar.gz"
	printf dump >"$d/$DB-20261025-000000.dump"
	printf uploads >"$d/game-uploads-20261025-000000.tar.gz"
	ln -s "$d/notes.txt" "$d/covers-20261025-000000.tar.gz"
	printf half >"$d/.$DB-20261028-033000.dump.partial"
}
untouchable() {
	printf '%s\n' "$DB-20261020-000000.dump" "$DB-20261021-120000.dump" "covers-20261021-120000.tar.gz" \
		notes.txt "$DB-latest.dump" covers-20261022-000000.tar.gz.part \
		otherdb-20261023-000000.dump covers-20261023-000000.tar.gz game-uploads-20261023-000000.tar.gz \
		"$DB-20261024-000000.dump" covers-20261024-000000.tar.gz game-uploads-20261024-000000.tar.gz \
		"$DB-20261025-000000.dump" game-uploads-20261025-000000.tar.gz covers-20261025-000000.tar.gz \
		".$DB-20261028-033000.dump.partial"
}

check_rotation() {
	local t=$1 out want
	echo "Rotation"

	clean_env
	tempting "$t/r1"
	out=$(BACKUP_KEEP=3 BACKUP_KEEP_DAILY=2 "$B/rotate.sh" "$t/r1" 2>&1) ||
		fail "rotate.sh succeeds" "$out"
	want=$({ setfiles 20261031-033000 20261101-093000 20261102-091500 20261102-093000 20261102-094500; untouchable; } | sorted)
	expect "keeps the newest 3 sets and the last set of the 2 days before them, and nothing else is touched" \
		"$(listing "$t/r1")" "$want"
	expect_contains "says what it removed" "$out" "removed set 20261028-033000"
	expect_contains "names the incomplete sets it left alone" "$out" "left alone, incomplete: 20261021-120000"
	expect_contains "and the unfinished files of a stopped run" "$out" "left alone, unfinished: .$DB-20261028-033000.dump.partial"
	expect_contains "states what it kept" "$out" "kept 5 complete sets"

	clean_env
	tempting "$t/r2"
	want=$(listing "$t/r2")
	out=$(BACKUP_KEEP=3 BACKUP_KEEP_DAILY=2 "$B/rotate.sh" --dry-run "$t/r2" 2>&1) ||
		fail "a dry run succeeds" "$out"
	expect "a dry run removes nothing" "$(listing "$t/r2")" "$want"
	expect_contains "and says what it would remove" "$out" "would remove set 20261102-090000"

	clean_env
	mkdir -p "$t/r3"
	for s in 20261101-090000 20261101-091500 20261101-093000 20261101-094500 20261101-100000; do mkset "$t/r3" "$s"; done
	out=$(BACKUP_KEEP=1 BACKUP_KEEP_DAILY=0 "$B/rotate.sh" "$t/r3" 2>&1) || fail "keep one succeeds" "$out"
	expect "keeping one keeps the newest set" "$(listing "$t/r3")" "$(setfiles 20261101-100000 | sorted)"

	want=$(listing "$t/r3")
	refused "keeping none is refused" "BACKUP_KEEP=0 is not a whole number from 1" \
		env BACKUP_KEEP=0 "$B/rotate.sh" "$t/r3"
	expect "and removes nothing" "$(listing "$t/r3")" "$want"
	refused "a count that is not a number is refused" "BACKUP_KEEP=ten is not" \
		env BACKUP_KEEP=ten "$B/rotate.sh" "$t/r3"
	refused "a count beyond the bound is refused" "BACKUP_KEEP=100000 is not" \
		env BACKUP_KEEP=100000 "$B/rotate.sh" "$t/r3"
	refused "a database name that is not an identifier is refused" "is not a plain identifier" \
		env CORE_DB_NAME='x*' "$B/rotate.sh" "$t/r3"

	clean_env
	mkdir -p "$t/r4"
	for s in 20261101-090000 20261101-091500 20261101-093000 20261101-094500; do mkset "$t/r4" "$s"; done
	printf 'ADMIN_NAME=System Administrator\nBACKUP_KEEP=2\nBACKUP_KEEP_DAILY=0\n' >"$ROOT/deploy/.env"
	out=$("$B/rotate.sh" "$t/r4" 2>&1) || fail "rotation reading deploy/.env succeeds" "$out"
	expect "reads its counts from deploy/.env" "$(listing "$t/r4")" "$(setfiles 20261101-093000 20261101-094500 | sorted)"
}

check_wrapper() {
	local t=$1 out rc now first newest
	local dir="$t/w" tf="$t/textfile"
	echo "The scheduled run"
	mkdir -p "$dir" "$tf"
	for s in 20260101-033000 20260102-033000 20260103-033000 20260104-033000; do mkset "$dir" "$s"; done
	printf notes >"$dir/notes.txt"

	clean_env
	out=$(BACKUP_DIR="$dir" BACKUP_KEEP=2 BACKUP_KEEP_DAILY=0 BACKUP_TEXTFILE_DIR="$tf" \
		"$B/scheduled-backup.sh" 2>&1) || fail "a scheduled run succeeds" "$out"
	expect_contains "runs make backup into the configured directory" "$(cat "$STUB_LOG")" \
		"-s -C $ROOT backup BACKUP_DIR=$dir"
	expect_contains "naming the set in UTC" "$(stamp_of_run)" "$(date -u +%Y%m%d)-"
	newest=$(stamp_of_run)
	expect "with a Z" "${newest: -1}" "Z"
	newest="$DB-$(stamp_of_run).dump"
	expect "the newest set is the one just taken" "$(newest_dump_stamp "$dir")" "$(stamp_of_run)"
	expect "rotation followed it" "$(complete_count "$dir")" "2"
	expect "and left the other files alone" "$(test -f "$dir/notes.txt" && echo kept)" "kept"
	expect "the new set is readable by its owner only" \
		"$(stat -c '%a' "$dir/$newest")" "600"
	now=$(date +%s)
	first=$(metric dbcontest_backup_last_success_timestamp_seconds "$tf/dbcontest_backup.prom")
	expect "the textfile records the success" \
		"$(metric dbcontest_backup_last_run_success "$tf/dbcontest_backup.prom")" "1"
	expect "with its time" "$(((now - ${first:-0}) < 60))" "1"
	expect "and node_exporter can read it" "$(stat -c '%a' "$tf/dbcontest_backup.prom")" "644"
	expect_contains "says the off-host copy is off" "$out" "off-host copy is off"

	clean_env
	rc=0
	out=$(STUB_MAKE=fail BACKUP_DIR="$dir" BACKUP_TEXTFILE_DIR="$tf" "$B/scheduled-backup.sh" 2>&1) || rc=$?
	expect "a failed make backup fails the run" "$((rc != 0))" "1"
	expect_contains "and says why" "$out" "make backup failed"
	expect_contains "with what make said" "$out" "pg_dump: error"
	out=$(JOURNAL_STREAM=8:1 STUB_MAKE=fail BACKUP_DIR="$dir" "$B/scheduled-backup.sh" 2>&1) || true
	expect_contains "under systemd the reason is logged at error priority" "$out" "<3>make backup failed"
	expect "the textfile records the failure" \
		"$(metric dbcontest_backup_last_run_success "$tf/dbcontest_backup.prom")" "0"
	expect "and keeps the last success" \
		"$(metric dbcontest_backup_last_success_timestamp_seconds "$tf/dbcontest_backup.prom")" "$first"

	clean_env
	rc=0
	out=$(STUB_MAKE=dump-only BACKUP_DIR="$dir" "$B/scheduled-backup.sh" 2>&1) || rc=$?
	expect "a run that leaves no complete set fails" "$((rc != 0))" "1"
	expect_contains "and says so" "$out" "did not leave a complete set"

	clean_env
	: >"$STUB_LOG"
	rc=0
	expect "before the full-disk case there are two sets or more" "$(($(complete_count "$dir") >= 2))" "1"
	out=$(BACKUP_DIR="$dir" BACKUP_TEXTFILE_DIR="$tf" BACKUP_KEEP=1 BACKUP_KEEP_DAILY=0 \
		BACKUP_MIN_FREE_MB=9000000 "$B/scheduled-backup.sh" 2>&1) || rc=$?
	expect "a disk without room fails the run" "$((rc != 0))" "1"
	expect_contains "and says how much room it needs" "$out" "free and a run needs"
	expect_contains "and the command that frees it now" "$out" "BACKUP_KEEP=<n> $B/rotate.sh $dir"
	expect "a lowered BACKUP_KEEP is applied before the disk is measured" "$(complete_count "$dir")" "1"
	expect "before make backup runs" "$(cat "$STUB_LOG")" ""
	expect "and the textfile records the failure" \
		"$(metric dbcontest_backup_last_run_success "$tf/dbcontest_backup.prom")" "0"

	clean_env
	(flock 9 && sleep 5) 9>"$dir/.scheduled-backup.lock" &
	while flock -n "$dir/.scheduled-backup.lock" true; do sleep 0.05; done
	rc=0
	out=$(BACKUP_DIR="$dir" timeout 3 "$B/scheduled-backup.sh" 2>&1) || rc=$?
	expect "a second run while one is going fails at once" "$((rc != 0 && rc != 124))" "1"
	expect_contains "and says another is running" "$out" "still running"
	expect "before make backup runs" "$(cat "$STUB_LOG")" ""
	wait

	echo "The off-host copy"
	clean_env
	mkdir -p "$t/remote-rsync"
	out=$(BACKUP_DIR="$dir" BACKUP_OFFHOST=rsync BACKUP_OFFHOST_TARGET="$t/remote-rsync" \
		"$B/scheduled-backup.sh" 2>&1) || fail "a run with an rsync copy succeeds" "$out"
	expect "rsync copies exactly the new set" "$(listing "$t/remote-rsync")" "$(setfiles "$(stamp_of_run)" | sorted)"
	expect_lacks "without printing the target" "$out" "$t/remote-rsync"

	clean_env
	mkdir -p "$t/remote-rclone"
	out=$(BACKUP_DIR="$dir" BACKUP_OFFHOST=rclone BACKUP_OFFHOST_TARGET="$t/remote-rclone" \
		"$B/scheduled-backup.sh" 2>&1) || fail "a run with an rclone copy succeeds" "$out"
	expect "rclone copies exactly the new set" "$(listing "$t/remote-rclone")" "$(setfiles "$(stamp_of_run)" | sorted)"

	clean_env
	rc=0
	out=$(BACKUP_DIR="$dir" BACKUP_TEXTFILE_DIR="$tf" BACKUP_OFFHOST=rsync \
		BACKUP_OFFHOST_TARGET="/nonexistent/s3cr3t-token/dbcontest" "$B/scheduled-backup.sh" 2>&1) || rc=$?
	expect "a failed off-host copy fails the run" "$((rc != 0))" "1"
	expect_contains "and says so" "$out" "off-host copy failed"
	expect_lacks "without echoing the target" "$out" "s3cr3t-token"
	expect "the set itself still counts as a success" \
		"$(metric dbcontest_backup_last_run_success "$tf/dbcontest_backup.prom")" "1"
	expect "and the off-host failure is recorded" \
		"$(metric dbcontest_backup_offhost_last_run_success "$tf/dbcontest_backup.prom")" "0"

	clean_env
	: >"$STUB_LOG"
	refused "an unknown off-host tool is refused" "BACKUP_OFFHOST must be rsync, rclone or empty" \
		env BACKUP_DIR="$dir" BACKUP_OFFHOST=ftp BACKUP_OFFHOST_TARGET=x "$B/scheduled-backup.sh"
	refused "an off-host copy with no target is refused" "needs BACKUP_OFFHOST_TARGET" \
		env BACKUP_DIR="$dir" BACKUP_OFFHOST=rsync "$B/scheduled-backup.sh"
	refused "a bad rotation count is refused" "BACKUP_KEEP=0 is not" \
		env BACKUP_DIR="$dir" BACKUP_KEEP=0 "$B/scheduled-backup.sh"
	local target
	for target in 'backup:pa55word@nas:/srv/dbcontest' 'rsync://backup:pa55word@nas/dbcontest' '-e pa55word'; do
		rc=0
		out=$(BACKUP_DIR="$dir" BACKUP_OFFHOST=rsync BACKUP_OFFHOST_TARGET="$target" "$B/scheduled-backup.sh" 2>&1) || rc=$?
		expect "an rsync target carrying a password is refused: $target" "$((rc != 0))" "1"
		expect_contains "an rsync target carrying a password or an option is refused" "$out" "BACKUP_OFFHOST_TARGET is refused"
		expect_lacks "without echoing it" "$out" "pa55word"
	done
	for target in ':s3,access_key_id=AK,secret_access_key=pa55word:bucket' 's3,secret_access_key=pa55word:bucket' 'remote{pa55word}:x'; do
		rc=0
		out=$(BACKUP_DIR="$dir" BACKUP_OFFHOST=rclone BACKUP_OFFHOST_TARGET="$target" "$B/scheduled-backup.sh" 2>&1) || rc=$?
		expect "an rclone connection string is refused: $target" "$((rc != 0))" "1"
		expect_contains "an rclone target that is not a named remote or a path is refused" "$out" "BACKUP_OFFHOST_TARGET is refused"
		expect_lacks "without echoing it" "$out" "pa55word"
	done
	expect "all before make backup runs" "$(cat "$STUB_LOG")" ""

	echo "Which set a run calls its own"
	local fut="$t/future" remote="$t/remote-future"
	mkdir -p "$fut" "$remote"
	mkset "$fut" 20991231-235959Z
	clean_env
	rc=0
	out=$(STUB_MAKE=dump-only BACKUP_DIR="$fut" BACKUP_TEXTFILE_DIR="$tf" BACKUP_OFFHOST=rsync \
		BACKUP_OFFHOST_TARGET="$remote" "$B/scheduled-backup.sh" 2>&1) || rc=$?
	expect "a set dated later than now does not pass for this run's" "$((rc != 0))" "1"
	expect "the textfile records the failure" \
		"$(metric dbcontest_backup_last_run_success "$tf/dbcontest_backup.prom")" "0"
	expect "and nothing is copied off the host" "$(listing "$remote")" ""
	clean_env
	out=$(BACKUP_DIR="$fut" BACKUP_KEEP=1 BACKUP_KEEP_DAILY=0 BACKUP_OFFHOST=rsync \
		BACKUP_OFFHOST_TARGET="$remote" "$B/scheduled-backup.sh" 2>&1) || fail "a run beside a future-dated set succeeds" "$out"
	expect "it copies the set it wrote, not the newest by name" "$(listing "$remote")" "$(setfiles "$(stamp_of_run)" | sorted)"
	expect "and rotation keeps the set it wrote" "$(is_kept "$fut" "$(stamp_of_run)")" "kept"

	echo "A symlinked backup directory"
	mkdir -p "$t/disk/sets"
	ln -s "$t/disk/sets" "$t/sets-link"
	for s in 20260101-033000 20260102-033000 20260103-033000; do mkset "$t/disk/sets" "$s"; done
	clean_env
	out=$(BACKUP_DIR="$t/sets-link" BACKUP_KEEP=2 BACKUP_KEEP_DAILY=0 "$B/scheduled-backup.sh" 2>&1) ||
		fail "a run into a symlinked directory succeeds" "$out"
	expect "and rotates it" "$(complete_count "$t/disk/sets")" "2"
	expect "the link itself is left as it was" "$(test -L "$t/sets-link" && echo link)" "link"

	echo "A run stopped by systemd"
	clean_env
	rm -f "$tf/dbcontest_backup.prom"
	: >"$STUB_LOG"
	setsid env STUB_MAKE=hang BACKUP_DIR="$dir" BACKUP_TEXTFILE_DIR="$tf" "$B/scheduled-backup.sh" >"$t/term.log" 2>&1 &
	local pid=$!
	until grep -q hanging "$STUB_LOG"; do sleep 0.05; done
	kill -TERM -- "-$pid"
	wait "$pid" 2>/dev/null || true
	expect "still writes the textfile, as a failure" \
		"$(metric dbcontest_backup_last_run_success "$tf/dbcontest_backup.prom")" "0"
	expect_contains "and says the run failed" "$(cat "$t/term.log")" "scheduled backup FAILED"
}
is_kept() { # dir stamp
	[ -f "$1/$DB-$2.dump" ] && [ -f "$1/covers-$2.tar.gz" ] && [ -f "$1/game-uploads-$2.tar.gz" ] && echo kept
}

# The real target, against the fake Compose: nothing it reaches is real.
real_backup() { # dir
	/usr/bin/make -s -C "$ROOT" backup COMPOSE="$FAKE_COMPOSE" ENV_FILE="$ROOT/deploy/.env" \
		CORE_DB_PASSWORD=unused BACKUP_DIR="$1"
}
finals() { # dir: files named as a set's pieces, whatever their state
	find "$1" -maxdepth 1 \( -name "$DB-*.dump" -o -name 'covers-*.tar.gz' -o -name 'game-uploads-*.tar.gz' \) -printf '%f\n'
}
partials() { find "$1" -maxdepth 1 -name '.*.partial' -printf '%f\n'; }
generic() { sed -E -e 's/-[0-9]{8}-[0-9]{6}Z/-T/' -e 's/\.[0-9]+\.partial$/.P.partial/' | sorted; }
stopped_backup() { # signal dir setting: the target stopped mid-run, its staging in dir/../tmp-<dir>
	local pid tmp="$2.tmp"
	mkdir -p "$tmp"
	setsid env "$3" TMPDIR="$tmp" bash -c "$(declare -f real_backup); ROOT=$ROOT FAKE_COMPOSE=$FAKE_COMPOSE real_backup $2" \
		>"$2.log" 2>&1 &
	pid=$!
	until grep -q "^hanging" "$FAKE_LOG" 2>/dev/null; do sleep 0.05; done
	kill "-$1" -- "-$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	: >"$FAKE_LOG"
}

check_make_backup() {
	local t=$1 out rc stamp dump
	echo "make backup itself"
	clean_env
	mkdir -p "$t/volumes/covers" "$t/volumes/uploads"
	printf 'a cover' >"$t/volumes/covers/cover-a"
	printf 'an upload' >"$t/volumes/uploads/upload-1"
	export FAKE_VOLUMES="$t/volumes" FAKE_LOG="$t/fake.log"
	: >"$FAKE_LOG"

	mkdir -p "$t/m1"
	rc=0
	out=$(umask 022 && real_backup "$t/m1" 2>&1) || rc=$?
	expect "make backup succeeds" "$rc" "0"
	dump=$(find "$t/m1" -maxdepth 1 -name "$DB-*.dump" -printf '%f\n')
	stamp=${dump#"$DB-"}
	stamp=${stamp%.dump}
	expect "and writes one set under the names the restores read" "$(listing "$t/m1")" "$(setfiles "$stamp" | sorted)"
	expect "stamped in UTC" "$(grep -cE "^$(date -u +%Y%m%d)-[0-9]{6}Z\$" <<<"$stamp")" "1"
	expect "every file of it readable by its owner only, whatever the caller's umask" \
		"$(cd "$t/m1" && stat -c '%a' -- * | sort -u)" "600"
	expect "the archives hold the volumes" \
		"$(tar -xzOf "$t/m1/covers-$stamp.tar.gz" ./cover-a; tar -xzOf "$t/m1/game-uploads-$stamp.tar.gz" ./upload-1)" \
		"a coveran upload"

	mkdir -p "$t/m1b"
	out=$(BACKUP_STAMP=20261102-091500Z real_backup "$t/m1b" 2>&1) || fail "make backup with BACKUP_STAMP succeeds" "$out"
	expect "BACKUP_STAMP names the set" "$(listing "$t/m1b")" "$(setfiles 20261102-091500Z | sorted)"
	printf 'the earlier dump' >"$t/m1b/$DB-20261102-091500Z.dump"
	refused "a set already under that stamp is not overwritten" "already exists" \
		env BACKUP_STAMP=20261102-091500Z /usr/bin/make -s -C "$ROOT" backup COMPOSE="$FAKE_COMPOSE" \
		ENV_FILE="$ROOT/deploy/.env" CORE_DB_PASSWORD=unused BACKUP_DIR="$t/m1b"
	expect "the earlier file is untouched" "$(cat "$t/m1b/$DB-20261102-091500Z.dump")" "the earlier dump"
	expect "and nothing unfinished is left" "$(partials "$t/m1b")" ""
	refused "a BACKUP_STAMP that is not a timestamp is refused" "is not a timestamp" \
		env BACKUP_STAMP='../x' /usr/bin/make -s -C "$ROOT" backup COMPOSE="$FAKE_COMPOSE" \
		ENV_FILE="$ROOT/deploy/.env" CORE_DB_PASSWORD=unused BACKUP_DIR="$t/m1b"

	mkdir -p "$t/m2"
	rc=0
	out=$(FAKE_DUMP=garbage real_backup "$t/m2" 2>&1) || rc=$?
	expect "a dump that is not a pg_dump archive fails make backup" "$((rc != 0))" "1"
	expect_contains "and says so" "$out" "not a pg_dump archive"
	expect "and leaves nothing behind" "$(listing "$t/m2")" ""

	mkdir -p "$t/m3"
	rc=0
	out=$(FAKE_DUMP=fail real_backup "$t/m3" 2>&1) || rc=$?
	expect "a failed pg_dump fails make backup and leaves nothing" "$((rc != 0)):$(listing "$t/m3")" "1:"

	mkdir -p "$t/m4"
	rc=0
	out=$(FAKE_CP=fail-uploads real_backup "$t/m4" 2>&1) || rc=$?
	expect "an unreadable volume fails make backup" "$((rc != 0))" "1"
	expect "keeping the checked dump and covers, which are no set without the uploads" \
		"$(finals "$t/m4" | generic)" "$(printf '%s\n' covers-T.tar.gz "$DB-T.dump" | sorted)"
	expect "and no unfinished file" "$(partials "$t/m4")" ""

	mkdir -p "$t/m5"
	stopped_backup TERM "$t/m5" FAKE_CP=hang-uploads
	expect "a backup stopped (SIGTERM) while copying the uploads leaves nothing in the directory" "$(listing "$t/m5")" ""
	expect "nor in TMPDIR" "$(listing "$t/m5.tmp")" ""
	expect_contains "and says so" "$(cat "$t/m5.log")" "make backup was stopped"

	mkdir -p "$t/m6"
	stopped_backup KILL "$t/m6" FAKE_DUMP=hang
	expect "a backup killed outright during the dump leaves no file named as a set's" "$(finals "$t/m6")" ""
	expect "only its unfinished dump" "$(partials "$t/m6" | generic)" ".$DB-T.dump.P.partial"

	mkdir -p "$t/m7"
	stopped_backup KILL "$t/m7" FAKE_CP=hang-uploads
	expect "a backup killed outright while copying the uploads leaves no file named as a set's" "$(finals "$t/m7")" ""
	expect "only its unfinished files" "$(partials "$t/m7" | generic)" \
		"$(printf '%s\n' ".$DB-T.dump.P.partial" .covers-T.tar.gz.P.partial | sorted)"
	out=$("$B/rotate.sh" "$t/m7" 2>&1) || fail "rotation beside unfinished files succeeds" "$out"
	expect_contains "which rotation does not count" "$out" "kept 0 complete sets"
	expect_contains "names as unfinished" "$out" "left alone, unfinished: .$DB-"
	expect "and leaves in place" "$(partials "$t/m7" | wc -l | tr -d ' ')" "2"
}

calendar_step() { # expression: seconds between its next two elapses
	local a b
	mapfile -t lines < <(systemd-analyze calendar --iterations=2 "$1" |
		sed -nE 's/^ *(Next elapse|Iter\. #2): [A-Za-z]+ ([0-9-]+ [0-9:]+) .*/\2/p')
	a=$(date -d "${lines[0]}" +%s)
	b=$(date -d "${lines[1]}" +%s)
	echo $((b - a))
}
oncalendar() { sed -n 's/^OnCalendar=\(..*\)$/\1/p' "$1" | tail -n1; }

check_schedule() {
	local t=$1 out
	local units="$t/units" etc="$t/etc"
	echo "The schedule"
	clean_env
	id deploy >/dev/null 2>&1 || useradd --system deploy

	out=$("$B/schedule.sh" render "$units" deploy 2>&1) || fail "render succeeds" "$out"
	expect "renders no placeholder" "$(grep -l '@[A-Z]*@' "$units"/* 2>/dev/null)" ""
	expect "the service runs the wrapper of this checkout" \
		"$(sed -n 's/^ExecStart=//p' "$units/dbcontest-backup.service")" "$B/scheduled-backup.sh"
	expect "as the user named" "$(sed -n 's/^User=//p' "$units/dbcontest-backup.service")" "deploy"

	mkdir -p /etc/systemd/system/dbcontest-backup.timer.d
	cp "$units"/dbcontest-backup.service "$units"/dbcontest-backup.timer /etc/systemd/system/
	cp "$ROOT/deploy/systemd/contest-window.conf" /etc/systemd/system/dbcontest-backup.timer.d/
	if out=$(systemd-analyze verify /etc/systemd/system/dbcontest-backup.service \
		/etc/systemd/system/dbcontest-backup.timer 2>&1); then
		expect "systemd-analyze verify accepts the units and the contest drop-in" "$out" ""
	else
		fail "systemd-analyze verify accepts the units and the contest drop-in" "$out"
	fi

	expect "the daily schedule fires once a day" \
		"$(calendar_step "$(oncalendar "$units/dbcontest-backup.timer")")" "86400"
	expect "the contest window fires every 15 minutes" \
		"$(calendar_step "$(oncalendar "$ROOT/deploy/systemd/contest-window.conf")")" "900"
	expect "the contest drop-in replaces the daily schedule rather than adding to it" \
		"$(grep -c '^OnCalendar=$' "$ROOT/deploy/systemd/contest-window.conf")" "1"

	mkdir -p "$etc"
	chown root:root "$ROOT"
	refused "install does not choose root on its own" "install root" \
		env SYSTEMD_DIR="$etc" "$B/schedule.sh" install
	expect "and installs nothing" "$(listing "$etc")" ""
	: >"$STUB_LOG"
	out=$(SYSTEMD_DIR="$etc" "$B/schedule.sh" install deploy 2>&1) || fail "install succeeds" "$out"
	expect_contains "install names the schedule it leaves on" "$out" "daily at 03:30"
	expect "install puts both units in place" \
		"$(listing "$etc")" "$(printf '%s\n' dbcontest-backup.service dbcontest-backup.timer)"
	expect_contains "and enables the timer" "$(cat "$STUB_LOG")" "enable --now dbcontest-backup.timer"

	: >"$STUB_LOG"
	out=$(SYSTEMD_DIR="$etc" "$B/schedule.sh" contest 2>&1) || fail "switching to the contest window succeeds" "$out"
	expect "contest installs the drop-in" \
		"$(cmp -s "$etc/dbcontest-backup.timer.d/contest-window.conf" "$ROOT/deploy/systemd/contest-window.conf" && echo same)" "same"
	expect_contains "reloads systemd" "$(cat "$STUB_LOG")" "daemon-reload"
	expect_contains "and restarts the timer" "$(cat "$STUB_LOG")" "restart dbcontest-backup.timer"
	out=$(SYSTEMD_DIR="$etc" BACKUP_DIR="$t/w" "$B/schedule.sh" status 2>&1) || fail "status succeeds" "$out"
	expect_contains "status names the contest window" "$out" "contest window"
	expect_contains "and the newest complete set" "$out" "newest complete set: "
	out=$(SYSTEMD_DIR="$etc" "$B/schedule.sh" install deploy 2>&1) || fail "a second install succeeds" "$out"
	expect_contains "and a second install says the contest window is still on" "$out" "contest window"

	: >"$STUB_LOG"
	out=$(SYSTEMD_DIR="$etc" "$B/schedule.sh" daily 2>&1) || fail "switching back to daily succeeds" "$out"
	expect "daily removes the drop-in" "$(test -e "$etc/dbcontest-backup.timer.d/contest-window.conf" || echo gone)" "gone"
	expect_contains "and restarts the timer" "$(cat "$STUB_LOG")" "restart dbcontest-backup.timer"
	out=$(SYSTEMD_DIR="$etc" BACKUP_DIR="$t/w" "$B/schedule.sh" status 2>&1) || fail "status succeeds" "$out"
	expect_contains "status names the daily schedule" "$out" "mode: daily"

	out=$("$B/schedule.sh" cron deploy 2>&1) || fail "cron succeeds" "$out"
	local at
	at=$(oncalendar "$units/dbcontest-backup.timer")
	at=${at#'*-*-* '}
	expect "cron gives the daily line at the timer's own time, active" \
		"$(grep -c "^$((10#$(cut -d: -f2 <<<"$at"))) $((10#$(cut -d: -f1 <<<"$at"))) \* \* \* deploy $B/scheduled-backup.sh" <<<"$out")" "1"
	expect "and the contest line, commented out" \
		"$(grep -c "^#\*/15 \* \* \* \* deploy $B/scheduled-backup.sh" <<<"$out")" "1"
}

inside() {
	local t
	t=$(mktemp -d)
	export STUB_LOG="$t/stub.log"
	: >"$STUB_LOG"
	mkdir -p "$t/bin"
	# Stands in for `make backup`: a set named as the real target names it.
	cat >"$t/bin/make" <<-'EOF'
		#!/usr/bin/env bash
		set -euo pipefail
		echo "make $* BACKUP_STAMP=${BACKUP_STAMP:-}" >>"$STUB_LOG"
		dir=
		for a in "$@"; do case $a in BACKUP_DIR=*) dir=${a#BACKUP_DIR=} ;; esac; done
		stamp=${BACKUP_STAMP:?the scheduled run names the set}
		case ${STUB_MAKE:-complete} in
		hang) echo hanging >>"$STUB_LOG"; sleep 60 ;;
		fail) echo "pg_dump: error: connection to server failed" >&2; exit 2 ;;
		dump-only) printf dump >"$dir/dbcontest_core-$stamp.dump" ;;
		complete)
			printf dump >"$dir/dbcontest_core-$stamp.dump"
			printf covers >"$dir/covers-$stamp.tar.gz"
			printf uploads >"$dir/game-uploads-$stamp.tar.gz" ;;
		esac
	EOF
	# Stands in for docker compose under the real `make backup`: pg_dump prints
	# a custom-format header, and the api container's volumes are directories.
	export FAKE_COMPOSE="$t/bin/fake-compose"
	cat >"$FAKE_COMPOSE" <<-'EOF'
		#!/usr/bin/env bash
		set -euo pipefail
		case $1 in
		exec)
			case ${FAKE_DUMP:-ok} in
			ok) printf 'PGDMP a custom-format dump' ;;
			garbage) printf 'not a dump' ;;
			hang) printf 'PGDMP the first half'; echo hanging >>"$FAKE_LOG"; sleep 60 ;;
			fail) echo "pg_dump: error: connection to server failed" >&2; exit 1 ;;
			esac ;;
		cp)
			src=${2#api:}
			name=$(basename "${src%/.}")
			case ${FAKE_CP:-}:$name in
			hang-uploads:uploads) echo hanging >>"$FAKE_LOG"; sleep 60 ;;
			fail-uploads:uploads) echo "no such directory: $src" >&2; exit 1 ;;
			esac
			cp -R "$FAKE_VOLUMES/$name/." "$3" ;;
		esac
	EOF
	chmod +x "$FAKE_COMPOSE"
	# Stands in for systemctl, which has no manager to talk to in here.
	cat >"$t/bin/systemctl" <<-'EOF'
		#!/usr/bin/env bash
		echo "systemctl $*" >>"$STUB_LOG"
	EOF
	chmod +x "$t/bin/make" "$t/bin/systemctl"
	export PATH="$t/bin:$PATH"

	check_rotation "$t"
	check_make_backup "$t"
	check_wrapper "$t"
	check_schedule "$t"

	if [ "$failures" -gt 0 ]; then
		echo "$failures check(s) failed"
		exit 1
	fi
	echo "all checks passed"
}

if [ "${1:-}" = "--inside" ]; then inside; else outside; fi

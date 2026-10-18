#!/usr/bin/env bash
# One scheduled backup: rotation, `make backup`, rotation again, then the
# optional off-host copy of the new set. The systemd timer and the cron line
# both run this; neither runs `make backup` directly.
#
# It exits non-zero when any step fails, and says why on stderr, which
# journald keeps under the unit (journalctl -u dbcontest-backup). With
# BACKUP_TEXTFILE_DIR set it also writes dbcontest_backup.prom there for
# node_exporter or Alloy's textfile collector, success or not.
#
# Settings come from the environment, else deploy/.env:
#   BACKUP_DIR           where the sets go (deploy/backups)
#   BACKUP_KEEP, BACKUP_KEEP_DAILY   rotation, see rotate.sh
#   BACKUP_MIN_FREE_MB   free space to leave on the backup disk (2048)
#   BACKUP_TMPDIR        where make backup stages the volumes (TMPDIR)
#   BACKUP_OFFHOST       rsync or rclone; empty leaves the copy off
#   BACKUP_OFFHOST_TARGET    rsync destination or rclone remote:path
#   BACKUP_OFFHOST_SSH_KEY   key for rsync over ssh (the user's default)
#   BACKUP_RCLONE_CONFIG     rclone.conf to use (rclone's default)
#   BACKUP_TEXTFILE_DIR  where to write the metric; empty writes none
set -euo pipefail

# shellcheck source=deploy/backups/lib.sh
. "$(dirname "$0")/lib.sh"
here="$(cd "$(dirname "$0")" && pwd)"

# The sets hold participants' answers and personal data.
umask 077

dir=$(backup_dir)
db=$(core_db_name)
min_free_mb=$(conf_int BACKUP_MIN_FREE_MB 2048 0 10000000)
offhost=$(conf BACKUP_OFFHOST)
target=$(conf BACKUP_OFFHOST_TARGET)
textfile_dir=$(conf BACKUP_TEXTFILE_DIR)
tmpdir=$(conf BACKUP_TMPDIR)
# Read now, so a bad value stops the run before the dump rather than after it.
conf_int BACKUP_KEEP 48 1 10000 >/dev/null
conf_int BACKUP_KEEP_DAILY 14 0 3650 >/dev/null

# Credentials belong in the tool's own files, an SSH key or rclone.conf. A
# target is on the tool's command line, which every local user can read in
# ps, and in the tool's messages, so one that could carry a password or a key
# is refused rather than redacted, and is never repeated in the refusal.
plain_target() {
	case $target in -* | *[[:space:]]*) return 1 ;; esac
	case $offhost in
	rsync)
		local rest=${target#rsync://}
		if [[ $rest == *@* && ${rest%%@*} == *:* ]]; then return 1; fi
		;;
	rclone)
		[[ $target == /* || $target =~ ^[A-Za-z0-9_.+-]+: ]] || return 1
		;;
	esac
}

case $offhost in
"") ;;
rsync | rclone)
	[ -n "$target" ] || die "BACKUP_OFFHOST=$offhost needs BACKUP_OFFHOST_TARGET"
	plain_target || die "BACKUP_OFFHOST_TARGET is refused: give a plain destination" \
		"(user@host:/path or a path for rsync, a named remote:path or a path for rclone)" \
		"and keep credentials in the SSH key or rclone.conf (BACKUP_OFFHOST_SSH_KEY, BACKUP_RCLONE_CONFIG)"
	command -v "$offhost" >/dev/null || die "BACKUP_OFFHOST=$offhost, but $offhost is not installed"
	;;
*) die "BACKUP_OFFHOST must be rsync, rclone or empty" ;;
esac
if [ -n "$tmpdir" ]; then export TMPDIR="$tmpdir"; fi

mkdir -p "$dir"
dir=$(canonical_dir "$dir")
exec 9>"$dir/.scheduled-backup.lock"
flock -n 9 || die "another scheduled backup is still running in $dir; this run is skipped"

metric_file=${textfile_dir:+$textfile_dir/dbcontest_backup.prom}
previous_metric() { # name: its value in the last textfile written, or 0
	local v=
	if [ -n "$metric_file" ] && [ -f "$metric_file" ]; then v=$(sed -n "s/^$1 //p" "$metric_file"); fi
	printf '%s' "${v:-0}"
}
last_success=$(previous_metric dbcontest_backup_last_success_timestamp_seconds)
offhost_last_success=$(previous_metric dbcontest_backup_offhost_last_success_timestamp_seconds)
backup_ok=0 offhost_ok=0 set_size=0 status=0 stamp=

write_metrics() {
	local tmp
	[ -d "$textfile_dir" ] || {
		error "BACKUP_TEXTFILE_DIR=$textfile_dir is not a directory; no metric written"
		return 1
	}
	tmp=$(mktemp "$textfile_dir/.dbcontest_backup.prom.XXXXXX")
	{
		echo "# HELP dbcontest_backup_last_success_timestamp_seconds When a scheduled run last wrote a complete backup set."
		echo "# TYPE dbcontest_backup_last_success_timestamp_seconds gauge"
		echo "dbcontest_backup_last_success_timestamp_seconds $last_success"
		echo "# HELP dbcontest_backup_last_run_timestamp_seconds When the scheduled backup last ran."
		echo "# TYPE dbcontest_backup_last_run_timestamp_seconds gauge"
		echo "dbcontest_backup_last_run_timestamp_seconds $(date +%s)"
		echo "# HELP dbcontest_backup_last_run_success Whether the last scheduled run wrote a complete set (1) or not (0)."
		echo "# TYPE dbcontest_backup_last_run_success gauge"
		echo "dbcontest_backup_last_run_success $backup_ok"
		echo "# HELP dbcontest_backup_last_set_bytes Size of the set the last successful run wrote."
		echo "# TYPE dbcontest_backup_last_set_bytes gauge"
		echo "dbcontest_backup_last_set_bytes $(if ((backup_ok)); then echo "$set_size"; else previous_metric dbcontest_backup_last_set_bytes; fi)"
		echo "# HELP dbcontest_backup_sets Complete backup sets on this host after the run."
		echo "# TYPE dbcontest_backup_sets gauge"
		echo "dbcontest_backup_sets $(complete_sets "$dir" "$db" | wc -l | tr -d ' ')"
		if [ -n "$offhost" ]; then
			echo "# HELP dbcontest_backup_offhost_last_success_timestamp_seconds When a set was last copied off the host."
			echo "# TYPE dbcontest_backup_offhost_last_success_timestamp_seconds gauge"
			echo "dbcontest_backup_offhost_last_success_timestamp_seconds $offhost_last_success"
			echo "# HELP dbcontest_backup_offhost_last_run_success Whether the last run copied its set off the host (1) or not (0)."
			echo "# TYPE dbcontest_backup_offhost_last_run_success gauge"
			echo "dbcontest_backup_offhost_last_run_success $offhost_ok"
		fi
	} >"$tmp"
	# The collector runs as another user.
	chmod 0644 "$tmp"
	mv -f "$tmp" "$metric_file"
}

# Every way out from here writes the metric and the verdict: an unexpected
# failure under set -e, and systemd's SIGTERM at the unit's time limit, too.
finish() {
	local rc=$?
	if ((rc != 0)); then status=1; fi
	if [ -n "$textfile_dir" ]; then write_metrics || status=1; fi
	if ((status != 0)); then error "scheduled backup FAILED (see the messages above)"; fi
	exit "$status"
}
trap finish EXIT
trap 'error "the run was stopped by a signal"; exit 143' INT TERM HUP

# A lowered BACKUP_KEEP takes effect before the disk is measured, so it is a
# remedy for the refusal below rather than something only a success applies.
"$here/rotate.sh" --quiet "$dir" || {
	error "rotation failed"
	status=1
}

# Docker's volumes and PostgreSQL's data are usually on the same disk as the
# backups, and filling it stops the database in the middle of a contest. A
# missed backup is the lesser loss, so the run is refused while the disk could
# not take one more set and its staging copy and still keep the margin.
room() {
	local previous last_bytes=0 free_kb need_kb
	previous=$(complete_sets "$dir" "$db" | tail -n1)
	if [ -n "$previous" ]; then last_bytes=$(set_bytes "$dir" "$db" "$previous"); fi
	free_kb=$(df -Pk "$dir" | awk 'NR == 2 { print $4 }')
	need_kb=$((min_free_mb * 1024 + 2 * last_bytes / 1024))
	if ((free_kb < need_kb)); then
		error "backup refused: $dir has $(human $((free_kb * 1024))) free and a run needs $(human $((need_kb * 1024)))" \
			"(BACKUP_MIN_FREE_MB=$min_free_mb plus twice the last set)."
		error "Free space, or lower BACKUP_KEEP in deploy/.env: every run rotates down to it before this check." \
			"To free it now: BACKUP_KEEP=<n> $here/rotate.sh $dir (add --dry-run first to see what goes)."
		return 1
	fi
}

# The run names its set and then looks for exactly that name, never for the
# newest one: a set dated later than now (a clock that was ahead, a manual run
# finishing meanwhile) must not pass for this run's.
new_stamp() {
	local s
	for _ in 1 2 3 4 5; do
		s=$(date -u +%Y%m%d-%H%M%SZ)
		if [ -z "$(find -H "$dir" -maxdepth 1 -name "*-$s.*" -print -quit)" ]; then
			printf '%s' "$s"
			return
		fi
		sleep 1
	done
	return 1
}

if ! room; then
	status=1
elif ! stamp=$(new_stamp); then
	error "every stamp for the last five seconds is taken in $dir; is the clock stuck?"
	status=1
elif BACKUP_STAMP=$stamp make -s -C "$ROOT" backup BACKUP_DIR="$dir"; then
	if is_complete "$dir" "$db" "$stamp"; then
		backup_ok=1
		last_success=$(date +%s)
		set_size=$(set_bytes "$dir" "$db" "$stamp")
		echo "backup set $stamp is complete ($(human "$set_size"))"
	else
		error "make backup succeeded but did not leave a complete set $stamp in $dir; is DB_ONLY set in deploy/.env?"
		status=1
	fi
else
	error "make backup failed; see its output above"
	status=1
fi

if ((backup_ok)); then
	"$here/rotate.sh" --protect "$stamp" "$dir" || {
		error "rotation failed"
		status=1
	}
fi

# The target was checked above to carry no credentials; it still stays out of
# the log, and the tool's messages are shown with it replaced.
copy_offhost() {
	local list out line rc=0
	list=$(mktemp)
	set_files . "$db" "$stamp" | sed 's|^\./||' >"$list"
	case $offhost in
	rsync)
		local ssh="ssh -o BatchMode=yes"
		local key
		key=$(conf BACKUP_OFFHOST_SSH_KEY)
		if [ -n "$key" ]; then ssh+=" -i $(printf '%q' "$key")"; fi
		out=$(rsync -a --timeout=300 -e "$ssh" --files-from="$list" -- "$dir/" "$target" 2>&1) || rc=$?
		;;
	rclone)
		local cfg
		cfg=$(conf BACKUP_RCLONE_CONFIG)
		if [ -n "$cfg" ]; then export RCLONE_CONFIG="$cfg"; fi
		out=$(rclone copy -q --contimeout 60s --timeout 300s --retries 3 \
			--files-from-raw "$list" -- "$dir" "$target" 2>&1) || rc=$?
		;;
	esac
	rm -f "$list"
	if ((rc != 0)); then
		error "off-host copy failed ($offhost exited $rc); set $stamp is only on this host:"
		while IFS= read -r line; do error "  $line"; done <<<"${out//"$target"/<BACKUP_OFFHOST_TARGET>}"
		return 1
	fi
}

if ((backup_ok)); then
	if [ -z "$offhost" ]; then
		echo "off-host copy is off (BACKUP_OFFHOST is empty); set $stamp is only on this host"
	elif copy_offhost; then
		offhost_ok=1
		offhost_last_success=$(date +%s)
		echo "copied set $stamp off the host with $offhost"
	else
		status=1
	fi
fi

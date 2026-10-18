#!/usr/bin/env bash
# One scheduled backup: `make backup`, then rotation, then the optional
# off-host copy of the new set. The systemd timer and the cron line both run
# this; neither runs `make backup` directly.
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

case $offhost in
"") ;;
rsync | rclone)
	[ -n "$target" ] || die "BACKUP_OFFHOST=$offhost needs BACKUP_OFFHOST_TARGET"
	command -v "$offhost" >/dev/null || die "BACKUP_OFFHOST=$offhost, but $offhost is not installed"
	;;
*) die "BACKUP_OFFHOST must be rsync, rclone or empty" ;;
esac
if [ -n "$tmpdir" ]; then export TMPDIR="$tmpdir"; fi

mkdir -p "$dir"
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
			"(BACKUP_MIN_FREE_MB=$min_free_mb plus twice the last set). Free space or lower BACKUP_KEEP."
		return 1
	fi
}

backup_ok=0 offhost_ok=0 set_size=0 status=0
started=$(date +%Y%m%d-%H%M%S)

if ! room; then
	status=1
elif make -s -C "$ROOT" backup BACKUP_DIR="$dir"; then
	stamp=$(complete_sets "$dir" "$db" | tail -n1)
	# The timestamp has a resolution of one second, and make backup takes it
	# before the dump, so a set from this run is never older than $started.
	if [ -n "$stamp" ] && [[ ! $stamp < $started ]]; then
		backup_ok=1
		last_success=$(date +%s)
		set_size=$(set_bytes "$dir" "$db" "$stamp")
		echo "backup set $stamp is complete ($(human "$set_size"))"
	else
		error "make backup succeeded but left no complete set newer than $started in $dir; is DB_ONLY set in deploy/.env?"
		status=1
	fi
else
	error "make backup failed; see its output above"
	status=1
fi

if ((backup_ok)); then
	"$(dirname "$0")/rotate.sh" "$dir" || {
		error "rotation failed"
		status=1
	}
fi

# The target never reaches the log: it can carry a user and a host, and with a
# connection-string remote it can carry a key. The tool's own messages are
# shown with it replaced.
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
		out=$(rsync -a --timeout=300 -e "$ssh" --files-from="$list" "$dir/" "$target" 2>&1) || rc=$?
		;;
	rclone)
		local cfg
		cfg=$(conf BACKUP_RCLONE_CONFIG)
		if [ -n "$cfg" ]; then export RCLONE_CONFIG="$cfg"; fi
		out=$(rclone copy -q --contimeout 60s --timeout 300s --retries 3 \
			--files-from-raw "$list" "$dir" "$target" 2>&1) || rc=$?
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

if [ -n "$textfile_dir" ]; then write_metrics || status=1; fi

if ((status != 0)); then
	error "scheduled backup FAILED (see the messages above)"
fi
exit "$status"

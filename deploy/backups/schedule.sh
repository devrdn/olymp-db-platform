#!/usr/bin/env bash
# Installs the backup schedule and switches it between its two settings:
# daily, and the contest window (every 15 minutes) for a beta or an olympiad.
#
# The schedule is one timer, dbcontest-backup.timer, firing daily. The contest
# window is a drop-in beside it that replaces the daily time with every 15
# minutes; switching is putting the drop-in in place or taking it away. With
# one timer there is no arrangement in which both schedules, or neither, are
# on.
#
# Usage: deploy/backups/schedule.sh COMMAND
#   install [USER]   render the units for this checkout, install and start the
#                    timer (root). USER runs the backup: it must be able to run
#                    docker compose. Defaults to the checkout's owner, unless
#                    that is root, which has to be named: install root.
#   contest          every 15 minutes, from now until `daily` (root)
#   daily            back to once a day, at the timer's time (root)
#   status           the setting, the next run, the last run, the newest set
#   uninstall        stop the timer and remove the units (root)
#   render DIR [USER]   write the rendered units into DIR and nothing else
#   cron [USER]      print the /etc/cron.d lines, for a host without systemd
# SYSTEMD_DIR overrides /etc/systemd/system.
set -euo pipefail

# shellcheck source=deploy/backups/lib.sh
. "$(dirname "$0")/lib.sh"

UNITS="$ROOT/deploy/systemd"
SYSTEMD_DIR=${SYSTEMD_DIR:-/etc/systemd/system}
DROPIN_DIR="$SYSTEMD_DIR/dbcontest-backup.timer.d"
DROPIN="$DROPIN_DIR/contest-window.conf"

# The daily time is written once, in the timer; the cron lines and the
# messages read it from there.
daily_time() { sed -n 's/^OnCalendar=\*-\*-\* \([0-9][0-9]:[0-9][0-9]\):00$/\1/p' "$UNITS/dbcontest-backup.timer"; }

usage() { sed -n 's/^# \{0,1\}//; /^Usage:/,/^SYSTEMD_DIR/p' "$0" >&2; exit 2; }

# The path and the user are written into unit files, where a space or a
# specifier character would change what systemd reads.
backup_user() {
	local user=${1:-}
	if [ -z "$user" ]; then
		user=$(stat -c '%U' "$ROOT")
		[ "$user" != root ] || die "the checkout belongs to root: name the user who runs make deploy," \
			"or say 'install root' if the backup really is to run as root"
	fi
	[[ $user =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "user $user is not a plain login name"
	printf '%s' "$user"
}
check_root_path() {
	[[ $ROOT =~ ^/[A-Za-z0-9_./-]+$ ]] || die "the checkout path $ROOT has characters a unit file cannot carry unquoted; move it"
}

render() { # dir user
	check_root_path
	mkdir -p "$1"
	local unit
	for unit in dbcontest-backup.service dbcontest-backup.timer; do
		sed -e "s|@ROOT@|$ROOT|g" -e "s|@USER@|$2|g" "$UNITS/$unit" >"$1/$unit"
		chmod 0644 "$1/$unit"
	done
}

need_write() {
	[ -w "$SYSTEMD_DIR" ] || die "$SYSTEMD_DIR is not writable: run this with sudo"
}

restart_timer() {
	systemctl daemon-reload
	systemctl restart dbcontest-backup.timer
	systemctl list-timers dbcontest-backup.timer --no-pager || true
}

mode() {
	if [ -f "$DROPIN" ]; then
		echo "mode: contest window (every 15 minutes); switch back with: sudo $0 daily"
	else
		echo "mode: daily at $(daily_time); before a contest switch with: sudo $0 contest"
	fi
}

status() {
	local dir db sets newest age
	mode
	systemctl list-timers dbcontest-backup.timer --no-pager || true
	systemctl show dbcontest-backup.service --no-pager \
		-p Result -p ExecMainStatus -p ExecMainExitTimestamp || true
	dir=$(backup_dir)
	db=$(core_db_name)
	if ! [ -r "$dir" ] || ! [ -x "$dir" ]; then
		echo "cannot read $dir: run status as the backup's user"
		return
	fi
	dir=$(canonical_dir "$dir")
	mapfile -t sets < <(complete_sets "$dir" "$db")
	if ((${#sets[@]} == 0)); then
		echo "newest complete set: none in $dir"
		return
	fi
	newest=${sets[-1]}
	age=$(($(date +%s) - $(stamp_epoch "$newest")))
	echo "newest complete set: $newest, $((age / 3600))h $((age % 3600 / 60))m old; ${#sets[@]} complete sets in $dir"
}

cron() { # user
	local at
	check_root_path
	at=$(daily_time)
	cat <<-EOF
		# /etc/cron.d/dbcontest-backup: DB Contest backups, for a host without systemd.
		# Keep exactly one schedule line uncommented. Before a beta or an olympiad,
		# swap the comment onto the daily line; swap it back after the contest.
		# The output goes to syslog under dbcontest-backup.
		PATH=/usr/local/bin:/usr/bin:/bin
		# Daily, outside the contest window:
		$((10#${at#*:})) $((10#${at%:*})) * * * $1 $ROOT/deploy/backups/scheduled-backup.sh 2>&1 | logger -t dbcontest-backup
		# The contest window, every 15 minutes:
		#*/15 * * * * $1 $ROOT/deploy/backups/scheduled-backup.sh 2>&1 | logger -t dbcontest-backup
	EOF
}

case ${1:-} in
install)
	need_write
	user=$(backup_user "${2:-}")
	id -u "$user" >/dev/null 2>&1 || die "no user $user on this host"
	if [ "$user" != root ] && ! id -nG "$user" | tr ' ' '\n' | grep -qx docker; then
		warn "warning: $user is not in the docker group; the backup will fail unless it can reach Docker another way"
	fi
	render "$SYSTEMD_DIR" "$user"
	systemctl daemon-reload
	systemctl enable --now dbcontest-backup.timer
	echo "installed: the backup runs as $user"
	mode
	;;
contest)
	need_write
	mkdir -p "$DROPIN_DIR"
	install -m 0644 "$UNITS/contest-window.conf" "$DROPIN"
	restart_timer
	echo "contest window on: a backup every 15 minutes until: sudo $0 daily"
	;;
daily)
	need_write
	rm -f "$DROPIN"
	rmdir "$DROPIN_DIR" 2>/dev/null || true
	restart_timer
	echo "daily schedule on: a backup at $(daily_time)"
	;;
status) status ;;
uninstall)
	need_write
	systemctl disable --now dbcontest-backup.timer || true
	rm -f "$DROPIN" "$SYSTEMD_DIR/dbcontest-backup.timer" "$SYSTEMD_DIR/dbcontest-backup.service"
	rmdir "$DROPIN_DIR" 2>/dev/null || true
	systemctl daemon-reload
	echo "the backup schedule is removed; the sets in $(backup_dir) are untouched"
	;;
render)
	[ -n "${2:-}" ] || usage
	render "$2" "$(backup_user "${3:-}")"
	;;
cron) cron "$(backup_user "${2:-}")" ;;
*) usage ;;
esac

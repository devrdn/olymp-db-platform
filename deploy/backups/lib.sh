# shellcheck shell=bash
# What a backup set is, and where the scheduled backup reads its settings.
# Sourced by the scripts beside it; it sets ROOT and defines functions only.
#
# A set is what one `make backup` writes under one timestamp:
#   <CORE_DB_NAME>-<stamp>.dump
#   covers-<stamp>.tar.gz
#   game-uploads-<stamp>.tar.gz
# The stamp is YYYYmmdd-HHMMSSZ in UTC; sets taken before that change carry
# local time and no Z, and are sets all the same. A set is complete when all
# three are regular files. Rotation removes only complete sets, and then
# exactly those three files: never an incomplete set, a stopped run's
# .<name>.<pid>.partial files, another database's dump, or any other name.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="$ROOT/deploy/.env"
STAMP_RE='[0-9]{8}-[0-9]{6}Z?'

# Under systemd, stderr goes to the journal and a <N> prefix sets the line's
# priority, so `journalctl -p err` shows the reasons a run failed.
error() { printf '%s%s\n' "${JOURNAL_STREAM:+<3>}" "$*" >&2; }
warn() { printf '%s%s\n' "${JOURNAL_STREAM:+<4>}" "$*" >&2; }
die() {
	error "$@"
	exit 1
}

# conf KEY DEFAULT: the environment's value, else deploy/.env's, else DEFAULT.
# An empty value counts as unset. deploy/.env is read line by line rather than
# sourced: it is a Compose and Make file, and a value such as
# "ADMIN_NAME=System Administrator" is a command to a shell that sources it.
conf() {
	local key=$1 default=${2-} line value
	if [ -n "${!key:-}" ]; then
		printf '%s' "${!key}"
		return
	fi
	line=$(grep -E "^[[:space:]]*(export[[:space:]]+)?${key}=" "$ENV_FILE" 2>/dev/null | tail -n1) || true
	value=${line#*=}
	case $value in
	\"*\") value=${value#\"} value=${value%\"} ;;
	\'*\') value=${value#\'} value=${value%\'} ;;
	*) value=${value%%[[:space:]]#*} ;;
	esac
	value=${value%"${value##*[![:space:]]}"}
	printf '%s' "${value:-$default}"
}

# conf_int KEY DEFAULT MIN MAX: conf, refused unless a whole number in range.
conf_int() {
	local value
	value=$(conf "$1" "$2")
	if ! [[ $value =~ ^[0-9]{1,9}$ ]] || ((10#$value < $3 || 10#$value > $4)); then
		die "$1=$value is not a whole number from $3 to $4"
	fi
	printf '%d' "$((10#$value))"
}

backup_dir() {
	local dir
	dir=$(conf BACKUP_DIR deploy/backups)
	case $dir in /*) ;; *) dir="$ROOT/$dir" ;; esac
	printf '%s' "$dir"
}

# The directory with every symlink resolved, so that find, df and the
# printed commands all name the disk the sets are really on.
canonical_dir() { (cd -P -- "$1" && pwd -P); }

valid_stamp() { [[ $1 =~ ^${STAMP_RE}$ ]]; }

stamp_epoch() { # stamp: seconds since the epoch; a stamp without Z is local time
	local s=$1 tz=
	if [[ $s == *Z ]]; then tz=-u; fi
	date $tz -d "${s:0:4}-${s:4:2}-${s:6:2} ${s:9:2}:${s:11:2}:${s:13:2}" +%s
}

# The name is spliced into a find pattern and a sed expression, so it is held
# to what a PostgreSQL identifier can be without quoting.
core_db_name() {
	local name
	name=$(conf CORE_DB_NAME dbcontest_core)
	[[ $name =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || die "CORE_DB_NAME=$name is not a plain identifier"
	printf '%s' "$name"
}

set_files() { # dir db stamp
	printf '%s\n' "$1/$2-$3.dump" "$1/covers-$3.tar.gz" "$1/game-uploads-$3.tar.gz"
}

is_complete() { # dir db stamp
	local f
	while IFS= read -r f; do
		[ -f "$f" ] && [ ! -L "$f" ] || return 1
	done < <(set_files "$1" "$2" "$3")
}

# Every timestamp some file of a set is named with, oldest first.
set_stamps() { # dir db
	find -H "$1" -maxdepth 1 -type f \( -name "$2-*.dump" -o -name 'covers-*.tar.gz' -o -name 'game-uploads-*.tar.gz' \) \
		-printf '%f\n' |
		sed -nE -e "s/^$2-(${STAMP_RE})\.dump\$/\1/p" \
			-e "s/^(covers|game-uploads)-(${STAMP_RE})\.tar\.gz\$/\2/p" |
		LC_ALL=C sort -u
}

complete_sets() { # dir db: oldest first
	local s
	while IFS= read -r s; do
		if is_complete "$1" "$2" "$s"; then printf '%s\n' "$s"; fi
	done < <(set_stamps "$1" "$2")
}

incomplete_sets() { # dir db: oldest first
	local s
	while IFS= read -r s; do
		if ! is_complete "$1" "$2" "$s"; then printf '%s\n' "$s"; fi
	done < <(set_stamps "$1" "$2")
}

# The .partial files a stopped make backup left behind.
unfinished_files() { # dir db
	find -H "$1" -maxdepth 1 -type f -name '.*.partial' -printf '%f\n' |
		grep -E "^\.($2-${STAMP_RE}\.dump|(covers|game-uploads)-${STAMP_RE}\.tar\.gz)(\.[0-9]+)?\.partial\$" |
		LC_ALL=C sort || true
}

set_bytes() { # dir db stamp
	local -a files
	mapfile -t files < <(set_files "$1" "$2" "$3")
	stat -c '%s' -- "${files[@]}" | awk '{ n += $1 } END { print n + 0 }'
}

human() { numfmt --to=iec-i --suffix=B "$1"; }

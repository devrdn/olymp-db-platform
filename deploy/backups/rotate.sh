#!/usr/bin/env bash
# Removes old backup sets: keeps the newest BACKUP_KEEP complete sets, and
# beyond them the newest set of each of the BACKUP_KEEP_DAILY most recent days
# not already covered. Everything else in the directory is left alone: an
# incomplete set, a file of any other name, a set of another database.
#
# The two tiers bound the directory by count, whatever the schedule: at most
# BACKUP_KEEP + BACKUP_KEEP_DAILY sets. Days are counted among the sets that
# exist rather than back from today, so backups that stop for a week do not
# age the history away while nobody is taking new ones.
#
# Usage: deploy/backups/rotate.sh [--dry-run] [DIR]
#   DIR defaults to BACKUP_DIR. Settings come from the environment, else
#   deploy/.env: BACKUP_KEEP (48), BACKUP_KEEP_DAILY (14), CORE_DB_NAME.
set -euo pipefail

# shellcheck source=deploy/backups/lib.sh
. "$(dirname "$0")/lib.sh"

dry=
if [ "${1:-}" = "--dry-run" ]; then
	dry=1
	shift
fi
dir=${1:-$(backup_dir)}
db=$(core_db_name)
# The newest set is never removed, so keeping none is refused rather than read
# as "remove everything".
keep=$(conf_int BACKUP_KEEP 48 1 10000)
keep_daily=$(conf_int BACKUP_KEEP_DAILY 14 0 3650)
[ -d "$dir" ] || die "no backup directory at $dir"

mapfile -t sets < <(complete_sets "$dir" "$db")
count=${#sets[@]}

declare -A day_kept=()
kept=() remove=()
days=0
for ((i = count - 1; i >= 0; i--)); do
	stamp=${sets[i]}
	day=${stamp%%-*}
	if ((count - i <= keep)); then
		kept+=("$stamp")
		day_kept[$day]=1
	elif [ -z "${day_kept[$day]:-}" ] && ((days < keep_daily)); then
		kept+=("$stamp")
		day_kept[$day]=1
		days=$((days + 1))
	else
		remove+=("$stamp")
	fi
done

for stamp in "${remove[@]}"; do
	[ "$stamp" != "${sets[count - 1]}" ] || die "refusing to remove the newest set, $stamp"
	size=$(human "$(set_bytes "$dir" "$db" "$stamp")")
	if [ -n "$dry" ]; then
		echo "would remove set $stamp ($size)"
	else
		mapfile -t files < <(set_files "$dir" "$db" "$stamp")
		rm -f -- "${files[@]}"
		echo "removed set $stamp ($size)"
	fi
done

while IFS= read -r stamp; do
	echo "left alone, incomplete: $stamp (only a complete set is ever removed; delete it by hand once you know why it is partial)"
done < <(incomplete_sets "$dir" "$db")

total=0
for stamp in "${kept[@]}"; do total=$((total + $(set_bytes "$dir" "$db" "$stamp"))); done
if ((${#kept[@]} > 0)); then
	newest=$(set_bytes "$dir" "$db" "${kept[0]}")
	ceiling=$(((keep + keep_daily + 1) * newest))
	echo "kept ${#kept[@]} complete sets ($(human "$total")); the newest, ${kept[0]}, is $(human "$newest")," \
		"so $keep + $keep_daily sets and one being written need up to $(human "$ceiling")"
else
	echo "kept 0 complete sets: $dir holds none"
fi

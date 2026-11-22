#!/usr/bin/env bash
# Proves what the host targets take from deploy/.env, with the real Makefile and
# a throwaway env file:
#
#   - `make run` and `make runner` hand their processes the settings the file
#     sets, through the environment and never on the echoed command line, while
#     the container-only values (REDIS_ADDR, QUERY_RUNNER_ADDR, GAME_UPLOAD_DIR,
#     ENV) are the host's;
#   - no target that reads the file shows a password in `make -n`, and those
#     that hand a DSN or a password to a process still hand it the same one;
#   - a value on the make command line beats the file, and the shell does not;
#   - an empty API_PORT still gives the API and the interface a port;
#   - `make migrate-force` takes MIGRATION only from its own command line and
#     only as a version number, and hands it to the program as a value;
#   - `migrate-force`, `restore`, `restore-covers` and `restore-game-uploads`
#     take CONFIRM, and the restores FILE, only from the command line, so a
#     value the shell exported answers none of them;
#   - the targets that need the file refuse an empty CORE_DB_PASSWORD, and
#     `dev-down`, `dev-logs` and `stack-down` accept a file other accounts
#     can read and hand Compose a CORE_DB_PASSWORD given on the command line,
#     the way past Compose's own refusal of an empty one;
#   - `make front-install` installs once per lockfile, and refuses rather than
#     install when it cannot hash the lockfile;
#   - a value make cannot expand, an open `$(` or `${` such as a provider may
#     put in a secret, changes nothing in `make backup`, the deploys, the
#     restores, `make game-check` or teardown: the scheduled backup runs on
#     servers that never run `make run`.
#
# `go` and `docker` are stubs on PATH that record their environment and
# arguments, so nothing is compiled, started or reached; the other targets are
# dry runs. deploy/.env is never read.
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
# `go run ./cmd/api` records as api, `go test` as test.
cat > "$WORK/bin/go" <<'STUB'
#!/bin/sh
case "$1" in run) name="$(basename "$2")" ;; *) name="$1" ;; esac
env > "$HOST_CHECK_DUMP/$name.env"
echo "$*" > "$HOST_CHECK_DUMP/$name.args"
STUB
cat > "$WORK/bin/docker" <<'STUB'
#!/bin/sh
echo "$*" >> "$HOST_CHECK_DUMP/docker.calls"
env > "$HOST_CHECK_DUMP/docker.env"
STUB
chmod 700 "$WORK/bin/go" "$WORK/bin/docker"

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

# Every secret is distinct, so finding one names it.
SECRETS=(check-github-secret check-author-password check-device-secret check-runner-token
	check-core-password check-game-password check-redis-password
	check-reader-password check-writer-password)
NORMAL=(
	API_PORT=8085
	QUERY_RUNNER_ADDR=queryrunner:9300
	GITHUB_CLIENT_SECRET=check-github-secret
	GAME_AUTHOR_PASSWORD=check-author-password
	DEVICE_COOKIE_SECRET=check-device-secret
	QUERY_RUNNER_TOKEN=check-runner-token
	CORE_DB_PASSWORD=check-core-password
	GAME_DB_PASSWORD=check-game-password
	REDIS_PASSWORD=check-redis-password
	GAME_READER_PASSWORD=check-reader-password
	GAME_WRITER_PASSWORD=check-writer-password
	SMTP_PORT=2525
	QUERY_PER_MINUTE=77
)
normal="$(fixture normal "${NORMAL[@]}")"
CORE_DSN="postgres://dbcontest:check-core-password@localhost:5432/dbcontest_core?sslmode=disable"
CORE_TEST_DSN="postgres://dbcontest:check-core-password@localhost:5432/dbcontest_core_test?sslmode=disable"
GAME_DSN="postgres://dbcontest_game:check-game-password@localhost:5433/dbcontest_game?sslmode=disable"
GAME_TEST_DSN="postgres://dbcontest_game:check-game-password@localhost:5434/dbcontest_game_test?sslmode=disable"
READER_DSN="postgres://game_reader:check-reader-password@localhost:5433/postgres?sslmode=disable"
WRITER_DSN="postgres://game_writer:check-writer-password@localhost:5433/postgres?sslmode=disable"

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
has "$api" CORE_DB_DSN "$CORE_DSN"
has "$api" REDIS_ADDR "redis://:check-redis-password@localhost:6379/0"
has "$api" GAME_PROVISIONER_DSN "$GAME_DSN"
has "$api" GAME_UPLOAD_DIR "$ROOT/deploy/game-uploads"
has "$api" ENV development
lacks "$api" SMTP_HOST
has "$runner" QUERY_RUNNER_TOKEN check-runner-token
has "$runner" QUERY_PER_MINUTE 77
has "$runner" QUERY_RUNNER_ADDR 127.0.0.1:9300
has "$runner" GAME_DB_DSN "$READER_DSN"
has "$runner" GAME_DB_WRITER_DSN "$WRITER_DSN"
lacks "$runner" GITHUB_CLIENT_SECRET
for s in "${SECRETS[@]}"; do
	if grep -qF "$s" "$WORK/run.out" "$WORK/runner.out"; then fail "$s is on an echoed command line"
	else pass "$s is on no echoed command line"; fi
done

echo "== no password in a dry run"
# Every target that reads the file, taken from the Makefile rather than listed
# here, so a target added later is checked too.
targets="$(sed -nE 's/^([a-z][a-z0-9-]*):.*require-env.*/\1/p' "$ROOT/Makefile")"
[ -n "$targets" ] || fail "no target depends on require-env"
for t in $targets; do
	# A dry run that stops early prints nothing to find, so it fails.
	if ! out="$(mk "$normal" -n "$t" MIGRATION=4242 CONFIRM=yes FILE=x 2>&1)"; then
		fail "make -n $t exited non-zero: $(echo "$out" | tail -1)"
		continue
	fi
	leaked=
	for s in "${SECRETS[@]}"; do case "$out" in *"$s"*) leaked="$leaked $s" ;; esac; done
	if [ -n "$leaked" ]; then fail "make -n $t shows$leaked"; else pass "make -n $t shows no password"; fi
done

echo "== the processes still get their DSNs"
# Real runs with go and docker stubbed: each target hands its process the
# value it no longer writes on the command line.
started() { # the name go records, then make arguments
	local name="$1"
	shift
	rm -f "$WORK/$name.env"
	mk "$normal" "$@" > "$WORK/started.out" 2>&1 || { cat "$WORK/started.out"; fail "make $* exited non-zero"; }
	[ -f "$WORK/$name.env" ] || { fail "make $* did not start $name"; : > "$WORK/$name.env"; }
	for s in "${SECRETS[@]}"; do
		if grep -qF "$s" "$WORK/started.out"; then fail "make $* echoes $s"; fi
	done
}
for t in migrate-up migrate-down migrate-version; do
	started migrate "$t"
	has "$WORK/migrate.env" CORE_DB_DSN "$CORE_DSN"
done
started bootstrap bootstrap
has "$WORK/bootstrap.env" CORE_DB_DSN "$CORE_DSN"
started gameorphans game-orphans
has "$WORK/gameorphans.env" CORE_DB_DSN "$CORE_DSN"
has "$WORK/gameorphans.env" GAME_DB_ADMIN_DSN "$GAME_DSN"
started consoleload loadtest
has "$WORK/consoleload.env" CORE_DB_DSN "$CORE_DSN"
has "$WORK/consoleload.env" GAME_PROVISIONER_DSN "$GAME_DSN"
started gamedb game-roles
has "$WORK/gamedb.env" GAME_DB_ADMIN_DSN "$GAME_DSN"
has "$WORK/gamedb.env" GAME_READER_PASSWORD check-reader-password
has "$WORK/gamedb.env" GAME_WRITER_PASSWORD check-writer-password
has "$WORK/gamedb.env" GAME_AUTHOR_PASSWORD check-author-password
started test test-db
has "$WORK/test.env" CORE_DB_DSN "$CORE_TEST_DSN"
has "$WORK/testdb.env" CORE_DB_DSN "$CORE_TEST_DSN"
has "$WORK/migrate.env" CORE_DB_DSN "$CORE_TEST_DSN"
lacks "$WORK/test.env" GAME_DB_DSN
started test test-game
has "$WORK/test.env" GAME_DB_DSN "$GAME_TEST_DSN"
has "$WORK/test.env" GAME_READER_PASSWORD check-reader-password
has "$WORK/test.env" GAME_WRITER_PASSWORD check-writer-password
has "$WORK/test.env" GAME_AUTHOR_PASSWORD check-author-password
started test test-game-build
has "$WORK/test.env" CORE_DB_DSN "$CORE_TEST_DSN"
has "$WORK/test.env" GAME_DB_DSN "$GAME_TEST_DSN"
has "$WORK/test.env" GAME_READER_PASSWORD check-reader-password
# A command-line value of the name the program reads does not redirect the
# tests or the runner: make derives theirs, as it did on the command line.
started test test-db CORE_DB_DSN=postgres://elsewhere/dbcontest_core
has "$WORK/test.env" CORE_DB_DSN "$CORE_TEST_DSN"
started test test-game GAME_DB_DSN=postgres://elsewhere/dbcontest_game
has "$WORK/test.env" GAME_DB_DSN "$GAME_TEST_DSN"
started queryrunner runner GAME_DB_DSN=postgres://elsewhere/dbcontest_game
has "$WORK/queryrunner.env" GAME_DB_DSN "$READER_DSN"

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

echo "== migrate-force"
forced() { # the environment's MIGRATION or "", then make arguments
	local inherited="$1"
	shift
	rm -f "$WORK/migrate.args"
	env -i PATH="$WORK/bin:/usr/bin:/bin" HOST_CHECK_DUMP="$WORK" ${inherited:+MIGRATION="$inherited"} \
		"$MAKE_BIN" --no-print-directory -C "$ROOT" ENV_FILE="$normal" migrate-force CONFIRM=yes "$@" \
		> "$WORK/force.out" 2>&1
}
refused() { # what, then forced's arguments
	local what="$1"
	shift
	if forced "$@"; then fail "migrate-force refuses $what, exited 0"
	elif [ -f "$WORK/migrate.args" ]; then fail "migrate-force refuses $what, ran $(cat "$WORK/migrate.args")"
	else pass "migrate-force refuses $what: $(grep -v '^make' "$WORK/force.out" | head -1)"; fi
}
refused "no MIGRATION" ""
refused "MIGRATION=0" "" MIGRATION=0
refused "MIGRATION=007" "" MIGRATION=007
refused "MIGRATION=7x" "" MIGRATION=7x
refused "a MIGRATION carrying a command" "" "MIGRATION=7\"; touch $WORK/ran; echo \""
if [ -e "$WORK/ran" ]; then fail "a MIGRATION carrying a command ran it"; fi
refused "a MIGRATION from the environment" 7
if forced "" MIGRATION=4242 && [ "$(cat "$WORK/migrate.args" 2>/dev/null)" = "run ./cmd/migrate force 4242" ]; then
	pass "migrate-force MIGRATION=4242 runs migrate force 4242"
else
	fail "migrate-force MIGRATION=4242 runs migrate force 4242, ran: $(cat "$WORK/migrate.args" 2>/dev/null || tail -1 "$WORK/force.out")"
fi
has "$WORK/migrate.env" CORE_DB_DSN "$CORE_DSN"
if forced "" MIGRATION=4242 -n && ! grep -qF 4242 "$WORK/force.out"; then
	pass "make -n migrate-force writes no MIGRATION into the shell text"
else
	fail "make -n migrate-force writes no MIGRATION into the shell text: $(grep -F 4242 "$WORK/force.out" | head -1)"
fi

echo "== CONFIRM and FILE come from the command line"
# Real runs with docker stubbed: a refusal must stop before docker, and the
# same answers typed on the command line must get past the questions to it.
mkdir "$WORK/archive"
echo cover > "$WORK/archive/a"
tar -czf "$WORK/archive.tar.gz" -C "$WORK/archive" .
echo PGDMP > "$WORK/core.dump"
asked() { # VAR=value ... for the environment, --, then make arguments
	local inherited=()
	while [ "$1" != -- ]; do inherited+=("$1"); shift; done
	shift
	rm -f "$WORK/docker.calls" "$WORK/migrate.args"
	# The empty-array form, which bash 3.2 under `set -u` requires.
	env -i PATH="$WORK/bin:/usr/bin:/bin" HOST_CHECK_DUMP="$WORK" ${inherited[@]+"${inherited[@]}"} \
		"$MAKE_BIN" --no-print-directory -C "$ROOT" ENV_FILE="$normal" "$@" > "$WORK/asked.out" 2>&1
}
not_answered() { # what, then asked's arguments
	local what="$1"
	shift
	if asked "$@"; then fail "$what is refused, exited 0"
	elif [ -e "$WORK/docker.calls" ] || [ -e "$WORK/migrate.args" ]; then fail "$what is refused, but ran docker or go"
	else pass "$what is refused: $(grep -v '^make' "$WORK/asked.out" | tail -1)"; fi
}
for target in restore restore-covers restore-game-uploads; do
	if [ "$target" = restore ]; then file="$WORK/core.dump"; else file="$WORK/archive.tar.gz"; fi
	not_answered "$target with CONFIRM=yes from the shell" CONFIRM=yes -- "$target" FILE="$file"
	not_answered "$target with FILE from the shell" FILE="$file" -- "$target" CONFIRM=yes
	asked -- "$target" FILE="$file" CONFIRM=yes || true
	if [ -s "$WORK/docker.calls" ]; then pass "$target with both on the command line reaches docker"
	else fail "$target with both on the command line reaches docker: $(grep -v '^make' "$WORK/asked.out" | tail -1)"; fi
done
not_answered "migrate-force with CONFIRM=yes from the shell" CONFIRM=yes -- migrate-force MIGRATION=7

echo "== the env file's checks, and teardown"
# A file other accounts can read, as a plain `cp` leaves it: stopping the stack
# must still work, and starting it must not.
readable="$(fixture readable "${NORMAL[@]}")"
chmod 644 "$readable"
# A private file with no CORE_DB_PASSWORD. make lets teardown through, but
# Compose refuses an empty one itself, so what is proven here is the way past
# it: a value on the command line reaches Compose.
nopass="$(fixture nopass "${NORMAL[@]}" CORE_DB_PASSWORD=)"
refuses() { # what the output must say, the env file, then make arguments
	local says="$1" file="$2"
	shift 2
	rm -f "$WORK/docker.calls" "$WORK"/*.args
	if mk "$file" "$@" > "$WORK/refuses.out" 2>&1; then
		fail "make $* with $(basename "$file") is refused, exited 0"
	elif [ -e "$WORK/docker.calls" ] || ls "$WORK"/*.args > /dev/null 2>&1; then
		fail "make $* with $(basename "$file") is refused, but ran docker or go"
	elif ! grep -qF "$says" "$WORK/refuses.out"; then
		fail "make $* with $(basename "$file") is refused saying \"$says\", says: $(head -1 "$WORK/refuses.out")"
	else
		pass "make $* with $(basename "$file") is refused: $(grep -F "$says" "$WORK/refuses.out" | head -1)"
	fi
}
for goal in dev-down dev-logs stack-down; do
	rm -f "$WORK/docker.calls"
	if mk "$readable" "$goal" > "$WORK/teardown.out" 2>&1 && [ -s "$WORK/docker.calls" ]; then
		pass "$goal runs with a file other accounts can read"
	else
		fail "$goal runs with a file other accounts can read: $(grep -v '^make' "$WORK/teardown.out" | head -1)"
	fi
	refuses "absent.env is missing" "$WORK/absent.env" "$goal"
	rm -f "$WORK/docker.env"
	mk "$nopass" "$goal" CORE_DB_PASSWORD=unused > "$WORK/teardown.out" 2>&1 ||
		fail "$goal CORE_DB_PASSWORD=unused exited non-zero: $(head -1 "$WORK/teardown.out")"
	[ -f "$WORK/docker.env" ] || : > "$WORK/docker.env"
	has "$WORK/docker.env" CORE_DB_PASSWORD unused
done
refuses "readable by other accounts" "$readable" dev-up
refuses "CORE_DB_PASSWORD is not set" "$nopass" dev-up
refuses "CORE_DB_PASSWORD is not set" "$nopass" migrate-up
# Into a directory of its own, so a guard that let it through writes nothing
# into the checkout.
refuses "CORE_DB_PASSWORD is not set" "$nopass" backup BACKUP_DIR="$WORK/backups"

echo "== front-install"
# Against a lockfile of its own, never frontend/: the target removes a
# symlinked node_modules, which in a worktree may be another checkout's.
mkdir "$WORK/front"
echo '{"lockfileVersion": 3}' > "$WORK/front/package-lock.json"
cat > "$WORK/bin/npm" <<'STUB'
#!/bin/sh
echo "$*" >> "$HOST_CHECK_DUMP/npm.calls"
mkdir -p node_modules
STUB
chmod 700 "$WORK/bin/npm"
npm_calls() { if [ -f "$WORK/npm.calls" ]; then wc -l < "$WORK/npm.calls" | tr -d ' '; else echo 0; fi; }
front() { # PATH, then make arguments
	local path="$1"
	shift
	env -i PATH="$path" HOST_CHECK_DUMP="$WORK" \
		"$MAKE_BIN" --no-print-directory -C "$ROOT" ENV_FILE="$normal" FRONTEND="$WORK/front" "$@"
}
front "$WORK/bin:/usr/bin:/bin:/usr/sbin:/sbin" front-install > "$WORK/front1.out" 2>&1 ||
	{ cat "$WORK/front1.out"; fail "front-install exited non-zero"; }
front "$WORK/bin:/usr/bin:/bin:/usr/sbin:/sbin" front-install > "$WORK/front2.out" 2>&1 ||
	{ cat "$WORK/front2.out"; fail "front-install exited non-zero"; }
if [ "$(npm_calls)" = 1 ]; then pass "front-install installs once for one lockfile"
else fail "front-install installs once for one lockfile, ran npm $(npm_calls) times"; fi
# With neither hashing tool, or with no lockfile, the stamp could not tell one
# lockfile from another.
mkdir "$WORK/nohash"
for tool in cat cut rm; do ln -s "$(command -v "$tool")" "$WORK/nohash/$tool"; done
ln -s "$WORK/bin/npm" "$WORK/nohash/npm"
mkdir "$WORK/nolock"
unhashable() { # what, the PATH, then make arguments
	local what="$1"
	shift
	rm -rf "$WORK/front/node_modules" "$WORK/nolock/node_modules" "$WORK/npm.calls"
	if front "$@" > "$WORK/front3.out" 2>&1; then
		fail "front-install refuses $what, exited 0"
	elif [ "$(npm_calls)" != 0 ]; then
		fail "front-install refuses $what, ran npm first"
	elif ! grep -qF "cannot hash package-lock.json" "$WORK/front3.out"; then
		fail "front-install refuses $what, saying: $(grep -v '^make' "$WORK/front3.out" | head -1)"
	else
		pass "front-install refuses $what: $(grep -v '^make' "$WORK/front3.out" | head -1)"
	fi
}
unhashable "without sha256sum or shasum" "$WORK/nohash" front-install
unhashable "without a lockfile" "$WORK/bin:/usr/bin:/bin:/usr/sbin:/sbin" FRONTEND="$WORK/nolock" front-install

echo "== a value make cannot expand"
# The database passwords are among them, so the DSN exports and require-env's
# password check are shown to stay as lazy as the settings' are.
hostile="$(fixture hostile "${NORMAL[@]}" "GITHUB_CLIENT_SECRET='ab\$(cd'" "SMTP_PASSWORD='x\${y'" \
	"CORE_DB_PASSWORD='c\$(cd'" "GAME_DB_PASSWORD='g\$(cd'" "REDIS_PASSWORD='p\$(cd'" \
	"GAME_READER_PASSWORD='r\${y'" "GAME_WRITER_PASSWORD='w\$(cd'")"
for goal in backup "deploy VERSION=v1.2.3" "deploy-api VERSION=v1.2.3" "deploy-web VERSION=v1.2.3" \
	deployed game-check "restore-check FILE=x" "restore FILE=x CONFIRM=yes" \
	"restore-covers FILE=x CONFIRM=yes" "restore-game-uploads FILE=x CONFIRM=yes" stack-down dev-down; do
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

echo "== deploy-web and deploy-api"
# Every image is tagged ${VERSION}, so `up web` without --no-deps would
# also bring api, which web waits for, and its migrations to the new tag, which
# an interface-only deploy must not do.
up="$(mk "$normal" -n deploy-web VERSION=v1.2.3 2>&1 | grep -F ' up ')" || up=""
if grep -qE ' up -d --no-deps web$' <<<"$up"; then pass "deploy-web recreates only the interface: $up"
else fail "deploy-web does not pass --no-deps to up: ${up:-no up command}"; fi
# api's dependency on migrate is what runs the migrations before the new API.
up="$(mk "$normal" -n deploy-api VERSION=v1.2.3 2>&1 | grep -F ' up ')" || up=""
if grep -qE ' up -d api$' <<<"$up" && ! grep -q -- '--no-deps' <<<"$up"; then
	pass "deploy-api keeps api's dependencies, so migrate runs first: $up"
else fail "deploy-api must start api with its dependencies: ${up:-no up command}"; fi

echo "== images-push"
# The workstation builds for its own architecture only, so a tag CI writes
# (a version, sha-<commit>, latest) pushed from here would point at one
# platform.
for tag in v1.2.3 v1.2.3-rc.1 sha-0123abc latest; do
	: > "$WORK/docker.calls"
	if mk "$normal" images-push VERSION="$tag" > "$WORK/push.out" 2>&1; then
		fail "images-push VERSION=$tag pushed a tag CI writes from a workstation"
	elif [ -s "$WORK/docker.calls" ]; then
		fail "images-push refuses $tag, but ran docker first: $(head -1 "$WORK/docker.calls")"
	elif ! grep -qF "CI" "$WORK/push.out"; then
		fail "images-push refuses $tag without pointing at CI: $(tail -1 "$WORK/push.out")"
	else
		pass "images-push refuses $tag before building: $(grep -F CI "$WORK/push.out" | head -1)"
	fi
done
: > "$WORK/docker.calls"
if mk "$normal" images-push VERSION=dev-test > "$WORK/push.out" 2>&1 &&
	[ "$(grep -c '^push ' "$WORK/docker.calls")" -eq 3 ]; then
	pass "images-push still pushes the three images under a tag CI never writes"
else
	fail "images-push VERSION=dev-test: $(tail -1 "$WORK/push.out")"
fi

if [ "$failures" -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all host-target checks passed"

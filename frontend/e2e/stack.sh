#!/usr/bin/env bash
# A throwaway copy of the deployed stack for the browser smoke test.
#
#   frontend/e2e/stack.sh up     build the images from this tree and start them
#   frontend/e2e/stack.sh down   remove the containers, volumes, network and images
#
# It is the deployment's own compose file with its own images, under a compose
# project of its own, on HTTPS at 127.0.0.1 (E2E_HTTPS_PORT, 18443 by default),
# with secrets generated for it. Nothing here reads deploy/.env or touches a
# running stack: compose is given its own env file, which replaces the
# default deploy/.env, and every name it creates carries the project name.
#
# `up` also creates the first administrator and writes what the suite needs
# into smoke.env in the stack directory (E2E_STACK_DIR, a temporary directory
# by default), mode 0600. Run the suite with
#
#   make e2e-smoke E2E_ENV_FILE=<stack dir>/smoke.env
#
# E2E_SUBNET_PREFIX (10.231.40 by default) is the stack's /24; change it to
# avoid a VPN or LAN route, or to run a second stack beside the first under
# another E2E_PROJECT and E2E_HTTPS_PORT. KEEP_IMAGES=1 makes `down` leave the
# built images for the next `up`. Give `down` the same variables as `up`.
#
# Needs Docker with Compose 2.24 or later (for `!override`) and openssl.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PROJECT="${E2E_PROJECT:-dbcontest-e2e}"
PORT="${E2E_HTTPS_PORT:-18443}"
SUBNET="${E2E_SUBNET_PREFIX:-10.231.40}"
DIR="${E2E_STACK_DIR:-${TMPDIR:-/tmp}/${PROJECT}}"
DIR="${DIR%/}"
ENV_FILE="$DIR/stack.env"
SMOKE_ENV="$DIR/smoke.env"
IMAGE_REGISTRY="$PROJECT"
IMAGE_TAG="local"
IMAGES=(
	"$IMAGE_REGISTRY/db-contest-backend:$IMAGE_TAG"
	"$IMAGE_REGISTRY/db-contest-queryrunner:$IMAGE_TAG"
	"$IMAGE_REGISTRY/db-contest-frontend:$IMAGE_TAG"
)

# Compose is run with an empty environment but for what Docker itself needs:
# the shell's values beat an env file in interpolation, so anything exported
# here for another stack (a password, COVER_DIR, a REDIS_ADDR) would leak in.
compose() {
	env -i PATH="$PATH" HOME="$HOME" \
		${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} \
		${DOCKER_CONTEXT:+DOCKER_CONTEXT="$DOCKER_CONTEXT"} \
		${DOCKER_CONFIG:+DOCKER_CONFIG="$DOCKER_CONFIG"} \
		docker compose -p "$PROJECT" --env-file "$ENV_FILE" \
		-f "$ROOT/deploy/docker-compose.yml" \
		-f "$ROOT/deploy/docker-compose.build.yml" \
		-f "$ROOT/frontend/e2e/compose.e2e.yml" "$@"
}

secret() { openssl rand -hex 32; }

write_env() {
	mkdir -p "$DIR"
	chmod 700 "$DIR"
	umask 077
	cat >"$ENV_FILE" <<EOF
REGISTRY=$IMAGE_REGISTRY
VERSION=$IMAGE_TAG
E2E_HTTPS_PORT=$PORT
E2E_SUBNET_PREFIX=$SUBNET
ENV=production
SITE_ADDRESS=localhost
TRUSTED_PROXIES=$SUBNET.10/32,$SUBNET.11/32
CORE_DB_USER=dbcontest
CORE_DB_NAME=dbcontest_core
CORE_DB_PASSWORD=$(secret)
GAME_DB_USER=dbcontest_game
GAME_DB_NAME=dbcontest_game
GAME_DB_PASSWORD=$(secret)
GAME_READER_PASSWORD=$(secret)
GAME_WRITER_PASSWORD=$(secret)
GAME_AUTHOR_PASSWORD=$(secret)
REDIS_PASSWORD=$(secret)
QUERY_RUNNER_TOKEN=$(secret)
DEVICE_COOKIE_SECRET=$(secret)
INGRESS_SECRET=$(secret)
GRAFANA_PASSWORD=$(secret)
EOF
}

wait_for() { # url, what
	local deadline=$((SECONDS + 300))
	until curl -ksf -o /dev/null "$1"; do
		if [ "$SECONDS" -ge "$deadline" ]; then
			echo "stack: $2 did not answer at $1 within five minutes" >&2
			compose ps >&2 || true
			exit 1
		fi
		sleep 3
	done
}

up() {
	# One set of secrets per stack: the volumes were initialised with them.
	[ -f "$ENV_FILE" ] || write_env

	echo "stack: building the images from this tree"
	compose build api queryrunner web
	echo "stack: starting project $PROJECT"
	compose up -d --no-build
	wait_for "https://localhost:$PORT/api/v1/version" "the API"
	wait_for "https://localhost:$PORT/login" "the interface"

	# bootstrap prints the one-time password on stdout and nothing else there,
	# and prints nothing when the administrator already exists.
	local password
	# The profile is named on this command alone: on `up` it would start the
	# job too, and its password would go to a log driver that keeps nothing.
	password="$(compose --profile bootstrap run --rm -T bootstrap 2>"$DIR/bootstrap.log")"
	if [ -n "$password" ]; then
		umask 077
		cat >"$SMOKE_ENV" <<EOF
E2E_BASE_URL=https://localhost:$PORT
E2E_ADMIN_LOGIN=admin
E2E_ADMIN_PASSWORD=$password
E2E_IGNORE_HTTPS_ERRORS=1
E2E_ALLOW_ADMIN_PASSWORD_CHANGE=1
EOF
	fi
	[ -f "$SMOKE_ENV" ] || {
		echo "stack: the administrator exists but $SMOKE_ENV does not; run down, then up" >&2
		exit 1
	}

	echo "stack: up at https://localhost:$PORT"
	echo "stack: run the suite with: make e2e-smoke E2E_ENV_FILE=$SMOKE_ENV"
}

# The password the suite set for this stack's administrator (e2e/api.ts)
# dies with the stack; left behind it would be tried, and refused, first on
# every run against the next stack on this port.
forget_admin_password() {
	local file="${E2E_STATE_DIR:-$ROOT/frontend/e2e/.state}/admin.json"
	if [ -f "$file" ] && grep -qF "\"baseURL\": \"https://localhost:$PORT\"" "$file"; then
		rm -f "$file"
	fi
}

down() {
	if [ -f "$ENV_FILE" ]; then
		compose --profile bootstrap down -v --remove-orphans
	else
		# Without the env file compose cannot even read the project (the
		# directory was purged, or `down` was given another E2E_STACK_DIR), so
		# what carries the project's label is removed directly.
		echo "stack: $ENV_FILE is missing; removing project $PROJECT by its label"
		local label="label=com.docker.compose.project=$PROJECT"
		docker ps -aq --filter "$label" | xargs docker rm -f >/dev/null 2>&1 || true
		docker volume ls -q --filter "$label" | xargs docker volume rm >/dev/null 2>&1 || true
		docker network ls -q --filter "$label" | xargs docker network rm >/dev/null 2>&1 || true
	fi
	if [ "${KEEP_IMAGES:-}" != "1" ]; then
		docker image rm "${IMAGES[@]}" >/dev/null 2>&1 || true
	fi
	forget_admin_password
	rm -rf "$DIR"

	local left
	left="$(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT")$(docker volume ls -q --filter "label=com.docker.compose.project=$PROJECT")"
	if [ -n "$left" ]; then
		echo "stack: project $PROJECT still has containers or volumes" >&2
		exit 1
	fi
	echo "stack: project $PROJECT removed"
}

case "${1:-}" in
up) up ;;
down) down ;;
*)
	echo "usage: $0 up|down" >&2
	exit 2
	;;
esac

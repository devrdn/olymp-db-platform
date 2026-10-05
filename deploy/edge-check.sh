#!/usr/bin/env bash
# Proves what deploy/Caddyfile hands the API and the interface about the
# client, with the real Caddy image and the real Caddyfile.
#
# The API believes X-Forwarded-For and X-Forwarded-Proto only from Caddy and
# the interface (TRUSTED_PROXIES), so what Caddy writes into them is the
# client's address and scheme as far as the login throttle, a contest's
# network restriction, the audit trail and the cross-origin check are
# concerned. That is decided here, by Caddy, and only a running Caddy can
# show it — so this starts one, puts an echo service where api and web would
# be, and sends requests from fixed addresses on a throwaway network:
#
#   - Caddy validates with EDGE_TRUSTED_PROXIES unset and set;
#   - unset, a client's forwarded headers are not believed: the API sees the
#     connecting address and the scheme Caddy itself was reached by;
#   - set to an edge proxy's address, that proxy's chain is read right to
#     left — the address it appended, not one the client typed — and its
#     https is kept;
#   - the same headers from any other address are still not believed;
#   - /api/* reaches the API itself, not the interface, with the browser's
#     Host — the API compares the Origin against it;
#   - X-Ingress-Secret never reaches the API, and the interface receives the
#     configured value, never the client's.
#
# Usage: deploy/edge-check.sh   (or `make edge-check`). Needs Docker.
set -euo pipefail

IMAGE="${CADDY_IMAGE:-caddy:2-alpine}"
SUBNET="${EDGE_CHECK_SUBNET:-10.231.42.0/24}"
PREFIX="${SUBNET%.*}"
CADDY_IP="$PREFIX.10"
EDGE_IP="$PREFIX.50"
STRANGER_IP="$PREFIX.51"
SECRET="edge-check-ingress-secret-0123456789abcdef"
HERE="$(cd "$(dirname "$0")" && pwd)"
RUN="edge-check-$$"
WORK="$(mktemp -d)"

cleanup() {
	docker rm -f "$RUN-caddy" "$RUN-echo" >/dev/null 2>&1 || true
	docker network rm "$RUN" >/dev/null 2>&1 || true
	rm -rf "$WORK"
}
trap cleanup EXIT

failures=0
pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n        got: %s\n' "$1" "$2"; failures=$((failures + 1)); }
expect() { # name, got, want
	if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "$2 (want $3)"; fi
}

# The echo service answers on both upstream ports, naming which one it is, with
# exactly the headers this check is about: a response line is what api or web
# would have read.
cat >"$WORK/echo.Caddyfile" <<'EOF'
{
	admin off
	auto_https off
}
:8080 {
	respond "api host=[{host}] xff=[{header.X-Forwarded-For}] proto=[{header.X-Forwarded-Proto}] secret=[{header.X-Ingress-Secret}]"
}
:3000 {
	respond "web host=[{host}] xff=[{header.X-Forwarded-For}] proto=[{header.X-Forwarded-Proto}] secret=[{header.X-Ingress-Secret}]"
}
EOF

validate() { # trusted-proxies value
	docker run --rm -e SITE_ADDRESS=http://edge.test -e INGRESS_SECRET="$SECRET" \
		-e EDGE_TRUSTED_PROXIES="$1" -v "$HERE/Caddyfile:/etc/caddy/Caddyfile:ro" \
		"$IMAGE" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1
}

start_caddy() { # trusted-proxies value
	docker rm -f "$RUN-caddy" >/dev/null 2>&1 || true
	docker run -d --name "$RUN-caddy" --network "$RUN" --ip "$CADDY_IP" \
		-e SITE_ADDRESS=http://edge.test -e INGRESS_SECRET="$SECRET" -e EDGE_TRUSTED_PROXIES="$1" \
		-v "$HERE/Caddyfile:/etc/caddy/Caddyfile:ro" "$IMAGE" >/dev/null
	for _ in $(seq 1 50); do
		if docker run --rm --network "$RUN" "$IMAGE" wget -qO /dev/null --header "Host: edge.test" \
			"http://$CADDY_IP/" 2>/dev/null; then
			return 0
		fi
		sleep 0.2
	done
	echo "Caddy did not start:" >&2
	docker logs "$RUN-caddy" >&2
	exit 1
}

ask() { # from-address, path, then extra headers
	local from="$1" path="$2"
	shift 2
	local headers=(--header "Host: edge.test")
	for h in "$@"; do headers+=(--header "$h"); done
	docker run --rm --network "$RUN" --ip "$from" "$IMAGE" \
		wget -qO- "${headers[@]}" "http://$CADDY_IP$path"
}

echo "Caddy configuration"
if validate ""; then pass "validates with EDGE_TRUSTED_PROXIES unset"; else fail "validates with EDGE_TRUSTED_PROXIES unset" "rejected"; fi
if validate "192.0.2.10/32 2001:db8::10/128"; then pass "validates with EDGE_TRUSTED_PROXIES set"; else fail "validates with EDGE_TRUSTED_PROXIES set" "rejected"; fi

docker network create --subnet "$SUBNET" "$RUN" >/dev/null
docker run -d --name "$RUN-echo" --network "$RUN" --network-alias api --network-alias web \
	-v "$WORK/echo.Caddyfile:/etc/caddy/Caddyfile:ro" "$IMAGE" >/dev/null

spoofed=("X-Forwarded-For: 6.6.6.6, 203.0.113.7" "X-Forwarded-Proto: https" "X-Ingress-Secret: forged")

echo "Trusting nobody (EDGE_TRUSTED_PROXIES unset)"
start_caddy ""
expect "/api/* reaches the API, which sees the connecting address and Caddy's own scheme" \
	"$(ask "$STRANGER_IP" /api/v1/ping "${spoofed[@]}")" "api host=[edge.test] xff=[$STRANGER_IP] proto=[http] secret=[]"
expect "the interface sees the connecting address and the configured secret" \
	"$(ask "$STRANGER_IP" /login "${spoofed[@]}")" "web host=[edge.test] xff=[$STRANGER_IP] proto=[http] secret=[$SECRET]"

echo "Trusting an edge proxy (EDGE_TRUSTED_PROXIES=$EDGE_IP/32)"
start_caddy "$EDGE_IP/32"
expect "the edge's chain is read right to left and its https is kept" \
	"$(ask "$EDGE_IP" /api/v1/ping "${spoofed[@]}")" "api host=[edge.test] xff=[203.0.113.7] proto=[https] secret=[]"
expect "the interface is handed the same client address" \
	"$(ask "$EDGE_IP" /login "${spoofed[@]}")" "web host=[edge.test] xff=[203.0.113.7] proto=[https] secret=[$SECRET]"
expect "an edge request with no chain is the edge itself" \
	"$(ask "$EDGE_IP" /api/v1/ping)" "api host=[edge.test] xff=[$EDGE_IP] proto=[http] secret=[]"
expect "any other address is still not believed" \
	"$(ask "$STRANGER_IP" /api/v1/ping "${spoofed[@]}")" "api host=[edge.test] xff=[$STRANGER_IP] proto=[http] secret=[]"

if [ "$failures" -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"

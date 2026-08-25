# Core API

Backend service of the DB Contest platform. Steps 1 and 2 of the plan in
[docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md) are implemented: the foundation
(layout, configuration, logging, metrics, health probes, core schema) and
authentication with role-based access control. Contests, the game databases and
the query runner arrive in the following steps.

## Layout

```
backend/
├── cmd/                 entry points, thin: flags, signals, exit codes
│   ├── api/             Core API server
│   ├── migrate/         schema migrations (embedded)
│   └── bootstrap/       creates the first administrator
├── internal/
│   ├── app/             composition root: builds the graph, runs it, tears it down
│   ├── api/             HTTP surface: router assembly and handlers
│   ├── auth/            who is this: passwords, sessions, login, middleware
│   ├── rbac/            may they do this: the two-level permission model
│   ├── users/           accounts, roles, password rules
│   ├── audit/           append-only trail of who did what
│   ├── health/          liveness, readiness, self-probe
│   ├── postgres/        every repository implementation — all SQL lives here
│   └── platform/        infrastructure, imports no domain package
│       ├── cache/       Cache interface: Redis or in-process fallback
│       ├── config/      environment configuration
│       ├── httpx/       middleware, CSRF guard, client IP, JSON responses
│       ├── logging/     slog setup and request correlation
│       ├── metrics/     Recorder interface: prometheus, log or none
│       ├── password/    argon2id hashing
│       ├── server/      HTTP listener with graceful shutdown
│       └── storage/     PostgreSQL pool, querier seam, unit of work
└── migrations/          core schema, applied by cmd/migrate
```

### Finding your way

The layout is by layer, and dependencies point one way: `platform` knows nothing
about domains, domains know nothing about the database, and only `internal/app`
knows about everything. A domain package declares the storage interface it
needs; `internal/postgres` implements it. That is the dependency rule from Clean
Architecture without its folder ceremony.

| Looking for | Open |
|---|---|
| what happens on sign-in | `internal/auth/service.go` |
| who may do what | `internal/rbac/rbac.go` |
| the SQL behind an account | `internal/postgres/users.go` |
| which routes exist | `internal/api/router.go` and the `*_handler.go` beside it |
| how the service is wired | `internal/app/app.go` |
| a setting and its default | `internal/platform/config/config.go` |

Files are named for what is in them, not for a technical role: there is no
`model.go`/`service.go`/`repository.go` split, because in Go the package is the
unit of encapsulation and such a split costs navigation while hiding nothing.
`go doc ./internal/<pkg>` states what each package answers.

### Tests

A test file mirrors its source file — `session.go` is tested by
`session_test.go` — so there is never a question of where a test lives or where
a new one goes. Two exceptions, both named so you can spot them:
`integration_test.go` assembles the whole service over real listeners, and
`support_test.go` holds helpers shared within a package. Compile-time interface
assertions (`var _ users.Repository = (*Users)(nil)`) sit next to the type they
concern, not in a test file. Helpers another package
needs live in a `<pkg>test` package (`users/userstest`,
`platform/password/passwordtest`), which is also where the in-memory
repositories live — business rules are tested against those, not against mocks.

## Two listeners

The service opens two ports, and the split is deliberate:

| Listener | Default | Serves | Exposure |
|---|---|---|---|
| public | `:8080` | `/api/v1/...` | published through the reverse proxy |
| internal | `:9090` | `/metrics`, `/healthz`, `/readyz` | private network only |

Metrics reveal traffic shape and internal topology, and readiness reports which
dependency is down — none of it belongs on a public port. Configuration refuses
to start if both listeners are given the same address.

`/healthz` reports only that the process runs; it stays green during a database
outage, because restarting the container would not fix one. `/readyz` probes
PostgreSQL and Redis and returns 503 when either is unusable, without echoing
driver errors that carry hosts and user names.

## Configuration

Read from the environment at startup; a missing required value aborts the boot.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `CORE_DB_DSN` | yes | — | PostgreSQL connection string |
| `REDIS_ADDR` | no | — | `host:port` or `redis://…`; empty selects the in-process cache |
| `METRICS_BACKEND` | no | `prometheus` | `prometheus`, `log` or `none` |
| `HTTP_ADDR` | no | `:8080` | public listener |
| `INTERNAL_ADDR` | no | `:9090` | metrics and health listener |
| `ENV` | no | `development` | environment name |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | no | `15s` | drain period on SIGTERM |
| `SESSION_TTL` | no | `12h` | idle lifetime of a session; slides on activity |
| `TRUSTED_PROXIES` | no | — | CIDRs whose `X-Forwarded-For` is believed for client IPs |
| `COOKIE_SECURE` | no | true outside `development` | mark the session cookie Secure |

## Running locally

Compose and the Makefile both read `deploy/.env`, and the host-side targets
take the database password from it, so create it first:

```bash
cp deploy/.env.example deploy/.env
```

Fill in the passwords, then start the infrastructure, apply migrations and run
the service:

```bash
make dev-up && make migrate-up && make bootstrap && make run
```

`make bootstrap` prints the first administrator's one-time password. Then:

```bash
curl -s localhost:8080/api/v1/version && curl -s localhost:9090/readyz
```

Note that `make run` sets `ENV=development`, which turns off the Secure flag on
the session cookie. That is required locally: a browser discards a Secure cookie
delivered over plain HTTP, so signing in would appear to work and then fail on
the next request. The full stack puts Caddy in front and serves HTTPS, where the
flag belongs on.

Want dashboards and logs while developing this way? `make` happily runs several
targets in one invocation, so bring up the dev infrastructure and observability
together:

```bash
make dev-up dev-observability
```

See [Observability](#observability) below for what that gets you and, just as
important, what it does not.


## Authentication and access

### Getting in the first time

A freshly migrated installation has no accounts, so there is a command for it —
not an HTTP endpoint, because an unauthenticated route that mints
administrators stays a liability long after it is needed once. It is idempotent
and prints the password to stdout, apart from the logs:

```bash
CORE_DB_DSN=... go run ./cmd/bootstrap -login root -name "Root Administrator"
```

The account is flagged so the first sign-in ends in choosing a real password —
and the flag is enforced: until the password is changed, every endpoint except
`/auth/password`, `/auth/logout` and `/auth/me` answers 403
`password_change_required`. An administrator-issued password is a handover
secret, not a credential to live on.

### Endpoints

| Method | Path | Who |
|---|---|---|
| POST | `/api/v1/auth/login` | anyone |
| GET | `/api/v1/auth/me` | signed in |
| POST | `/api/v1/auth/logout` | signed in |
| POST | `/api/v1/auth/password` | signed in (own password) |
| GET, POST | `/api/v1/users` | `users.manage` |
| GET, PATCH | `/api/v1/users/{id}` | `users.manage` |
| POST | `/api/v1/users/{id}/block`, `/unblock` | `users.manage` |
| POST | `/api/v1/users/{id}/password-reset` | `users.manage` |
| PUT | `/api/v1/users/{id}/roles` | `users.manage` |

### Two levels of authorisation

Global roles grant installation-wide abilities: creating contests, managing
accounts. Contest roles (`contest_managers`) grant power over **one** contest.
They are not interchangeable — somebody running the March olympiad gains no say
over the April one, and never gains account management.

```go
mw.RequirePermission(rbac.PermissionUsersManage)   // installation-wide
mw.RequireContestPermission(rbac.PermissionContestEdit) // scoped to {contestID}
```

`contest.admin_all` lifts the scope and is held only by `admin`. It is a
permission rather than a hard-coded "is admin" check, so the same reach can be
given to a new role from data.

Owner and manager differ in one thing: only an owner appoints managers. That is
the one power a manager must not be able to grant themselves more of.

### What the design protects against

**Account enumeration.** An unknown login and a wrong password return the same
message *and take the same time* — a missing account is still verified against a
dummy digest, because returning in microseconds instead of tens of milliseconds
is a timing oracle. A blocked account learns it is blocked only after the
password checked out: the owner deserves to know, a guesser does not.

**Brute force.** Fixed windows of 10 attempts per login and 30 per address, both
over 15 minutes. Addresses are resolved proxy-aware: `TRUSTED_PROXIES` names the
ingress, whose `X-Forwarded-For` is then believed (rightmost untrusted hop wins);
without it, behind a proxy, every client would share the proxy's address and the
per-address window would throttle the whole installation at once. Fixed rather than sliding: a sliding window extended by every
attempt never resets under sustained load, which turns an attack on one account
into a denial of service against its owner. A cache failure refuses the attempt
— an unmaintained counter means no protection.

**Stolen session store.** The cookie holds a 256-bit opaque token; the cache
holds only its SHA-256. A dump yields digests, which cannot authenticate.

**Sessions outliving their welcome.** `users.session_generation` advances on
block, password change, password reset and role change; a session records the
generation it was issued at, so all of them die at once. The account is also
re-read on every request, so blocking takes effect on the next request rather
than whenever the session lapses. That costs one indexed query, which is the
right trade at this scale.

**XSS and CSRF.** The token lives in an `HttpOnly` cookie, so page script cannot
read it. `SameSite=Lax` plus an `Origin` check on every mutating request covers
CSRF. A request with *no* `Origin` passes: browsers always send it on
cross-origin writes, while curl and health probes send nothing.

**Leaking the digest.** API responses are built from dedicated DTOs, never by
serialising `users.User` — that would publish `PasswordHash` the first time
somebody adds a field without thinking. Tests assert it on every endpoint.

**A trail that lies.** Every multi-write account operation runs inside a unit of
work injected into the service, so the action and its audit entry land together
or not at all — a user cannot exist without their roles or without the entry
naming who created them. Request origin (IP, user agent) travels in the context
and is stamped onto every entry automatically. Payloads
are redacted at any depth: `password`, `token`, `secret` and friends never reach
a table kept for a year.

## Optional dependencies

Only the core database is required. Redis and Prometheus are each behind an
interface with a working substitute, so a single-node install runs with neither.

**Cache.** An empty `REDIS_ADDR` selects the in-process store: an LRU bounded by
entry count, with a TTL per entry. It is **correct for one replica and wrong for
several** — nothing is shared between instances, so sessions and rate limits
diverge, and everything is lost on restart. The service therefore says so at
startup (a WARN naming the consequence) and reports the active mode in
`/readyz` as `"cache": "memory"`, so the degradation stays visible.

A configured but unreachable Redis is an error, not a reason to fall back: the
operator named that server, and silently using a different store would hide a
broken deployment.

To turn Redis on, set `REDIS_PASSWORD` in `deploy/.env` and start the `shared`
profile (`make dev-up`, or `docker compose --profile shared up -d redis`) —
**leave `REDIS_ADDR` itself empty.** That single password then produces two
different, both correct, addresses on its own:

- `docker-compose.yml` builds the containerized api's address as
  `redis://:${REDIS_PASSWORD}@redis:6379/0` — `redis` is the compose service
  name, resolvable only on the compose network.
- The Makefile builds `make run`'s address as
  `redis://:$(REDIS_PASSWORD)@localhost:$(REDIS_PORT)/0` — a bare process on
  the host cannot resolve `redis` at all, so it needs the loopback form
  instead.

Writing a finished `REDIS_ADDR` into `deploy/.env` yourself breaks one side or
the other, for two separate reasons worth knowing about:

1. **`.env` files are not shell-interpolated.** `REDIS_ADDR=redis://:${REDIS_PASSWORD}@redis:6379/0`
   in `.env` does *not* expand `${REDIS_PASSWORD}` — Compose only expands
   variables inside the compose YAML itself, never recursively inside another
   `.env` value — so the password ships as the literal four characters
   `${REDIS_PASSWORD}`.
2. **A value in `.env`, even empty, reaches `make run` and wins.** The Makefile
   includes `deploy/.env` directly, so `REDIS_ADDR=` there is *not* the same as
   "unset" from Make's point of view — a `?=` default only fires on a variable
   that was never assigned at all, and an included empty assignment still
   counts as an assignment. A `redis://...@redis:6379/0` value meant for the
   container would silently become `make run`'s address too, and a bare host
   process cannot resolve `redis`. (This is exactly the shape of bug this
   project tries to design out elsewhere — one fact with two different correct
   readings, sourced from a single, ambiguous place. Here the fix was to stop
   sourcing the host address from `.env` at all: the Makefile now always
   derives it fresh from `REDIS_PASSWORD`.)

Only set `REDIS_ADDR` explicitly if you want the **containerized** api to reach
a Redis that is not this stack's own `redis` service; it has no effect on
`make run` regardless.

**Metrics.** Prometheus scrapes the service, so its absence cannot break
anything — but it is not a required dependency either. `METRICS_BACKEND`
selects where request statistics go:

| Value | Behaviour |
|---|---|
| `prometheus` | private registry plus `/metrics` on the internal listener |
| `log` | periodic digest into the log stream: count, average and max per method + route + status |
| `none` | observations are discarded |

The instrumentation is identical for every backend, so switching one cannot
change *what* is measured — only where it goes. `/metrics` is registered only
when the backend actually has a scrape endpoint: serving an empty page would
tell a scraper the service is instrumented while its numbers live elsewhere.

## Observability

Prometheus, Loki, Promtail and Grafana are optional and off by default — a
small install stays small. Bring them up:

```bash
make compose-observability   # alongside the containerized stack (make compose-up)
make dev-observability       # alongside the dev infrastructure (make dev-up)
```

Both are pure additions: they only start the four observability services by
name, never the rest of the stack, and `make dev-down` / `make compose-down`
tear them down along with everything else. Grafana is at
`http://localhost:${GRAFANA_PORT:-3001}` (bound to loopback), login `admin` /
your `GRAFANA_PASSWORD`; Prometheus and Loki are provisioned as its
datasources automatically, nothing to click through.

**Metrics work in both modes without touching anything.** Prometheus is
configured with two scrape jobs pointed at the same `service: core-api` label:
`api:9090` for the containerized service, and `host.docker.internal:9090` for
`make run`'s process on the host — Docker Desktop resolves that hostname to
the host machine from any container, regardless of which network it is on.
Whichever one you are actually running answers `up`; the other one just shows
as a down target, which is the correct way to say "not running that way right
now" rather than a toggle to remember. See
[`deploy/observability/prometheus.yml`](../deploy/observability/prometheus.yml).

**Logs only work for the containerized stack.** Promtail discovers what to
tail through the Docker socket (`docker_sd_configs`), so it can only ever see
containers. A bare `go run ./cmd/api` process from `make run` has no
container to discover — its logs go to your terminal (or wherever you redirect
them) and nowhere else. If you want `make run`'s logs in Grafana too, you would
need to give Promtail a file-based scrape target and have `make run` tee its
output there; nothing in this repository does that today, and it has not been
worth the plumbing so far.

## Swapping the database

The game cluster is PostgreSQL and stays that way: per-participant `CREATE
DATABASE … TEMPLATE`, the real PostgreSQL parser for validating student SQL, and
role-level `statement_timeout` have no portable equivalent — and the product is
literally "students write SQL against PostgreSQL".

The core database is swappable, and the seam sits at the repository level rather
than around the driver. Each domain package declares the interface it needs in
its own terms; a `postgres` package implements it and keeps all SQL inside.
Changing engines means writing a new implementation package, with no edit to
domain code — and, more usefully day to day, business logic is testable with no
database at all.

Multi-write atomicity goes through `storage.UnitOfWork`. The transaction travels
in the context and repositories pick it up with `storage.QuerierFrom(ctx, pool)`,
so one repository method works standalone and inside a caller's transaction —
which is how an audit entry lands in the same transaction as the action it
records.

## Migrations

Migrations are embedded in the binary, so an image always carries the schema its
code expects. They run as a separate step — never on API startup, where several
replicas would race to alter the schema.

```bash
make migrate-up        # apply everything pending
make migrate-version   # current version
make migrate-down      # roll back one step
```

Each migration ships an `up` and a `down` file; a test in `migrations/` fails
the build if a pair is incomplete or a version number is reused.

## Logging

Records are JSON on stdout, ready for Promtail. Every request gets a
`request_id`: reused from `X-Request-Id` when it is a valid UUID, generated
otherwise — arbitrary client text must never reach the logs. The logger reads
the identifier from the request context, so handlers do not pass it explicitly.

## Troubleshooting

**`bind: address already in use` on port 80/443 (`make compose-up`).** Caddy
wants those ports and something else on the host already has them — commonly
macOS's built-in Apache (`sudo apachectl stop`, and
`sudo launchctl disable system/org.apache.httpd` if you want it to stay off
across reboots). Find the actual culprit with `lsof -nP -iTCP:80 -sTCP:LISTEN`
before assuming it is Apache.

**A profile flag adds services, it does not select them.** `docker compose
--profile observability up -d` (no service names) does *not* start only the
observability stack — it starts observability **in addition to** every service
that has no `profiles:` key at all (`api`, `caddy`, `pg-core`, `migrate`),
because those are Compose's unconditional "default set" and start on any `up`
regardless of which profiles are active. The only way to actually limit `up`
to a specific set is to name the services explicitly, which is what
`compose-observability` and `dev-observability` both do
(`up -d prometheus loki promtail grafana`) — `--profile` is still required
too, or Compose skips them as "not in an active profile".

**`make dev-down` (or `compose-down`) leaves containers running / refuses to
remove the network.** Same root cause as above, in reverse: `down` without the
right `--profile` flags does not know about profiled services that are
currently up, so it cannot stop them, and then can't remove a network they are
still attached to. Both `dev-down` and `compose-down` pass every profile this
project defines for exactly this reason — if you add a new profile to
`docker-compose.yml`, add it there too.

**A `go run` process outlives the `kill` you sent it.** `go run` compiles to a
temp binary and execs it as a *child* process; the PID your shell's `$!` gives
you is the `go` wrapper, not the binary actually holding the port. Killing the
wrapper can leave the real process listening behind it. If a port that should
be free still answers, find the actual owner —
`lsof -nP -iTCP:<port> -sTCP:LISTEN` — and kill that PID instead.

## Development

```bash
make check       # format, vet, test
make test-race   # tests with the race detector
make cover       # coverage summary
make help        # every target
```

CI additionally runs `govulncheck` and `gosec`. The Go patch version in
`go.mod` and in the Dockerfile is pinned to the one that currently scans clean;
bump both together when a new advisory lands.

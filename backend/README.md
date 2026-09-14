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
│   ├── contests/        contests, content, staff, participants, publish gate
│   ├── audit/           append-only trail of who did what
│   ├── health/          liveness, readiness, self-probe
│   ├── postgres/        every repository implementation — all SQL lives here
│   └── platform/        infrastructure, imports no domain package
│       ├── cache/       Cache interface: Redis or in-process fallback
│       ├── config/      environment configuration
│       ├── httpx/       middleware, CSRF guard, client IP, JSON responses
│       ├── i18n/        language negotiation (Accept-Language, fallbacks)
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
| when a contest may still be edited | `internal/contests/contests.go` |
| what stops a contest being published | `internal/contests/publish.go` |
| who may sign themselves up | `internal/contests/enrollment.go` |
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

The repository tests in `internal/postgres` run their SQL against a **real**
PostgreSQL, each inside a transaction that is rolled back, so they leave nothing
behind. Without `CORE_DB_DSN` they skip rather than fail, which keeps `make
test` runnable with no database to hand; `make test-db` is what actually
exercises the SQL, and CI sets the variable. A query is the one thing a fake
cannot verify.

Those tests never run against the database the product serves from. `make
test-db` drops and recreates `dbcontest_core_test` (`cmd/testdb`), migrates it
with `cmd/migrate`, and hands the tests that. The tests themselves connect only
through `internal/platform/storage/storagetest`, which refuses any database
whose name — as the server reports it — does not end in `_test`, so a copied or
mistyped `CORE_DB_DSN` fails the run instead of filling a real database with
fixtures.

The game-cluster tests get a whole cluster of their own, `pg-game-test`, which
`make test-game` and `make test-game-build` recreate for every run: what those
tests act on — databases and the shared participant roles — is cluster-wide,
so a separate database inside `pg-game` would isolate nothing.
`internal/gamedb/gamedbtest` applies the same `_test` rule to the maintenance
database `GAME_DB_DSN` names.

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
| `SESSION_MAX_LIFETIME` | no | `12h` | absolute lifetime of a session from sign-in, 5m to 168h; activity does not extend it |
| `MAX_LOGIN_ATTEMPTS_PER_ADDRESS` | no | `300` | sign-in attempts from one address per 15 minutes, successes included |
| `MAX_LOGIN_ATTEMPTS_PER_ACCOUNT` | no | `100` | sign-in attempts at one account from all addresses per 15 minutes; the guessing limit itself is 10 per account and address |
| `DEVICE_COOKIE_SECRET` | outside `development` | generated per start in development | HMAC key (at least 32 bytes) of the cookie marking a browser the owner signed in from |
| `DEVICE_COOKIE_TTL` | no | `720h` | lifetime of that cookie, 1h to 2160h |
| `MAX_LOGIN_ATTEMPTS_PER_DEVICE` | no | `10` | sign-in attempts through one trusted browser per 15 minutes, successes included, instead of the address and account limits |
| `MAX_TRUSTED_LOGIN_ATTEMPTS_PER_ACCOUNT` | no | `20` | sign-in attempts through all of one account's trusted browsers together per 15 minutes, successes included |
| `PASSWORD_HASH_CONCURRENCY` | no | one per CPU, at least 2 | argon2id computations run at once (1-64); 64 MiB each, so size the memory limit from it |
| `PASSWORD_HASH_MAX_WAIT` | no | `2s` | how long a sign-in or password change waits for a hashing slot before a 503 `sign_in_busy` (at most `30s`) |
| `ANSWER_RATE_PER_MINUTE` | no | `6` | answers one registration may submit a minute, refused ones included (1-60); past it a 429 `answer_too_often` |
| `TRUSTED_PROXIES` | no | — | CIDRs whose `X-Forwarded-For` is believed for client IPs |
| `DEFAULT_LOCALE` | no | `en` | language of last resort; must exist in the `languages` table |
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

Want dashboards, logs, or a database browser while developing this way? `make`
happily runs several targets in one invocation, so combine whatever you need:

```bash
make dev-up dev-observability dev-db-ui
```

See [Observability](#observability) below for what the first gets you and,
just as important, what it does not; the database browser is covered right
after it.


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
| GET | `/api/v1/settings` | anyone — the sign-in screen carries the installation's name |
| GET | `/api/v1/settings/all`, PUT `/api/v1/settings` | `settings.manage` |
| GET | `/api/v1/settings/images/{kind}` | anyone — the sign-in screen wears them |
| PUT, DELETE | `/api/v1/settings/images/{kind}` | `settings.manage`. `kind` is logo, icon or favicon |
| GET | `/api/v1/roles` | `users.manage` |
| GET, POST | `/api/v1/users` | `users.manage` |
| GET, PATCH | `/api/v1/users/{id}` | `users.manage` |
| POST | `/api/v1/users/{id}/block`, `/unblock` | `users.manage` |
| POST | `/api/v1/users/{id}/password-reset` | `users.manage` |
| PUT | `/api/v1/users/{id}/roles` | `users.manage` |
| POST | `/api/v1/users/import` | `users.manage` |
| GET | `/api/v1/audit` | `audit.view` |
| GET | `/api/v1/contests` | signed in (scoped by who you are; `scope=participant` and `enrolled=true|false` cut a participant's own two lists) |
| POST | `/api/v1/contests` | `contest.create` |
| GET | `/api/v1/contests/{id}` | `contest.view` on that contest |
| PATCH, DELETE | `/api/v1/contests/{id}` | `contest.edit` |
| GET | `/api/v1/contests/{id}/publish-check` | `contest.view` |
| POST | `/api/v1/contests/{id}/status` | `contest.publish` |
| PUT | `/api/v1/contests/{id}/languages`, `/translations` | `contest.edit` |
| GET, PUT | `/api/v1/contests/{id}/sql-policy` | `contest.view` / `contest.edit` |
| GET, PUT | `/api/v1/contests/{id}/story` | `contest.view` / `contest.edit` |
| GET, POST | `/api/v1/contests/{id}/questions` | `contest.view` / `contest.edit` |
| PUT | `/api/v1/contests/{id}/questions/order` | `contest.edit` |
| GET, PUT, PATCH, DELETE | `/api/v1/contests/{id}/questions/{qid}` | `contest.view` / `contest.edit`. PUT replaces the whole question — fields, wording and answers — in one transaction; PATCH edits its own fields only |
| PUT | `/api/v1/contests/{id}/questions/{qid}/texts`, `/answers` | `contest.edit` |
| GET | `/api/v1/contests/{id}/managers` | `contest.view` |
| PUT, DELETE | `/api/v1/contests/{id}/managers/{userId}` | `contest.manage` (owner) |
| GET, POST | `/api/v1/contests/{id}/participants` | `participant.manage` |
| DELETE | `/api/v1/contests/{id}/participants/{userId}` | `participant.manage` |
| POST | `/api/v1/contests/{id}/participants/{userId}/disqualify` | `participant.manage` |
| POST | `/api/v1/contests/{id}/enroll` | signed in (the contest decides) |

The Postman collection in `postman/` covers all of them with the request bodies
and the response codes each one answers with.

### Running a contest

The constructor is a pipeline, and each step refuses what the next one could not
survive:

1. **Create** — the author becomes the contest's `owner`, and a read-only SQL
   policy is stored in the same transaction. A contest with nobody who may
   appoint staff, or with undefined SQL access, is never observable.
2. **Languages and translations** — the set of languages is what the contest
   offers; exactly one is the default. Language codes are checked against the
   `languages` table, so adding a fourth language to the installation is an
   `INSERT` there, with no migration and no deploy.
3. **Story, questions, answers** — authored per language. `is_visible` defaults
   to true: hiding a question is the deliberate choice, and a hidden question
   still scores.
4. **Publish check** — reports *everything* missing at once, as machine codes
   the interface translates. An organizer fixing a contest one refusal at a time
   would need a round trip per missing translation.
5. **Status** — draft → published → running → finished → archived. The only step
   back is published → draft, because publishing is how an organizer finds out
   the gate passes. The gate runs at **both** doors: content stays editable
   while published, so "publish, remove the story, start" is a sequence the
   rules allow, and the invariant has to hold when participants are let in.

What may still change depends on where the contest is, and the two lines are
deliberately different. **Content** freezes at the start: changing a question
while people answer it changes their task. **Settings** stay open while running,
because extending the window after a power cut and correcting a network range
that turned out wrong are exactly what a running contest needs — what cannot
move is the shape (question mode, timing model, session length).

### Enrollment and network restrictions

`enrollment` decides only **who creates the registration**: `open` lets a
student sign themselves up, `invite_only` means staff add them. Past that point
both paths are identical.

A roster import takes logins as well as identifiers, because what an organizer
has is a spreadsheet of student numbers. It reports partial success honestly:
one mistyped login does not reject the other three hundred rows, and every row
it could not use comes back with a reason.

`allowed_cidrs` limits participation to given networks — an on-site olympiad run
from one lecture hall. It is checked against the address resolved through
`TRUSTED_PROXIES`, never a header read at the endpoint, and an address that
cannot be resolved is refused rather than admitted: failing open would turn
every proxy misconfiguration into an open door. It applies to participants only.
Staff are exempt, so an administrator who mistypes a range cannot lock
themselves out of the contest they are configuring. Every refusal is written to
`audit_log` — the entry that proves the rule works is the same signal that
somebody tried from an outside device.

### The audit trail

Who did what, from where and when — kept for a year, and the record the
specification asks for. Entries are written in the same transaction as the
action they describe, so an action and its entry land together or neither
does.

`GET /api/v1/audit` reads it back, newest first, filtered by actor, action,
entity or a time window — each of which narrows an index the table already
carries. There is no route that writes: the trail is append-only, and entries
never arrive over HTTP.

It sits behind `audit.view` and nothing else, because it names who blocked whom
and from which address. The actor comes back as a login rather than an
identifier — a page of UUIDs answers nothing — and is empty for a system event
or an account deleted since. The trail outlives the people in it, which is the
point of keeping one.

Passwords, hashes and tokens never reach a payload: they are stripped at any
depth before the entry is written.

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
make stack-observability   # alongside the containerized stack (make stack-up)
make dev-observability       # alongside the dev infrastructure (make dev-up)
```

Both are pure additions: they only start the four observability services by
name, never the rest of the stack, and `make dev-down` / `make stack-down`
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

## Database browser (dev only)

```bash
make dev-db-ui   # http://localhost:${ADMINER_PORT:-8081}
```

[Adminer](https://www.adminer.org/), not pgAdmin: a single ~50 MiB image with
no account of its own and nothing to persist, against pgAdmin's own volume and
first-run master password — for a dev convenience that gets started and
stopped constantly, that overhead bought nothing here. It authenticates as
whatever Postgres role you log in with, so access is exactly Postgres's own,
nothing extra to lock down. The login form already knows the server
(`pg-core`); type in `CORE_DB_USER` / `CORE_DB_PASSWORD` / `CORE_DB_NAME` from
`deploy/.env` and you're at the schema.

Bound to loopback, like Grafana. Not something this repo runs in production —
if a production database browser turns out to be worth it, it needs its own
answer for access control and audit that "loopback-only" sidesteps in dev, and
that's a separate decision.

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

## Releases and deployment

Development does not go near the registry. `make dev-up` starts PostgreSQL and
Redis in containers, and the API and the interface run from your editor against
them — no images are involved at all. `make stack-up` builds the whole stack
from the working tree when you want to see it as it will be served.

A server, on the other hand, never compiles anything:

```bash
make deploy      VERSION=v1.4.0   # everything
make deploy-api  VERSION=v1.4.0   # only the API and its migration job
make deploy-web  VERSION=v1.4.0   # only the interface
make deployed                     # what is running right now
```

Three properties are worth naming, because each of them is the point:

**What CI proved is what runs.** The base compose file names images and has no
build definitions, so `docker compose up` on a missing tag fails loudly instead
of quietly compiling something new. Building on the machine that serves an
olympiad would also put the toolchain, the sources and a compile's worth of CPU
on it, in the hour that has the least of both to spare.

**Rolling back is the same command with the previous version.** Seconds, and no
network beyond the registry. `VERSION` has no default that would deploy: naming
it is what makes a deploy a decision.

**One component at a time.** The API and the interface are separate images with
separate tags, so releasing one leaves the other — and every session it is
serving — untouched.

Deployment is pull-based and run by a person on the host. Nothing reaches into
the network from outside, no production key lives in a cloud CI system, and a
merge never restarts a service in the middle of a running contest.

## Backups

On-premise means nobody else is backing this machine up, and what is in the
core database — the participants' answers and the results of an olympiad —
cannot be reconstructed by reinstalling anything.

```bash
make backup                                   # deploy/backups/<db>-<timestamp>.dump
make restore-check FILE=deploy/backups/....dump
make restore       FILE=deploy/backups/....dump CONFIRM=yes
```

`backup` dumps from inside the container, so no PostgreSQL client is needed on
the host, and refuses to keep an empty file. Copy the result off the machine:
a backup that only exists on the host it came from is not a backup.

`restore-check` loads the dump into a throwaway database and drops it again. It
touches nothing real, and it is the whole difference between having backups and
believing you do — run it after every schema change.

`restore` replaces the live database and says so twice before doing it: it needs
`CONFIRM=yes`. When the API is running it is stopped for the duration, because
pg_restore cannot drop objects a live service holds open, and started again
afterwards whether the restore worked or not.

## Logging

Records are JSON on stdout, ready for Promtail. Every request gets a
`request_id`: reused from `X-Request-Id` when it is a valid UUID, generated
otherwise — arbitrary client text must never reach the logs. The logger reads
the identifier from the request context, so handlers do not pass it explicitly.

## Troubleshooting

**`bind: address already in use` on port 80/443 (`make stack-up`).** Caddy
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
`stack-observability` and `dev-observability` both do
(`up -d prometheus loki promtail grafana`) — `--profile` is still required
too, or Compose skips them as "not in an active profile".

**`make dev-down` (or `stack-down`) leaves containers running / refuses to
remove the network.** Same root cause as above, in reverse: `down` without the
right `--profile` flags does not know about profiled services that are
currently up, so it cannot stop them, and then can't remove a network they are
still attached to. Both `dev-down` and `stack-down` pass every profile this
project defines for exactly this reason — if you add a new profile to
`docker-compose.yml`, add it there too.

**A new service you add can't reach `pg-core` or `redis` by name.** Every
service in `docker-compose.yml` declares `networks: [internal]` explicitly —
without that line on a new service, Compose attaches it to a separate,
implicitly-created default network instead, and service-name DNS
(`pg-core`, `redis`, `api`) simply does not resolve across that boundary.
`docker compose config` won't catch this — it looks correct right up until
something actually tries to connect.

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

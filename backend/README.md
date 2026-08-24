# Core API

Backend service of the DB Contest platform. This is step 1 of the plan in
[docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md): the foundation — project
layout, configuration, logging, metrics, health probes and the core database
schema. Business endpoints arrive in the following steps.

## Layout

```
backend/
├── cmd/
│   ├── api/          Core API server
│   └── migrate/      schema migration tool (embedded migrations)
├── internal/
│   ├── api/          router assembly (public and internal)
│   ├── health/       liveness, readiness, self-probe
│   └── platform/
│       ├── cache/    Cache interface: Redis or in-process fallback
│       ├── config/   environment configuration
│       ├── httpx/    middleware and JSON responses
│       ├── logging/  slog setup and request correlation
│       ├── metrics/  Recorder interface: prometheus, log or none
│       ├── server/   HTTP listener with graceful shutdown
│       └── storage/  PostgreSQL pool, querier seam, unit of work
└── migrations/       core schema, applied by cmd/migrate
```

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

## Running locally

Start the infrastructure, apply migrations, run the service:

```bash
make dev-up && make migrate-up && make run
```

Then:

```bash
curl -s localhost:8080/api/v1/version && curl -s localhost:9090/readyz
```

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

```bash
REDIS_ADDR=            # in-process cache, single instance
REDIS_ADDR=redis:6379  # shared cache, required before scaling out
```

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

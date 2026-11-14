# 8. Troubleshooting

What goes wrong when you download the project and run it for the first time,
and how to get past it. Entries are grouped by the stage you are at; each says
what you see, why, and what to type.

Messages in code spans are the project's own wording. A message from Docker,
Go, npm, PostgreSQL or a browser is described in words instead, because those
tools word things differently between versions.

- [Before you start](#before-you-start)
- [Getting the code](#getting-the-code)
- [Configuration](#configuration)
- [Ports and containers](#ports-and-containers)
- [Database](#database)
- [Frontend](#frontend)
- [Running](#running)
- [Still stuck](#still-stuck)

## Before you start

Check these first; most first-run failures are one of them.

| You need | Version | Why |
|---|---|---|
| Go | 1.26.11 (the exact patch in `backend/go.mod`) | Go 1.21 or newer downloads that toolchain on first use if it can reach the network; older Go cannot read `go.mod` at all |
| A C compiler | `gcc` or `clang` | The Query Runner, `make check`, `make test` and `make build` link PostgreSQL's own parser through cgo |
| Node.js | 22 (what CI and `frontend/Dockerfile` use; the interface is Next.js 16 on React 19) | Nothing in the repository pins it, so an older Node fails inside Next |
| Docker with Compose v2 | the `docker compose` plugin | Every target calls `docker compose`; the standalone `docker-compose` v1 is not supported |
| `make` | GNU Make | The one that ships with macOS and Linux is what the Makefile was run with |
| `openssl` | any | To generate secrets |
| Docker resources | 2 CPUs and 8 GiB of memory recommended | The game cluster container is declared with 2 CPUs and a 7 GiB limit |

Platform notes:

- **macOS.** Install the compiler with `xcode-select --install`. Start Docker
  Desktop before any `make` target that uses containers. On Apple Silicon
  `make dev-up` and `make stack-up` work because they build the project images
  for your machine; released images are built for amd64 only (see
  [Ports and containers](#ports-and-containers)).
- **Linux.** Install the compiler with `sudo apt install build-essential` (or
  your distribution's equivalent). Add yourself to the `docker` group so you
  need no `sudo`: `sudo usermod -aG docker $USER`, then sign in again.
- **Windows.** Use WSL2 and clone the repository inside the Linux file system.
  The Makefile and the scripts under `deploy/` are POSIX shell; they do not run
  in PowerShell or `cmd`.

The ports the project uses on your machine:

| Port | Who | When | Setting in `deploy/.env` |
|---|---|---|---|
| 5432 | Core database (PostgreSQL) | `make dev-up`, on loopback | `CORE_DB_PORT` |
| 5433 | Game cluster (PostgreSQL) | `make dev-up`, on loopback | `GAME_DB_PORT` |
| 5434 | Test game cluster | `make test-game` and friends only | `GAME_TEST_DB_PORT` |
| 6379 | Redis | `make dev-up`, on loopback | `REDIS_PORT` |
| 8080 | Core API | `make run` | none: `HTTP_ADDR` is read from the shell |
| 9090 | API metrics, `/healthz`, `/readyz` | `make run` | none: `INTERNAL_ADDR` is read from the shell |
| 9100, 9101 | Query Runner (gRPC, metrics), on loopback | `make runner` | the port of `QUERY_RUNNER_ADDR` |
| 3000 | Interface | `make front`, `make front-start` | `FRONT_PORT` only sets the origin `make run` accepts (`FRONT_ORIGIN`); it does not move the interface, which listens on `PORT` |
| 80, 443 | Caddy | `make stack-up` | `HTTP_PORT`, `HTTPS_PORT` |
| 8081 | Adminer | `make dev-db-ui` | `ADMINER_PORT` |
| 3001 | Grafana | the observability profile | `GRAFANA_PORT` |

Two ways to run it, and they do not mix: **development** (`make dev-up`, then
`make run`, `make runner` and `make front` on your machine, with only the
databases and Redis in containers) and the **stack** (`make stack-up`, every
service in a container behind Caddy). The entries below say which one they are
about when it matters.

## Getting the code

### A ZIP download has no `.git`

**Symptom.** Nothing fails. `git describe` is unavailable and the version
stamped into builds is `dev`.

**Cause.** The Makefile takes its `VERSION` from `git describe` and falls back
to `dev`. It only tags locally built images and labels the binaries.

**Fix.** None needed for development or `make stack-up`. `make deploy` and the
other deploy targets refuse `dev` on purpose (`name the version to deploy, for
example:`); they pull released images and need `VERSION=vX.Y.Z`.

### Only part of the repository was downloaded

**Symptom.** In `frontend/`, `npm run error-codes` stops with
`Cannot read ../docs/api/error-codes.json. Regenerate it with `make api-contract`.`
and `npm test` fails reading `../docs/api/audit-actions.json`.

**Cause.** The interface checks its dictionaries against contract files that the
Go backend generates under `docs/api/`, outside `frontend/`.

**Fix.** Keep the whole repository. The interface alone can still be installed
and run (`npm ci && npm run dev`); only those two checks need `docs/api/`.

### `deploy/.env` is not in the clone

**Symptom.** `make` stops with `deploy/.env is missing. Create it first:`.

**Cause.** The file holds secrets and is git-ignored; only
`deploy/.env.example` is committed.

**Fix.** `install -m 600 deploy/.env.example deploy/.env`. Details in
[Configuration](#configuration).

### `node_modules` is a symlink to another checkout

**Symptom.** `make front-install` does nothing although dependencies are wrong,
or the bundler complains about files outside the project.

**Cause.** The target only checks that `frontend/node_modules` exists; a
symlink satisfies it.

**Fix.** `rm frontend/node_modules && make front-install`, so this checkout has
its own install.

## Configuration

Every configuration failure ends the same way: the process prints
`fatal: <message>` to stderr and exits with status 1. The API and the Query
Runner word a settings problem as `fatal: load configuration: <message>`; the
migrate and bootstrap commands print `migrate: <message>` and
`bootstrap: <message>`. In the stack the container then shows as exited, or
restarts in a loop (`api`, `queryrunner`), and the services that wait for it
never start. Read the message, then fix the variable it names.

### The `deploy/.env` file

**Symptom.** `deploy/.env is missing. Create it first:`, from a `make` target
that checks for it (`run`, `runner`, `migrate-up`, `bootstrap`, `game-roles`
and others). `make dev-up` and `make stack-up` do not check; there Docker
Compose says a required variable such as `CORE_DB_USER` is missing a value.

**Cause.** The file was never created, or it was written by hand with fewer
variables than Compose requires. Compose reads the whole file for every command
and every `:?` variable in it is required, including the Grafana password when
you do not use Grafana.

**Fix.** Start from the example, never from scratch:

```bash
install -m 600 deploy/.env.example deploy/.env
```

### `deploy/.env is readable by other accounts`

**Symptom.** `deploy/.env is readable by other accounts, and it holds every
secret. Run:` followed by a `chmod` line. Group-readable and world-readable
files are both refused.

**Cause.** A plain `cp` leaves the file readable by others.

**Fix.** `chmod 600 deploy/.env`.

### `CORE_DB_PASSWORD is not set in deploy/.env.`

**Symptom.** That sentence, from any target that checks the file.

**Cause.** The value is empty.

**Fix.** Give it any non-empty value. In development the placeholders
(`change-me-before-first-run`) are accepted, so a first run needs no edits. See
the next entry before changing them.

### A password with `/`, `@`, `:`, `?`, `#` or `%` in it

**Symptom.** The API stops with `fatal: parse core database DSN: invalid connection string`,
`migrate` and `bootstrap` fail to parse the address, the game-prepare job says
the provisioning DSN is not a URL, or sign-in to a database fails with a
password that looks right.

**Cause.** The database and Redis passwords are placed into connection URLs
(`postgres://user:password@host/...`) without encoding. Base64 output, which is
what the hint at the top of `.env.example` suggests, often contains `/`, and
always may contain `+` and `=`.

**Fix.** Use URL-safe characters for the passwords that sit in URLs
(`CORE_DB_PASSWORD`, `GAME_DB_PASSWORD`, `GAME_READER_PASSWORD`,
`GAME_WRITER_PASSWORD`, `REDIS_PASSWORD`):

```bash
openssl rand -hex 32
```

Base64 is fine for `DEVICE_COOKIE_SECRET`, which is not part of a URL.

### The values in `deploy/.env` are not what Compose and `make` read

**Symptom.** A host-side target (`make migrate-up`, `make run`) cannot sign in
to a database that works in the stack, or the reverse; or a value copied with
quotes or a trailing comment behaves strangely.

**Cause.** Compose and Make parse the file differently: Make reads it as Make
syntax, keeps quote characters, treats `#` as the start of a comment and expands
`$`.

**Fix.** Write each value bare: no quotes, no trailing comment, and no `$` or
`#` inside a password. For an SMTP password that must contain `$`, single-quote
it (`SMTP_PASSWORD='...'`); that only helps the containers.

### `make run`, `make runner` and `make front` ignore most of `deploy/.env`

**Symptom.** You set `SMTP_HOST`, `PUBLIC_URL`, `SELF_SIGNUP_ENABLED`, a sign-in
provider, `DEVICE_COOKIE_SECRET` or a `QUERY_*` limit in `deploy/.env`, restart
the host process, and nothing changes. The API logs `mail is off: SMTP_HOST is not set`.

**Cause.** The Makefile reads `deploy/.env` for its own variables and hands each
host process a fixed list. `make run` passes the database and Redis addresses,
the game-cluster address and author password, the upload and cover directories,
`PUBLIC_ORIGINS`, `QUERY_RUNNER_ADDR`, `TRUSTED_PROXIES`, the Query Runner token,
`ENV=development` and `LOG_LEVEL=debug`. `make runner` passes its two game-role
addresses, its listen addresses and the token. `make front` passes only
`API_ORIGIN`. Everything else comes from your shell. The containers of the stack
read the whole file.

**Fix.** Put the setting in the shell for that command:

```bash
SMTP_HOST=localhost SMTP_PORT=1025 SMTP_TLS=none SMTP_FROM=dev@example.edu \
  PUBLIC_URL=http://localhost:3000 SELF_SIGNUP_ENABLED=true make run
```

For a configuration that reads the whole file, use the stack.

### `API_PORT` does not move the API

**Symptom.** After `API_PORT=8090` in `deploy/.env`, the interface shows
`The server is unreachable. Check your connection.` while the API still listens
on 8080.

**Cause.** `API_PORT` tells `make front` where to look and sets the published
port of the container form of the API. `make run` never passes `HTTP_ADDR`, whose
default is `:8080`.

**Fix.** Leave `API_PORT` at 8080, or move both ends: `HTTP_ADDR=:8090 make run`
with `API_PORT=8090` in the file.

### `still carries the placeholder from deploy/.env.example`

**Symptom.** `fatal: load configuration: CORE_DB_DSN: still carries the placeholder from deploy/.env.example; set a generated value (required outside development)`.
The same sentence names, one at a time, `REDIS_ADDR`, `GAME_PROVISIONER_DSN`,
`GAME_AUTHOR_PASSWORD`, `DEVICE_COOKIE_SECRET`, `QUERY_RUNNER_TOKEN` and the
SMTP and sign-in provider secrets. The Query Runner names `GAME_DB_DSN`,
`GAME_DB_WRITER_DSN` or `QUERY_RUNNER_TOKEN`; the game-prepare job names its
admin DSN or one of the role passwords.

**Cause.** `.env.example` ships `ENV=production` and `change-me-before-first-run`
as every password. Outside development a value containing `change-me` anywhere,
a DSN included, is refused. The API checks the names in no fixed order, so after
fixing one the next start may name another.

**Fix.** Replace every secret in one pass with generated values (see the
password entry above), then start again. For a throwaway local stack only, you can
instead set `ENV=development`, which accepts the placeholders (and, in the
containers, nothing else is relaxed: the stack still sends secure cookies, so
open it over `https://localhost`). Never use it for anything other people reach.

### `ENV` must be exactly `development`

**Symptom.** The refusals above, or `DEVICE_COOKIE_SECRET: required outside development`,
on a laptop where you set `ENV=dev`, `ENV=local` or `ENV=Development`.

**Cause.** Only the exact string `development` counts. Any other value is
treated as production, and `ENV` itself is not validated. A host process
with `ENV` unset counts as development; the stack's `.env.example` sets
`ENV=production`.

**Fix.** `ENV=development`.

### `DEVICE_COOKIE_SECRET` and `QUERY_RUNNER_TOKEN`

**Symptom.** One of `DEVICE_COOKIE_SECRET: required outside development`;
`DEVICE_COOKIE_SECRET: N bytes, at least 32 are required`;
`DEVICE_COOKIE_SECRET and QUERY_RUNNER_TOKEN must be different secrets`;
`QUERY_RUNNER_TOKEN: required outside development ...` (the API asks for it only
when `QUERY_RUNNER_ADDR` is set; the runner always does);
`QUERY_RUNNER_TOKEN: N bytes, want between 32 and 512`.

**Cause.** Both are blank in `.env.example` on purpose. Outside development they
are required, at least 32 bytes, and must differ. The token must be the same on
`api` and `queryrunner`, 32 to 512 printable characters with no spaces.

**Fix.**

```bash
openssl rand -base64 48   # DEVICE_COOKIE_SECRET
openssl rand -hex 32      # QUERY_RUNNER_TOKEN
```

Changing the device secret later makes every remembered browser sign in once
more.

### The interface container exits: `refusing to start: INGRESS_SECRET`

**Symptom.** The `web` container exits at once with
`web: refusing to start: INGRESS_SECRET is not set. ...`; the tail of the first
sentence may instead say it is shorter than 32 characters or still the
placeholder. The message ends by naming `ALLOW_MISSING_INGRESS_SECRET=true`.

**Cause.** A production server (the default in the image and in Compose) needs a
secret of at least 32 characters that Caddy also sets, so that the address Caddy
forwards can be believed. `.env.example` leaves it empty.

**Fix.** In the stack, set it once; Compose gives it to both `caddy` and `web`:

```bash
INGRESS_SECRET=$(openssl rand -hex 32)   # put the output in deploy/.env
```

For a production build on your host with no proxy, set
`ALLOW_MISSING_INGRESS_SECRET=true`; `make front-start` already does. It must
differ from `QUERY_RUNNER_TOKEN`.

### Database and Redis addresses

**Symptom.** `CORE_DB_DSN: required environment variable is not set`,
`CORE_DB_DSN is not set` (migrate, bootstrap), `core database is unreachable: ...`,
`redis is unreachable: ...`, or an error parsing the Redis address.

**Cause.** The wrong host or port, a database that is not up yet, or a leftover
password. The names `pg-core`, `pg-game` and `redis` exist only inside the
Compose network; on your machine use `localhost` and the published port. A value
copied from the stack into a host process fails.

**Fix.** Let `make` build the addresses; it derives them from `deploy/.env`.
Check the container is healthy first
(`docker compose -f deploy/docker-compose.yml ps`). A stale empty `REDIS_ADDR=`
line in an old `.env` silently moves the stack to an in-process cache: delete the
line.

### Other settings that stop the API

**Symptom.** `fatal: load configuration: <NAME>: <reason>` for a value that is
out of range or malformed, for example `CORE_DB_POOL_MAX: N is outside [0, 100]`,
`COOKIE_SECURE:` followed by Go's boolean parse error for a value such as `yes`,
`TRUSTED_PROXIES: "x" is neither a CIDR nor an address`,
`LOG_LEVEL: unknown level "x", ...`, or
`INTERNAL_ADDR: must differ from HTTP_ADDR (both are ":8080")`.

**Cause.** Settings are validated at start; a bad one refuses the start instead
of being ignored.

**Fix.** Correct the variable the message names. Booleans take Go's spellings
(`true`, `false`, `1`, `0`, `t`, `f`), not `yes` or `on`. Durations use Go syntax
(`5s`, `15m`). A trusted email domain must be bare and lowercase
(`TRUSTED_EMAIL_DOMAIN=example.edu`); it is refused, not lowercased.

### The Query Runner refuses to start on memory

**Symptom.** `fatal: load configuration: QUERY_CONCURRENT=N needs B bytes of game-cluster memory (...), but GAME_DB_MEMORY_BYTES is M: raise GAME_DB_MEMORY_BYTES or lower QUERY_CONCURRENT`.
Related refusals name `QUERY_MAX_BYTES`, `QUERY_IDLE_CONNS` or a metrics address
that equals the runner's.

**Cause.** The runner checks that the game cluster has room for its concurrent
queries (plus a fixed reserve) inside `GAME_DB_MEMORY_BYTES`. The two values
move together.

**Fix.** If you lower `GAME_DB_MEMORY_BYTES` to fit a small machine, lower
`QUERY_CONCURRENT` with it. The defaults in `.env.example` fit each other.

### The game-prepare job refuses (stack)

**Symptom.** `fatal: reach the game cluster: ...`, a missing `GAME_DB_ADMIN_DSN`
or role password, or one of the memory checks (for example the cluster's memory
limit differs from the configured `GAME_DB_MEMORY_BYTES`). `make stack-up` ends
with a dependency that failed to start: the job must finish before `queryrunner`
starts (the `api` does not wait for it).

**Cause.** After changing `GAME_DB_MEMORY_BYTES` or `GAME_DB_PROCESS_MEMORY_BYTES`
the running `pg-game` container keeps its old limits. The job also fails when
the cluster is down or its password is wrong.

**Fix.** Recreate the cluster so it reads the new values, then run the job again:

```bash
VERSION=$(git describe --tags --always --dirty) \
  docker compose -f deploy/docker-compose.yml up -d --force-recreate pg-game
```

On a host whose containers cannot report their memory limit (cgroup v1), set
`GAME_DB_ALLOW_UNREADABLE_MEMORY_LIMIT=true`. `make game-roles` skips the memory checks
when neither `GAME_DB_MEMORY_BYTES` nor `GAME_DB_PROCESS_MEMORY_BYTES` is in its
environment, which is the case in development.

### `PUBLIC_URL`

**Symptom.** `PUBLIC_URL: required environment variable is not set (mail links point at it)`
(also naming the Microsoft, Google or GitHub redirect), `PUBLIC_URL: "x" is not an absolute address like ...`,
or `PUBLIC_URL: "http://localhost:3000" must be https (http only with ENV=development)`.

**Cause.** `PUBLIC_URL` is required as soon as `SMTP_HOST` or any sign-in
provider is set. It may carry a path but not `?`, `#` or credentials; a trailing
`/` is trimmed. Outside development it must be `https`.

**Fix.** `PUBLIC_URL=https://contest.example.edu`; for development,
`http://localhost:3000`.

### Mail settings

**Symptom.** With `SMTP_HOST` set the API refuses to start, for example
`SMTP_HOST: "smtp.example.edu:587" is not a host name; the port goes in SMTP_PORT`,
`SMTP_PASSWORD: required when SMTP_USERNAME is set` (and the reverse),
`SMTP_TLS: none never carries SMTP_USERNAME and SMTP_PASSWORD; use starttls or tls`,
`SMTP_TLS: none is allowed only with ENV=development`, or
`SMTP_FROM: required environment variable is not set`. With `SMTP_HOST` empty the
API starts and logs `mail is off: SMTP_HOST is not set`.

**Cause.** Mail is all-or-nothing: one bad companion value refuses the start. With
mail off there is no "Forgot password?" and no password sign-up, and
`SELF_SIGNUP_ENABLED=true` logs that password sign-up is not offered.

**Fix.** Host without a port, `SMTP_FROM` and `PUBLIC_URL` set, the username and
password together. For local work, run a catch-all SMTP sink and use
`SMTP_HOST=localhost SMTP_PORT=1025 SMTP_TLS=none` with `ENV=development`
(passed in the shell for `make run`, see above).

### Sign-in provider settings

**Symptom.** `MICROSOFT_CLIENT_SECRET: required when MICROSOFT_CLIENT_ID is set`
(and the same pair for `GOOGLE_` and `GITHUB_`, in both directions), a
client ID that is not an application ID, a `MICROSOFT_TENANT` that is not
`common`, `organizations`, `consumers` or a tenant ID, or the placeholder
refusal for a secret.

**Cause.** A provider's client ID and secret are both-or-neither; half a pair
refuses the start instead of quietly switching the provider off. Each needs
`PUBLIC_URL`, and the redirect registered with the provider must be exactly
`PUBLIC_URL` followed by `/api/v1/auth/microsoft/callback` (or `google`,
`github`).

**Fix.** Fill both or neither, register that callback address, and recreate the
`api` container after a change. [backend/README.md](../../backend/README.md)
has the provider-by-provider setup.

### The interface needs `API_ORIGIN`

**Symptom.** `API_ORIGIN is not set. It is required in production: the interface has no other way to reach the Core API.`

**Cause.** In development the interface defaults to `http://localhost:8080`; a
production build has no default. In the stack Compose sets it to
`http://api:8080`, which is wrong on your machine because `api` exists only
inside Compose.

**Fix.** In the stack, leave it. For a production build on your host:
`API_ORIGIN=http://localhost:8080 make front-start`. To run `npm run dev` by
hand: `cp frontend/.env.example frontend/.env.local`.

## Ports and containers

### Docker is not running, or you may not use it

**Symptom.** Docker reports it cannot connect to the daemon, or that permission
to the Docker socket is denied.

**Cause.** Docker Desktop is not started, or on Linux your account is not in the
`docker` group.

**Fix.** Start Docker Desktop, or `sudo usermod -aG docker $USER` and sign in
again.

### `docker compose` is not a command

**Symptom.** Docker says `compose` is not a docker command, or an old Compose
cannot parse the files.

**Cause.** The Makefile always runs `docker compose` (Compose v2). The files use
the top-level `name:`, `depends_on` with `service_completed_successfully`,
profiles and resource limits.

**Fix.** Install Docker Desktop or the `docker-compose-plugin` package. The
standalone `docker-compose` v1 binary is not supported.

### A port is already in use

**Symptom.** Docker reports a port as already allocated for `127.0.0.1:5432`
(or 5433, 6379, 80, 443). The API says
`fatal: start public server:` followed by the operating system's "address already in use". The
Query Runner says `fatal: listen on 127.0.0.1:9100: ...`.

**Cause.** Something else owns the port: a local PostgreSQL on 5432, a local
Redis on 6379, a web server on 80, another project on 3000, 8080 or 9090. A stale
`go run` child can outlive a killed parent. It is worse for `make migrate-up`
and `make bootstrap`, which connect to `localhost:5432` and would quietly talk
to the other PostgreSQL if it answers.

**Fix.** Find the owner and stop it:

```bash
lsof -nP -iTCP:5432 -sTCP:LISTEN
```

Or move this project's ports in `deploy/.env` (`CORE_DB_PORT`, `GAME_DB_PORT`,
`REDIS_PORT`, `HTTP_PORT`, `HTTPS_PORT`); the Makefile derives its addresses from
them. The API, Query Runner and interface ports are not all configurable from the
file (see the `API_PORT` entry above).

### `make stack-bootstrap` or another stack command cannot find its images

**Symptom.** After `make stack-up` worked, `make stack-bootstrap` (or a
`docker compose -f deploy/docker-compose.yml ...` command typed by hand) tries to
pull `db-contest-backend:dev` from the registry and fails, or says the image tag
does not exist.

**Cause.** `make stack-up` builds and tags the images with the output of
`git describe` (for example `v0.10.1-6-gabc1234` or `abc1234-dirty`). The other
commands do not pass that value, so Compose falls back to the tag `dev`, which
does not exist. Without a `.git` directory both use `dev` and it works.

**Fix.** Pass the same value to every command:

```bash
VERSION=$(git describe --tags --always --dirty) make stack-bootstrap
```

### The subnet is taken

**Symptom.** `make stack-up` fails because the address pool overlaps another
network.

**Cause.** The stack pins its network to `172.28.0.0/16` so that `TRUSTED_PROXIES`
can name `caddy` (`172.28.0.10`) and `web` (`172.28.0.11`). A VPN or another
Compose project may hold that range.

**Fix.** Remove the other network. If you change the subnet instead, change the
two fixed addresses in `deploy/docker-compose.yml` and `TRUSTED_PROXIES` together,
and never widen `TRUSTED_PROXIES` to the whole range.

### A password changed after the first start: authentication fails

**Symptom.** The API (stack) loops on
`fatal: core database is unreachable: ... password authentication failed ...`;
`make migrate-up` and `make bootstrap` fail the same way. Or the database
`dbcontest_core` does not exist after you changed `CORE_DB_NAME`.

**Cause.** The PostgreSQL image applies `CORE_DB_USER`, `CORE_DB_PASSWORD` and the
database name only when the data volume is first created. The usual route: the
first `make stack-up` ran with the example's placeholders, the API refused to
start, you fixed `deploy/.env`, and the database kept the placeholder. The game
roles are not affected (the game-prepare job restates them every run) and neither
is Redis.

**Fix.** While there is nothing to keep, delete the volumes and start again. This
destroys all data:

```bash
docker compose -f deploy/docker-compose.yml down -v        # the stack
make dev-down && docker volume rm db-contest_pg-core-data db-contest_pg-game-data   # development
```

With data you want, change the password inside the database instead
(`ALTER USER ... PASSWORD '...'`).

### The stack starts partially: `dependency failed to start`

**Symptom.** `make stack-up` ends with a dependency that failed to start, or
`web` stays `Created` or `unhealthy`.

**Cause.** The services start in order: databases and Redis (healthy), then
`migrate` (which must exit 0) and then `api`; `game-prepare` (which must exit 0)
and then `queryrunner`; `web` after them, then `caddy`. The first failure in the Configuration
section shows up as everything after it not starting.

**Fix.** Read the failing step:

```bash
docker compose -f deploy/docker-compose.yml logs migrate game-prepare api queryrunner web
```

### `https://localhost` shows a certificate warning

**Symptom.** The browser warns that the certificate authority is not trusted.

**Cause.** With `SITE_ADDRESS=localhost` Caddy issues certificates from its own
local authority, which your browser does not know. The stack must be opened over
`https`, because the session cookie is secure.

**Fix.** Accept the warning once. Keep the default ports on a development
machine: `HTTP_PORT` and `HTTPS_PORT` change only the published host ports, not
the redirect Caddy sends.

### Docker has too few resources

**Symptom.** Docker says the CPU range is limited to the CPUs available, or a
build is killed (exit status 137), or the game cluster is killed under load.

**Cause.** The game cluster is declared with `cpus: 2.0`, a 7 GiB limit
(`GAME_DB_CPUS`, `GAME_DB_MEMORY_BYTES`) and `shared_buffers=1GB`. The stack is
sized for an olympiad.

**Fix.** Give Docker at least 2 CPUs and 8 GiB. For a toy run lower
`GAME_DB_CPUS`; if you lower `GAME_DB_MEMORY_BYTES`, lower `QUERY_CONCURRENT` too
([the runner entry](#the-query-runner-refuses-to-start-on-memory)).

### Released images do not run on an ARM machine

**Symptom.** `make deploy VERSION=...` on Apple Silicon fails because no image
matches the platform.

**Cause.** The published images are built for amd64 only.

**Fix.** Use `make stack-up`, which builds for your machine. To run a release
anyway, accept emulation: `DOCKER_DEFAULT_PLATFORM=linux/amd64 make deploy VERSION=vX.Y.Z`.

## Database

### `make migrate-up` straight after `make dev-up` fails to connect

**Symptom.** `migrate: initialise migration driver: ...` with a refused
connection, or PostgreSQL saying it is starting up.

**Cause.** `make dev-up` returns as soon as the containers are created; the core
database takes seconds on a first start.

**Fix.** Wait until it is healthy, then run it again (`migrate up` is safe to
repeat):

```bash
docker compose -f deploy/docker-compose.yml ps pg-core
make migrate-up
```

### A migration left the database dirty

**Symptom.** Every later `up`, `down` or `version` stops with
`migrate:` followed by the migration library's complaint that the database version N is dirty and must be forced, and
`make migrate-version` reports `dirty: true`.

**Cause.** A migration that fails or is interrupted leaves its version marked
dirty until you clear it.

**Fix.** Fix the cause first. Then force the **last version that fully applied**,
which is the one before the dirty one (53 when 54 is dirty), and migrate again.
There is no `make` target for `force`:

```bash
cd backend && CORE_DB_DSN='postgres://USER:PASSWORD@localhost:5432/DATABASE?sslmode=disable' \
  go run ./cmd/migrate force 53
cd .. && make migrate-up
```

Forcing the dirty number itself skips that migration. If the failed file had
partly applied, undo that by hand first.

### Migration 54 refuses: accounts that differ only in case

**Symptom.** `make migrate-up` fails with
`live accounts share an email that differs only in case; resolve them before migrating`
and leaves version 54 dirty.

**Cause.** Migration 54 makes email addresses unique regardless of case. It only
meets this on a database that already holds data (an upgrade, or a restored
dump); a fresh development database never does.

**Fix.** List the pairs, change or delete one of each, then force 53 and migrate
as in the previous entry:

```sql
SELECT lower(email), array_agg(login) FROM users
WHERE status <> 'deleted' AND email IS NOT NULL
GROUP BY 1 HAVING count(*) > 1;
```

### The API runs but the database has no tables

**Symptom.** The API starts and `/readyz` is green, then sign-in answers
`The server could not process the request.` and the API log says a relation such
as `users` does not exist.

**Cause.** The API does not check the schema version at start. Either
`make migrate-up` was skipped, or it ran against a different PostgreSQL than the
API uses (see the port entry above).

**Fix.** `make migrate-version` should print the latest migration number (the
highest-numbered file in `backend/migrations`). Run `make migrate-up` against the
database the API uses.

### `make migrate-down` goes back one step only

**Symptom.** One migration is undone per call. Migration 54's down file refuses
while accounts without a password exist; you reach it only after rolling back
57, 56 and 55.

**Cause.** That is how the command works; the guard stops a rollback from leaving
accounts that have no password.

**Fix.** Give those accounts a password first, or use a scratch database for
experiments (`make test-db` recreates its own).

### No game can be built and the console is empty: the game roles do not exist

**Symptom.** A contest's game stays in a failed state with PostgreSQL's own
authentication error for the `game_author` role, and the console shows
`The query service is not answering right now.` ([Running](#running)).

**Cause.** `make dev-up` starts the game cluster with only its administrative
role. The three roles that organisers' scripts and participants' queries run as
(`game_reader`, `game_writer`, `game_author`) are created by a separate step.

**Fix.** Run once after `make dev-up` (it is safe to repeat; run it again after
changing any `GAME_*_PASSWORD`, then restart `make runner`):

```bash
make game-roles
```

### A game build fails or is refused

**Symptom.** One of these answers when an organiser builds or replaces a game:
`game_template_too_large` (the database grew past the size one game may take),
`game_cluster_full` (no room left on the game cluster), `game_script_too_long`,
`game_uploads_disabled` (this installation has no upload directory),
`build_in_progress`, or `game_not_editable` (a running contest's game cannot be
replaced).

**Cause.** Each names a real limit. `GAME_TEMPLATE_MAX_BYTES` (64 MiB to 64 GiB)
caps one template and `GAME_CLUSTER_MAX_BYTES` caps the cluster. A script is
limited to what a request may carry (1 MiB). Uploads need `GAME_UPLOAD_DIR`;
it has no default, uploads are off when it is empty, and `make run` and the
stack set it. A build that never finishes ends after
`GAME_BUILD_TIMEOUT` (default 30 minutes) and drops its template.

**Fix.** For a script that is too long, generate rows instead of writing them:
`INSERT ... SELECT generate_series(...)`, or upload a dump. For a limit, raise the
named setting and rebuild.

### The API cannot create its directories

**Symptom.** `fatal: open the cover directory: mkdir /var/lib/dbcontest: permission denied`
or `fatal: open the upload directory: ...`.

**Cause.** `COVER_DIR` defaults to a path inside the container, which a
process on your machine cannot create, and covers are not optional. A
`GAME_UPLOAD_DIR` copied from the stack fails the same way; left empty, an API
started by hand turns uploads off and answers `game_uploads_disabled`.

**Fix.** Start it with `make run`, which points them at `deploy/covers` and
`deploy/game-uploads`, or set both to directories you can write. In the stack the
image prepares its directory for user 65532; a custom path needs a directory that
user owns.

### The API cannot reach the game cluster

**Symptom.** `fatal: connect to the game cluster: core database is unreachable: ...`.

**Cause.** `GAME_PROVISIONER_DSN` is wrong or `pg-game` is down. The wording says
"core database" because the same helper opens every PostgreSQL pool; the cluster
meant is the game cluster.

**Fix.** Check that `pg-game` is healthy and that `GAME_DB_PORT` matches. `make run`
builds the address from `GAME_DB_USER`, `GAME_DB_PASSWORD`, `GAME_DB_PORT` and
`GAME_DB_NAME`.

## Frontend

### The first `make front` is slow, and stale after a pull

**Symptom.** The first run spends a while installing; after pulling new commits
the build says a module cannot be resolved.

**Cause.** `make front-install` runs `npm ci` only when `frontend/node_modules` is
missing. It never notices a changed `package-lock.json`.

**Fix.**

```bash
cd frontend && npm ci
```

Use `npm ci`, not `npm install`, so the lockfile is not rewritten.

### Node is too old

**Symptom.** Next.js says your Node.js version is below its minimum, or `npm ci`
fails.

**Cause.** The repository does not pin Node. The project is built and tested on
Node 22.

**Fix.** Install Node 22 LTS.

### Type checking fails on a fresh checkout

**Symptom.** Running `npx tsc --noEmit` directly cannot find
`./.next/types/routes.d.ts`, or `PageProps` is undefined.

**Cause.** `next-env.d.ts` and `.next/types` are generated by Next and
git-ignored.

**Fix.** `npm run typecheck` (it generates them first) or `make front-check`.

### `make front-check` fails on a contract

**Symptom.** `npm run error-codes` reports a code with no sentence in a locale,
or says it cannot read `../docs/api/error-codes.json`.

**Cause.** The interface's three locales must hold a sentence for every error
code the API can answer; the code list is generated from the Go source.

**Fix.** Run `make api-contract` to regenerate the file, then add the missing
sentence to every locale ([5. Frontend](05-frontend.md) walks through it).

### `make front-start`: `/api/*` answers an HTML 404

**Symptom.** Pages and sign-in work under `make front-start`, but requests the
browser sends straight to `/api/*` (cover images, logos, upload chunks) get the
interface's HTML 404.

**Cause.** The `/api/*` rewrite is computed when `next build` runs, and it adds
no rule when `API_ORIGIN` is not set at that moment. `make front-start` passes
`API_ORIGIN` only to the start step, not to the build that precedes it.

**Fix.** Set it in the environment so the build sees it:

```bash
API_ORIGIN=http://localhost:8080 make front-start
```

### `npm run start` on its own refuses

**Symptom.** `No standalone build at ... Run "npm run build" first.`, or
`web: refusing to start: INGRESS_SECRET is ...`.

**Cause.** `npm run start` serves a production build, which must exist, and a
production server needs the ingress secret.

**Fix.** Use `make front-start`, which builds first and sets
`ALLOW_MISSING_INGRESS_SECRET=true` and `COOKIE_SECURE=false` for a plain-http
run on your machine.

### Every list says the server did not answer

**Symptom.** Lists show `The server did not answer. Check the connection and try again.`
and toasts say `The server is unreachable. Check your connection.`

**Cause.** The API is not running at `API_ORIGIN`, or the interface was given the
Compose hostname `api`, which does not resolve on your machine.

**Fix.** Start `make run`, then check it:

```bash
curl localhost:8080/api/v1/version
curl localhost:9090/readyz
```

### Opening the interface by another name breaks uploads

**Symptom.** The API answers 403 `cross_origin` (`The request did not come from this site. Reload the page and try again.`)
to uploads and other writes the browser sends directly, when the page was opened
as `http://127.0.0.1:3000` or by your machine's network address.

**Cause.** `make run` accepts exactly `http://localhost:3000` as the page's origin.

**Fix.** Open `http://localhost:3000`, or start with
`FRONT_ORIGIN=http://<name>:3000 make run`. In the stack the interface and the
API share Caddy's address, so this cannot happen.

## Running

### The build looks hung

**Symptom.** No output for minutes after `go: downloading ...`, with the CPU busy.

**Cause.** The first build compiles PostgreSQL's parser; later builds are cached.
The same applies to the image build in `make stack-up`.

**Fix.** Wait.

### The compiler or cgo is missing

**Symptom.** `make runner`, `make check`, `make test` or `make build` stop because
Go cannot find a C compiler (on macOS, a missing developer tools path from `xcrun`).

**Cause.** The Query Runner and everything that links the SQL checker use cgo.
Only the API, migrate and bootstrap build without it.

**Fix.** macOS: `xcode-select --install`. Debian or Ubuntu:
`sudo apt install build-essential`.

### `CGO_ENABLED=0` is set in your shell

**Symptom.** `make runner` or `make check` fails inside the SQL parser package,
saying no Go files match.

**Cause.** A global `CGO_ENABLED=0` disables the one package the runner cannot do
without.

**Fix.** `unset CGO_ENABLED`, or `CGO_ENABLED=1 make runner`.

### Go is too old

**Symptom.** Go cannot read `backend/go.mod`, or says the module requires a newer
Go; with the default `GOTOOLCHAIN=auto` the first command stalls downloading a
toolchain.

**Cause.** `go.mod` pins an exact patch release (1.26.11), and the Dockerfiles use
the same one.

**Fix.** Install Go 1.21 or newer and let it download the toolchain (the network
and `GOPROXY` must be reachable), or install 1.26.11 directly.

### Where is the administrator's password?

**Symptom.** `make bootstrap` ran, but you do not have the password; a second run
prints only `administrator already exists, nothing to do`.

**Cause.** The first run prints the generated password alone on a line, between the
log lines `administrator created` and `this password is shown once and must be changed at first sign-in`.
It is never shown again, and no command resets the first administrator's password.

**Fix.** While the database holds nothing you need, create another administrator
with a different login (`make bootstrap ADMIN_LOGIN=root2`) or delete the volumes
and start again (see the password-changed entry). In the stack the password is
printed to the terminal of `make stack-bootstrap`, not to `docker logs`.

### `Wrong login or password.` on the first sign-in

**Symptom.** `Wrong login or password.` right after bootstrap.

**Cause.** The login is `admin` (or your `ADMIN_LOGIN`), not an email address; the
password was copied together with log text or a trailing space; or bootstrap ran
against a different database than the API uses.

**Fix.** Copy only the password line, sign in with the login, and check both
commands use the same database (`make migrate-version`, the port entry above).

### `Change the temporary password to continue.`

**Symptom.** After the first sign-in every page leads to a password form.

**Cause.** Expected. A generated password must be changed before anything else
works.

**Fix.** Set a new password on the form the interface shows.

### `Too many attempts. Wait and try again.`

**Symptom.** A 429 answer on sign-in.

**Cause.** The sign-in limiter counts failed attempts per account and address
(10 for one account from one address in 15 minutes, 100 for the account from all
addresses) and all attempts per address (300). In development every request comes
from one address. The counters live in Redis, so restarting the API does not clear
them.

**Fix.** Wait 15 minutes, or clear the counters while developing:

```bash
docker compose -f deploy/docker-compose.yml exec redis sh -c 'REDISCLI_AUTH=$REDIS_PASSWORD redis-cli FLUSHDB'
```

This also signs everyone out: it clears the sessions and every other cache key.

### Sign-in succeeds and the next click returns to the sign-in form

**Symptom.** No error; you sign in and land back on the form.

**Cause.** A browser silently discards a `Secure` cookie delivered over plain
http. That happens when the API or the interface runs with production-like
settings (`ENV` not `development`, or `COOKIE_SECURE=true`) while you use
`http://localhost`. The two must also agree: each derives the cookie name from
`COOKIE_SECURE`, and a mismatch makes the interface look for the wrong cookie.

**Fix.** `make run`, `make front` and `make front-start` already set development
values. In the stack open `https://localhost` rather than `http://`. If you set
`COOKIE_SECURE`, set it identically for `api` and `web`.

### The console says `The query service is not answering right now.`

**Symptom.** The play screen shows that sentence (the API answers
`query_service_down`, HTTP 503), and the API log says
`the query service could not be reached`.

**Cause**, most likely first:

1. `make runner` was never started. The Query Runner is a third host process,
   not optional for the play screen.
2. The runner is up, but `make game-roles` was never run, so it cannot
   authenticate as `game_reader`.
3. `QUERY_RUNNER_TOKEN` differs between the API and the runner. The runner logs
   `query runner refused a call without a valid token`.
4. `QUERY_RUNNER_ADDR` names a host that exists only in Compose
   (`queryrunner:9100`) while the API runs on your machine.
5. In the stack, `queryrunner` is not healthy; look at `game-prepare` first.

`/readyz` does not check the runner, so a green `/readyz` proves nothing here.

**Fix.** In development, in this order:

```bash
make game-roles   # once
make runner       # its own terminal
make run          # after the runner
```

Use the same token for both or none (in development both run without one and
warn).

### A query is refused or limited

**Symptom.** The console answers that you are sending queries too quickly, that
the result is too large, or `query_busy`.

**Cause.** Each is a deliberate limit: 60 queries a minute per participant
(`QUERY_PER_MINUTE`), 1000 rows and 5 MiB per result (`QUERY_MAX_ROWS`,
`QUERY_MAX_BYTES`), a 5 second deadline per query (`QUERY_DEADLINE`), 12 answers
a minute per registration. A refused query still counts against the rate.

**Fix.** Wait a moment, or narrow the query with `WHERE` or fewer columns. The
API and the runner read the same `QUERY_PER_MINUTE`; in development set it in the
shell for both processes.

### Mail does not send

**Symptom.** No password-setup, sign-up or reset mail; "Forgot password?" or the
sign-up link is missing from the sign-in page.

**Cause.** Mail is off (log `mail is off: SMTP_HOST is not set`; remember that
`make run` does not read `SMTP_*` from `deploy/.env`), or the relay refuses
messages (the answer is in the API log and in the administrator's test message),
or a startup refusal (see Configuration), or the per-minute cap
(`SMTP_MAX_PER_MINUTE`) is reached.

**Fix.** Send the administrator's test message first; check the network path to
the relay (`nc -vz smtp.example.edu 587`); for local work use a catch-all SMTP
sink with `SMTP_TLS=none` and `ENV=development`.
[backend/README.md](../../backend/README.md) lists the relay's replies and what
each means.

### A participant signs in but the contest has no game

**Symptom.** `no_game_yet` (`This contest's database has not been prepared yet.`),
an empty schema panel, or the query-service message above.

**Cause.** The contest has no built game, or the runner or the roles are missing.
After restoring a backup or losing `pg-game`, a contest can record a template as
built that the cluster no longer has.

**Fix.** Build the game for the contest as an organiser. Run `make game-roles` and
`make runner` if they are missing. `make game-check` (it runs inside the `api` container, so it needs the stack)
lists contests whose built template is missing on the cluster.
[7. Running a contest](07-running-a-contest.md) covers the repair.

### A large story, script or file is rejected

**Symptom.** Saving a very large Markdown story, script or CSV fails with a
generic `Something went wrong. Try again.`, or Caddy answers 413, or the API
answers `invalid_request`.

**Cause.** Three layers each allow 1 MiB: Caddy on everything but `/api/*`, the
interface's server actions, and the API's JSON body. Dumps and CSV files do not
go through them; they are uploaded in chunks straight to `/api`.

**Fix.** Keep scripts under 1 MiB (generate rows with `generate_series`), or
upload a dump. Raising the limit means changing the Caddyfile and the
interface's server-action limit together. A cover picture has its own bound:
up to 8 MiB, 8000 pixels on a side and 40 megapixels. A settings image (a logo)
is limited to 512 KB and 4096 pixels on a side.

### Signing in with Microsoft, Google or GitHub fails

**Symptom.** After the round trip to the provider:
`Signing in through that service did not work. Try again.`, or a message that the
account is not linked and new accounts cannot be created, or that the address
belongs to an organiser's or administrator's account.

**Cause.** The callback address registered with the provider does not match
`PUBLIC_URL` plus `/api/v1/auth/<provider>/callback`; the client secret expired;
the tenant or home-domain rule does not admit the account; an administrator
switched the provider off; or `SELF_SIGNUP_ENABLED` is false and the account does
not exist yet.

**Fix.** Compare the registered callback with `PUBLIC_URL`, renew the secret,
and check the provider's switch on the administrator's Integrations page.

### Every visitor shares one sign-in budget

**Symptom.** Many unrelated people get `Too many attempts` at once, and the audit
trail and logs show one address.

**Cause.** The client address is believed only from `TRUSTED_PROXIES`, and the
interface's forwarded address only with a valid `INGRESS_SECRET`. A wrong list, a
missing secret, or an edge proxy that is not declared collapses everyone into one
address.

**Fix.** Keep the shipped `TRUSTED_PROXIES`, set the same `INGRESS_SECRET` for
`caddy` and `web`, and behind an edge proxy set `SITE_ADDRESS=http://<host>` and
`EDGE_TRUSTED_PROXIES=<edge address>` (`make edge-check` proves the setup).

## Still stuck

Collect what the failing part says before asking anyone.

Development:

- The host processes (`make run`, `make runner`, `make front`) write to their own
  terminal; copy from there, or run them with `2>&1 | tee run.log`.
- The infrastructure containers: `make dev-logs` (follows PostgreSQL and Redis).
- State of the containers: `docker compose -f deploy/docker-compose.yml ps`.
- Probes: `curl localhost:9090/healthz` and `curl localhost:9090/readyz`.
- The schema version: `make migrate-version`.

The stack:

```bash
docker compose -f deploy/docker-compose.yml ps
docker compose -f deploy/docker-compose.yml logs --tail=200 migrate game-prepare api queryrunner web caddy
```

Add the `VERSION=$(git describe --tags --always --dirty)` prefix that
[the images entry](#make-stack-bootstrap-or-another-stack-command-cannot-find-its-images)
explains if Compose complains about an image.

Report what you found in the repository's GitHub issues. Say which way you ran it
(development or the stack), your operating system, the versions of Go, Node and
Docker Compose, the command that failed, and the log lines around the first
`fatal:`. Before pasting, remove every secret from the output: the contents of
`deploy/.env`, passwords, tokens and connection addresses that carry them. A
security problem goes to the contact in [SECURITY.md](../../SECURITY.md), not to a
public issue.

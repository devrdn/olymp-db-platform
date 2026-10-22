# Core API

Backend of the DB Contest platform: the Core API (`cmd/api`), which serves
`/api/v1`, and the Query Runner (`cmd/queryrunner`), a separate process that
checks and runs participants' SQL on the game cluster. For how the code fits
together, a worked package and recipes for common changes, see the
[developer guide, chapter 4](../docs/guide/04-backend.md).

## Layout

```
backend/
├── cmd/                 entry points, thin: flags, signals, exit codes
│   ├── api/             Core API server
│   ├── queryrunner/     Query Runner (cgo: links PostgreSQL's parser)
│   ├── migrate/         schema migrations (embedded)
│   ├── bootstrap/       creates the first administrator
│   ├── gamedb/          prepares the game cluster's roles, on every deploy
│   ├── gameorphans/     finds and removes dropped databases and orphaned covers
│   ├── gamecheck/       finds built templates the game cluster does not have
│   ├── testdb/          recreates the test database for `make test-db`
│   ├── consoleload/     load test of the SQL console
│   └── apicontract/, auditcontract/   generate docs/api/*.json
├── internal/
│   ├── app/             composition root: builds the graph, runs it, tears it down
│   ├── api/             HTTP surface: routes, handlers, error table, codes
│   ├── auth/, rbac/, users/, audit/, settings/
│   │                    accounts, sessions, permissions, the trail, installation settings
│   ├── contests/        contests, content, staff, participants, the participation gate
│   ├── leaderboard/, monitor/, profile/, showcase/, covers/, workspace/
│   │                    standings, watching participants, own profile, front page,
│   │                    cover pictures, notes and SQL tabs
│   ├── mailing/         the texts of email, the outbox and the job that delivers it
│   ├── provisioning/, gamefile/, gamedb/
│   │                    game templates and per-participant databases, uploaded dumps,
│   │                    the game cluster's roles and grants
│   ├── queryproxy/      the Core API's side of a query: who, whether, which database
│   ├── queryrunner/, sqlpolicy/, rpc/
│   │                    the Query Runner: execution, the SQL policy, the gRPC contract
│   ├── health/          liveness, readiness, self-probe
│   ├── postgres/        every core-database repository; all core SQL lives here
│   └── platform/        infrastructure, imports no domain package: cache, config,
│                        filestore, flight, httpx, i18n, logging, mail, metrics,
│                        password, sentineltest, server, storage
├── proto/               the Core API to Query Runner contract (generated code in internal/rpc)
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
| `SESSION_ACCOUNT_CACHE_TTL` | no | `5s` | how long authentication may use a cached copy of the account, 0s to 30s (0s reads it every request); changes made in the application invalidate it at once, a change made directly in the database applies within this long |
| `MAX_LOGIN_ATTEMPTS_PER_ADDRESS` | no | `300` | sign-in attempts from one address per 15 minutes, successes included |
| `MAX_LOGIN_ATTEMPTS_PER_ACCOUNT` | no | `100` | sign-in attempts at one account from all addresses per 15 minutes; the guessing limit itself is 10 per account and address |
| `DEVICE_COOKIE_SECRET` | outside `development` | generated per start in development | HMAC key (at least 32 bytes) of the cookie marking a browser the owner signed in from |
| `DEVICE_COOKIE_TTL` | no | `720h` | lifetime of that cookie, 1h to 2160h |
| `MAX_LOGIN_ATTEMPTS_PER_DEVICE` | no | `10` | sign-in attempts through one trusted browser per 15 minutes, successes included, instead of the address and account limits |
| `MAX_TRUSTED_LOGIN_ATTEMPTS_PER_ACCOUNT` | no | `20` | sign-in attempts through all of one account's trusted browsers together per 15 minutes, successes included |
| `PASSWORD_HASH_CONCURRENCY` | no | one per CPU, at least 2 | argon2id computations run at once (1-64); 64 MiB each, so size the memory limit from it |
| `PASSWORD_HASH_MAX_WAIT` | no | `2s` | how long a sign-in or password change waits for a hashing slot before a 503 `sign_in_busy` (at most `30s`) |
| `ANSWER_RATE_PER_MINUTE` | no | `6` | answers one registration may submit a minute, refused ones included (1-60); past it a 429 `answer_too_often` |
| `EXPORT_CONCURRENCY` | no | `5` | CSV downloads that may hold a core-database connection at once, across every export route (1-10, one fifth of `CORE_DB_POOL_MAX` by default); past it a 503 `too_many_exports` with `Retry-After` |
| `TRUSTED_PROXIES` | no | — | CIDRs whose `X-Forwarded-For` is believed for client IPs |
| `QUERY_RUNNER_TOKEN` | outside `development`, when `QUERY_RUNNER_ADDR` is set | — | secret presented on every call to the Query Runner (32-512 printable ASCII bytes); the runner requires the same value |
| `DEFAULT_LOCALE` | no | `en` | language of last resort; must exist in the `languages` table |
| `COOKIE_SECURE` | no | true outside `development` | mark the session cookie Secure |

### The mail relay

The API sends plain-text mail through one SMTP relay
(`internal/platform/mail`); what it sends, and how to find out why a message
did not arrive, is [Email](#email). Mail is off while `SMTP_HOST` is empty,
and nothing else changes: the service starts and runs without any of these
variables. With `SMTP_HOST` set, a missing or invalid companion aborts the
boot like any other setting. A changed value reaches the API when its
container is recreated, with the tag of the running `api` image that
`make deployed` prints:

```bash
VERSION=<tag> docker compose -f deploy/docker-compose.yml up -d --no-deps api
```

`VERSION` is required, since `deploy/.env` does not set it, and `--no-deps`
keeps the migration job from running; `make deploy-api` would run it. During
a contest, do it at a break: recreating the API drops the live channels,
which reconnect.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `SMTP_HOST` | no | — | the relay's host name, without a port; empty turns mail off |
| `SMTP_PORT` | no | `587` | the relay's port (implicit TLS is usually `465`) |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | together, or neither | — | the relay account; both empty sends no AUTH. Outside `development` a password containing `change-me` is refused, as for every secret |
| `SMTP_FROM` | with mail | — | the sending address, optionally named: `Olimpiada USM <noreply-olymp@usm.md>` |
| `SMTP_TLS` | no | `starttls` | `starttls` (refuses a relay that does not offer it), `tls` for implicit TLS, `none` only with `ENV=development` and never with credentials |
| `SMTP_MAX_PER_MINUTE` | no | `25` | messages handed to the relay in any minute, across every replica (1-600) |
| `PUBLIC_URL` | with mail | — | the site's absolute `https://` address, for the links in messages (`http://` only in `development`) |

USM's relay is Microsoft 365 (Exchange Online) SMTP AUTH client submission,
through the mailbox `noreply-olymp@usm.md`; the mailbox's password comes from
the university's administrators and never enters the repository:

```sh
SMTP_HOST=smtp.office365.com
SMTP_PORT=587
SMTP_TLS=starttls
SMTP_USERNAME=noreply-olymp@usm.md
# From the administrators.
SMTP_PASSWORD=
# The display name is the installation's choice.
SMTP_FROM=Olimpiada USM <noreply-olymp@usm.md>
PUBLIC_URL=https://olympdb.usm.md
```

- Microsoft 365 sends only as the mailbox that signed in: `SMTP_FROM`'s
  address must be the `SMTP_USERNAME` mailbox, or one it has Send As rights
  to. SPF and DKIM are the relay's domain's, so any other address is refused
  or lands in spam.
- Authenticated SMTP must be enabled for that mailbox in the Exchange admin
  center, and the account must not require MFA.
- Microsoft limits a mailbox to 30 messages a minute; `SMTP_MAX_PER_MINUTE`
  stays under it by default.
- Microsoft keeps basic authentication for SMTP AUTH until the end of
  December 2026, when it becomes off by default; OAuth (XOAUTH2) is the
  follow-up ([ARCHITECTURE.md, section 15, item 52](../docs/ARCHITECTURE.md#remaining)).
- STARTTLS and implicit TLS both require TLS 1.2 or later, as Microsoft does.
- Compose expands `$` in an unquoted or double-quoted value of
  `deploy/.env`, so a password containing one goes in single quotes:
  `SMTP_PASSWORD='…'`. Unquoted, it reaches the relay altered, and the
  relay answers 535.

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

Every route sits under `/api/v1` and belongs to one handler in
`internal/api`. Each handler's `Mount` method is where its routes and their
guards live: `Authenticate` for a session, `RequirePermission` for an
installation-wide permission, `RequireContestPermission` for one scoped to
the `{contestID}` in the path. `router.go` registers only `/version` itself
and mounts the handlers that `internal/app` assembles, so any other route
missing from a `Mount` does not exist.

A few routes answer without a session: sign-in, the installation's name and
images (the sign-in screen wears them), the landing page's reads under
`/public`, a contest's public leaderboard and its cover. `GET /contests` is
scoped by who is asking, and `POST /contests/{id}/enroll` is open to any
signed-in account, with the contest's own rules deciding.

The verified map of every route, the screen that calls it, the package that
holds its rule and the permission that guards it is
[docs/guide/02-use-cases.md](../docs/guide/02-use-cases.md). The Postman
collection in [`postman/`](../postman) carries request bodies and expected
response codes for much of the API.

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
   to true: hiding a question is the deliberate choice. A hidden question is
   left out of the participant's list entirely, identifier included, so
   nobody can answer it; staff still see it.
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

## Email

The API writes two messages today: an account's sign-in details, and the test
message an administrator sends themselves. The test message goes to the relay
at once and the screen shows the relay's answer; every other message is queued
in the outbox and leaves in the background, at the pace the relay allows. The
relay is set up in [The mail relay](#the-mail-relay); why the design is what
it is, and its known limits, are in [ARCHITECTURE.md, section
10.2](../docs/ARCHITECTURE.md#102-email).

Every message is plain text, written in English, Romanian and Russian
(`internal/mailing/texts`, one template per message and language), and the
installation sends all of its email in the one language chosen on the
Installation screen (`installation.mail_language`, Romanian until somebody
changes it). The sign-in link in a message is `PUBLIC_URL` followed by
`/login`.

### What an administrator sees

- **Installation** (`/settings`): the **Email** block says whether mail is on
  and the address it is sent as, or that it is off because SMTP is not
  configured. While it is on, **Send a test message** sends the test message
  to the address on the administrator's own account. **Email language** in
  the form above it is the installation's mail language; the contact address
  set there is printed in every sign-in message.
- **Accounts** (`/users`): creating an account, importing a roster, resetting
  one account's password and the bulk reset each offer **Send sign-in details
  by email** while mail is on, and nothing while it is off. One account's
  page offers it only for an active account with an address. Every password
  is still shown on the screen, emailed or not. The answer says how many
  messages were queued and how long sending them takes, about
  ⌈N / `SMTP_MAX_PER_MINUTE`⌉ minutes for N messages (longer when other
  email is already waiting), and lists the accounts that were not emailed with
  the reason.
- **Audit** (`/audit`): `user.sign_in_details_emailed` for every account
  whose sign-in details were queued, never with the password; and
  `settings.mail_test` for every test message, with its outcome and the
  relay's reply code, but not the reply's text, which can quote the address.
- **The recipient** gets the message from `SMTP_FROM`: their login, their
  one-time password, the sign-in link and, when one is set, the
  installation's contact address. The first sign-in asks them to choose
  their own password, as with a password handed over in person.

### The test message

`POST /settings/mail-test` (`settings.manage`) sends the test message at
once, not through the outbox, to the address on the caller's own account and
never another, and answers with how the relay took it: the address it went
to, or a 422 carrying the relay's reply as `subject` (its SMTP code, its
enhanced code and at most 200 bytes of its text):

- `mail_relay_failed`: the relay could not be reached or secured, or refused
  the sending account or its sender address. The fault is in the SMTP
  settings or the mailbox.
- `mail_refused`: the relay took the account and refused this message, most
  often for its recipient.
- `mail_no_address` (409): the caller's account has no address to send to.
- `mail_off` (409) while mail is off, and `mail_test_too_often` (429) past
  three a minute for one administrator, relay refusals included.

The relay's answers are a 422, not a 5xx: the API worked, and a relay that
refuses an administrator's test must not page as an API outage
(`DBContestAPIErrors`). A test message is not counted in the mail metrics
either, which describe the outbox. `GET /settings/all` says whether mail is on
and the address it is sent as.

### Sign-in details

`POST /users`, `POST /users/import`, `POST /users/{userID}/password-reset`
and `POST /users/bulk/password-reset` take `send_by_email`. With it set, each
account that has an address is queued the sign-in details message in the
transaction that issues its password, so the message and the password commit
or roll back together.

- An account that is not active (a blocked one) is never emailed, judged
  again inside the transaction.
- Each password in the answer says `emailed`, or `email_skipped_reason`:
  `not_active`, or `no_address` for an account without an address.
- With mail off the option is refused with `mail_off` (409) before anything
  changes: no account is created and no password is reset.
- A message that cannot be written changes nothing; an import stops at that
  row.
- `GET /users/mail-status` (`users.manage`) tells the accounts screens
  whether mail is on and how many messages leave a minute, and nothing about
  the relay.

**A new password supersedes the old message.** Issuing an account a new
password, emailed or not, by a reset or the bulk reset, gives up that
account's sign-in messages still waiting, and so does blocking or deleting
the account: in the same transaction they are marked abandoned and their
bodies erased. Re-sending sign-in details is a reset with the option ticked.
A message a sender is handing to the relay at that moment is left to finish.

### The outbox

A queued message is a row in `mail_outbox`. The API's background job
`mail-outbox` (`internal/mailing`) sends it within seconds: a queued message
wakes the job, which also runs every ten seconds and at startup. It sends one
message at a time, at most `SMTP_MAX_PER_MINUTE` in any minute across every
replica (the last minute's claims are counted in the table, under an
advisory lock), so five hundred messages leave over twenty minutes at the
default 25. Each replica claims a message with `FOR UPDATE SKIP LOCKED` and a
five-minute lease, so no two send the same one, and a process that dies
mid-send leaves the message due again when the lease ends.

What can become of a message:

| It is | When | Its body |
|---|---|---|
| queued | a request has committed it | kept |
| sent | the relay accepted it | erased |
| retried | the relay deferred it, or refused it without naming the recipient: tried again after 1, 2, 4, 8, 16 and 32 minutes, then an hour | kept |
| abandoned | the relay refused its recipient; or its eighth attempt failed; or its next retry would come a day after it was queued | erased |
| superseded (marked abandoned) | a new password was issued for the account, or the account was blocked or deleted ([above](#sign-in-details)) | erased |
| expired (marked abandoned) | it was queued 24 hours ago and is still unsent: each run first abandons those, whatever the relay is doing, and with mail off a sweep every ten minutes does | erased |

The body, which holds a one-time password, is kept only while the message may
still be sent; the table refuses a finished row that keeps one. The last
error is kept, at most 500 bytes, with every long word of the body (the
password, the address) replaced by `[redacted]`. Rows are not deleted: a sent
or abandoned row is the record that the message went, or why it did not.

**When the relay itself fails.** A failure that says nothing about the
message pauses the job instead of charging the message: the relay could not
be reached or secured, refused the account (`535`) or the sender (Exchange's
`5.7.60` or `SendAsDenied`, even at DATA), dropped the connection, or deferred
a message after accepting its recipient (Microsoft's throttling). The message
is put back with its attempt refunded, and the job sends nothing for 30
seconds, then 1, 2, 4 and 8 minutes, then every 15, until a message is
delivered.

**When the relay refuses everyone.** A refusal naming the recipient (RFC 3463
X.1.1, X.1.2, X.1.3, X.1.6, X.1.10, X.2.1 or X.2.2, such as Microsoft's `550
5.1.10` for an unknown address, or a 5xx to RCPT without an enhanced code)
abandons that message alone, and the job goes on to the next. Any other
refusal (a 5xx to DATA, `5.1.8` for the sender, a `5.7.x` policy refusal) is
retried rather than given up. Three different messages refused that way in a
row, with nothing delivered between them, trip a breaker: the job pauses as
for a relay failure, then judges the next messages afresh, and only a delivery
shortens the pause again. A refused recipient neither counts toward the
breaker nor resets it, so a roster's bad addresses never pause the queue.

### Diagnosing

**Start with the test message.** It crosses the same relay with the same
settings, at once, and the screen shows the relay's own words. A test message
that arrives proves the settings, the mailbox and the network; one that lands
in spam is a matter for the mailbox's domain (SPF, DKIM), not for this
service. Check the network from the host first when nothing answers at all:

```bash
nc -vz smtp.office365.com 587    # "succeeded" or "open"; a timeout is a firewall
```

**A wrong address outside the tenant is counted as sent.** Microsoft 365
client submission checks a recipient at RCPT only in the tenant's own domains,
`usm.md` for USM. It accepts any other address, so a mistyped
`student@gmial.com` is marked sent, its body erased, and
`DBContestMailAbandoned` never fires; the failure comes back later as a
non-delivery report ("Undeliverable") to the sending mailbox,
`noreply-olymp@usm.md`, where nothing in this service sees it. After a bulk
send, and again the next morning, read that mailbox; correct each bounced
account's address and reset its password with the option ticked.

**What Microsoft 365 commonly answers.** The enhanced code (the second
number) says more than the first, and the text after it usually names the
cause.

| Reply | What it means | What to do |
|---|---|---|
| `535 5.7.139 Authentication unsuccessful…` | the account was refused: SMTP AUTH is disabled for the mailbox or the tenant, security defaults or a policy demands MFA, or the password is wrong; the rest of the text says which | ask the university's administrators to enable Authenticated SMTP for the mailbox and exempt it from MFA, or correct `SMTP_PASSWORD` |
| another `535`, such as `535 5.7.3 Authentication unsuccessful` | the relay refused `SMTP_USERNAME` or `SMTP_PASSWORD` | correct them |
| `530 5.7.57 Client not authenticated…` | nothing signed in, most often because `SMTP_USERNAME` and `SMTP_PASSWORD` are empty | set both |
| `550 5.7.60 Client does not have permissions to send as this sender`, or `SendAsDenied` (`554 5.2.0 STOREDRV.Submission.Exception:SendAsDeniedException…`) | `SMTP_FROM`'s address is not the signed-in mailbox | make it the `SMTP_USERNAME` mailbox, or have the administrators grant Send As |
| `550 5.1.8 Access denied, bad outbound sender` | Microsoft has restricted the mailbox for sending spam, usually because the account was compromised | the administrators release it in the Microsoft Defender portal (Restricted entities) and change its password; then update `SMTP_PASSWORD` |
| a 4xx, such as `432 4.3.2 STOREDRV.ClientSubmit; sender thread limit exceeded` | throttling: the relay will take the message later | nothing; the job waits and tries again. If it persists, lower `SMTP_MAX_PER_MINUTE` |
| `550 5.1.10 RESOLVER.ADR.RecipientNotFound`, `550 5.1.1` | the recipient's address does not exist. Only a `usm.md` recipient meets this reply; any other bounces later (above) | correct the address on the account and reset its password again with the option |
| no reply: `no such host`, a timeout, `connection refused` | `SMTP_HOST` is wrong, or outbound port 587 is closed | correct the host, or open the port (`nc` above) |
| no reply: a certificate or TLS error | the relay's certificate is not trusted, or something on the network intercepts TLS | the host's network, not the settings |
| `550 5.7.708 Access denied, traffic not accepted from this IP` | the relay's: Exchange refuses connections from the host's address | ask USM's administrators; the job pauses and tries again meanwhile |
| `421 4.7.66 TLS 1.0 and 1.1 are not supported` | the relay's: it saw TLS below 1.2, which this service never offers, so something on the network intercepts TLS | the host's network; the job pauses and tries again meanwhile |

**Where else the replies are.** The API log has every failure with the
relay's reply, under "the relay failed; no message is sent until it is tried
again", "taken for the relay refusing everyone", "the relay did not take a
message; it will be tried again" and "a message was abandoned and will not be
sent". `mail_outbox.last_error` keeps the last one per message. Neither
carries the list of recipients the mail library appends to a failure, though
a reply that quotes the address itself, as Microsoft's `5.1.10` does, may
still name it. This lists what is still waiting, and what was given up in
the last day, without the recipients or the bodies:

```bash
docker compose -f deploy/docker-compose.yml exec -T pg-core \
  psql --username=<CORE_DB_USER> --dbname=<CORE_DB_NAME> -c "
    SELECT id, kind, attempts, created_at, next_attempt_at, abandoned_at, last_error
    FROM mail_outbox
    WHERE sent_at IS NULL AND (abandoned_at IS NULL OR abandoned_at > now() - interval '1 day')
    ORDER BY id"
```

A superseded message's `last_error` reads "superseded: a newer one-time
password was issued" or "the account was blocked or deleted"; an expired
one's, "abandoned unsent: queued more than 24h0m0s ago".

**The metrics,** on the internal listener while mail is on:

| Metric | Type | What it says |
|---|---|---|
| `mail_messages_total` (`kind`, `outcome`) | counter | delivery attempts by how they ended: `sent`, `retry` or `abandoned`; superseded messages are not delivery outcomes and are not counted |
| `mail_relay_failures_total` | counter | attempts that failed at the relay, and breaker trips |
| `mail_relay_failing` | gauge | 1 while this process has paused sending for the relay, 0 once the relay next answers for a message |
| `mail_outbox_due` | gauge | messages due and not claimed at the job's last run; a count of the shared table, so read it with `max()` |

`max(mail_outbox_due)` falling by about `SMTP_MAX_PER_MINUTE` a minute is a
bulk send going out. The dashboard has no email panel; ask Grafana's Explore.

**The alerts** ([the table](#alerts-and-the-dashboard)) are both warnings:
`DBContestMailAbandoned` fires on any message abandoned in the last hour,
each one a person who will not get it, and `DBContestMailRelayFailing` on ten
minutes of a paused relay. Their descriptions repeat where to look. Once the
cause is fixed, sign-in details that were abandoned are sent again by
resetting those passwords with the option ticked.

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

The compose stack runs Redis as an ordinary service: set `REDIS_PASSWORD` in
`deploy/.env` and **leave `REDIS_ADDR` unset.** (It was behind a `shared`
profile, which nothing in the deploy path activated while the API's default
address already named the service — so a stock deployment pointed the API at a
container that was never started, and an unreachable cache is fatal.) That
single password then produces two different, both correct, addresses on its
own:

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

### Finding the metrics endpoints from the Docker API

The `api` and `queryrunner` containers carry labels in
`deploy/docker-compose.yml` that say where their metrics are, so a collector
need not be given our service names. `prometheus.io/scrape: "true"` says there
is something to scrape; `prometheus.io/port` is the private port the endpoint
listens on (`9090` for the API, `9101` for the runner: the ports
`INTERNAL_ADDR` and `QUERY_RUNNER_METRICS_ADDR` bind); `prometheus.io/path` is
the path on it (`/metrics`); `prometheus.io/job` is the job name to record the
series under (`core-api`, `queryrunner`), used only by a collector with a rule
that reads it.

No collector acts on these labels by itself. Grafana Alloy and Prometheus
with Docker service discovery expose a container's labels and ports to relabel
rules; a rule the operator writes keeps the containers whose `scrape` is
`true` and points the address at the labelled `port`. `scrape`, `port` and
`path` are the labels such rules for this convention usually read. `job` is
not among them, so without a rule that reads it the series are recorded under
whatever job name the collector chose. The job names here are the ones
[`deploy/observability/prometheus.yml`](../deploy/observability/prometheus.yml)
scrapes, so a query selecting on `job` works unchanged when a collector does
read them. The bundled jobs also attach a `service` target label, which a
label-discovered target does not have, so a query selecting on `service` does
not carry over. The ports are not published, so only a collector on the same
Docker host or network reaches them.

Whether a labelled endpoint exists depends on one variable per service:
`METRICS_BACKEND` for the API, `QUERY_RUNNER_METRICS_ADDR` for the runner. What
an install must do with the label when it changes the variable is written beside
the labels in `deploy/docker-compose.yml`, where a test also holds it.

### Query Runner metrics

The Query Runner serves `/metrics` over plain HTTP on a listener of its own,
`QUERY_RUNNER_METRICS_ADDR`: `127.0.0.1:9101` by default, so `make runner`
keeps it on loopback (`curl localhost:9101/metrics`), and `:9101` in
`deploy/docker-compose.yml`. Like the API's `9090`, the port is never
published: it is reachable only on the compose network, by the optional
Prometheus (job `queryrunner`, target `queryrunner:9101`) or the host's
collector. It is a separate port because the gRPC port (`9100`) asks every
caller for `QUERY_RUNNER_TOKEN`, which a scraper does not carry. The runner
always records into Prometheus; `METRICS_BACKEND` is the API's setting.

`QUERY_RUNNER_METRICS_ADDR=off` (exactly that lowercase word; an empty value
means the default) turns the listener off, and the runner logs one line saying
metrics are not served. Any other value is an address, and an address that
cannot be bound, a port conflict on `9101` for one, stops the runner from
starting. That is deliberate: whoever names an address asked for metrics, and a
refusal at startup is instant and visible, where a runner that silently has no
observability during a contest is not.

With metrics off, also drop the `queryrunner` job from `prometheus.yml` (or from
the collector) and the `DBContestQueryRunnerDown` and
`DBContestQueryRunnerNotScraped` rules from `dbcontest-rules.yml`, with their
cases in `dbcontest-rules.test.yml`: until those go too, promtool's unit tests
fail in `make observability-check`, which CI runs as its alert rules and
dashboard job. On the compose stack `deploy/.env` does not reach the container,
so set the literal in `deploy/docker-compose.yml` to `"off"` and the service's
`prometheus.io/scrape` label to `"false"`. Left in place, the job reports the
runner down and `DBContestQueryRunnerDown` pages critically that participants
cannot run queries, and its annotation says to start the runner again, which
does nothing to a running container; an operator who restarts it instead drops
every running query and live channel mid-contest. With the job dropped and the
rules kept, `DBContestQueryRunnerNotScraped` fires five minutes later.

Every label is a fixed set the runner declares at startup, never a contest,
participant, database or SQL value. A value outside that set is counted as
`other`, so a series labelled `other` means the runner reported something it
never declared. Every declared series is exported at zero from the start.

| Metric | Type | Labels | Recorded |
|---|---|---|---|
| `queryrunner_queries_total` | counter | `outcome`, `reason` | once per query, however it ended (`queryrunner.Runner.Run`) |
| `queryrunner_query_duration_seconds` | histogram | `outcome` | for an admitted query, from admission to its end, connecting included |
| `queryrunner_admission_wait_seconds` | histogram | — | for a query that waited for a slot, whether it got one (zero when admitted at once), timed out or was abandoned by its caller; a query refused at the gate's door never waited |
| `queryrunner_active_queries` | gauge | — | queries holding a running slot, read from the gate at scrape time |
| `queryrunner_admission_queue_depth` | gauge | — | queries waiting for a slot, read from the gate at scrape time |
| `queryrunner_result_cutoffs_total` | counter | `limit` | where an answer is cut to the result budget |

`outcome` is the query log's status, so a dashboard and the journal agree:

| `outcome` | `reason` |
|---|---|
| `ok` | empty |
| `rejected` | `rate_limited` (`QUERY_PER_MINUTE`), `busy` (every slot and the queue full), `already_running` (the participant's previous query is still running), `disk_full` (the database is at its quota), or the checker's refusal code (`parse_error`, `function_not_supported`, … — `sqlpolicy.Codes()`) |
| `timeout` | `queue` (waited past `QUERY_QUEUE_WAIT`), `deadline` (ran past `QUERY_DEADLINE`) |
| `error` | `database` (PostgreSQL refused the query), `internal` (this service failed, including a connection it could not open), `cancelled` (the caller stopped waiting), `result_too_large` (see `socket` below) |

`limit` is `rows` (`QUERY_MAX_ROWS`), `bytes` (`QUERY_MAX_BYTES`, counted
over the values read), `socket` (the read budget on the connection refused the
answer before any of it could be kept; the query also ends as
`error`/`result_too_large`) or `rendered` (the gRPC server's count of the
rendered answer cut it further; bytea, for one, doubles in hex). One answer can
be counted under two limits when both cut it.

Useful starting points: `sum by (outcome, reason)
(rate(queryrunner_queries_total[5m]))` for what participants meet,
`histogram_quantile(0.95, sum by (le)
(rate(queryrunner_admission_wait_seconds_bucket[5m])))` for how long a round's
queries queue, and `queryrunner_admission_queue_depth` against
`QUERY_QUEUE_DEPTH`: a full queue answers `busy` at once.

### Alerts and the dashboard

[`deploy/observability/import/`](../deploy/observability/import) holds files
to load into a Prometheus and a Grafana you already run, whatever scrapes the
services (the host's Alloy in production). Nothing in the repository deploys
them, and the collector needs both targets, `api:9090` and `queryrunner:9101`,
on the compose network.

| File | What it is |
|---|---|
| `dbcontest-rules.yml` | recording and alert rules, a standard Prometheus rules file |
| `dbcontest-rules.test.yml` | promtool unit tests for those rules; not a rules file, do not load it |
| `dbcontest-dashboard.json` | the Grafana dashboard |

**The selector is the one thing to edit.** The first group of the rules,
`dbcontest-selector`, records `dbcontest:target` from two matchers,
`up{job="core-api"}` and `up{job="queryrunner"}`, and every alert reaches the
services through it. Change them to the labels your collector gives the two
targets; in Grafana's Explore, `count by (job, instance)
(game_instances_reclaimed_total)` names the API's and `count by (job, instance)
(queryrunner_active_queries)` the Query Runner's. Add an instance or
environment matcher when one Prometheus scrapes more than one deployment.
Loading the file unedited is safe: a matcher that names no target fires
`DBContestAPINotScraped` or `DBContestQueryRunnerNotScraped` five minutes later
instead of leaving the other alerts quietly blind.

**Loading the rules.**

- Prometheus: copy the rules file next to its configuration, list it under
  `rule_files`, and reload (`SIGHUP`, or `POST /-/reload` with
  `--web.enable-lifecycle`). Its `/rules` page then lists four groups.
- A Mimir, Cortex or Loki ruler behind a Grafana data source (data-source
  managed rules): `mimirtool rules load dbcontest-rules.yml` against that
  ruler, or paste the groups into Grafana's rule editor for that data source.
- Grafana-managed alerting: import the whole file through Grafana's import of
  Prometheus rules where your version offers it. The alerts read the recorded
  series, so the recording group must come along and write to the Prometheus
  the alerts query.

The rules only raise alerts. Delivering them is Alertmanager's or Grafana's
contact points' job, and this repository runs neither for production; route
on the `severity` label, `critical` (act now, even mid-contest) and `warning`
(look at the next break).

**Importing the dashboard.** Grafana → Dashboards → New → Import, upload
`dbcontest-dashboard.json` and pick the Prometheus data source. The data source
is a variable and so is each service's job, found by a metric only that
service exports, so the file carries no UID to rewrite. Its uid,
`dbcontest-overview`, makes a second import replace the first.

**What pages, and why.** Each rule carries its threshold's reasoning in a
comment beside it. The Query Runner's thresholds assume its defaults
(`QUERY_CONCURRENT=8`, `QUERY_QUEUE_DEPTH=512`, `QUERY_QUEUE_WAIT=15s`,
`QUERY_DEADLINE=5s`): the runner does not export its configuration, so a
changed setting means changing the number in the rules too.

| Alert | Severity | Fires when |
|---|---|---|
| `DBContestAPINotScraped`, `DBContestQueryRunnerNotScraped` | critical | the selector matches no target for 5 minutes: a wrong selector, or a stopped container under a collector that discovers containers |
| `DBContestAPIDown`, `DBContestQueryRunnerDown` | critical | the scrape fails for 2 minutes |
| `DBContestProcessRestarted` | warning | either process restarted in the last 15 minutes |
| `DBContestAPIErrors` | critical | over 5% of API answers are 5xx, at least 10 in 5 minutes, for 3 minutes |
| `DBContestAPISlow` | warning | the API's p95, slow-by-design routes left out, is over 1 s with at least 15 of those requests in 5 minutes, for 10 minutes |
| `DBContestReclaimFailing` | warning | the reclaim sweep failed in the last hour |
| `DBContestMailAbandoned` | warning | an email message was abandoned in the last hour |
| `DBContestMailRelayFailing` | warning | sending email has been paused for 10 minutes: the relay itself failed (connection, TLS, the account, the sender), or refused three messages in a row ([Diagnosing](#diagnosing)) |
| `DBContestQueriesFailingInternally` | critical | 5 or more `error`/`internal` queries in 5 minutes, for 2 minutes |
| `DBContestQueriesTimingOut` | warning | over 20% of admitted queries hit the deadline, at least 10 in 5 minutes, for 10 minutes |
| `DBContestQueryRunnerFull` | critical | 5 or more `busy` refusals and queue timeouts in 5 minutes, for 2 minutes |
| `DBContestAdmissionWaitHigh` | warning | the admission wait's p95 is over 5 s for 5 minutes |
| `DBContestAdmissionQueueDeep` | warning | 16 or more queries wait for 3 minutes |

Participants' own SQL errors and checker refusals never page; the dashboard
shows them by reason.

**Checking the files.** `make observability-check` runs a Go test that
gathers the series each service registers, on a registry per service, and
holds every selector in the rules, their tests and the dashboard to the names,
labels and declared values of the service it reads; it also holds the
slow-by-design route pattern in the rules and the dashboard equal. Then
promtool, from a throwaway `prom/prometheus` container with the files mounted
read-only, loads the rules, runs their unit tests and parses every dashboard
query. The unit tests feed both sides of every alert: the series that should
page, the zero-valued series an idle contest exports, and values just below
each threshold, so loosening a threshold fails them as surely as breaking it.
CI runs the whole target whenever the code or these files change.

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

**There are three things to copy, not one.** The database is the first. The
second is the `contest-covers-data` volume: a contest's cover picture is a
file in `COVER_DIR` (`internal/platform/filestore`), deliberately not a row in
any table, so `pg_dump` does not see it and a restore from the dump alone
gives back every contest without its cover. The third is the
`game-uploads-data` volume in `GAME_UPLOAD_DIR`: organisers' uploaded dumps,
and the table builder's CSV files under its `tables/` directory. A game built
from a file is built from them, so without them it cannot be built again.
`make backup` writes all three, under one timestamp, and fails loudly rather
than writing only part of them; `DB_ONLY=1` takes the dump alone and says so.

```bash
make backup                                   # deploy/backups/<db>-<timestamp>.dump
                                              # deploy/backups/covers-<timestamp>.tar.gz
                                              # deploy/backups/game-uploads-<timestamp>.tar.gz
make restore-check        FILE=deploy/backups/....dump
make restore              FILE=deploy/backups/....dump              CONFIRM=yes
make restore-covers       FILE=deploy/backups/covers-....tar.gz       CONFIRM=yes
make restore-game-uploads FILE=deploy/backups/game-uploads-....tar.gz CONFIRM=yes
make game-check                               # built templates the game cluster lacks
```

`backup` dumps from inside the container, so no PostgreSQL client is needed on
the host, and refuses to keep a dump without pg_dump's custom-format header,
an empty one included. Each file is written as a hidden
`.<name>.<pid>.partial` and takes its real name only once it is checked (the
dump's header, `gzip -t` for an archive), and a name that is already taken is
never overwritten. A backup stopped by a signal — a timeout, a Ctrl-C —
removes its own unfinished files and staging directory; one killed outright
leaves only `.partial` files, never a set whose last archive is cut short, and
those you delete by hand. A step that fails, a full disk included, removes its
own partial file and keeps what was already checked under its real name: an
incomplete set. The timestamp is UTC and ends in `Z`
(`dbcontest_core-20261102-091500Z.dump`), so the hour a clock repeats when it
falls back cannot give two sets one name or a later set an earlier one; sets
taken before this carry local time and no `Z`, and restore exactly the same,
since every restore takes the file you name. Rotation, `status` and the disk
check order sets by the time their stamp names, reading a stamp without `Z`
in the host's own time zone, so the two forms sort together by when they
were taken and never by their text. A set dated more than five minutes
ahead of the host's clock was taken by a wrong one: it is left alone and
named, and never counted as the newest. The scheduled run also protects
the set the last successful run wrote, whatever sorts after it. The files are readable by their
owner only (umask 077): they hold the participants' answers. The volumes come out through
`docker compose cp` on the `api` service — the mount point is the compose
file's business, not the Makefile's — which reads a stopped container as
happily as a running one, but needs the container to exist. Each volume is
staged in a temporary directory before it is archived, and the uploads can
run to gigabytes: on a host whose `/tmp` is held in memory, point `TMPDIR` at
a disk with room for them (`TMPDIR=/srv/tmp make backup`; the restores
stage the same way). Copy all three files off the machine: a backup that only
exists on the host it came from is not a backup.

`restore-covers` and `restore-game-uploads` copy an archive back into its
volume, owned by the API's user (65532), and say to restart the `api`. Both
are additive and do not empty the directory first. That is safe for the
covers because a cover is named by the hash of its own content, so a file
that is already there is replaced by an identical one; and for the uploads
because an upload is named by its own id. **Restore the uploads after
`make restore`, not before:** until the rows are back, the upload sweep treats
the files as orphans and deletes them, which is also what clears away any
upload the volume held that the restored database does not know. Section 13
of [the architecture](../docs/ARCHITECTURE.md) has the whole move procedure.

`make restore-covers-check` and `make game-uploads-check` prove that
ownership against a throwaway compose project, the second also that
`make backup` takes the uploads; neither touches `deploy/.env` or a running
stack.

`restore-check` loads the dump into a throwaway database and drops it again. It
touches nothing real, and it is the whole difference between having backups and
believing you do — run it after every schema change.

`restore` replaces the live database and says so twice before doing it: it needs
`CONFIRM=yes`. When the API is running it is stopped for the duration, because
pg_restore cannot drop objects a live service holds open, and started again
afterwards whether the restore worked or not.

`game-check` is the step after `restore` on a new host. The game templates are
in no backup: they live on the game cluster, which a move leaves behind, while
the restored rows still record every template as built. It runs `gamecheck`,
which the API image carries, inside the running `api` container: that
container reaches both databases on the compose network, while the host has
no Go toolchain and publishes neither database's port, so the stack must be up
(`make restore` starts the `api` again if it was running). It reads both
databases and lists each contest whose template is recorded as built and is not
on the cluster, with the contest's status. It changes nothing. It fails while
a contest still to be played — draft, published or running — lacks its
template; a finished or archived contest is listed as needing no game and does
not fail it, since what is shown of it is read from the core database. Each
contest still to be played gets its game back from
`POST /api/v1/contests/{id}/game/build`, called with an organiser's session. A
running contest is rebuilt this way too: the route accepts it only while the
template is missing from the cluster, which is the state a move leaves, and
records the request in the audit trail with `recovery: true`. Its participants
can run no query until the build finishes, and then each gets a fresh copy of
the game: what they changed in their own database is gone, their answers and
score are not.

### On a schedule

A timer runs `deploy/backups/scheduled-backup.sh` on the host: `make backup`,
then rotation, then, if configured, a copy of the new set off the host. It
has two settings: **daily** at 03:30, and the **contest window**, every 15
minutes, which is the setting for a beta or an olympiad, so that a lost disk
costs at most the last 15 minutes of a contest.

Install it once, from the checkout on the host:

```bash
sudo deploy/backups/schedule.sh install deploy   # as the user who runs make deploy
sudo deploy/backups/schedule.sh install          # as the checkout's owner
```

The user must be able to run `docker compose` (the `docker` group): the one
who runs `make deploy`. Left out, it is the checkout's owner, except that a
checkout belonging to root is refused rather than quietly scheduled as root:
`install root` says so on purpose. `install` renders `deploy/systemd/dbcontest-backup.service`
and `.timer` with the checkout's path and that user, installs them in
`/etc/systemd/system` and starts the timer, and says which schedule is on.
Run it again after moving the checkout. The service runs sandboxed as far as
Docker allows (a private `/tmp`, which also clears a killed run's staging
copies; a read-only `/usr`, `/boot` and `/etc`; no devices, no kernel
settings), and after the clock is synchronised, so a catch-up run at boot
does not stamp its set with a time the clock is about to take back. `sudo deploy/backups/schedule.sh uninstall` takes them away and
leaves the sets alone.

**Before the beta or the olympiad, switch to the contest window; after it,
switch back:**

```bash
sudo deploy/backups/schedule.sh contest   # every 15 minutes, until 'daily'
sudo deploy/backups/schedule.sh daily     # back to once a day, at 03:30
deploy/backups/schedule.sh status         # which one is on, the next run, the last result, the newest set
```

There is one timer, and the contest window is a drop-in
(`dbcontest-backup.timer.d/contest-window.conf`) that replaces its daily time,
so the two settings can never both be on, or both off. Switch the evening
before, and check `status` shows the contest window. Do not deploy during
the window: `pg_dump` holds a share lock on every table for the seconds it
runs, and a migration waiting for its exclusive lock holds up the API's own
queries behind it.

**Rotation** runs at the start of every run and again after a successful
one, which never removes the set that run has just written. It keeps the newest
`BACKUP_KEEP` complete sets (48) and, beyond them, the last set of each of the
`BACKUP_KEEP_DAILY` most recent days not already covered (14). A *set* is the
three files of one timestamp; only a complete set is ever removed, and then
exactly those three files. An incomplete set (a run that could not read a
volume keeps its dump), the `.partial` files of a stopped run, another
database's dump and any file of another name are left alone; rotation names
the incomplete sets and the unfinished files, for you to delete once you know
why. It counts the sets `make backup` writes by hand too: to keep one out of
rotation, move it out of `deploy/backups` or rename it.

The arithmetic. With `S` the size of one set (the dump plus both archives),
the directory never holds more than `(BACKUP_KEEP + BACKUP_KEEP_DAILY + 1) × S`
— the kept sets and the one being written — and a run stages one more copy of
the volumes in `TMPDIR` (for the timer, `BACKUP_TMPDIR` in `deploy/.env`). In the contest window four sets an hour are taken,
so 48 sets are the last 12 hours at 15-minute steps; daily, the same 48 are
seven weeks. With the defaults, 63 sets:

| `S` | the directory at most | |
|---|---|---|
| 20 MiB | 1.2 GiB | a small installation's beta |
| 200 MiB | 12.3 GiB | |
| 2 GiB | 126 GiB | lower `BACKUP_KEEP`: 16 (four hours of a contest) makes it 31 sets, 62 GiB |

The game uploads usually decide `S`, since every set archives the whole
volume (up to `GAME_UPLOAD_MAX_DIR_BYTES` plus `GAME_UPLOAD_TABLE_MAX_DIR_BYTES`).
Measure it with `du -ch deploy/backups/*-<timestamp>.*`; rotation also prints
the newest set's size and the ceiling it implies after every run. A run is
refused, and fails, while the disk has less than `BACKUP_MIN_FREE_MB` (2048)
plus twice the last set free: Docker's volumes are usually on the same disk,
and a full disk stops PostgreSQL in the middle of the contest, which is worse
than a missed backup. The refusal says what frees space: lowering
`BACKUP_KEEP` in `deploy/.env` takes effect at the next run, whose first step
is rotation, and `BACKUP_KEEP=<n> deploy/backups/rotate.sh <dir>` frees it
at once (`--dry-run` first shows what goes).

**The off-host copy** is off by default. Set `BACKUP_OFFHOST=rsync` or
`rclone` and `BACKUP_OFFHOST_TARGET` in `deploy/.env` (a destination directory:
`backup@nas:/srv/backups/dbcontest`, or an rclone `remote:path`), and each run
copies its new set there. Credentials never go in the target, and a target
that could carry them is refused before anything runs: a `user:password@`, an
rsync URL with one, an rclone connection string (`:s3,secret_access_key=…:`)
or any rclone target that is not a named remote or a path, and anything
starting with `-`. The target is on the tool's command line, which every user
on the host can read. rsync authenticates with an SSH key
(`BACKUP_OFFHOST_SSH_KEY`, else the user's own, with no password prompt:
connect once by hand as that user, so the target's host key is already
known), rclone with a named remote in its config file (`BACKUP_RCLONE_CONFIG`,
else the user's own). The target is never written to the log either. A failed
copy fails the run and leaves that set on this host only; the next run copies
its own set, not the missed one. Nothing is deleted on the target: what it
keeps is the target's business, and the design of a second server pulling the
backups is still to come (item 35 of the architecture).

**Was the last backup taken?**

```bash
deploy/backups/schedule.sh status                  # last result and the newest set's age
journalctl -u dbcontest-backup -n 50               # the last runs, with make backup's own output
journalctl -u dbcontest-backup -p err --since today   # the failed runs, with the script's own reason
```

A failed run exits non-zero, so `systemctl --failed` lists the unit until the
next success. `-p err` shows the script's verdict lines (a refused disk, an
incomplete set, a failed copy); what `make backup` itself printed, such as
which volume could not be read, is in the full log of that run, at the
normal priority. With
`BACKUP_TEXTFILE_DIR` set, each run also writes `dbcontest_backup.prom` there
for the textfile collector of node_exporter, or of Alloy's
`prometheus.exporter.unix` (point its `textfile` block's `directory` at the
same path): `dbcontest_backup_last_success_timestamp_seconds`,
`dbcontest_backup_last_run_success`, `dbcontest_backup_last_set_bytes`,
`dbcontest_backup_sets`, and the same two for the off-host copy when it is
on. In the contest window, `time() - dbcontest_backup_last_success_timestamp_seconds > 1800`
means two runs in a row have failed. A set that was written is still not a set
that restores: `make restore-check` the newest dump before the contest.

**Without systemd**, cron runs the same script. Print the lines for this
checkout and install them; the switch is moving the comment from the contest
line to the daily one and back:

```bash
deploy/backups/schedule.sh cron deploy | sudo tee /etc/cron.d/dbcontest-backup
```

The script holds a lock, so a run still going when the next one is due
makes the next one fail rather than run beside it; the output goes to syslog
under `dbcontest-backup`.

`make scheduled-backup-check` proves the rotation, the run and the units on
synthetic sets in a throwaway container: `make backup` is stubbed, nothing is
dumped, and `deploy/backups` is never mounted. It needs Docker.

## Load testing before a contest

`cmd/consoleload` drives synthetic participants through the participant's real
path (sign-in, then `POST /api/v1/contests/{id}/query` through the API and the
Query Runner) and reports what they waited. `-profile beta` turns it from a
measurement into a test: forty participants in a read-write contest, fifteen
minutes of steady load with 5–30 s between one participant's queries, a mix of
40% lookups, 30% joins, 20% full-table aggregates and 10% writes, and a verdict
at the end. The profile's values and the reasoning behind each threshold are
in `cmd/consoleload/profile.go`; a flag given on the command line overrides the
profile's (`-participants 60` asks whether the server holds sixty).

Every run is judged on five checks, and fails on any of them:

| check | limit |
|---|---|
| queries sent | at least one |
| p95 latency of every query, refusals included | at most 2.5 s |
| refused (any 4xx) | at most 1% of the queries |
| errors (no answer, or one that could not be read) | at most 0.5% |
| server errors (any 5xx: a full queue, a timeout, an internal error) | none |

### Prerequisites

- **Nothing else live.** Run it at a quiet time, well before the contest: the
  load is real and its participants would share it. It refuses to start while
  any other contest is running or published to start before the run could
  end (its planned load plus an hour for setup and teardown, about 1 h 15 min
  for the beta profile), and names each one. A contest an earlier, killed run
  left counts too: `consoleload sweep` lists it and `sweep -yes` removes it. `-allow-live-contests` overrides; never
  use it with real participants on the server.
- **A built game of the detective story on the target cluster**, named by
  `-source-template`. The queries read `persons`, `calls`, `statements`,
  `cases` and `evidence`, and the writes update `evidence`; the contest's own
  template is the most faithful source, and it is copied, never modified. On
  the core database, `SELECT contest_id, template_db FROM game_templates
  WHERE status = 'ready'` lists them. Another game stops the run at its first
  query (or at provisioning, if it has no `evidence`), before any load.
- **Room on the game cluster**, within `GAME_CLUSTER_MAX_BYTES` and beside the
  copies the real contest will need, for one more template copy and the
  participant copies: forty plus `-headroom` spare. Pass the server's own
  `GAME_POOL_DEPTH` as `-headroom` (the workstation's environment usually
  lacks it, and the default is 10): the API's pool tender serves the test
  contest like any live one and tops its spare copies up to that depth, so a
  smaller headroom only means the server makes the difference itself.
- **A Go toolchain on a workstation and SSH to the host.** The server
  compiles nothing and publishes neither database's port, so the command runs
  on the workstation, reaching the API as participants do and the two
  databases through an SSH tunnel.
- **Two superuser credentials.** `CORE_DB_DSN` uses `CORE_DB_USER` and
  `GAME_PROVISIONER_DSN` uses `GAME_DB_USER` from the host's `deploy/.env`:
  each is its cluster's superuser, able to read or destroy everything on it.
  Keep them out of files and shell history, close the tunnel and `unset` them
  when the run ends, and never use `-keep` against production, which leaves
  the accounts and the running contest behind.

### Running it against the server

```bash
# On the host: the two clusters' addresses on the compose network.
docker inspect -f '{{.Name}} {{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' \
    db-contest-pg-core-1 db-contest-pg-game-1

# On the workstation: tunnel both to loopback, and leave this running.
ssh -N -L 127.0.0.1:15432:<core address>:5432 -L 127.0.0.1:15433:<game address>:5432 <host>

# In another terminal, from backend/. `read -rs` keeps the passwords out of
# the shell's history.
read -rs CORE_PW; read -rs GAME_PW
CORE_DB_DSN="postgres://<CORE_DB_USER>:$CORE_PW@127.0.0.1:15432/dbcontest_core?sslmode=disable" \
GAME_PROVISIONER_DSN="postgres://<GAME_DB_USER>:$GAME_PW@127.0.0.1:15433/dbcontest_game?sslmode=disable" \
go run ./cmd/consoleload -profile beta -api https://olympdb.usm.md \
    -source-template game_tpl_c<12 hex> -headroom <GAME_POOL_DEPTH> \
    -label "beta rehearsal, $(date +%F)"

# Afterwards: stop the tunnel (Ctrl+C in its terminal) and forget the passwords.
unset CORE_PW GAME_PW
```

The database names are `CORE_DB_NAME` and `GAME_DB_NAME` from the same
`.env`. A staging copy (a restored backup on another machine) is run the same
way, or with `make loadtest ARGS="-profile beta -source-template …"` when it
is the local stack. A whole run takes about twenty-five minutes: setup and
provisioning, forty sign-ins, a first query each, half a minute of quiet, the
fifteen minutes of load and the teardown. `docker stats` on the host, in
another session, shows what the containers spent; `-watch-container` only
works where the command runs.

### What it creates, and how it goes away

It reads no real participant's data and writes none. What it creates is new,
named by a run identifier of eight hex digits, and owned by the run:

- accounts `loadtest-<run>-owner` (blocked, for `contests.created_by`) and
  `loadtest-<run>-p001` to `-p040` (active, with random passwords that exist
  only in the command's memory);
- one untitled, invite-only contest with fixed timing, running until the
  run's planned end (its load and twice the hour of setup margin, about
  2 h 15 min for the beta profile; a reused `-fixture` moves it to the new
  run's, and is refused once the server's scheduler has finished the
  contest, about two hours after the run that kept it, since a finished
  contest refuses every query and cannot be revived),
  read-write with `evidence` writable, its forty registrations and its
  `game_templates` row;
- on the game cluster, `game_tpl_c<contest>` copied from the source, and the
  participant copies (`game_pool_c<contest>_*`, `game_c<contest>_*`), which
  the writes change and nothing else reads;
- what playing leaves: sessions (signed out at the end), journal rows, audit
  entries, and the sign-in limiter's counters in the cache.

It is not invisible. While it runs, the untitled contest heads the public
homepage's list of recent contests and its forty registrations count in the
homepage's figures; staff see it in the contest list. A killed run leaves it
there until it is swept.

At the end, however the run ended — finished, failed, or stopped with
Ctrl+C — it removes all of it: first it marks the template dropped, so the
API's pool tender stops stocking the contest; then it drops the databases,
the template last, and looks at the cluster once more for a copy that
finished meanwhile; only then does it delete the contest, its rows, the
accounts and the audit entries by those accounts or about that contest and
those accounts, including any entry staff wrote about them. Only the
limiter's counters stay, and they expire on their own.

Before removing anything it checks that every account has a login of
exactly that shape (the owner's blocked) and created, joined and manages
nothing else, and that the contest was created by the run's blocked owner,
has only the run's participants and shares its twelve-hex-digit database
prefix with no other contest; anything else is refused and nothing is
removed. Every database dropped and every row deleted is named by rows that
exist and passed that check, so an identifier in a fixture that matches no
row, or another kind of row, removes nothing. The databases drop with no
transaction open on the core database: the server's scheduler finishes
running contests by predicate, and a locked test contest would stall its
tick, and with it every real contest's scheduled start and finish, for as
long as the drops take. Only then does one short transaction lock the
contest and the accounts, check them again and delete them; it waits at
most five seconds for the rows and otherwise refuses, leaving them for
another try. A reused `-fixture`
is refused unless its owner is the run's owner account that created its
contest and its participants' logins are their accounts' own. Should a test
contest ever share its prefix with another contest by chance, it is refused
on every pass and has to be removed by hand.

If the command is killed outright or the tunnel drops first, the contest and
accounts are still there, and nothing stays locked for long: no transaction
is open while the databases drop, and the one that deletes the rows lasts
moments and is abandoned by the server ten seconds after the command stops
talking, so even a tunnel that dies silently inside it holds the rows for
ten seconds. A failed teardown prints the command that finishes
it, `consoleload sweep -run <run> -yes -force`. Otherwise `consoleload sweep`
(same environment) lists every leftover run: when it started, its contest,
its accounts and its databases. It removes nothing until `-yes`, and even
then leaves two kinds of run alone, saying so:

- a run before its planned end, the end of its contest, which may be
  somebody else's load test still going — `-force` removes it once you know
  it is not; a run with no contest is dated by the longest profile run, and
  if the profiles cannot be read from the environment it is held as well;
- accounts no contest ties to the load test, left when a run died before
  creating its contest — `-orphans` removes them.

`-run <eight hex digits>` narrows any of this to one run. A run that fails
the checks is named and kept; the others are still removed. On the local
stack the same goes through `make loadtest ARGS="sweep -yes"`.

### Reading the result

The command prints each run's percentiles and outcomes, then
`beta profile: PASSED` or `FAILED` with every check that failed. The results
directory it names at the start holds `report.md`, which opens with the
verdict table (run, check, measured, limit, pass or fail), `report.json` with
the same, and every query as a line in `<run>.jsonl`. The exit status is 0
when every run passed, 2 when the runs were measured and missed a threshold
and nothing else failed, and 1 when the run itself failed or did not clean
up.

Where to look when a check fails: a slow p95 is explained by the latency by
query class, the queries in flight against those the game cluster ran (the
difference is the Query Runner's queue) and `docker stats`; refusals and
server errors are counted by status and code in the outcomes table, and the
API's log has the cause of a 500. The counts table at the end compares the
installation before and after: accounts, contests and game cluster databases
should show no difference.

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

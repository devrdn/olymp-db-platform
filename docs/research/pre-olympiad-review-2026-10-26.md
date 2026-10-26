# Pre-olympiad security and performance review

A review of the platform ahead of the olympiad on 2 November 2026, made on
`main` at commit `149f634` (2026-10-26). Two reviews ran separately, one on
security and resilience (what a participant or an organiser could do to read
what they should not, or to take the service down) and one on performance
with the play screen first. Each finding below was checked against the code
before it was acted on; the one claimed crash was reproduced in an isolated
process rather than taken on trust. What was fixed is on the branch this
document arrived with; what was not is listed with its mitigation.

Method: a static read of the whole adversarial-SQL path (`queryproxy`,
`sqlpolicy/checker`, `queryrunner`, `gamedb`), of authentication and the
trust boundaries, and of the play screen's hot paths; `go vet`, gosec and the
full unit suites of both halves; the frontend typecheck, lint and production
build; a probe of the parser's limits; and a Server-Sent Events stream
through Caddy with the deployment's own configuration. Not run here, and due
on a networked workstation before the release: the database- and
cluster-backed tests (`make test-db`, `make test-game`, `make test-game-build`)
and `govulncheck`, which the sandbox could not reach vuln.go.dev for.

## Summary

| # | Finding | Severity | Status |
|---|---|---|---|
| S1 | A chain of about 25,000 binary operators (50 KiB, inside the 64 KiB cap) overflows the parser's C stack and ends the Query Runner, and with it every participant's console, in one request | Critical | Fixed: `backend/internal/sqlpolicy/checker/nesting.go` |
| S2 | Nothing bounded the time a query held an execution slot: eight participants each repeating a statement that runs to the deadline hold all eight slots for the rest of the contest | Medium | Fixed: `backend/internal/queryrunner/budget.go` |
| S3 | `CREATE MATERIALIZED VIEW` was admitted, but never droppable or refreshable: a one-way door at the disk quota | Low | Fixed |
| S4 | A read in a `read_write` contest ran in a READ WRITE transaction | Low | Fixed |
| S5 | The cluster-wide catalogs (`pg_shdepend`, `pg_tablespace`, the `pg_stat_*` server views) were readable | Low | Fixed |
| S6 | `INHERITS`, `PARTITION OF` and `TABLESPACE` on an own table were left to a privilege error | Low | Fixed |
| S7 | No background disk monitor (gaps N3): in a `read_write` contest concurrent writers can overshoot the shared disk | Medium | Open; see below |
| S8 | The production CSP keeps `script-src 'unsafe-inline'` (gaps N9) | Low | Open; no injection sink was found |
| S9 | "Reset my database" at three per ten minutes per registration is generous for a large roster | Low | Open |
| S10 | `QUERY_CONCURRENT=8` on two game-cluster CPUs oversubscribes once the slots fill | Low | Open; load-test on the host |
| P1 | The query log's refresh re-read up to the server's cap of 200 rows and cut the list to that | Low | Fixed |
| P2 | Caddy's `encode` was suspected of buffering the events stream | None | Verified not an issue |
| P3 | The story is rendered twice per play-screen load, once for the screen and once for print | Low | Open |
| P4 | The whole of zod reaches the play route's client bundle (21 KiB gzipped) | Low | Open |
| P5 | Nothing load-tests the whole play screen; `cmd/consoleload` covers the query burst | Medium (process) | Open; see below |
| P6 | Every waiting participant refreshes in the same instant the contest starts | Low | Open; harmless at forty |

## The fixes

### S1. The parser's stack

The checker parses with PostgreSQL's own parser through cgo, and the depth
bound it carried (`maxDepth`) runs on the tree, after the parse. The parse
itself recurses on the C stack, and a C stack overflow is a SIGSEGV the Go
runtime cannot recover. Measured in isolated processes, each construct that
nests was pushed up to and past the byte cap:

| Construct | Bytes per level | Outcome |
|---|---|---|
| binary operator chain, `1+1+1+…` | 2 | clean parse error from 5,000 to 22,000; crash at 25,000 (50 KiB) |
| nested parentheses | 2 | clean parse error from 10,000 |
| nested subqueries | 18 | clean parse error from 2,000 |
| `CASE WHEN … END` | 20 | clean parse error from 3,000 |
| `NOT NOT …` | 4 | clean parse error from 6,000 |
| casts `::int::int…` | 5 | clean parse error from 6,000 |
| nested calls `abs(abs(…))` | 5 | clean parse error from 6,000 |
| nested `ARRAY[…]` | 7 | clean parse error from 6,000 |
| unary minus chain `------1` | 1 | parses, even at 60 KiB |

The operator chain was the one input that reached the crash inside 64 KiB:
every other construct costs enough bytes per level that the parser's own
stack check refuses it first. A lexical scan now runs before the parser and
refuses, as `too_deep`, more than 4,000 operators or 500 nested brackets.
It skips string and dollar-quoted constants, quoted identifiers and both
kinds of comment, so a long constant is never mistaken for a long
expression. The bounds sit far above anything a person types and far below
the overflow, so no query the parser would otherwise accept is refused. The
regression test sends the crashing chain; without the guard it kills the
test binary rather than failing.

The earlier test of this risk nested 200 subqueries, which the parser
handles; it gave false comfort.

### S2. Slot time

The runner keeps a sliding sum, per participant, of the time their queries
held a slot over the last minute, and refuses a participant past two
`QUERY_DEADLINE`s of it with the rate's own answer: `query_too_often`, 429,
`Retry-After`. Reusing that answer meant no change to the gRPC contract, the
error table or the locales. Asking spends nothing, so a refused participant
retrying does not extend their wait. Like the rate, the budget is checked
before the parser. The threshold is a constant, not a setting
(`slotBudgetPerMinute`): two deadlines lets anybody run a slow query twice
before being asked to wait, and a legitimate query on a contest game
answers in well under a second.

One caveat: the message the participant sees is the rate's ("You are
sending queries too quickly"), which is approximately true. A wording of its
own would need a new code and three locale strings.

### S3 to S6

Each is a refusal or a narrowing with a test beside it. S4 changes the
transaction's access mode to follow the checker's decision rather than the
contest's mode; the connection's role still follows the contest. No
allow-listed function has a side effect and `EXPLAIN` only ever wraps a
read, so nothing legitimate changes. S5 adds names to the sensitive list
that `gamedb.hideSensitiveCatalogs` revokes; it already skips a relation the
server lacks, so the names that exist only on newer servers are safe. S6
keeps a foreign key into a game table allowed, as the existing test says: a
constraint reads the table rather than writing it, and the writer role
holds no `REFERENCES` privilege, which is where that one is refused.

The cluster-backed tests that exercise S4 and S5 against a real PostgreSQL
(`make test-game`) could not run here and are owed before the release.

### P1

The Log tab re-read as many rows as it held and replaced the list with the
answer, so past 200 loaded rows a refresh cut the list back. It now reads
one page and lays it over the rows held, keeping the older pages when the
page reaches them.

### P2, verified

The deployment's `encode zstd gzip` at site level was suspected of buffering
`text/event-stream` until its minimum length filled, which would delay the
first `sync` and the `contest_started` event. Tested with Caddy 2.10, the
deployment's `Caddyfile` shape and a flushing upstream: the stream passed
with no `Content-Encoding`, and each event arrived at the instant it was
sent. No change. The production Caddy's version may differ, so the same
check there takes a minute:

```bash
curl -N --compressed -H 'Accept-Encoding: gzip, zstd' -b 'session=…' \
  https://HOST/api/v1/contests/ID/events
```

`retry:` and the first `event: sync` should print at once.

## Open, with their mitigations

**S7, the disk monitor.** The architecture names a background sweep that
turns an over-quota instance read-only as the safety net behind the
per-statement size check; it does not exist. The check reads the size
before a statement, so one `INSERT … SELECT` overshoots by what fits in the
deadline, and concurrent writers overshoot independently. For 2 November:
run the contest `read_only` unless writing is part of the exercise, keep
`DiskQuotaRatio` conservative, and alert on the `pg-game` volume.

**S8, the CSP.** No `dangerouslySetInnerHTML` or other sink was found;
`'unsafe-inline'` is a defence that is missing, not a hole. The nonce and
`strict-dynamic` approach is described in `frontend/README.md`.

**S9 and S10.** Operational: consider one reset per ten minutes for a
large roster, and settle `QUERY_CONCURRENT` against the game cluster's CPUs
by the load test rather than by the default.

**P3, P4, P6.** Each is small and each touches the play screen a week
before the contest: the print copy of the story (a second `<StoryText>`
in `print-view.tsx`), `zod/mini` for the client-side schemas (about 15 KiB
gzipped less first-load script), and a 0 to 1.5 s jitter before the
`router.refresh()` on `contest_started`. All three are worth doing after the
olympiad; none is worth the risk before it.

**P5, the rehearsal.** `cmd/consoleload` drives the query burst and
provisioning, not the browser. The real start-of-contest burst is forty
times the Next.js render, five parallel API reads, the events channel and
the clarifications fetch, in the same second; the steady state adds the
autosave, the signals, the standings polls and the queries, all on one
`CORE_DB_POOL_MAX=25` pool. The estimate is comfortable (twenty to forty
core-database queries a second at forty participants), but nothing has
measured it. A Playwright or k6 run of forty to sixty virtual participants
on the real compose stack, watching the `pg-core` pool wait, the web
container's CPU and the p95 of `/play/*`, turns "should be fine" into
"measured fine".

## Verified sound

So that nobody re-reviews these. The SQL path: the function allow-list
(pg_sleep, pg_read_file, dblink, lo_*, every reg* cast, `CURRENT_USER`,
`current_database` refused), the data-modifying CTEs, `FOR UPDATE`,
`SELECT INTO`, `COPY`, `SET`, `LOCK`, `MERGE`, `ALTER` and `TABLESAMPLE`
refused; the statement cut at the parser's own bounds; the wrapper immune to
trailing comments and dollar quotes; the result budget on the socket; the
deadline on the connection's context with a server-side cancel; the rate
checked before the parser and refusals counted; the game roles
`NOSUPERUSER NOCREATEDB NOCREATEROLE` with memberships stripped, the
sensitive catalogs revoked in the template and inherited by every copy,
`TEMPORARY` revoked per instance, and the author running as its own role
rather than `SET ROLE`. The API and runner: the token compared in constant
time over SHA-256 on every unary and streaming call, the disk quota crossing
the proto. Authentication: argon2id behind a hashing semaphore, the address
budget spent before the account's, a dummy hash and uniform errors against
enumeration, password change throttled, sessions keyed by token digest with
generation-based revocation, `HttpOnly` `SameSite=Lax` `Secure`, the Origin
check on every write, one forwarded-header trust boundary with the ingress
secret vouching for the interface's forwarding, IPv6 grouped by /64 for
limiter keys only, every `ILIKE` through `escapeLike`, CSV formula cells
defused, Markdown rendered without raw HTML, no self-registration. The play
screen: the result table windowed with a byte budget behind it, CodeMirror
loaded once with typing outside React, autosave debounced, the diagram's
layout memoised and lazy, one events connection with jittered backoff,
signals batched, standings polled only while visible against a cached and
single-flighted answer, every list filter backed by an index.

Tooling: `go vet` clean; gosec reports only G103 in the generated protobuf
code; the frontend build is clean, with the play route at about 246 KiB
gzipped on first load (117 KiB of route chunks over 129 KiB of framework),
CodeMirror's 98 KiB loaded after hydration and the diagram's 22 KiB only
when its dialog opens; Milkdown stays on the admin story editor.

## Before 2 November

1. On a networked workstation, at the release tag: `make test-all` for the
   vulnerability scan, and `make test-db`, `make test-game` and
   `make test-game-build` for what this review could not run.
2. Decide the contest's mode: `read_only` unless writing is part of the
   exercise (S7).
3. Rehearse the whole play screen under load on the real host (P5), and
   settle `QUERY_CONCURRENT` and the game cluster's CPUs from it (S10).
   `QUERY_DEADLINE=3s` is worth considering: the sample games answer in well
   under a second, and a shorter deadline shrinks every slow query's share
   of a slot.
4. Check the events stream through the production Caddy (P2).
5. Confirm `ENV=production` and three distinct secrets; raise
   `MAX_LOGIN_ATTEMPTS_PER_ADDRESS` if more than about 250 participants will
   sign in through one address in the first quarter hour, since it counts
   successes; keep the observability profile off unless its `docker.sock`
   mount is accepted.

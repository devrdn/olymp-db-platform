# Project Rules

## General Guidelines

1. Do not share sensitive information, including personal data, passwords, or confidential business information.
2. Do not use "---" to separate sections in your responses. Instead, use clear headings and bullet points for organization.

## Code

1. Write code in a clear and readable manner, following best practices for the programming language being used.
2. Include comments to explain complex logic or important sections of the code.

## Go layout conventions

These exist so anyone opening the project can find things without asking.

1. **Packages are the unit of encapsulation, files are not.** Do not split a
   package into `model.go` / `service.go` / `repository.go` by technical role —
   that is a Java habit that costs navigation and buys no encapsulation, since
   everything inside a package is visible anyway. Name files by *what is in
   them* (`session.go`, `limiter.go`, `cookie.go`) and split only when a file
   has actually grown.
2. **Behaviour lives with its data.** `User.IsActive()` belongs next to `User`.
   A separate `model.go` of bare structs produces an anemic domain model.
3. **Interfaces are declared by the consumer, and kept to what it uses.** A
   domain package states what it needs (`users.Repository`);
   `internal/postgres` implements it. Domain code never imports a database
   driver — this is the dependency rule that matters from Clean Architecture,
   without its four rings of folders. When a consumer needs only part of an
   existing interface, it declares its own narrow one (`auth.UserStore` is four
   of the ten methods on `users.Repository`) rather than importing the wide one.
4. **HTTP belongs to the HTTP layer.** A domain package must not contain
   handlers or middleware: `internal/api` adapts HTTP to the domain, and
   `platform/httpx` holds the plumbing. Auth middleware is the exception that
   proves the rule — it *is* HTTP by nature — but a write-side concern like
   `audit` takes a plain `context.Context`, never an `*http.Request`.
5. **Test files mirror their source file:** `foo.go` is tested by `foo_test.go`.
   Do not create test files named after a theme (`degradation_test.go`) — when
   the theme drifts, nobody can find the tests. Cross-cutting tests that
   assemble the whole service go in `integration_test.go`; shared test helpers
   go in `support_test.go`, or in a `<pkg>test` package when another package
   needs them too.
6. **Every package carries a doc comment** saying what it answers and what it
   deliberately does not — `go doc ./internal/auth` should orient a newcomer.
7. **`internal/platform/*` is infrastructure** (config, logging, HTTP plumbing,
   storage, cache, metrics) and must not import a domain package. Domain
   packages sit at the top of `internal/`. `internal/app` is the composition
   root and is the only place allowed to know about all of them.
## Security and performance rules

Distilled from a review of the whole service. Each one names a class of
mistake that was actually found, not a hypothetical.

1. **Every error a service hands to the HTTP layer is a declared sentinel** (or
   wraps one with `%w`). A bare `errors.New` inside a service is a 500 waiting
   to happen: the handler's `fail` switch cannot name it, so the client is
   told "internal error" for its own typo. When adding a refusal, add the
   sentinel, the mapping in `fail`, and a handler test asserting the 4xx.
2. **Every field and every list that reaches storage has an explicit bound in
   the domain.** Columns are unbounded `text`, and the 1 MiB body limit bounds
   the request, not a field: without a check a login can be a megabyte long
   and a roster can hold thirty thousand identifiers inside one transaction.
   Lengths go on strings, a maximum count on any slice a request carries.
3. **Free text that lands in a `LIKE`/`ILIKE` pattern goes through
   `escapeLike`** (`internal/postgres/like.go`). A parameter stops injection,
   not a change of meaning: an unescaped `%` matches every row and a trailing
   backslash is a 500. If a query builds a pattern, it calls the helper.
4. **Anything that verifies a password is throttled, not only `/login`.**
   Changing a password verifies the current one, and a borrowed session must
   not be a place to guess it. Use `auth.Limiter`; count every attempt; reset
   on success.
5. **Check the bounded rate-limit key before the unbounded one.** Every limiter
   subject becomes a cache key. The address is one key per machine; the login
   is one key per string the caller invents. Spending the address budget first
   caps how many counters a refused caller can create — on the in-process
   cache a full store refuses every counter, which is a denial of service on
   sign-in for everybody.
6. **A middleware write per request needs a reason.** Authentication is one
   read; extending a session is a write, and moving an expiry by a few seconds
   is not worth one. Skip the write when nothing observable changes (see
   `SessionStore.Touch`) — and apply the same test to any hot path that
   touches the cache or the database.
7. **A filter the API offers is backed by an index, in the same change.** Adding
   a query parameter to a `List` endpoint is adding a `WHERE` clause on the
   largest tables; the migration that serves it lands with the code, or the
   comment claiming "the table carries an index for each" is a lie.
8. **Partial-success imports classify errors explicitly.** Only a row-level
   sentinel (taken login, invalid row) becomes a "skipped" entry. Anything
   else — the database, the audit trail — aborts and surfaces, or an outage
   is reported as three hundred rows the importer has to "fix".
9. **Trust boundaries are named once.** A forwarded address is believed only
   from `TRUSTED_PROXIES`, and only `httpx.IPResolver` reads
   `X-Forwarded-For`; everything else asks `httpx.ClientIP`. The one other
   forwarded header the service reads, `X-Forwarded-Proto` in `httpx.isTLS`,
   comes from any peer, so nothing may fail open because of it: a spoofed
   value must only ever make a request stricter, never looser.
10. **Prove a guarantee on the path the deployment uses.** A write path tested
    only as the reader role never ran a write, and three defects hid behind
    the privilege error it produced instead. Tests of a role-dependent
    behaviour connect as that role; tests of a transport-dependent behaviour
    cross that transport.
11. **A value that drives a check crosses every boundary it has to.** The disk
    quota was decided on one side of the gRPC contract and checked on the
    other, and the contract carried no field for it, so the check was dead in
    the only arrangement the deployment uses. When adding a check, trace its
    input from where it is decided to where it is applied through every
    proto, DTO and repository on the way.
12. **Bound memory where the bytes arrive.** A limit applied to decoded values
    comes after the allocation it exists to prevent: a driver reads a whole
    row before handing any of it over. A result budget is enforced on the
    socket (`queryrunner.readMeter`), and the same reasoning applies to any
    reader of untrusted-sized input.
13. **Rate-limit before the expensive step, and count refusals.** The SQL
    parser is C code reading text an adversary chose. A refused query still
    cost a parse, so it still counts against the participant's rate; a limit
    that only counts successes is a limit on the wrong thing.
14. **Cut SQL where the parser said the statement ends.** Wrapping a query is
    string surgery; use the parser's own statement bounds
    (`sqlpolicy.Statement.Text`) rather than trimming semicolons, or a valid
    `SELECT 1; -- note` becomes a syntax error inside the wrapper.
15. **A pool's statement timeout matches its workload.** The core API's ten
    seconds is right for request queries and wrong for `CREATE DATABASE …
    TEMPLATE`; a maintenance pool takes its own (`storage.NewMaintenancePool`).

# Project Rules

## General Guidelines

1. Do not share sensitive information, including personal data, passwords, or confidential business information.
2. Do not use "---" to separate sections in your responses. Instead, use clear headings and bullet points for organization.
3. **Commit messages, pull request titles and pull request descriptions are
   written in English.** So is everything in the source itself: identifiers,
   comments, test names, log messages. These are the parts of the work that
   outlive the conversation about them — a commit message is read years later
   by whoever is bisecting, and a pull request is the record of why a change
   was made. Conversation with the team can be in whatever language suits it;
   what gets committed cannot.

   The prose documentation under `docs/` is English too, and the README with
   it. It used to be Russian, which was defensible while the readers were the
   people in the room; it stopped being defensible the moment the repository
   was meant to be opened. A stranger who can read the code and not the
   document explaining it has been handed the harder half.

   The one thing this rule does not cover is user-visible interface text,
   which lives in the locale dictionaries and exists in every locale the
   platform serves — including Russian and Romanian. Writing the project in
   English is not the same as serving it in English.

   Do not quote Russian into an English comment either. A comment that
   switches language mid-sentence to cite a brief or a conversation is
   unreadable to exactly the person the rule is written for; translate the
   point or drop the quotation.
4. **History stays linear: integrate branches by rebase, never by merge
   commit.** `main` has no merge commit anywhere in its history, and the
   client merges pull requests on GitHub by rebase or squash, never by the
   default merge button. A branch that falls behind is caught up with
   `git rebase`, not `git merge`; nothing ever runs `git merge` against `main`.

   Every commit builds and passes its checks on its own, so `git bisect` lands
   on the commit that broke a behaviour: a refactor that failed alone and was
   repaired by a later commit was caught only by running each commit by
   itself. A review fix is folded into the commit it corrects when that commit
   would otherwise fail alone, or when the branch will be merged by rebase;
   otherwise a new commit is fine. Fold one this way; before git 2.44,
   `--autosquash` without `-i` leaves the `fixup!` commit in place:

   ```bash
   git commit --fixup=<commit>
   GIT_SEQUENCE_EDITOR=: git rebase -i --autosquash <base>
   ```

## Code

These rules and the Go layout conventions are the code shape every change is
reviewed for, on a par with the security rules.

1. **Reuse before you write: state a rule once, where it lives, and call it
   from every consumer.** Before writing a check, a normalisation, a predicate
   or a loop, search the tree for the module that already holds it and call or
   extend it. A second copy is a defect even while the copies agree, because
   they drift: thirteen hand-written request budgets disagreed on
   `Retry-After` before `httpx.Budgets` declared them once, and CI's race run
   carried its own `-timeout 20m` while `make test-race` kept go test's ten,
   until CI ran the make target and the Makefile's `RACE_TIMEOUT` held the
   value. The email width rule, the chunk-upload lifecycle, the contest status
   predicates and "a contest write revalidates the layout" each lived in two
   to nine places until one module held each (`users.LowerCaseKeepsWidth`,
   `provisioning.Uploads`, `lib/api/contests-terms.ts`, `contestChanged`). A
   module that replaces a helper moves every caller and deletes the helper in
   the same change. A copy across a process boundary, a Go limit the frontend
   repeats to stop a field at it, is kept only where a check fails when the
   two drift; otherwise the value is stated once and passed across. About
   forty Go bounds were copied into the interface with only a comment naming
   the Go constant, and thirty sentences spelled them out, until
   `cmd/apicontract` wrote them to `docs/api/limits.json`,
   `lib/api/limits-contract.test.ts` compared every constant with it, and
   `lib/i18n/bounds.ts` filled the sentences from those constants.
2. **Comments say what the code cannot.** Code documents itself through its
   names and structure; a comment earns its place only by giving the reason
   behind a non-obvious choice (a race, a security rule, a boundary such as
   "refused at the instant, not after", a measured cost), or a contract a
   caller must keep (units, bounds, what nil means, "inside a transaction").
   Write it in one or two plain sentences, true on every path; a longer one is
   for genuinely subtle reasoning. Leave out what the code already says,
   history ("used to", review findings, task numbers), and the same reasoning
   repeated in several places (rule 1 applies to reasoning as it does to
   code). Every package still carries its short doc comment (Go layout rule
   6). When a change moves behaviour, reread the comments above the code it
   touches and the doc comments of what it calls, and correct them in the same
   commit: `CancelQuestion.now` still promised one read after `cancel()`
   gained a second.
3. **Put a rule every caller must keep inside the module, where no caller can
   skip it.** A rule each call site applies by hand is forgotten by the next
   one. `participation.Admitter.Admit` takes the budget as a required argument
   and charges it before the gate, `httpx.Budgets.Charge` orders the keys by
   kind whatever order a handler names them in, and `ActionForm` derives its
   locked state before hydration, so security rules 13 and 5 and the hydration
   guard cannot be got wrong at a call site. Where the rule cannot move inside
   (a route must still call `admit`), a test walks every caller (security rule
   10).
4. **Apply the deletion test to every module, interface and hook you add.**
   Imagine deleting it: if its complexity reappears in every caller, it is a
   deep module and earns its interface; if nothing is lost, it is a
   pass-through, so fold it into its caller. The settings `ContestPanel` held
   only a reload counter and forwarded its props to `ContestForm` until the
   form moved up into it; what stayed below, `ContestFields`, earns its place
   because it remounts with the form's revision. `lib/pdf` was a toolbox each
   document reassembled until `renderPdf` became the one way to make a file.
   The same test places a seam: one adapter is a hypothetical seam, two is a
   real one (postgres and its in-memory fake count as two). An interface a
   domain package declares so that it need not import its adapter (Go layout
   rule 3) is a seam for direction and stands with one. A hook carries only
   what its adapters differ in: the upload lifecycle has three hooks, and a
   fourth for "what completion hands to the build" was dropped as shallow.
5. **One concept, one name, everywhere it appears.** The success flag of a
   server action was `saved`, `done`, `enrolled`, `sent`, `{}` or a bare
   result, so every panel's state type was its own and no shared form could
   read it; every action now answers `ok` (`lib/api/server.ts`).
6. **Code two routes share lives in `lib/`, `components/` or the folder of the
   route that contains both.** The PDF engine sat in the story route and the
   standings export reached it through `../../../` seven times, until it moved
   to `lib/pdf`; `/open` imported the participant register from its sibling
   `/my` until the register moved to `app/(participant)`.

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
8. **The domain decides what it can; the store answers only for what only the
   store can know.** An adapter, postgres or the in-memory fake, refuses only
   what the database enforces race-free, and answers it with the domain's
   sentinel. For instance: the row changed since it was read
   (`writeIfUnchanged`, `contests.ErrStatusChanged`), a unique index refused
   it (`users.ErrLoginTaken`, `provisioning.ErrUploadInProgress`), a foreign
   key named a parent that is gone (the parent's not-found sentinel, through
   `missingParent`), or a check constraint held a bound
   (`provisioning.ErrTooManyDeletedRows`). Every other rule is the domain's.
   Whether a contest row may be written lived in the domain, in an SQL `WHERE`
   and in a copy inside the fake, and an edit decided outside the transaction
   could land on a contest the scheduler had just started; the domain now
   decides inside the unit of work, and the contract tests in `conteststest`
   hold both adapters to the same refusals.

## Security and performance rules

Distilled from a review of the whole service. Each one names a class of
mistake that was actually found, not a hypothetical. A bare "CLAUDE.md rule
N" in a comment means one of these; cite the others as "Code rule N",
"Go layout rule N" or "general rule N".

1. **Every error a service hands to the HTTP layer is a declared sentinel** (or
   wraps one with `%w`), **and every sentinel has a declared answer.** A bare
   `errors.New` inside a service is a 500 waiting to happen: no mapping can
   name it, so the client is told "internal error" for its own typo. Where the
   answer lives depends on who gives it:
   - **A package more than one handler answers** lists its errors in
     `Errors()`, and `internal/api/errortable.go` holds one row per error.
     `sentineltest.AssertListed` (in the package's tests) and
     `TestEvery<Pkg>ErrorHasItsAnswer` keep the source, the list and the table
     in step, so a new refusal is: the sentinel, its name in `Errors()`, its
     row, and the row's literal in that test. A handler that must answer
     differently says so with `table.with(...)` beside it — only for a path it
     can actually reach.
   - **An error one handler answers** stays in that handler's `fail` switch,
     with a handler test asserting the 4xx.

   Either way its code is declared in `codes.go` and has a sentence in every
   locale; `make api-contract` and `make front-check` say when one is missing.

   A refusal the caller can fix gets a sentinel and a sentence of its own,
   naming the rule. A dotted login was refused only as the generic
   `ErrInvalidAccount`, so an administrator importing `name.surname` logins
   read a sentence naming no rule on every row; `ErrLoginHasDot` wraps the
   generic sentinel, so the import's classification still matches, and is
   answered `login_has_dot`.
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
   value must only ever make a request stricter, never looser. A limiter
   subject derived from that address is a different thing from the address
   itself: it goes through `httpx.AddressSubject` (or `httpx.ClientSubject`
   for the current request), which groups an IPv6 caller by its /64 rather
   than one address, because a single subscriber otherwise gets a fresh
   budget for every address in their own block. The audit trail and a
   contest's network restriction still use the exact address from
   `httpx.ClientIP`; only a rate-limit or lockout key uses the grouped one.
10. **Prove a guarantee on the path the deployment uses.** A write path tested
    only as the reader role never ran a write, and three defects hid behind
    the privilege error it produced instead. Tests of a role-dependent
    behaviour connect as that role; tests of a transport-dependent behaviour
    cross that transport. A test that holds every member of a set to a rule
    (every route admitted, every sentinel answered) enumerates the set the
    service builds; when it can walk only a set it assembles itself, its
    comment names what it misses, as `TestEveryParticipantRouteIsAdmittedFirst`
    does for a mount added in `internal/app` alone.
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

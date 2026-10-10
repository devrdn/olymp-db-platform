# 6. Testing

How the project is tested, both halves: which kinds of tests exist and where
each lives, how to run them, what CI runs, and what a good test looks like
here.

The rules behind it are in CLAUDE.md: Go layout rule 5 (where a test file
lives) and security rule 10 (prove a guarantee on the path the deployment
uses). The backend's own notes on its tests are in
[backend/README.md, Tests](../../backend/README.md#tests).

## Contents

1. [The kinds of tests](#1-the-kinds-of-tests)
2. [Running them](#2-running-them)
3. [What CI runs](#3-what-ci-runs)
4. [Writing a good test here](#4-writing-a-good-test-here)

## 1. The kinds of tests

| Kind | Where | Needs | Run by |
|---|---|---|---|
| Go unit tests | `foo_test.go` beside `foo.go` | Go and a C compiler | `make test` |
| In-memory fakes and repository contracts | `internal/<pkg>/<pkg>test/` | Go and a C compiler | `make test` |
| Repository tests on PostgreSQL | `internal/postgres`, `internal/provisioning`, `internal/queryproxy`, `cmd/migrate`, `internal/platform/storage` | the core test database | `make test-db` |
| Game cluster tests | `internal/gamedb`, `internal/queryrunner`, `internal/rpc` | the test game cluster | `make test-game` |
| The cross-cluster build tests | `internal/provisioning/game_integration_test.go` | both | `make test-game-build` |
| Whole-service tests | `integration_test.go` in `internal/api`, `internal/contests`, `internal/provisioning`, `internal/queryproxy`, `internal/rpc` | varies by package | the target for that package |
| Contract checks | `cmd/apicontract`, `cmd/auditcontract`, `migrations`, `sentineltest`, `internal/api/errortable_test.go` | Go and a C compiler | `make test` |
| Frontend unit and component tests | `*.test.ts` and `*.test.tsx` beside their source in `frontend/` | nothing | `make front-test` |
| Frontend checks | `npm run lint`, `typecheck`, `contrast`, `type-scale`, `error-codes` (in `make front-check`) and `smoke` (CI only) | nothing | `make front-check`, `npm run smoke` |

### Go unit tests

A test file mirrors its source file: `session.go` is tested by
`session_test.go` (CLAUDE.md, Go layout rule 5). Two names are exceptions:
`integration_test.go` for tests that assemble a whole service, and
`support_test.go` for helpers shared inside one package.
`internal/provisioning/game_integration_test.go` breaks the convention to keep
the cross-cluster tests apart. Most test files use the external package
(`package contests_test`), so they exercise only what is exported.

Business rules are tested against in-memory repositories, not mocks.
`make test` runs them with no database: the repository tests skip without one
(see [Skips](#what-skips-and-how-it-says-so)).

**A C compiler is required.** `go test ./...` compiles
`internal/sqlpolicy/checker`, which imports the cgo package
`github.com/pganalyze/pg_query_go/v6`. Without the Xcode Command Line Tools
on macOS, or `gcc` on Linux, `make check`, `make test` and `make test-all`
cannot build. The first build compiles PostgreSQL's parser and takes minutes;
after that the tests run in seconds.

### `<pkg>test` packages: fakes and contracts

When another package needs a fake, it lives in a `<pkg>test` package next to
the real one:

| Package | Provides |
|---|---|
| `internal/contests/conteststest` | in-memory implementations of every contests repository (`stores.go`), `NewFixture()` (the real `contests.Service` over them, with a fixed clock), and a contract per repository (`*_contract.go`) |
| `internal/users/userstest` | an in-memory `users.Repository` |
| `internal/workspace/workspacetest` | an in-memory `workspace.Repository` |
| `internal/platform/password/passwordtest` | digests for fixtures, including weak ones for the rehash path |
| `internal/platform/storage/storagetest` | the one way a test connects to the core test database |
| `internal/gamedb/gamedbtest` | a prepared game cluster and connections as each participant role |
| `internal/platform/sentineltest` | `AssertListed`: every exported `Err...` is in the package's `Errors()` |

A **repository contract** (such as `conteststest.StoryRepositoryContract`)
runs the same cases against the fake and against PostgreSQL, so a service test
on the fake can be trusted. Contracts exist for `contests` today; the pattern
is in [04-backend.md](04-backend.md#2-anatomy-of-a-domain-package).

### Repository tests on PostgreSQL

The SQL is the one thing a fake cannot verify, so `internal/postgres` runs
its queries against a real PostgreSQL.

- **A throwaway database.** `make test-db` runs `cmd/testdb`, which drops and
  recreates `<CORE_DB_NAME>_test` (by default `dbcontest_core_test`) on the
  development server, then migrates it from zero with `cmd/migrate up`, the
  same command a deployment uses. Nothing a previous run left survives, and
  nothing reaches the database `make run` serves from.
- **A guard.** Tests connect only through `storagetest.OpenCore`, which asks the
  server for `current_database()` and refuses any name that does not end in
  `_test`. A mistyped or copied `CORE_DB_DSN` fails the run rather than filling
  a real database with fixtures. The guard is enforced:
  `TestNoTestReadsADatabaseDSNPastTheGuard`
  (`internal/platform/storage/storagetest/storagetest_test.go`) fails
  `make test` on any test that reads `CORE_DB_DSN` or `GAME_DB_DSN` itself, so
  a new package's database tests open their pool through
  `storagetest.OpenCore` (or `gamedbtest`).
- **One transaction per test.** `withTx` in
  `internal/postgres/support_test.go` runs the test inside a transaction that
  is always rolled back. Repositories join it through `storage.QuerierFrom`,
  as in production. A few tests need what two connections see (`SKIP LOCKED`,
  `queryproxy` agreeing with `postgres.Registrations`); those commit their
  fixture and delete it in `t.Cleanup` (`contestRow` in the same file).
- **The deployment's credentials.** The test database DSN uses the same
  `CORE_DB_USER` and password from `deploy/.env` that `make run` uses, which
  in the bundled stack is pg-core's superuser. The core database has no
  separate application role, so these tests do not prove grants; the game
  cluster's role tests below do.
- **Migrations.** `cmd/migrate/main_test.go` rolls a migration back and forward
  on a scratch database of its own
  (`TestTheMonitoringMigrationRollsBackAndForward`).

### Game cluster tests

Everything `internal/gamedb` promises is a refusal by PostgreSQL to a
particular role: the participant cannot write, cannot read the sensitive
catalogues, cannot reach another database. That cannot be faked, and it
cannot be checked as a superuser. So these tests connect **as the
deployment's own roles**, `game_reader`, `game_writer` and `game_author`, and
provoke what each must not be able to do (CLAUDE.md, security rule 10). An
example is `TestTheReaderCannotGrantItselfMore` in
`internal/gamedb/database_test.go`.

- **A cluster of their own.** `make test-game` recreates the `pg-game-test`
  container from `deploy/docker-compose.dev.yml` (profile `test`, port 5434)
  for every run. What the tests change, databases and the shared roles, is
  cluster-wide, so a separate database inside `pg-game` would isolate nothing,
  and rewriting the roles there would take a running Query Runner's
  credentials away.
- **The production configuration.** The test cluster carries the same memory
  cap and worker settings as `pg-game`, so a test such as
  `TestAnOverAllocatingQueryFailsInItsOwnBackend`
  (`internal/queryrunner/runner_test.go`) proves the real bound.
- **Helpers.** `gamedbtest` refuses a cluster whose maintenance database does
  not end in `_test`, and reads the three role passwords from
  `GAME_READER_PASSWORD`, `GAME_WRITER_PASSWORD` and `GAME_AUTHOR_PASSWORD`.
  A missing password fails the test rather than inventing one.
- **Across both clusters.** `make test-game-build` runs the tests that start
  from a script saved in the core database and end with a real database on
  the game cluster: the seven `TestAScriptSavedInTheCoreDatabase...` tests in
  `internal/provisioning/game_integration_test.go`, selected by that
  `-run` prefix.

### Whole-service tests: `integration_test.go`

| File | Proves |
|---|---|
| `internal/api/integration_test.go` | the assembled service on real listeners: public and internal ports, readiness, metrics, running with neither Redis nor Prometheus |
| `internal/contests/integration_test.go` | every mutating operation writes its audit entry inside its transaction |
| `internal/rpc/integration_test.go` | client, socket, server, runner and a real game database agree (needs `make test-game`) |
| `internal/queryproxy/integration_test.go` | the console's façade over the real repositories, where the schema decides (needs `make test-db`) |
| `internal/provisioning/integration_test.go` | the database pool over the real core repository and a fake cluster (needs `make test-db`) |

### Contract checks

These keep two sides of a boundary in step. All run in `make test`:

| Test | Fails when |
|---|---|
| `sentineltest.AssertListed` in a package's `errors_test.go` (`contests`, `monitor`; `users_test.go`, `queryproxy_test.go`) | an exported sentinel is missing from `Errors()` |
| `TestEvery<Pkg>ErrorHasItsAnswer` in `internal/api/errortable_test.go` | a listed sentinel has no row, a different answer, or a code missing from the catalogue |
| `TestTheCommittedContractIsCurrent` in `cmd/apicontract` | `docs/api/error-codes.json` is stale; run `make api-contract` |
| `TestTheCommittedContractIsCurrent` in `cmd/auditcontract` | `docs/api/audit-actions.json` is stale; run `make audit-contract` |
| `TestEveryActionIsListed` in `internal/audit` | an action constant is missing from the list |
| `migrations/migrations_test.go` | a migration lacks a direction, reuses a version, is empty, or breaks the lock convention |

The frontend checks the other side of the same files:

- `npm run error-codes` (`frontend/scripts/error-codes.mjs`) fails when a code
  in `docs/api/error-codes.json` has no message in a locale, or a locale
  keeps a message for a code the API no longer returns.
- `frontend/lib/i18n/dictionary.test.ts` fails when a locale misses a key the
  English dictionary has, or an action in `docs/api/audit-actions.json` has no
  translation.

### Frontend tests

Vitest, on jsdom, with Testing Library (`frontend/vitest.config.mts`). A test
sits beside its source: `{app,lib,components}/**/*.test.{ts,tsx}`, plus
`proxy.test.ts`, `next.config.test.ts` and `scripts/**/*.test.ts`.
`vitest.setup.ts` adds the jest-dom matchers, cleans up after each test, and
stubs the two `Range` methods CodeMirror needs.

- **Unit tests** (`*.test.ts`): API schemas and helpers in `lib/api/`,
  formatting, i18n, the play screen's hooks and refusal handling.
- **Component tests** (`*.test.tsx`): render a component and assert what a
  user sees, by role and accessible name
  (`components/product/tab-strip.test.tsx`).
- **Checks that are not tests:** `npm run lint`, `npm run typecheck`
  (generates Next's route types first), `npm run contrast` (palette contrast
  in both themes), `npm run type-scale` (every `text-*` class names a real
  step), `npm run error-codes`, and `npm run smoke`, which serves the
  production build and fetches `/login`.

How the frontend is organised: [05-frontend.md](05-frontend.md).

## 2. Running them

All `make` targets run from the repository root. The database targets read
`deploy/.env` (copy it from `deploy/.env.example` and fill in the passwords);
they stop with an explanation when it is missing.

| Command | Runs | Needs |
|---|---|---|
| `make check` | `gofmt -w`, `go vet`, the no-cgo build check, `go test ./...` | Go and a C compiler (cgo, for the SQL checker) |
| `make test` | `go test ./...` | Go and a C compiler |
| `make test-race` | the same, with the race detector | Go and a C compiler |
| `make cover` | the tests with a coverage summary | Go and a C compiler |
| `make test-db` | recreates and migrates the test database, then the repository packages with `-count=1` | Docker: `make dev-up` first, for `pg-core` |
| `make test-game` | recreates `pg-game-test`, then `internal/gamedb`, `internal/queryrunner`, `internal/rpc` | Docker |
| `make test-game-build` | the cross-cluster tests | Docker: `pg-core` running; recreates both test databases |
| `make test-all` | `fmt-check`, `tidy-check`, `vet`, `test-race`, `build`, `vuln`, `sec` | Go and a C compiler; installs `govulncheck` and `gosec` if missing |
| `make front-test` | `npm test` (`vitest run`) | Node; installs dependencies if missing |
| `make front-check` | lint, contrast, type-scale, error-codes, typecheck, tests, build (not `smoke`) | Node |
| `make restore-covers-check`, `make edge-check` | the backup restore and the Caddy forwarding, against throwaway containers | Docker |

One test or one package, from `backend/`:

```bash
go test ./internal/contests -run TestStoryIsStoredInEveryLanguageItWasAuthoredIn
go test -count=1 ./internal/postgres -run TestStories   # needs CORE_DB_DSN, see below
```

For a single database test, export the test DSN that `make test-db` uses
(`postgres://<CORE_DB_USER>:<CORE_DB_PASSWORD>@localhost:<CORE_DB_PORT>/<CORE_DB_NAME>_test?sslmode=disable`)
as `CORE_DB_DSN`. The database must exist and be migrated: run `make test-db`
once, or `make test-db-reset`.

From `frontend/`:

```bash
npm test                                  # every test once
npm run test:watch                        # re-run on change
npx vitest run components/product/tab-strip.test.tsx
```

### What skips, and how it says so

Without a database the tests that need one skip rather than fail, so
`make test` works on any machine. Run with `-v` to see which, and why:

Each message names the variable and, usually, the target that sets it:

| Message | From | Means |
|---|---|---|
| `` CORE_DB_DSN is not set; run `make test-db` `` | `internal/postgres`, `internal/provisioning` | no core test database |
| `set CORE_DB_DSN to run the database tests` | `internal/postgres` (`withTx` and several tests), `storagetest`'s own tests | the same |
| `set CORE_DB_DSN to run this test against a real database` | `internal/queryproxy/integration_test.go` | the same |
| `set CORE_DB_DSN to run the migration round trip` | `cmd/migrate` | the same |
| `` GAME_DB_DSN is not set; run `make test-game` `` | `gamedbtest`, so `internal/gamedb`, `internal/queryrunner`, `internal/rpc` | no test game cluster |
| `` CORE_DB_DSN is not set; run `make test-game-build` `` and `` GAME_DB_DSN is not set; run `make test-game-build` `` | the cross-cluster tests in `internal/provisioning` | one of the two is missing; under `make test-db` the second always appears |

A skip is not a pass. A change to SQL is tested when `make test-db` ran it; a
change to the game cluster or the runner, when `make test-game` did.

Three things fail instead of skipping:

- a `CORE_DB_DSN` or `GAME_DB_DSN` that names a database not ending in
  `_test`, such as the product's own DSN left exported in a shell
  ("refusing to run tests against a database that is not a test database");
- a DSN that is set but points at a server that cannot be reached, the common
  first-day case being `CORE_DB_DSN` exported while `make dev-up` is not
  running. `internal/postgres` and `internal/provisioning` stop in `TestMain`
  ("cannot use the test database: ..."), and `gamedbtest` fails the test;
- a game cluster test with `GAME_DB_DSN` set but a role password missing.

The frontend tests need neither Docker nor the API.

## 3. What CI runs

Three workflows in `.github/workflows/`. `backend` and `frontend` are filtered
by path, on pushes to `main` as well as on pull requests, so a change runs only
the workflow for the half it touches; a `v*` tag runs both, since GitHub does
not apply path filters to tags. Pull requests run the checks; images are built
on pushes to `main` and published from tags.

### `backend.yml`

Runs on pushes to `main` and pull requests that touch `backend/**` or the
workflow itself, and on every `v*` tag.

**Job `build` ("build and test").** A `postgres:16-alpine` service holds
`dbcontest_core_test`; `CORE_DB_DSN`, `GAME_DB_DSN` and the three role
passwords are set for the whole job, so no database test skips. Steps:

1. Start the game cluster from the development overlay's own `pg-game-test`
   definition (`docker compose ... --profile test up -d --wait pg-game-test`),
   so CI proves the cluster configuration the product runs with.
2. Set up Go from `backend/go.mod`.
3. Fail if any file is not `gofmt -s` clean.
4. Fail if `go mod tidy` changes `go.mod` or `go.sum`.
5. `go vet ./...`.
6. Build `cmd/api`, `cmd/migrate` and `cmd/bootstrap` with `CGO_ENABLED=0`.
7. Apply the migrations: `go run ./cmd/migrate up`.
8. `go test -race -coverprofile=coverage.out ./...`. With both DSNs set, this
   one command covers what `make test`, `make test-db`, `make test-game` and
   `make test-game-build` run locally.
9. Print the coverage summary, then `go build ./...`.

**Job `security` ("security scan").** `govulncheck ./...` and
`gosec -exclude-generated ./...`.

**Jobs `build-images` and `publish-images`.** After `build` and `security`
pass, build the two backend images through `image.yml`: target `runtime` as
`db-contest-backend` and target `queryrunner` as `db-contest-queryrunner`.
On a push to `main` the images are built and discarded; on a `v*` tag they are
published.

### `frontend.yml`

Runs on pushes to `main` and pull requests that touch `frontend/**`,
`docs/api/error-codes.json`, `docs/api/audit-actions.json` or the workflow,
and on every `v*` tag. A change to either contract file runs the interface's checks,
because it changes what the interface must be able to say.

**Job `build` ("check and build"),** on Node 22: `npm ci`, then `lint`,
`contrast`, `type-scale`, `error-codes`, `typecheck`, `npm test`,
`npm run build`, and `npm run smoke` (serve the build, open `/login`).

**Jobs `build-image` and `publish-image`** build `db-contest-frontend` from
`frontend/` through `image.yml`, as for the backend.

### `image.yml`

A reusable workflow both call. It builds one Dockerfile target with Buildx,
tags it (`sha-<commit>`, the branch, the version from a tag), checks that the
image contains the expected binary and that its data directories belong to
the right user, and pushes only when asked to publish.

### Matching CI on your machine

| CI | Locally |
|---|---|
| backend `build` | `make test-all`, then `make test-db`, `make test-game` and `make test-game-build` (`test-all` alone runs the database tests as skips) |
| backend `security` | `make vuln sec` |
| frontend `build` | `make front-check`, then `cd frontend && npm run smoke` |

CI does not run `make proto-check`; run it yourself after changing
`backend/proto/` ([04-backend.md, recipe h](04-backend.md#h-change-the-query-runner-contract)).

## 4. Writing a good test here

Patterns the existing tests follow, each with an example to copy.

1. **Test at the package's public seam.** A service test builds the real
   service over fakes and calls exported methods, from the external test
   package. Example: `internal/contests/story_test.go` on
   `conteststest.NewFixture()`.
2. **Name the test after the behaviour it proves,** as a sentence:
   `TestAContestWithNoStoryAnswersNotFound`,
   `TestTheReaderCannotGrantItselfMore`. A failing test name then reads as the
   broken promise.
3. **A bug fix starts with a test that fails** (CONTRIBUTING.md). Write the
   test that shows the bug, watch it fail, then fix it, and land the fix with
   the test on every layer the bug crossed. Commit `73ddd726` (an ended
   contest answered `contest_not_running`) carries its tests in
   `standing_test.go`, `queryproxy_test.go` and the frontend's
   `refusals.test.ts`.
4. **Use fakes, not mocks, and arrange state the way production does.** A fake
   from `<pkg>test` keeps real behaviour; a test asserts outcomes, not which
   methods were called. The contracts arrange state only through the
   repositories' own methods, never through something only a fake offers
   (`conteststest/attempts_contract.go`).
5. **A storage behaviour the service relies on goes in the contract,** so
   the fake and PostgreSQL both prove it. Example:
   `conteststest/stories_contract.go`, run by
   `internal/postgres/stories_test.go`.
6. **A repository test runs inside its transaction.** Wrap it in `withTx`,
   create what it needs with the helpers in `internal/postgres/support_test.go`
   (`makeUser`, `makeContest`, `makeRegistration`), and let the rollback clean
   up. Commit only when the behaviour is about what a second connection sees,
   and then delete in `t.Cleanup` (`contestRow`).
7. **The clock is an input.** Contracts take the store's clock
   (`StoryTarget.Now`); the PostgreSQL side passes the transaction's own
   `now()` through `txNow`, and fixtures fix `conteststest.FixtureNow`. Never
   compare against `time.Now()` in an assertion.
8. **Assert the code, not the English.** A handler test checks the status and
   `errorCode(t, rec)` (`internal/api/contest_content_handler_test.go`). Expected
   answers in `errortable_test.go` are written out literally, because they are
   the contract the interface reads.
9. **Prove a guarantee where it holds** (CLAUDE.md, security rule 10). A
   permission of a database role is tested by connecting as that role
   (`internal/gamedb/database_test.go`); a value that crosses gRPC is tested
   across the wire (`internal/rpc/integration_test.go`); a write path is tested
   as the role that writes.
10. **Read a contract from its generated file, never a hand-typed copy.**
    `frontend/app/(participant)/contests/[contestId]/play/refusals.test.ts`
    and `frontend/lib/i18n/dictionary.test.ts` read `docs/api/*.json`.
11. **Test a component the way a user meets it.** Query by role and
    accessible name, assert attributes a user or assistive technology relies
    on (`components/product/tab-strip.test.tsx`).
12. **Keep test files where rule 5 puts them.** A new test for `foo.go` goes in
    `foo_test.go`, even when it is about a theme; helpers go in
    `support_test.go` or the `<pkg>test` package.

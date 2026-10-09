# DB Contest

A platform for running university SQL olympiads in a detective format. Students
are given the story of a crime and a database that holds the evidence. They
write SQL in a browser console, answer questions from what the queries tell
them, and name the culprit.

The database is the puzzle. Nothing is asked about SQL in the abstract: every
question is answerable only by querying the game database that came with the
story, and the only way to the answer is through a query that works.

## What it does

**For a participant.** A contest page with the story, the clock and the
questions. A SQL console with syntax highlighting, several tabs, a query
history and a result table you can open a row in. Notes that stay where they
were left. Answers checked the moment they are submitted, with attempts and
penalties as the organiser configured them. A profile that keeps their own
contests, their report and their history.

**For an organiser.** A contest builder: the story in Markdown, the questions
and their reference answers, and the game database itself — written as a SQL
script, uploaded as a finished dump, or described table by table in a builder
that takes CSV files and typed rows. Staff and their roles. A per-contest SQL
policy deciding what participants may run. Registration that is open or by
invitation, optionally restricted to an address range. Live monitoring of a
contest in flight, a query log with filters and export, a leaderboard with an
optional freeze, and reports afterwards.

**Underneath.** Every participant works in their own copy of the game
database, so nothing one of them does can be seen or felt by another. Every
query is parsed by PostgreSQL's own parser before it runs, checked against the
contest's policy, and executed under a statement timeout, a result-size budget
and a disk quota.

## How it is put together

```
        browser
           │
         Caddy  ── TLS, the only port open to the outside
           │
    ┌──────┴───────┐
    │              │
 Next.js        Core API ────────── PostgreSQL (core)
 (interface)    (Go monolith)        accounts, contests, questions,
                    │                submissions, audit, query log
                    │ gRPC
              Query Runner ───────── PostgreSQL (game cluster)
              (Go + cgo)             one database per participant,
                                     copied from the contest's template
```

Two processes rather than one, and the split is deliberate. The Query Runner
is the only component that executes SQL a stranger wrote, and it links
PostgreSQL's real parser through cgo to check a statement before running it —
C code reading adversarial text, where a crash is the end of the process
rather than a panic Go can recover from. Inside the API that would be a
reproducible way to take down sign-in, the clock and answer submission at the
moment they matter most. It is also the only process that holds the game
cluster's credentials: the Core API's configuration cannot name that cluster
at all.

The architecture document explains the rest, including what was considered and
rejected: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Running it locally

You will need Docker, Go 1.26+ and Node 22+.

```bash
cp deploy/.env.example deploy/.env
```

Fill in the passwords in `deploy/.env` — the file is read by everything below
and is never committed. Then:

```bash
make dev-up
```

That starts both PostgreSQL clusters — the core database and the game cluster
— along with Redis. Apply the schema and create the first administrator:

```bash
make migrate-up && make bootstrap
```

`make bootstrap` prints the password it generated; it is the only time that
password is shown. Then run the two halves, each in its own terminal:

```bash
make run
```

```bash
make front
```

The interface comes up on `http://localhost:3000`. To run the whole stack the
way a deployment does — Caddy, the API, the interface and both databases in
containers — use `make stack-up` instead, and `make stack-bootstrap` for the
first administrator.

`make help` lists every target.

## Testing

```bash
make check
```

Formats, vets and runs the unit tests — the command to run before pushing.

Some guarantees cannot be proved without a database, and those tests say so by
skipping rather than passing quietly:

```bash
make test-db          # the repository tests, on a database recreated from zero
make test-game        # the game cluster's isolation, as the participant's role
make test-game-build  # the one path that crosses both clusters
make front-check      # everything CI runs for the interface
```

`make test-all` runs what CI runs for the backend, including the race
detector, a vulnerability scan and static security analysis.

## Layout

```
backend/
  cmd/api/            the Core API
  cmd/queryrunner/    the Query Runner
  cmd/migrate/        schema migrations
  internal/           the domain packages, and platform/ beneath them
  migrations/         numbered SQL, applied by cmd/migrate
  proto/              the gRPC contract between the two services
frontend/             Next.js App Router, three locales
deploy/               Docker Compose, Caddy, observability
docs/                 the architecture, the design system, API contracts
```

`backend/README.md` and `frontend/README.md` go into each half in detail.
`CLAUDE.md` holds the conventions the code is written to — package layout,
error handling, the rules a change is reviewed against.

## Languages

The interface is served in English, Russian and Romanian; the locale
dictionaries under `frontend/lib/i18n/dictionaries/` hold every string, and
none is written inline in a component. The project itself — code, comments,
commit messages and this documentation — is written in English.

## Status

The platform runs contests. The architecture document's section 15 is the
honest list of what is finished and what is not.

## Contributing and security

How changes are made here — English in everything committed, a linear
history, tests on the path the deployment uses — is in
[CONTRIBUTING.md](CONTRIBUTING.md). A vulnerability is reported privately, not
in an issue: see [SECURITY.md](SECURITY.md).

## License

Copyright 2026 Nartea Nichita.

DB Contest is licensed under the [Apache License, Version 2.0](LICENSE). You
may use, modify and run it, including for commercial purposes, provided you
keep the license and the [NOTICE](NOTICE) with it and say what you changed.
The fonts it serves (JetBrains Mono, Literata, Onest) are under the SIL Open
Font License, and every dependency under its own license.

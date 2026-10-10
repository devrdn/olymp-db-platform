# Contributing

Thank you for looking. The rules below are short; the reasoning behind each of
them is in [CLAUDE.md](CLAUDE.md), which is the project's rulebook whoever — or
whatever — is writing the change.

## Before you start

- New to the project? Start with the [developer guide](docs/guide/01-overview.md):
  the product and its vocabulary, use cases, the key flows as sequence
  diagrams, and recipes for adding an endpoint, a package, a migration or a
  page. [docs/README.md](docs/README.md) lists every document.
- Read the [README](README.md) to run the platform locally, and
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for why it is put together the
  way it is.
- For anything larger than a fix, open an issue first and describe what you
  want to change and why. A security problem is never an issue: see
  [SECURITY.md](SECURITY.md).

## The workflow

1. **Branch from an up-to-date `main`**, named `<type>/<short-topic>`:
   `feat/roster-export`, `fix/play-dormant-refresh`, `docs/developer-guide`.
2. **Commit in small steps that each build and pass the tests.** One commit
   says one thing; a refactor and the feature that needs it are two commits.
3. **Keep the branch current with `git rebase origin/main`**, never
   `git merge`, and push a rebased branch with `git push --force-with-lease`.
4. **Open a pull request** against `main` once the checks below pass locally.
5. **Address review with new commits or a rebase**, whichever keeps the
   history readable, and re-run the checks.
6. **The pull request is merged by rebase or squash.** `main` has no merge
   commits anywhere in its history, and keeps it that way.

## Commit messages

The format is [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <what changes, in lowercase, no trailing period>

<why: the problem, the cause, and the choice made, wrapped at about 72
columns. Name what a reader bisecting this in a year needs.>
```

- **type** is one of `feat`, `fix`, `perf`, `refactor`, `test`, `docs`,
  `style`, `build`, `ci`, `chore`.
- **scope** is the package or area: `contests`, `postgres`, `api`, `play`,
  `i18n`, `provisioning`. Leave it out for a change that spans the project.
- **The subject says what is different afterwards**, in terms of behaviour:
  `fix(contests): an ended contest answers contest_ended, not contest_not_running`
  tells the reader more than `fix: handle ended contests`.
- **The body explains why.** A one-line change with an obvious reason needs no
  body; anything else does.

## What a change looks like

- **English in everything committed**: code, comments, tests, commit messages,
  pull requests and documentation. Text shown to users lives in the locale
  dictionaries and exists in every locale the platform serves.
- **Comments say what the code cannot**: the reason behind a non-obvious
  choice, or a contract a caller must keep (CLAUDE.md, Code rule 2).
- **Tests with the change**, on the path the deployment uses: a repository
  test runs against a real database, a role-dependent behaviour connects as
  that role. A bug fix starts with a test that fails. See
  [docs/guide/06-testing.md](docs/guide/06-testing.md).
- **An error the API returns is a declared code**: a sentinel, its answer —
  a row in `internal/api/errortable.go` when several handlers answer the
  package, the handler's own `fail` when one does (CLAUDE.md, security rule
  1) — the code in `docs/api/error-codes.json` and a message in every locale.
- **Every field and list that reaches storage has a bound**, and a new filter
  lands with the index that serves it (CLAUDE.md, security rules 2 and 7).

## Checks to run

```bash
make check         # backend: gofmt (rewrites), vet, cgo-free build, unit tests
make test-db       # repository tests against a throwaway core database
make test-game     # game cluster tests against a throwaway cluster
make front-check   # frontend: lint, design checks, error codes, types, tests, build
make sec           # gosec
```

The database checks need `deploy/.env` and the development databases
(`make dev-up`); see [docs/guide/06-testing.md](docs/guide/06-testing.md#2-running-them).
A pull request that changes the API or the audit trail also regenerates the
contracts with `make api-contract` and `make audit-contract`. `make test-all`
runs what CI's backend job runs except the database tests and the cgo-free
build: gofmt and go.mod checks, vet, the unit tests under the race detector,
a build, govulncheck and gosec.

## The pull request

- **The title** follows the commit format: `feat(play): …`.
- **The description** says what changes and why, how it was tested, and
  anything a reviewer should look at first: a migration, a new permission, a
  change to the gRPC contract, a new error code.
- **CI must be green.** `backend` runs on changes under `backend/`, `frontend`
  on changes under `frontend/` or to the contracts in `docs/api/`; both are
  described in [docs/guide/06-testing.md](docs/guide/06-testing.md). A pull
  request that touches neither, documentation only, runs no CI at all.

## License

By contributing you agree that your contribution is licensed under the
[Apache License, Version 2.0](LICENSE), the license of the project.

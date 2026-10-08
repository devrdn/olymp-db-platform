# Contributing

Thank you for looking. The rules below are short; the reasoning behind each of
them is in [CLAUDE.md](CLAUDE.md), which is the project's rulebook whoever — or
whatever — is writing the change.

## Before you start

- Read the [README](README.md) to run the platform locally, and
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how it is put together.
- For anything larger than a fix, open an issue first and describe what you
  want to change and why. A security problem is never an issue: see
  [SECURITY.md](SECURITY.md).

## What a change looks like

- **English in everything committed**: code, comments, tests, commit messages,
  pull requests and documentation. Text shown to users lives in the locale
  dictionaries and exists in every locale the platform serves.
- **A linear history.** Branches are brought up to date with `git rebase`,
  never `git merge`, and pull requests are merged by rebase or squash.
- **Tests with the change**, on the path the deployment uses: a repository
  test runs against a real database, a role-dependent behaviour connects as
  that role. A bug fix starts with a test that fails.
- **An error the API returns is a declared code**: a sentinel, its answer —
  a row in `internal/api/errortable.go` when several handlers answer the
  package, the handler's own `fail` when one does (CLAUDE.md, security rule
  1) — the code in `docs/api/error-codes.json` and a message in every locale.

## Checks to run

```bash
make test          # backend unit tests
make test-db       # repository tests against a throwaway core database
make test-game     # game cluster tests against a throwaway cluster
make front-check   # frontend lint, types and tests
make sec           # gosec
```

A pull request that changes the API or the audit trail also regenerates the
contracts with `make api-contract` and `make audit-contract`.

## License

By contributing you agree that your contribution is licensed under the
[Apache License, Version 2.0](LICENSE), the license of the project.

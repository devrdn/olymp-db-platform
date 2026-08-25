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
3. **Interfaces are declared by the consumer.** A domain package states what it
   needs (`users.Repository`); `internal/postgres` implements it. Domain code
   never imports a database driver — this is the dependency rule that matters
   from Clean Architecture, without its four rings of folders.
4. **Test files mirror their source file:** `foo.go` is tested by `foo_test.go`.
   Do not create test files named after a theme (`degradation_test.go`) — when
   the theme drifts, nobody can find the tests. Cross-cutting tests that
   assemble the whole service go in `integration_test.go`; shared test helpers
   go in `support_test.go`, or in a `<pkg>test` package when another package
   needs them too.
5. **Every package carries a doc comment** saying what it answers and what it
   deliberately does not — `go doc ./internal/auth` should orient a newcomer.
6. **`internal/platform/*` is infrastructure** (config, logging, HTTP plumbing,
   storage, cache, metrics) and must not import a domain package. Domain
   packages sit at the top of `internal/`. `internal/app` is the composition
   root and is the only place allowed to know about all of them.
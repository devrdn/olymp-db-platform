# Security policy

DB Contest runs SQL that participants write against databases on a shared
cluster, and it holds their accounts, their answers and the reference answers
they are trying to find. A weakness in it is rarely theoretical, so please
report one privately.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: the **Report a vulnerability**
button on the repository's **Security** tab. Please do not open a public issue,
a pull request or a discussion for it.

A useful report says what an attacker can do, from which starting point (an
anonymous visitor, a participant, an organiser), and how to reproduce it
against a local installation (`make dev-up`, see the README).

## What counts

Anything that lets someone do what their role does not allow, for example:

- a participant reaching another participant's database, the template, the
  core database or the host from the SQL console;
- a participant learning a reference answer, a hidden question, or anybody
  else's queries, answers or personal data;
- getting past sign-in, a rate limit, a contest's network restriction or its
  time window;
- exhausting a shared resource (the game cluster's disk, the connection
  pools, the Query Runner) from an ordinary participant account.

Weak passwords in a deployment's own `.env`, or a deployment that ignores the
settings documented in `deploy/.env.example`, are configuration rather than
vulnerabilities in the project.

## Supported versions

Only the `main` branch is supported. Fixes land there; there are no
maintained release branches.

# Implementation gaps

What the project's own documents say the platform will do, checked against the
code on `main` (commit `265c8653`, 2026-10-16). A document is taken as a
statement of intent; whether something exists is decided by the code alone.
Every claim cites `path:line`. Paths are relative to the repository root, and
line numbers are as of that commit; the documents have moved since.

Statuses:

- **Not implemented**: no code serves the intent.
- **Partial**: the data model or the API exists, but no path uses it, or a
  branch can never be reached.
- **Operational**: a gap in running, backing up or watching the installation.
- **Stale**: the code is ahead of, or different from, what the document says.

Where a document states an order or a priority, it is reported. The
architecture's section 15 says of its own list: "The order is numeric, not by
priority" (`docs/ARCHITECTURE.md:4031`).

## Summary

| # | Item | Status | Area | Source of intent |
|---|---|---|---|---|
| N1 | Query log: what is missing beyond per-contest monitoring (a view across contests, text search across all of a contest's participants, NDJSON/XLSX, async export) | Partial | Backend + UI | `docs/ARCHITECTURE.md:2265-2296` |
| N2 | Reports: results export and per-question breakdown (the standings screen exists) | Not implemented | Backend + UI | `docs/ARCHITECTURE.md:3386-3402` |
| N3 | Background disk monitor that makes an over-quota instance read-only | Not implemented | Provisioning | `docs/ARCHITECTURE.md:493-496` |
| N4 | Audit entries for the start and finish of a participation | Not implemented | Audit | `docs/ARCHITECTURE.md:2254` |
| N5 | Query-log archival after a contest's lifetime | Not implemented | Data retention | `docs/ARCHITECTURE.md:2255` |
| N6 | ER diagram and "show me the data" in the schema browser | Not implemented | Participant UI | `docs/ARCHITECTURE.md:162-170` |
| N7 | `Esc` aborts a running query | Not implemented | Participant UI + API | `docs/design/SPEC.md:982` |
| N8 | Readiness event when a participant's database is ready | Not implemented | Events | `docs/ARCHITECTURE.md:566-570` |
| N9 | Nonce-based CSP | Not implemented | Security | `frontend/README.md:215-219` |
| N10 | `/design` showcase | Not implemented (deferred, trigger passed) | Frontend | `docs/design/SPEC.md:938-944` |
| N11 | End-to-end tests | Not implemented | Testing | `docs/design/SPEC.md:929-931` |
| N12 | Accent colour: rule and code reconciled | Not implemented | Design | `docs/ARCHITECTURE.md:4088-4095` (§15 item 11) |
| N13 | Security audit plan beyond gosec and govulncheck | Not implemented | Security | `docs/ARCHITECTURE.md:3592-3606` |
| P1 | Registration lifecycle: `finished` never written, `active` only under individual timing | Partial | Contests | `docs/ARCHITECTURE.md:183`, `backend/internal/contests/enrollment.go:17-26` |
| P2 | `reports.view` seeded and granted, checked by no route | Partial | RBAC | `backend/migrations/000005_seed_roles_and_permissions.up.sql:17` |
| P3 | Publication and start do not check that the game is built (`Service.Ready` test-only) | Partial | Provisioning | `docs/ARCHITECTURE.md:596-597`, `:1067-1069` |
| P4 | A hidden question can never reach a participant once the contest runs | Partial | Contests | `docs/ARCHITECTURE.md:1053-1057` |
| P5 | A manager without `contest.create` has no list showing their contest | Partial | Contests + UI | `docs/guide/02-use-cases.md:264` |
| P6 | Endpoints with no screen: delete a draft, extend grace, PATCH question, PUT texts, PUT answers | Partial | Staff UI | `docs/guide/02-use-cases.md:269,272,332` |
| P7 | Contest package: export without import | Partial | Contests | `docs/ARCHITECTURE.md:4121-4128` (§15 item 12) |
| P8 | "Reset my database": `provisioning.Service.Reset` has no caller | Partial | Provisioning + UI | `docs/ARCHITECTURE.md:483-487` |
| P9 | Audit screen cannot filter by actor or entity id | Partial | Staff UI | `docs/ARCHITECTURE.md:4035-4042` (§15 item 8) |
| P10 | Participants from a file: paste works, file upload does not | Partial | Staff UI | `docs/ARCHITECTURE.md:131`, `:4109-4113` |
| P11 | Bulk account actions: one description of an action | Partial | Users | `docs/ARCHITECTURE.md:4043-4051` (§15 item 9) |
| P12 | Play-screen actions put ids into paths unchecked | Partial | Frontend security | `docs/guide/05-frontend.md:257-263` |
| O1 | Backups are manual, on the same host, unscheduled, unrotated | Operational | Backups | `backend/README.md:573-616` |
| O2 | `make backup` omits the game-uploads volume | Operational | Backups | `deploy/docker-compose.yml:796` |
| O3 | Game side is not reconciled after a restore to a new host | Operational | Recovery | `docs/ARCHITECTURE.md:3826-3827` |
| O4 | Restore drills are partial and unscheduled | Operational | Recovery | `docs/ARCHITECTURE.md:3834-3836` |
| O5 | No alerting, no dashboards, Query Runner unmeasured | Operational | Monitoring | `docs/ARCHITECTURE.md:538-539`, `:2253`, `:2261-2263` |
| O6 | No high availability; replicas blocked by local state | Operational | Availability | `docs/ARCHITECTURE.md:3613-3614` |
| O7 | CI and supply-chain promises not kept (npm audit, frontend scan, pinned images, Docker secrets) | Operational | CI | `docs/ARCHITECTURE.md:3527-3529`, `:3753` |
| S1 | §14 step 5 "The game" is unchecked but built | Stale | Docs | `docs/ARCHITECTURE.md:4001-4005` |
| S2 | §14 step 6 is unchecked; half of it is built | Stale | Docs | `docs/ARCHITECTURE.md:4006-4009` |
| S3 | §14 step 7 names k6; the load tool is `cmd/consoleload` | Stale | Docs | `docs/ARCHITECTURE.md:4010-4013` |
| S4 | §15 item 9's server half is already done | Stale | Docs | `docs/ARCHITECTURE.md:4043-4051` |
| S5 | §15 item 12: participant paste parsing already exists | Stale | Docs | `docs/ARCHITECTURE.md:4109-4113` |
| S6 | §13 "Exactly one thing is carried across" | Stale | Docs | `docs/ARCHITECTURE.md:3808` |
| S7 | Module names `registrations`, `submissions`, `reporting` do not exist | Stale | Docs | `docs/ARCHITECTURE.md:182-189`, `:3669-3670` |
| S8 | §6.1 says the gate checks instance versions | Stale (claims more than code) | Docs | `docs/ARCHITECTURE.md:1067-1069` |
| S9 | README organiser list overstates reports and the query log | Stale (claims more than code) | Docs | `README.md:21-28` |
| S10 | SPEC "Built so far" and "Next is item 3" | Stale | Design docs | `docs/design/SPEC.md:822-824`, `:1024-1025` |
| S11 | SPEC §17 side edit mentions TanStack Query, which is not used | Stale | Design docs | `docs/design/SPEC.md:1029-1034`, `docs/design/README.md:69-71` |

Counts: 13 not implemented, 12 partial, 7 operational, 11 stale. A further 13
items are recorded as deliberately deferred and confirmed absent (last
section).

## Not implemented

### N1. Query log: what is left beyond monitoring

The query log is implemented in three places:

- **The participant** sees their own log on the play screen
  (`frontend/app/(participant)/contests/[contestId]/play/query-log-panel.tsx`)
  and downloads it as CSV (`GET /contests/{contestID}/play/log.csv`).
- **The profile** keeps the participant's queries after the contest
  (`frontend/app/(session)/profile/contests/[contestId]/my-queries.tsx`,
  `backend/internal/api/profile_handler.go:114`).
- **Staff**, inside one contest's monitoring
  (`backend/internal/api/monitor_handler.go:106-121`): the contest's feed with
  filters by kind, time range and participant and a live mode
  (`monitor/live-feed.tsx`); each participant's queries with a status filter
  and a substring search (`?status=`, `?q=`, `monitor_handler.go:412-414`,
  `monitor/[registrationId]/queries-tab.tsx`); CSV of the whole contest and
  of one participant (`export.csv`, `monitor_handler.go:120-121`).

What §9.1 describes and does not exist (`docs/ARCHITECTURE.md:2265-2296`;
the document's "None of that paragraph exists yet" refers to the export
paragraph only):

- one view across contests for an administrator;
- a substring search over the queries of every participant of a contest at
  once (today the search is per participant);
- NDJSON and XLSX export, asynchronous export of large selections
  (`docs/ARCHITECTURE.md:2388-2391`; no XLSX library in `backend/go.mod`).

Impact: "who touched table `suspects`" is answered per participant, not for a
whole contest or across contests in one query.

### N2. Staff reports and results export

Intent: during a contest, progress by question, the failing-query rate and
activity over time; afterwards a breakdown by question (share solved, average
attempts and queries); CSV and XLSX "for the faculty office"; materialised
views refreshed on a schedule (`docs/ARCHITECTURE.md:3386-3402`).

Evidence: staff see the standings screen
(`frontend/app/(admin)/contests/[contestId]/standings`), but the leaderboard
is the only reporting module
(`backend/internal/api/leaderboard_handler.go:52-64`: public, participant,
live, reveal), with no export route. The monitoring roster carries per-person
counters, not per-question aggregates
(`backend/internal/api/monitor_handler.go:187-205`). No migration creates a
materialised view. The participant's personal report exists
(`backend/internal/api/profile_handler.go:110-111`).

Impact: an organiser cannot download final results or a per-question
breakdown; the faculty office gets nothing to file.

### N3. Background disk monitor for write mode

Intent: level 2 of the disk quota, "a background monitor reading
`pg_database_size()`" that puts an over-quota instance into read-only with
`ALTER DATABASE … SET default_transaction_read_only = on`
(`docs/ARCHITECTURE.md:493-496`).

Evidence: the only `ALTER DATABASE` in the code sets a connection limit
(`backend/internal/gamedb/grants.go:127`); `default_transaction_read_only`
appears only as the reader role's default
(`backend/internal/gamedb/cluster.go:144-146`). The background tasks are the
query-log sweep, the scheduler, pool tending, builds, the upload janitor and
reclamation (`backend/internal/app/app.go:107,167,175,209,213,350`); none
checks instance sizes. Level 1, the pre-DML check, exists
(`provisioning.Service.Quota`).

Impact: the bound on a single statement's overshoot is the only one; nothing
catches an instance that crosses the quota by other means.

### N4. Audit entries for a participation's start and finish

Intent: the audit trail records "the start and finish of a participation"
(`docs/ARCHITECTURE.md:2254`).

Evidence: the generated list of audit actions has no such action
(`docs/api/audit-actions.json`: `participant.add`, `.remove`, `.disqualify`,
`.enroll` only). See also P1: `finished` is never written at all.

Impact: "when did this participant start" has to be read from
`registrations.started_at`, which is set only under individual timing (P1).

### N5. Query-log archival

Intent: `query_log` is kept for "the contest's lifetime, then archive"
(`docs/ARCHITECTURE.md:2255`).

Evidence: no code deletes from or archives `query_log` or `audit_log`. The only
query-log job closes rows stranded at `running`
(`backend/internal/app/background.go:71-74`).

Impact: the largest tables grow without bound; every backup carries every
query ever run.

### N6. Schema browser: ER diagram and "show me the data"

Intent: the schema panel carries "a generated ER diagram of the game schema"
and a "show me the data" button that runs `SELECT * FROM t LIMIT 50` through
the normal pipeline (`docs/ARCHITECTURE.md:162-170`); ERDiagram is in the
component inventory (`docs/design/SPEC.md:813`).

Evidence: the panel lists tables, columns, types and foreign-key targets
(`frontend/app/(participant)/contests/[contestId]/play/schema-panel.tsx:9-15`);
no file in `frontend/` mentions an ER diagram, and the play folder issues no
generated `LIMIT` query. The cached schema description it reads does exist
(`backend/internal/provisioning/schema.go:51-53`).

### N7. Aborting a running query

Intent: "`Esc` aborts" in the console (`docs/design/SPEC.md:982`).

Evidence: the console's only route is `POST /contests/{id}/query`
(`backend/internal/api/console_handler.go:44`); there is no cancel endpoint,
and the editor binds `Mod-Enter` and the defaults only
(`frontend/components/product/code-editor-core.ts:153-173`). A query is bounded
by the runner's deadline instead.

### N8. Readiness event for a participant's database

Intent: a late enrolee sees "your database is being prepared", and "the
console opens on a readiness event over SSE" (`docs/ARCHITECTURE.md:566-570`).

Evidence: the event stream sends `sync`, `contest_started` and
`contest_finished` only (`backend/internal/api/events_handler.go:102-104`).
There is no "being prepared" state either: a participant's copy is made by
their first query or first read of the schema, inside that request, taken
from the pool or, when the pool is empty, copied from the template while the
request waits (`backend/internal/queryproxy/queryproxy.go:348`,
`backend/internal/queryproxy/schema.go:69`,
`backend/internal/provisioning/provisioning.go:244-267`). `no_game_yet` is
the answer for a contest whose template is not built (P3), not for a late
enrolee, and the play screen does not retry it on its own
(`frontend/app/(participant)/contests/[contestId]/play/result-panel.tsx:667-676`).

### N9. Nonce-based CSP

Intent: "Known next steps" (`frontend/README.md:215-219`); §11 promises "a
strict CSP" (`docs/ARCHITECTURE.md:3522`).

Evidence: production CSP allows `script-src 'self' 'unsafe-inline'`
(`frontend/next.config.ts:39-44`).

### N10. The `/design` showcase

Intent: deferred "until the constructor (architecture step 3) can give it real
ones" (`docs/design/SPEC.md:938-944`; `frontend/README.md:220-222`).

Evidence: no `app/design` route (`find frontend/app -name page.tsx`). Step 3 is
checked done (`docs/ARCHITECTURE.md:3883`), so the stated trigger has passed.

### N11. End-to-end tests

Intent: end-to-end tests "get their own directory when they arrive"
(`docs/design/SPEC.md:929-931`).

Evidence: none exist; CI runs one smoke script
(`frontend/package.json:17`, `.github/workflows/frontend.yml:88-89`).

### N12. Accent colour rule

Intent: §15 item 11, "either narrow the usage back, or rewrite the line"
(`docs/ARCHITECTURE.md:4088-4095`).

Evidence: SPEC still says the accent is "only for what is live"
(`docs/design/SPEC.md:76`) while it colours story links
(`frontend/components/product/story-text.tsx:60`), upload progress
(`frontend/app/(admin)/contests/[contestId]/game/game-upload.tsx:462`) and the
focus ring (`frontend/components/ui/tabs.tsx:142`; SPEC itself at
`docs/design/SPEC.md:979`). Item 11.1 (a new hue) is deferred by decision.

### N13. Security audit plan

Intent: before the first olympiad, an OWASP ASVS L2 checklist; gosec and
semgrep; an OWASP ZAP baseline scan; a manual penetration test of the Query
Runner written up as a regression suite; k6 load tests
(`docs/ARCHITECTURE.md:3592-3606`; §14 step 7 at `:4010-4013`).

Evidence: CI runs govulncheck and gosec only
(`.github/workflows/backend.yml:187-196`). No ASVS checklist, semgrep
configuration or ZAP job exists in the repository. Load testing exists in
another form (S3). Whether a pilot olympiad has been held cannot be decided
from the code; `deploy/docker-compose.yml:563` and
`docs/ARCHITECTURE.md:606-608` refer to "the pilot".

## Partially implemented

### P1. Registration lifecycle beyond `registered`

Intent: the `registrations` module handles "the start and finish of one
participant's run" (`docs/ARCHITECTURE.md:183`). The schema allows
`registered`, `active`, `finished`, `disqualified` and a `finished_at`
(`backend/migrations/000003_game_and_registrations.up.sql:23,27`);
`RegistrationFinished` means "done, by choice or by the clock"
(`backend/internal/contests/enrollment.go:23-24`).

Evidence:

- The only status writer is `Registrations.SetStatus`
  (`backend/internal/postgres/registrations.go:404-414`), and its one
  production caller writes `disqualified`
  (`backend/internal/contests/enrollment.go:478`). Nothing writes `finished`
  or `finished_at`.
- `active` and `started_at` are written by `Start`
  (`backend/internal/postgres/registrations.go:313-324`), called only when
  `ClockPending`, which is true only under individual timing
  (`backend/internal/contests/deadline.go:7-9`;
  `backend/internal/queryproxy/queryproxy.go:443-450`;
  `backend/internal/contests/submission.go:257-260`). Under a shared window
  every registration stays `registered`, as the code comment admits
  (`backend/internal/contests/enrollment.go:100-102`).

Unreachable consequences:

- The gate's `phaseFinished` and its `contest_finished` refusal
  (`backend/internal/contests/standing.go:121-123,200-202`);
  `docs/guide/02-use-cases.md:497` already says so.
- The profile summary's "finished" figure counts `status = 'finished'`
  (`backend/internal/postgres/profile.go:54`) and is shown to every user
  (`frontend/app/(session)/profile/summary.tsx:30`): it is always 0.
- The monitoring feed's `finished` kind reads `r.finished_at`
  (`backend/internal/postgres/watch_feed.go:325-328`) and never yields a row;
  its `started` kind yields nothing for a shared-window contest.

Impact: participants see "0 finished" on their profile however many contests
they completed; organisers watching a shared-window contest see no start or
finish events and every participant as `registered`.

### P2. `reports.view`

Intent: "View contest reports and the query journal"
(`backend/migrations/000005_seed_roles_and_permissions.up.sql:17`).

Evidence: declared at `backend/internal/rbac/rbac.go:29`, granted to every
contest owner and manager (`rbac.go:69`) and to administrators (all
permissions), taken from organizers
(`backend/migrations/000006_auth_columns_and_admin_permission.up.sql:32-38`).
No non-test Go file outside `rbac.go` names it. The frontend uses it only to
choose a landing page (`frontend/lib/auth/destination.ts:18`).
`docs/guide/02-use-cases.md:463` records the same.

Impact: none today; an administrator who builds a "reports only" role from it
gets a role that unlocks nothing. It waits for N1 and N2.

### P3. The game's readiness is not a condition of publication or start

Intent: "every instance is at the current version" is "a condition of a
contest entering `running`" (`docs/ARCHITECTURE.md:596-597`); the publication
gate checks "the game instances' versions" (`docs/ARCHITECTURE.md:1067-1069`).

Evidence: `provisioning.Service.Ready` does exactly that check
(`backend/internal/provisioning/provisioning.go:435-439`) and is called only
from tests. The gate's problem codes cover languages, story, questions,
schedule, attempts, the freeze, staff and the cover, none about the game
(`backend/internal/contests/publish.go:14-59,107-190`). The developer guide
says so: "The publication gate does not check that the game database is
built" (`docs/guide/03-flows.md:372-373`).

Impact: a contest with no game, or one whose build failed, can be published
and started; every participant's console then answers `no_game_yet`.

### P4. Hidden questions

Intent: a hidden question keeps its answers and points; the participant's list
omits it, identifier included (`docs/ARCHITECTURE.md:1053-1057`); "an
organizer may reveal one later" (`backend/internal/contests/publish.go:217`).

Evidence: participants read only `is_visible` questions
(`backend/internal/postgres/questions.go:134-140`). Visibility can change only
while content is editable, that is in `draft` or `published`
(`backend/internal/contests/contests.go:246-248`). Grading would accept an
answer to a hidden question (`backend/internal/contests/submission.go:186-187`),
but a participant never receives its id.

Impact: once a contest runs, a hidden question can be neither revealed nor
answered; it is dead weight in the question list (the gate already refuses one
that would block a sequential contest, `publish.go:28-32,176-177`).

### P5. A manager without `contest.create`

Evidence: `GET /contests` gives the staff list only to holders of
`contest.admin_all` or `contest.create`; anyone else gets the participant view
(`backend/internal/api/contests_handler.go:351-360`). After sign-in such an
account lands on `/my` (`frontend/lib/auth/destination.ts:14-20,44-47`).
Recorded as a known gap at `docs/guide/02-use-cases.md:264`.

Impact: a student appointed manager runs the contest only by typing its URL.

### P6. Endpoints with no screen

Evidence (routes in `backend/internal/api/contests_handler.go`; the frontend's
mutating calls, all listed by `grep '"DELETE"\|"PATCH"\|"PUT"'` over
`frontend/app` and `frontend/lib`, include none of these):

| Endpoint | Route | Guide |
|---|---|---|
| Delete a draft, `DELETE /contests/{id}` | `:89` | `docs/guide/02-use-cases.md:269` |
| Extend the grace, `PATCH /contests/{id}/grace` | `:88` | `docs/guide/02-use-cases.md:332` |
| `PATCH /questions/{id}` | `:96` | `docs/guide/02-use-cases.md:272` |
| `PUT /questions/{id}/texts` | `:101` | same |
| `PUT /questions/{id}/answers` | `:102` | same |

The last three predate the single Save button (`PUT /questions/{id}`,
`contests_handler.go:99`; §15 Done item 4 at `docs/ARCHITECTURE.md:4204`) and
are now redundant API surface.

Impact: an organiser cannot delete an abandoned draft, and cannot keep a
finished contest's databases past the grace period without calling the API by
hand.

### P7. Contest package import

Intent: accept a package "all or nothing, with every problem at once" and
refuse an unknown version (`docs/ARCHITECTURE.md:4121-4128`).

Evidence: `GET /contests/{id}/export` exists
(`backend/internal/api/contests_handler.go:107`); there is no import route
(the route list in that file has no matching `POST`), as
`docs/guide/01-overview.md:106` states. A game not written in the editor, an
uploaded dump or one described in the table builder, is not carried in the
package at all (`GameOmitted`; `backend/internal/provisioning/template.go:436`).

Impact: "take last year's olympiad and adjust it" stops at a JSON file nobody
can load.

### P8. "Reset my database"

Intent: in write mode "the interface offers a reset: provisioning recreates
their instance from the template in seconds, and the fact is written to the
audit trail and the query log" (`docs/ARCHITECTURE.md:483-487`).

Evidence: `provisioning.Service.Reset` exists
(`backend/internal/provisioning/provisioning.go:360-369`); no production code
calls it, no route exposes it, and no audit action records it
(`docs/api/audit-actions.json`).

Impact: a participant in a `read_write` contest who damages their data has no
way back.

### P9. Audit filters for actor and entity

Intent: §15 item 8 (`docs/ARCHITECTURE.md:4035-4042`).

Evidence: the API reads `actor` and `entity_id`
(`backend/internal/api/audit_handler.go:122-129`), both indexed
(`backend/migrations/000004_submissions_and_journals.up.sql:59`,
`backend/migrations/000011_audit_log_entity_index.up.sql:5`), and the client
helper can send them (`frontend/lib/api/audit.ts:53,56`). The page reads only
action, entity and two dates (`frontend/app/(admin)/audit/page.tsx:31-37`) and
the form offers only those (`frontend/app/(admin)/audit/filters.tsx:37-69`).
The index concern in the item is already met.

### P10. Participants from a file

Intent: participants "imported from CSV" (`docs/ARCHITECTURE.md:131`); §15
item 12, "A file adds only the parsing here — a CSV, or a column pasted from a
spreadsheet" (`docs/ARCHITECTURE.md:4109-4113`).

Evidence: a pasted list split on whitespace, commas and semicolons is parsed
(`frontend/lib/api/people.ts:88-95`) and posted to
`POST /contests/{id}/participants`. There is no file upload on the people
screen.

### P11. Bulk account actions

Intent: §15 item 9, one description of an action from which dialogues and
server actions derive (`docs/ARCHITECTURE.md:4043-4051`).

Evidence: on the server the rule is already single: `validateReason` trims,
refuses an empty reason and bounds the length
(`backend/internal/users/service.go:474-484`). In the frontend the empty-reason
check is still written per action
(`frontend/app/(admin)/users/bulk-actions.ts:57-62`;
`frontend/app/(admin)/users/[userId]/actions.ts:84-87,109`).

### P12. Unchecked ids in the play screen's actions

Evidence: the rule is "validate ids before they reach a path" with `isId`, and
the play screen is the named exception (`docs/guide/05-frontend.md:257-263`);
`contestId` and `questionId` go into paths unchecked
(`frontend/app/(participant)/contests/[contestId]/play/actions.ts:40,82`).

## Operational gaps

### O1. Backups: manual and on the same host

What exists: `make backup` dumps the core database and archives the covers
volume under one timestamp, refusing a partial result
(`Makefile:449-476`); `make restore`, `restore-check` and `restore-covers`
(`Makefile:482-561`). Every run ends with "Copy what was written off this
machine" (`Makefile:476`).

What it does not do:

- No schedule. Nothing in `deploy/` or the Makefile runs it periodically.
- No off-host copy, rotation or encryption. The output directory is
  `deploy/backups` on the host (`Makefile:27`), git-ignored
  (`.gitignore:17-18`).
- Secrets: the move procedure says the `.env` must live "somewhere that
  outlives the machine" (`docs/ARCHITECTURE.md:3854-3857`); nothing provides
  that.

### O2. The game-uploads volume is not backed up

Evidence: organisers' uploaded SQL dumps and the table builder's CSV data live
on `game-uploads-data` (`deploy/docker-compose.yml:562,623-625,796`).
`make backup` copies only the dump and the covers (`Makefile:449-476`); the
compose comments and `.env.example` flag covers as "a second thing to back up"
(`deploy/.env.example:237-240`) but say nothing of uploads. A file-sourced game
is built by streaming that file (`backend/internal/provisioning/template.go:657-689`).

Impact: after losing the host, a contest whose game was an uploaded dump or
CSV-filled tables cannot rebuild its template, and the package export does not
carry such a game either (P7).

### O3. The game side after a restore to a new host

Intent: "`pg-game-data`, the participants' instances, which provisioning
recreates on demand" (`docs/ARCHITECTURE.md:3826-3827`).

Evidence: the restored core database still records templates and instances as
built. No code checks that a recorded template exists on the cluster (the only
`pg_database` lookup is a size query,
`backend/internal/gamedb/provisioner.go:532-539`). A rebuild request is refused
once a contest is running (`backend/internal/provisioning/template.go:508-525`).
The move procedure's six steps contain no rebuild step
(`docs/ARCHITECTURE.md:3832-3844`).

Impact: the documented procedure is sound for core data; for a contest that is
running when the disk fails, nothing in the code or the runbook brings its game
back.

### O4. Restore drills

What exists: `restore-check` restores a dump into a scratch database on the
live `pg-core` and counts tables (`Makefile:482-497`);
`deploy/restore-covers-check.sh` proves file ownership with a synthetic
archive (`deploy/restore-covers-check.sh:1-15`).

What is missing: neither check runs in CI (no reference in
`.github/workflows/`), nothing schedules them, `restore-check` does not run
migrations or start the API against the restored data, and nothing exercises
the move procedure that §13 says can be "rehearsed completely"
(`docs/ARCHITECTURE.md:3846-3850`).

### O5. Monitoring and alerting

Intent: provisioning queue depth "is a metric with an alert"
(`docs/ARCHITECTURE.md:538-539`); metrics for active participants, student
query duration and refusal rate and queue depth
(`docs/ARCHITECTURE.md:2261-2263`); Loki with "dashboards in Grafana" and 30–90
days of logs (`docs/ARCHITECTURE.md:2253`); the Query Runner sending metrics to
Prometheus (`docs/ARCHITECTURE.md:113-114`).

Evidence:

- No alerting: `deploy/observability/prometheus.yml` has no `rule_files`, and
  the compose file has no Alertmanager (`deploy/docker-compose.yml:732-786`).
- No dashboards: Grafana is provisioned with datasources only
  (`deploy/observability/` holds `grafana-datasources.yml`, `prometheus.yml`,
  `promtail.yml`).
- Metrics defined: HTTP request counts and durations, stream durations
  (`backend/internal/platform/metrics/prometheus.go:34-44`) and reclamation
  counters (`backend/internal/platform/metrics/reclaim.go:30-46`). No queue
  depth, no active-participant gauge.
- The Query Runner exposes no metrics (`backend/cmd/queryrunner/main.go:9-24`
  imports no metrics package) and Prometheus scrapes only the API
  (`deploy/observability/prometheus.yml`).
- No PostgreSQL or Redis exporter.
- Loki runs on the image's built-in configuration
  (`deploy/docker-compose.yml:748`); no retention is configured in the
  repository.
- The whole stack is an optional profile (`deploy/docker-compose.yml:734`).

Impact: during an olympiad nobody is told when the runner, a cluster or the
provisioning queue is in trouble; someone has to be watching.

### O6. High availability

Intent: the API and Query Runner "scale horizontally behind the proxy"
(`docs/ARCHITECTURE.md:3613-3614`), though replication "is recorded as 'do not
make it impossible later'" (`docs/ARCHITECTURE.md:69-73`).

Evidence: one host, one `pg-core`, one `pg-game`, one Redis, no WAL archiving
or replica in the compose file. A second API replica is blocked by local
volumes for uploads and covers (`deploy/docker-compose.yml:605-618`) and by the
per-process provisioning guard
(`backend/internal/provisioning/provisioning.go:458`). The recovery point is
the last manual `make backup` (O1).

### O7. CI and supply-chain promises

| Promise | Source | Code |
|---|---|---|
| `npm audit` in CI | `docs/ARCHITECTURE.md:3529` | Frontend CI runs lint, contrast, type scale, error codes, types, tests, build, smoke (`.github/workflows/frontend.yml:54-89`); no audit |
| Images published "after the tests and the security scan" | `docs/ARCHITECTURE.md:3753` | Backend publish needs `[build, security]` (`.github/workflows/backend.yml:213,240`); frontend publish needs `build` only (`.github/workflows/frontend.yml:97,110`) |
| Pinned base images | `docs/ARCHITECTURE.md:3529` | Go builder pinned to a patch (`backend/Dockerfile:4,67`); `node:22-alpine` (`frontend/Dockerfile:8,22`), `postgres:16-alpine`, `redis:7-alpine`, `caddy:2-alpine` (`deploy/docker-compose.yml:24,69,317,696`) float |
| Secrets "through environment variables or Docker secrets" | `docs/ARCHITECTURE.md:3527` | Environment only; no `secrets:` in `deploy/docker-compose.yml` |

## Stale roadmap entries

Each of these has since been corrected in the document it names; they are
kept here as found.

- **S1. §14 step 5, "The game", is unchecked** (`docs/ARCHITECTURE.md:4001-4005`)
  but built: the play routes (`backend/internal/api/participant_handler.go:194-201`),
  the event stream (`backend/internal/api/events_handler.go:165`), the penalty
  and sequential progression (§15 Done items 2 and 3,
  `docs/ARCHITECTURE.md:4197-4203`).
- **S2. §14 step 6 is unchecked** (`docs/ARCHITECTURE.md:4006-4009`); the
  leaderboard and the scoring modes are done (§15 Done 1, 16, 18), the
  participant's CSV and monitoring's CSV exist. What remains is N1 and N2.
  Split the step.
- **S3. §14 step 7 names k6** (`docs/ARCHITECTURE.md:4010-4013`, also
  `:3602`). The load tool is `cmd/consoleload`, covering the burst scenario and
  provisioning (`backend/cmd/consoleload/main.go:1-10`, `Makefile:618`). The
  copy strategy is configurable (`backend/internal/gamedb/provisioner.go:80-90`).
- **S4. §15 item 9** describes the reason rule as written in four places with a
  length limit and a whitespace ban as hypothetical next changes
  (`docs/ARCHITECTURE.md:4043-4051`); the server already has one
  `validateReason` doing both (P11).
- **S5. §15 item 12** says participant import lacks "the parsing"
  (`docs/ARCHITECTURE.md:4109-4113`); paste parsing exists (P10). Only a file
  upload and the package import remain.
- **S6. §13 "Exactly one thing is carried across: the core database's dump"**
  (`docs/ARCHITECTURE.md:3808`) contradicts its own bullet on covers
  (`:3821-3822`), the Backups paragraph (`:3795-3800`) and `make backup`,
  which writes two files. Uploads (O2) are missing from all of them.
- **S7. Module names.** §2.2 lists `registrations`, `submissions` and
  `reporting` modules (`docs/ARCHITECTURE.md:182-189`) and §13's tree lists
  `submissions/` and `reporting/` (`:3669-3670`). None exists under
  `backend/internal/`; registrations and submissions live in `contests`, the
  leaderboard in `leaderboard`.
- **S8. §6.1 says the publication gate checks "the game instances' versions"**
  (`docs/ARCHITECTURE.md:1067-1069`). It does not (P3).
- **S9. README "For an organiser"** lists "a query log with filters and export
  … and reports afterwards" (`README.md:21-28`). The query log exists
  inside per-contest monitoring, not as the cross-contest §9.1 panel (N1), and
  staff reports beyond the standings do not exist (N2).
- **S10. SPEC.** "Built so far" (`docs/design/SPEC.md:822-824`) omits most of
  `frontend/components/` (Dialog, Tabs, Tooltip, Combobox, Checkbox, Textarea,
  Avatar, the code editor, standings and others). "Next is item 3 of the
  architecture" (`docs/design/SPEC.md:1024-1025`) is long past.
- **S11. SPEC §17's side edit** to the architecture (`docs/design/SPEC.md:1029-1034`;
  "has not been made yet", `docs/design/README.md:69-71`) plans a "TanStack
  Query invalidation strategy". The frontend does not use TanStack Query
  (`frontend/package.json`), and the architecture already says there is no
  client-side query cache (`docs/ARCHITECTURE.md:239`). The frontend
  architecture is written up in `docs/guide/05-frontend.md` instead.

## Deferred by decision, confirmed absent

Recorded so a reader does not mistake them for oversights. None has code.

| Item | Source |
|---|---|
| Team mode | `docs/ARCHITECTURE.md:4052-4087` (§15 item 10); no `team_id` in `backend/migrations` |
| A second accent hue | `docs/ARCHITECTURE.md:4096-4104` (§15 item 11.1) |
| A second visual language | `docs/ARCHITECTURE.md:4137-4172` (§15 item 14) |
| Achievements and levels | `docs/ARCHITECTURE.md:4173-4183` (§15 item 15) |
| Several tasks in one contest | `docs/ARCHITECTURE.md:4184-4191` (§15 item 17) |
| Manual review by a jury (`submissions.review_status`) | `docs/ARCHITECTURE.md:4240-4241` |
| University SSO (LDAP, OAuth) | `docs/ARCHITECTURE.md:4242-4243` |
| Content versioning of stories and wordings | `docs/ARCHITECTURE.md:2443-2450` |
| Erasure of personal data from the audit payload | `docs/ARCHITECTURE.md:2487-2492` |
| Schema per participant ("Plan B") | `docs/ARCHITECTURE.md:607-611` |
| Sharding the game cluster | `docs/ARCHITECTURE.md:3616-3620` |
| Kubernetes and autoscaling | `docs/ARCHITECTURE.md:3654-3656` |
| Object storage for covers and uploads | `docs/ARCHITECTURE.md:3322-3326`; `deploy/docker-compose.yml:614-618` |

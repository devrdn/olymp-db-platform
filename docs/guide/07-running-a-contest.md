# 7. Running a contest

The runbook for a live contest on a deployed installation: what to do in the
week, the day and the hour before, what to watch and what to type while it
runs, and what to close afterwards. It was written for the beta on
olympdb.usm.md (about forty remote participants, a `read_write` contest) and
the olympiad on 2 November, and holds for any contest after them.

It links to the reference instead of repeating it. The commands and their
settings are explained in [backend/README.md](../../backend/README.md); the
screens and the rules behind them in [2. Use cases](02-use-cases.md) and
[3. Flows](03-flows.md).

## Contents

1. [Where each step runs](#1-where-each-step-runs)
2. [Before the contest](#2-before-the-contest)
3. [During the contest](#3-during-the-contest)
4. [After the contest](#4-after-the-contest)
5. [The olympiad](#5-the-olympiad)

## 1. Where each step runs

| Where | What runs there | Needs |
|---|---|---|
| The host, in the checkout | every `make` target that touches the stack (`deploy`, `backup`, `restore-check`, `game-check`), `docker compose`, `deploy/backups/schedule.sh` | `deploy/.env`, membership of the `docker` group, `sudo` for the backup timer |
| A workstation | `make images` and the release checks, the load test, the browser smoke test | the repository at the release tag, Go, Node and Docker; a `deploy/.env` with the database passwords for `make test-game` and `make test-game-build`; SSH to the host for the load test |
| The browser | everything an organiser does: accounts, the contest, monitoring, standings | an administrator's account, or the contest's owner or a manager |
| Grafana and Prometheus | the dashboard and the alert rules | run outside this repository; the host's Alloy collects the metrics |

The host compiles nothing and publishes neither database's port, which is why
the load test and the smoke test run from a workstation. Every
`docker compose` command below runs from the checkout and names the
deployment's file, `docker compose -f deploy/docker-compose.yml`, as the
Makefile does.

Times in a contest's settings are Europe/Chisinau. The clocks go back on
25 October, between the beta and the olympiad; backup stamps are UTC and do
not care.

## 2. Before the contest

### A week before

**For the release that brings email (v0.5.0).** Step 1 deploys it. Two
things go with it:

- Set `SMTP_HOST`, `SMTP_PORT`, `SMTP_TLS`, `SMTP_USERNAME`,
  `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_MAX_PER_MINUTE` and `PUBLIC_URL` in
  `deploy/.env` before `make deploy` (step 6 has the values); step 1's
  `diff` lists them until then. Without `SMTP_HOST` the release runs with
  mail off.
- Load `dbcontest-rules.yml` again (step 3). It adds two alerts,
  `DBContestMailAbandoned` and `DBContestMailRelayFailing`, to a group the
  file already had, so the rules page still lists four groups.

**1. Back up, then deploy the release.** The release is v0.5.0. It carries
email, and everything v0.4.0 brought to a host still on an earlier tag: the
schema fix, the Query Runner's metrics, the scheduled backup and the feedback
link; the two releases' notes list the rest. Before it reaches the host, run
the checks from both releases' notes on a workstation. `make images`
builds from the working tree, so check the tag out first; `make test-game`
and `make test-game-build` read the database passwords from `deploy/.env`.
The first run of `make observability-check` pulls a Prometheus image, and
the first run of `make scheduled-backup-check` builds one from Debian unless
`CHECK_IMAGE` names an image built already, so each needs the network when
its image is missing:

```bash
git fetch --tags
git checkout v0.5.0
make images
make game-uploads-check
make test-game
make test-game-build
make observability-check
make scheduled-backup-check
```

On the host, back up with the checkout as it is, then bring the checkout to
the same tag. The images carry the code, but `make deploy` runs the compose
file, the Makefile and the scripts of the host's checkout: the Query Runner's
metrics listener (`QUERY_RUNNER_METRICS_ADDR: ":9101"`) is set in
`deploy/docker-compose.yml`, and `deploy/backups/schedule.sh` exists only
there. New images under an old checkout deploy without either.

```bash
make backup                                         # a set from before the deploy
make restore-check FILE=deploy/backups/<CORE_DB_NAME>-<stamp>.dump   # the dump it wrote
git status                                          # nothing modified in tracked files
git fetch --tags
git checkout v0.5.0
diff <(grep -o '^[A-Z0-9_]*=' deploy/.env.example | sort -u) \
     <(grep -o '^[A-Z0-9_]*=' deploy/.env | sort -u)   # settings the release added
make deploy VERSION=v0.5.0
make deployed                                       # every service on the new tag
make game-check                                     # no game the core database expects is missing
```

`make backup` prints the name of each file it writes. The `diff` compares
setting names only, so no secret reaches the screen. A name only in
`.env.example` is a setting your `deploy/.env` lacks: read its comment there
and decide (the scheduled backup's `BACKUP_*` ones are optional). The
`diff` cannot show a changed default: this release raises
`QUERY_PER_MINUTE` to 60 and `ANSWER_RATE_PER_MINUTE` to 12, and a
`deploy/.env` that carries `QUERY_PER_MINUTE=30` or
`ANSWER_RATE_PER_MINUTE=6` keeps the old limits, so raise those lines or
delete them to take the defaults. Use
`make deploy`, not `make deploy-api`: `deploy-api` pulls only the API and
its migration job, and the Query Runner moves to the new tag too. Rolling back
is checking out the previous tag and the same command with that version
([Releases and deployment](../../backend/README.md#releases-and-deployment)).

**2. Rebuild any game built before this release.** Every host does this
step, including one already on v0.4.0. A game built before v0.4.0 has no
stored description, so the schema panel reads each participant's own copy
on every request, and never keeps it (ARCHITECTURE.md §15, item 49). A game
built on v0.4.0 or v0.5.0, the beta's included, has a description without
its foreign keys: the panel works, but the diagram draws its boxes with no
arrows and the Relations list is empty, which reads as a schema with no
keys. Rebuild the beta's game while the contest is a draft or published;
once it runs, a rebuild is refused.
For a game written as a script, open the contest's **Database** tab and press
**Save and build** without changing the script. For any game, call the build
route as staff ([below](#calling-a-staff-route-from-the-command-line)):

```bash
curl -sS --fail-with-body -b "$JAR" -X POST https://olympdb.usm.md/api/v1/contests/<contest id>/game/build
```

Wait until the tab shows the game as **ready**. The rebuild raises the game's
version, so the copies already made are made again, which before the start
costs nothing. A game first built after the deploy needs none of this.

**3. Load the alert rules and the dashboard.** Both files are in
`deploy/observability/import/`, and
[Alerts and the dashboard](../../backend/README.md#alerts-and-the-dashboard)
says how to load each into the Prometheus or ruler and the Grafana you run:

1. Wherever the host's Alloy scrapes the API at `api:9090`, add the Query
   Runner at `queryrunner:9101`, the same way. Neither port is published: the
   collector reaches them on the compose network.
2. Edit the one selector, the `dbcontest-selector` group at the top of
   `dbcontest-rules.yml`, to the job labels your collector gives the two
   targets. In Grafana's Explore, `count by (job, instance)
   (game_instances_reclaimed_total)` names the API's and `count by (job,
   instance) (queryrunner_active_queries)` the Query Runner's.
3. Load `dbcontest-rules.yml` (never `dbcontest-rules.test.yml`) and check
   the rules page lists four groups.
4. Import `dbcontest-dashboard.json` (Dashboards, New, Import) and pick the
   Prometheus data source. It opens as **DB Contest**.
5. Route the alerts on `severity`: `critical` reaches someone at once,
   `warning` can wait for the next break.

Five minutes after loading, `DBContestAPINotScraped` or
`DBContestQueryRunnerNotScraped` firing means the selector names no target.

**4. Install the backup timer.** Once, from the checkout on the host, as the
user who runs `make deploy`
([On a schedule](../../backend/README.md#on-a-schedule)):

```bash
sudo deploy/backups/schedule.sh install <user>
deploy/backups/schedule.sh status
```

It starts on the daily setting, 03:30. Set the off-host copy in
`deploy/.env` (`BACKUP_OFFHOST`, `BACKUP_OFFHOST_TARGET`, and an SSH key or an
rclone remote) unless you copy the sets off the host by hand. `BACKUP_KEEP`
and `BACKUP_MIN_FREE_MB` decide how much disk the sets may take; the README
has the arithmetic.

**5. Restore-check the first scheduled set.** The morning after the install,
the timer has written its first set:

```bash
deploy/backups/schedule.sh status                  # the last result and the newest set's stamp
make restore-check FILE=deploy/backups/<CORE_DB_NAME>-<stamp>.dump
du -ch deploy/backups/*-<its timestamp>.*          # S, the size of one set
```

A set that was written is not yet a set that restores; this is the drill
that proves it. Check `S` against the table in the README before the contest
window multiplies it.

**6. Set up email.** Skip it to hand every password over by hand.

1. From the host, check that the relay can be reached. A timeout means
   outbound port 587 is closed, which the university's network team opens:

   ```bash
   nc -vz smtp.office365.com 587
   ```

2. Set the relay in `deploy/.env`:
   [The mail relay](../../backend/README.md#the-mail-relay) has USM's values
   and Microsoft's conditions on the mailbox. `SMTP_PASSWORD` comes from the
   university's administrators; `SMTP_MAX_PER_MINUTE` stays at 25, under
   the 30 a minute Microsoft allows; `PUBLIC_URL` is
   `https://olympdb.usm.md`. Set after step 1's deploy, they take effect
   when the API is recreated, with the tag of the `api` image that
   `make deployed` prints:

   ```bash
   VERSION=<tag> docker compose -f deploy/docker-compose.yml up -d --no-deps api
   ```

   The API's log then says `mail relay configured`.
3. On **Installation** (`/settings`), **Email** says it is on and the
   address it sends as. Give your own account an email address on
   **Accounts**, press **Send a test message**, and check that it arrived in
   the inbox rather than in spam. If it lands in spam, tell the university's
   administrators: SPF, DKIM and DMARC for `usm.md` are theirs. A refusal
   shows the relay's reply beside it;
   [Diagnosing](../../backend/README.md#diagnosing) says what Microsoft's
   codes mean.
4. In the same screen's form, choose the **Email language**, the one every
   message is written in (Romanian until changed), and set the **Contact
   email**, which every sign-in message gives as the address to write to
   when something does not work.
5. Before resetting the passwords of accounts that already exist, list the
   stored addresses that are not one bare address, which the message check
   refuses. One such address fails the whole bulk reset with a 500, so
   correct each on **Accounts** first. On the host, with the names from
   `deploy/.env`:

   ```bash
   docker compose -f deploy/docker-compose.yml exec -T pg-core \
     psql --username=<CORE_DB_USER> --dbname=<CORE_DB_NAME> -c "
       SELECT login, email FROM users
       WHERE email <> '' AND NOT (email ~ '^[^[:space:]<>@]+@[^[:space:]<>@]+$')"
   ```

   It should list no rows.

**7. Create the participants' accounts.** On **Accounts** (`/users`), choose
**Import a roster** and paste one line per person: login, full name and,
optionally, email, separated by commas; only an account with an address can be
emailed. Tick no role: a participant needs none. At most 500 lines a time; a
line that cannot be used is reported with its reason and the rest go in. Tick
**Send sign-in details by email**: each account with an address is sent its
login, its one-time password and the sign-in link. The one-time passwords are
also shown once, emailed or not. The first sign-in makes each participant
choose their own password.

- The answer says how many messages were queued and how long sending them
  takes: about ⌈N/25⌉ minutes for N accounts at the default rate, twenty
  for five hundred.
- An account without an address is listed as not emailed. Its password is
  on the screen with the others (**Copy all passwords** takes them all):
  hand it to its owner by a channel only they read.
- Watch `DBContestMailAbandoned` and `DBContestMailRelayFailing` until the
  queue has gone ([What to watch](#what-to-watch)).
- Then read the `noreply-olymp@usm.md` mailbox, and again the next morning.
  Microsoft 365 checks only `usm.md` addresses while sending: a mistyped
  address anywhere else is counted as sent, no alert fires, and its failure
  arrives there as a non-delivery report ("Undeliverable"). For each one,
  correct the account's address and reset its password with the option
  ticked.
- To send someone their details again, reset their password with the option
  ticked, on their account's page or for several at once from the list. The
  new message replaces an older one that has not gone out yet, and the
  older password stops working.
- Tell participants to look in their spam folder for the message from
  `noreply-olymp@usm.md`.

**8. Set up the contest.** In the contest's workspace:

| Tab | What to set for the beta |
|---|---|
| **Settings**, Who may enter, and from where | **Enrolment** by invitation; **Allowed networks** empty, since participants are remote; **Participants may download a result as CSV** on, the default, unless results should not leave the console as files |
| **Settings**, When it runs | **Starts** and **Ends**, both needed to publish |
| **Settings**, SQL access | **reading and writing**, the writable tables, the disk quota factor |
| **Story**, **Questions** | the content, in every declared language, with a reference answer per question |
| **Database** | the game, built and **ready** |
| **People** | **Add participants**: one login per line. Staff cannot also take part |

The **Overview** tab's **Ready to publish** panel lists everything the
publication gate still refuses, at once. Two of its refusals catch a beta
setup in particular: `no_game` (the game is not built, still building, or
failed) and `staff_registered` (a participant's account administers every
contest). Content freezes at the start; the window, the network and the CSV
download stay editable while the contest runs.

**9. Run the load test at the beta's size.** At a quiet time, never during
somebody else's contest. `consoleload -profile beta` drives forty synthetic
participants through the real path for fifteen minutes and passes or fails the
server on five thresholds. It runs on a workstation, reaching the API as
participants do and both databases through an SSH tunnel;
[Load testing before a contest](../../backend/README.md#load-testing-before-a-contest)
has the tunnel, the two credentials (`CORE_DB_DSN` and
`GAME_PROVISIONER_DSN`, both through the tunnel) and how to keep them out of
shell history. With those set, from `backend/`:

```bash
go run ./cmd/consoleload -profile beta -api https://olympdb.usm.md \
    -source-template game_tpl_c<12 hex> -headroom <GAME_POOL_DEPTH> \
    -label "beta rehearsal, $(date +%F)"
```

- It refuses to start while another contest is running or published to start
  within its run (about 1 h 15 min), and names it. Run it before the beta
  starts, not beside it; never pass `-allow-live-contests` with real
  participants on the server.
- The source template is a built game of the detective story; the beta's own
  is the most faithful.
- While it runs, its untitled contest heads the homepage's recent contests.
- It ends with `beta profile: PASSED` or `FAILED` and every failed check, and
  removes what it made. The whole run takes about twenty-five minutes.

If it was killed or the tunnel dropped, clean up with the same environment:

```bash
go run ./cmd/consoleload sweep          # lists what interrupted runs left
go run ./cmd/consoleload sweep -yes     # removes the runs that are over
```

A failed teardown prints the exact command that finishes it
(`consoleload sweep -run <run> -yes -force`).

### The day before

1. **Publish.** **Overview**, **Publish**. A published contest gets its
   spare pool, made in the background: a copy for every participant who has
   none yet, plus `GAME_POOL_DEPTH`. A participant is handed one of them on
   first use. The **Databases** tab lists them; check they reach **ready**.
2. **Run the browser smoke test.** From a workstation, after the load test
   and never beside it. It seeds an administrator, a participant and a
   running `read_write` contest of its own, runs a query, resets the database
   and answers, then archives the contest and deletes the two accounts. Put
   `E2E_BASE_URL=https://olympdb.usm.md`, `E2E_ADMIN_LOGIN` and
   `E2E_ADMIN_PASSWORD` in a file you write with an editor and `chmod 600`,
   and run:

   ```bash
   make e2e-smoke E2E_ENV_FILE=<that file>
   ```

   An administrator still on a one-time password is refused: sign in once by
   hand first. The run leaves an archived contest in the audit trail
   ([The browser smoke test](06-testing.md#the-browser-smoke-test)). If it was
   interrupted, check that no contest titled `E2E smoke …` is still running
   and no `e2e-admin-…` or `e2e-player-…` account is left; archive or delete
   what remains by hand, since an administrator account on production must
   not outlive the run.
3. **Set the feedback link.** **Installation** (`/settings`), **Feedback
   address**: a full `https` address, such as a form. Participants see
   **Send feedback** in their profile, and on the play screen only before
   their contest starts and once it is over for them. While the workspace runs
   it stays hidden, because opening another tab is reported to the organiser
   as an absence. An empty value removes the link.
4. **Switch the backups to the contest window**, every 15 minutes, and check
   it took:

   ```bash
   sudo deploy/backups/schedule.sh contest
   deploy/backups/schedule.sh status
   ```

   Do not deploy from here until the switch back: `pg_dump` holds a share lock
   on every table while it runs, and a migration waiting behind it holds up
   the API.

### The hour before

1. `docker compose -f deploy/docker-compose.yml ps`: every service up, the
   API and the Query Runner `healthy`.
2. `make game-check` passes.
3. `deploy/backups/schedule.sh status` shows the contest window, a successful
   last run and a set younger than 15 minutes.
4. The **DB Contest** dashboard shows both services up and no alert is
   firing.
5. The **Databases** tab shows at least as many **ready** copies as the
   roster has participants.
6. The scheduler starts the contest at **Starts**; **Overview**, **Start**
   opens it early if you mean to.

## 3. During the contest

### What to watch

Keep two screens open: the dashboard and the contest's **Monitoring** tab.

| Dashboard row | Panel | What it says |
|---|---|---|
| At a glance | **API**, **Query Runner** | `DOWN` is a failed scrape: the process is down or unreachable |
| At a glance | **API 5xx share** | the share of answers that were the server's fault |
| At a glance | **Running queries**, **Waiting queries** | eight slots and a queue of 32 by default; a full queue answers `busy` |
| API | **5xx by route** | which route fails; a 503 on the console is the Query Runner down or full |
| API | **Console query through the API** | what a participant waits for a query |
| Query Runner | **Not ok, by reason, per minute** | `error/internal` is the runner's own failure; `rejected/busy` and `timeout/queue` mean it is full; `error/database` and checker refusals are participants' own SQL |
| Query Runner | **Admission wait** | how long a query waits for a slot |
| Participant databases | **Database resets and game builds per hour** | resets participants make, and builds |
| Processes | **Resident memory**, **CPU** | per process |

The alerts, by what each asks of you
([the full table](../../backend/README.md#alerts-and-the-dashboard)):

| Alert | Severity | What to do |
|---|---|---|
| `DBContestAPIDown`, `DBContestQueryRunnerDown` | critical | [A service is down](#a-service-is-down) |
| `DBContestAPINotScraped`, `DBContestQueryRunnerNotScraped` | critical | a stopped container, or the selector or the Alloy target is wrong |
| `DBContestAPIErrors` | critical | **5xx by route**, then the API's log |
| `DBContestQueriesFailingInternally` | critical | the Query Runner's log, and whether `pg-game` is healthy |
| `DBContestQueryRunnerFull` | critical | participants are being told `busy`; see [the runner is full](#the-query-runner-is-full) |
| `DBContestProcessRestarted` | warning | a process restarted; find out why in its log |
| `DBContestAPISlow`, `DBContestQueriesTimingOut`, `DBContestAdmissionWaitHigh`, `DBContestAdmissionQueueDeep` | warning | the server is near its limits; look at the next break |
| `DBContestReclaimFailing` | warning | the sweep that drops finished contests' databases failed; it matters after the contest, not during |
| `DBContestMailAbandoned` | warning | somebody will not get an email. The API's log has the relay's reply; once the address or the relay is fixed, reset that password with the option ticked, or hand it over |
| `DBContestMailRelayFailing` | warning | no email is leaving; [Diagnosing](../../backend/README.md#diagnosing) |

Participants' own SQL errors and the checker's refusals never page.

The **Monitoring** tab (`/contests/<id>/monitor`) has the participants table,
with flags, and the live feed; a participant's row opens their timeline,
queries, answers and workspace. Flags are reasons to look, not findings: the
absence and paste flags rest on what the participant's own browser reported.

### What participants meet

Behaviour of the product that surprises people on the day:

- **The console closes once a participant has nothing left to answer**: every
  question answered correctly or out of attempts. The console answers
  `nothing_left_to_answer`; the story, the answers and the results stay. In a
  contest with a single question, a participant who solves it early, or spends
  its last attempt, has ended their own console. Say so in the instructions.
- **Staff cannot take part.** An account on the contest's staff is skipped
  when added as a participant ("already staffs this contest") and refused
  when it enrols itself (`staff_cannot_participate`), and an administrator on
  the roster holds publication back (`staff_registered`). Test as a
  participant with a plain account.
- **A participant can reset their own database** in a `read_write` contest:
  **Reset database** on the play screen puts every table back as the contest
  started it. Answers, score, notes, tabs and the query log stay. Three resets
  per ten minutes; the fourth answers `reset_too_often`.
- **The CSV download is a setting, not a lock.** With **Participants may
  download a result as CSV** off, the **Download CSV** button under a query
  result is gone; the rows are still on screen, can be selected and copied,
  and an open row keeps **Copy row**. A change while the contest runs reaches
  a participant's open play screen when they reload it.
- **Late answers.** `DEADLINE_GRACE`, five seconds by default, absorbs a
  request already on its way at the deadline. It is not extra time.

The participation gate refuses a participant with one code at a time. The
ones you hear about, and what to do
([the full table](02-use-cases.md#what-a-participant-is-refused)):

| Code | The participant sees | What it means for you |
|---|---|---|
| `not_a_participant` | "You are not taking part in this contest." | not on the roster, or disqualified. Add a latecomer on **People**; staff may add people while the contest runs |
| `contest_not_running` | "The contest is not open right now. It may open later." | not started yet. The waiting room opens by itself at the start |
| `contest_ended`, `contest_finished` | "The contest has ended…" | it is over; it cannot reopen |
| `deadline_passed` | "Your own deadline for this contest has passed." | their time is up. See [more time](#a-participant-needs-more-time) |
| `address_not_allowed` | "Access is allowed only from the university network." | the contest has **Allowed networks** set; for a remote contest, empty it |
| `no_game_yet` | "This contest's database has not been prepared yet…" | the contest's game is not built or is still building, for everybody: expected while a [recovery rebuild](#the-game-is-missing-or-the-game-cluster-was-lost) runs |
| `game_cluster_full` | "There is no room left on the game cluster…" | see [disk filling](#the-disk-is-filling) |
| `query_service_down` | "The query service is not answering right now…" | the Query Runner is down |
| `query_busy` | "The system is busy. Try again in a moment." | the Query Runner is full |
| `query_too_often` | "You are sending queries too quickly. Wait a moment." | past `QUERY_PER_MINUTE` (60 by default) or the contest's lower **Queries per minute**; lifts within the minute |
| `play_read_too_often` | "Too many requests from this page. Wait a minute and try again." | past `PLAY_READ_PER_MINUTE` (120 by default), about twenty reloads in a minute; lifts within the minute, and a console already on screen keeps running queries |
| `query_disk_full` | "Your database is at its size limit…" | their own copy hit its quota; they free it or reset |

### Incidents

#### A participant broke their database

1. In a `read_write` contest, ask them to press **Reset database** on the play
   screen.
2. If they cannot (the reset is refused, or the screen does not load), open
   **Databases** (`/contests/<id>/databases`), find their copy under
   **Held by** and press **Drop**. Their next query makes a fresh copy under
   the same name. Their answers, score and clock are not touched; a query they
   are running at that moment is lost.

#### The game is missing, or the game cluster was lost

Signs: 5xx answers on the console and `error/internal` climbing on the
dashboard (`DBContestQueriesFailingInternally`), or `pg-game` lost its volume.
The core database still records the game as ready, so participants are not
told `no_game_yet` until the rebuild below is pending.

1. If `pg-game` came back empty, recreate the game cluster's roles by running
   its preparation job, alone. It is the one part of a deploy a running
   contest may take, and the only exception to "do not deploy": never
   `make deploy` here, which pulls and recreates the whole stack and runs the
   migration job.

   ```bash
   VERSION=<tag> docker compose -f deploy/docker-compose.yml up -d game-prepare
   docker compose -f deploy/docker-compose.yml ps -a game-prepare    # Exited (0)
   docker compose -f deploy/docker-compose.yml logs game-prepare     # what it did
   ```

   `game-prepare` is a one-shot job that exits when done; `up -d` starts its
   stopped container again, after `pg-game` reports healthy. `<tag>` is the
   running one, from the `queryrunner` image `make deployed` prints:
   `deploy/.env` does not set `VERSION`, and without it Compose looks for the
   tag `dev`, which the registry does not have.
2. `make game-check` lists every contest whose game the core database records
   as built and the cluster lacks, with its status, and fails while one still
   to be played is among them.
3. Rebuild each contest it lists, as staff
   ([below](#calling-a-staff-route-from-the-command-line)):

   ```bash
   curl -sS --fail-with-body -b "$JAR" -X POST https://olympdb.usm.md/api/v1/contests/<contest id>/game/build
   ```

   A running contest is accepted only while its game is missing from the
   cluster; the audit trail records the request with `recovery: true`.
   Participants can run no query until the build finishes. Then each gets a
   fresh copy: what they changed in their database is gone, their answers and
   score are not. A game built from an uploaded file needs the uploads volume
   ([Backups](../../backend/README.md#backups)).

#### A participant needs more time

There is no extension for one participant. What exists:

- **Extend the contest for everybody.** **Settings**, When it runs, move
  **Ends** later; the window stays editable while the contest runs. Once the
  leaderboard is frozen, the freeze grows by the same minutes, so the frozen
  table does not reopen. Under individual timing the session length is
  frozen, so a later end helps only those whose time the contest's end was
  cutting short.
- **Nothing reopens a finished contest.** A finished contest moves only to
  archived. Extend before **Ends**, not after.

The **Databases kept after the end, minutes** field on the same tab is not
more time: it is how long participants' databases are kept after the contest
finishes ([After the contest](#4-after-the-contest)).

#### Cheating is suspected

1. **Monitoring**: filter **Flagged only** and read the flags' explanations
   (**What the flags mean**). Open the participant's row for their timeline,
   their queries and answers, and their workspace with its revisions.
2. **Same SQL** marks a successful query of at least 60 characters that
   matches another participant's; **IP** marks queries from more than one
   address; **2 sessions** marks a second session on the same registration
   while the first was active. The last two are what a shared account looks
   like.
3. To exclude someone: **People**, their row, **Disqualify**. Everything they
   did stays on the record and on the staff's standings, marked
   **Disqualified**; they are told `not_a_participant` from then on. Nothing
   in the product lifts a disqualification, and it is refused once the contest
   is archived.
4. The audit trail (`/audit`) has who disqualified whom, and when.

#### A service is down

```bash
docker compose -f deploy/docker-compose.yml ps                         # state and health
docker compose -f deploy/docker-compose.yml logs --since 15m api        # or queryrunner
docker compose -f deploy/docker-compose.yml restart queryrunner         # or api
```

Both services restart on their own after a crash (`restart: unless-stopped`);
a restart by hand is for one that is up and not answering. Restarting the
Query Runner fails the queries running at that moment; while it is down,
participants see `query_service_down`. Sessions live in Redis, so restarting
the API signs nobody out, and the play screen's live channel reconnects.
Do not deploy a new version mid-contest.

#### The Query Runner is full

`DBContestQueryRunnerFull`, or **Waiting queries** pinned at the queue's
depth: participants are told `busy` at once, and a query that waited past
`QUERY_QUEUE_WAIT` times out. The per-participant rate, **Queries per minute** on
**Settings**, can be lowered while the contest runs. Zero there means the
installation's `QUERY_PER_MINUTE` (60 by default), and a value above it
changes nothing.

The capacity itself is decided before the contest, not during it.
`QUERY_CONCURRENT` plus `QUERY_QUEUE_DEPTH` should be at least the number of
participants (8 + 512 by default: the queue costs nothing on the cluster, so
it is sized for an olympiad, not the beta's forty), `QUERY_CONCURRENT` also
sizes the game cluster's memory (`deploy/.env.example` has the arithmetic),
the alert thresholds assume the defaults ([Query Runner
metrics](../../backend/README.md#query-runner-metrics)), and the load test is
what showed the hardware holds them. Depth only decides who is told `busy`;
how long a queue of three hundred takes to drain is throughput, eight slots
at the mean query time, so a contest of slow queries still times out at
`QUERY_QUEUE_WAIT` unless the cluster has more slots.

#### The disk is filling

```bash
df -h
docker system df
deploy/backups/schedule.sh status       # the newest set's stamp and the number of sets
du -ch deploy/backups/*-<stamp>.*       # the size of that set
```

- **Backups.** A run is refused while the disk has less than
  `BACKUP_MIN_FREE_MB` plus twice the last set free, so a full disk never stops
  PostgreSQL for a backup. To free space now, lower the count and rotate:

  ```bash
  BACKUP_KEEP=16 deploy/backups/rotate.sh --dry-run deploy/backups
  BACKUP_KEEP=16 deploy/backups/rotate.sh deploy/backups
  ```

  Set the same `BACKUP_KEEP` in `deploy/.env` so the next run keeps it.
- **The game cluster.** `GAME_CLUSTER_MAX_BYTES` caps what all copies may
  take together; past it, a new copy is refused (`game_cluster_full`) and the
  provisioning tick logs both figures. **Databases** shows each copy's size.
  Check before the day that (roster + `GAME_POOL_DEPTH`) × the template's
  size fits the budget: the default 64 GiB holds 310 copies of a 210 MiB
  template, and a 1 GB dump leaves most of a three-hundred roster refused on
  their first query.
- **One participant's copy.** The disk quota factor caps each copy; at it,
  that participant's writes answer `query_disk_full`, and they free space or
  reset.

#### Calling a staff route from the command line

A few actions have no screen: rebuilding a running contest's lost game, and
extending the grace period after the end. Call them with an organiser's
session (an administrator, or the contest's owner or a manager), from a
machine with `curl` 7.76 or later and `jq`:

```bash
umask 077                         # curl rewrites the cookie jar; keep it owner-only
IFS= read -r LOGIN
IFS= read -rs PASSWORD            # typed, not echoed, never in history
export LOGIN PASSWORD
JAR=$(mktemp)                     # a 0600 file of its own
jq -n '{login: env.LOGIN, password: env.PASSWORD}' |
  curl -sS --fail-with-body -c "$JAR" -H 'Content-Type: application/json' \
    --data-binary @- https://olympdb.usm.md/api/v1/auth/login
unset PASSWORD

# ... the calls, each with --fail-with-body -b "$JAR" ...

curl -sS --fail-with-body -b "$JAR" -X POST https://olympdb.usm.md/api/v1/auth/logout
rm -f "$JAR"; unset LOGIN JAR
```

`jq` builds the body from the environment, so a quote or a backslash in the
password stays a valid string, and the password is never an argument any
process list shows. `IFS=` keeps leading and trailing spaces. With
`--fail-with-body` a refused call prints the API's answer and exits non-zero,
so a failed sign-in shows as itself rather than as a 401 on the next call.

The contest's id is the identifier in its workspace address,
`/contests/<id>`. Every route is listed with its permission in
[Contest staff: running a contest](02-use-cases.md#contest-staff-running-a-contest).

## 4. After the contest

1. **Switch the backups back** once the contest has finished, then take a
   last set of the finished contest and restore-check it:

   ```bash
   sudo deploy/backups/schedule.sh daily
   deploy/backups/schedule.sh status
   make backup                                                  # prints the files it writes
   make restore-check FILE=deploy/backups/<CORE_DB_NAME>-<stamp>.dump
   ```

   A set taken by hand counts in rotation like the others. With no off-host
   copy configured, copy it off the host now.
2. **Results.** The **Leaderboard** tab (`/contests/<id>/standings`) is the
   live table, disqualified participants included. If the leaderboard was
   frozen, **Reveal the results** shows everybody the final table; it cannot
   be undone. The public page and **Copy link** share it. There is no export
   of the standings; take them from the screen. **Monitoring**, Download,
   exports everything the participants did as CSV.
3. **Feedback.** Collect it from the form behind the feedback address. Empty
   **Feedback address** once you no longer want the link shown.
4. **Participants' databases are dropped after the grace period**: the
   contest's **Databases kept after the end, minutes**, or `GAME_INSTANCE_GRACE_MIN` (1440, a
   day) when it is zero. It is counted from the contest's last change of
   status or settings: the finish, and again from archiving or an extension,
   each of which restarts the count. The **Reclaim sweep per hour** panel
   shows them go. To keep them longer, for a dispute or a look at someone's
   work, extend it before it runs out
   ([as staff](#calling-a-staff-route-from-the-command-line)); a new value
   must be longer than the one in force:

   ```bash
   curl -sS --fail-with-body -b "$JAR" -X PATCH -H 'Content-Type: application/json' \
     -d '{"grace_period_min": 4320}' \
     https://olympdb.usm.md/api/v1/contests/<contest id>/grace
   ```

5. **Archive.** **Overview**, **Archive**, the contest's final state. An
   archived contest refuses disqualification; its grace can still be
   extended. Nothing deletes its query log or audit trail.
6. **Leftovers.** `consoleload sweep` lists any load-test run still on the
   server.

## 5. The olympiad

The olympiad on 2 November follows the same timeline. What differs:

- Run the load test again with the olympiad's numbers: `-participants`
  overrides the profile's forty.
- An on-site olympiad restricts **Allowed networks** to the hall's range.
  The check uses the address resolved through `TRUSTED_PROXIES`, and staff are
  exempt ([Enrollment and network
  restrictions](../../backend/README.md#enrollment-and-network-restrictions)).
- The feedback link may be taken down before it.
- Sign-in details by email need the release that brings it (v0.5.0) and
  step 6 of the week before; the paragraph above step 1 lists what that
  release adds.

What the beta taught goes into this chapter before then.

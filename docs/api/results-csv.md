# The results CSV, format version 1

The file an organiser downloads when they export a contest's results as CSV.
It is meant for another system, such as a gradebook, a faculty office's
spreadsheet or a script, so this document is the contract: a person writing
the importer can follow it without reading the code. The format is versioned
(see [Versioning](#versioning)); this is version 1.

The same endpoint also answers `format: "json"`, the report the interface
turns into a PDF. That representation is for the interface and is not
covered by the format version.

## Contents

1. [The request](#the-request)
2. [The response](#the-response)
3. [Encoding](#encoding)
4. [The columns](#the-columns)
5. [Rows](#rows)
6. [Reading the text cells](#reading-the-text-cells)
7. [The Excel flavour](#the-excel-flavour)
8. [Versioning](#versioning)
9. [Examples](#examples)

## The request

`POST /api/v1/contests/{contestID}/results/export`, with a JSON body and the
signed-in session's cookie. It is a POST because a hand-picked list can hold
two thousand identifiers, too long for a query string. Nothing is stored: the
file is built from the table as it stands at the moment of the request. The
[running-a-contest guide](../guide/07-running-a-contest.md#calling-a-staff-route-from-the-command-line)
shows how to call a staff route from the command line.

```json
{
  "format": "csv",
  "registrations": ["0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a51", "0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a52"],
  "detail": "summary",
  "fields": ["full_name", "login"],
  "csv": { "excel": false }
}
```

| Field | Values | Rule |
|---|---|---|
| `format` | `csv`, `json` | Required. |
| `registrations` | registration ids (UUIDs) | Required. 1 to 2000, no duplicates, each one on this contest's table. The file lists exactly these, no others. |
| `detail` | `summary`, `detailed` | Required. `detailed` adds the per-question columns. |
| `fields` | a non-empty subset of `full_name`, `login`, `email` | Required. Which identity columns the file carries. `email` needs the global permission `users.manage`. |
| `csv.excel` | boolean | Optional, default `false`. The [Excel flavour](#the-excel-flavour). Ignored when `format` is `json`. |

An unknown key is refused, and the whole body is limited to 1 MiB.

`registration_id` is the participant's identifier on this contest. The
contest's registrations, with the places they would have in a file, are read
by `GET /api/v1/contests/{contestID}/results`, the preview the export page
uses to offer its list.

### Who may export

Both routes need `contest.monitor` on the contest, which its owner and
managers hold, as does an administrator with `contest.admin_all`. The `email`
field additionally needs `users.manage`, because an email address is masked
for contest staff everywhere else in the product.

### What is checked, in what order

1. The session, then the permission.
2. The account's gate: one results export at a time per account.
3. The body: its shape, then whether `email` may be asked for.
4. The service-wide download slots, shared with every other export.
5. The report is built, from one read-only snapshot of the database, and the
   export is recorded in the audit trail (`contest.results_export`, with the
   format, the detail, the fields, the number of rows and the status; never
   the ids or the data).
6. The file is written.

A refusal before step 5 reads nothing and records nothing. The export is
recorded before the first byte is written, and an export that cannot be
recorded is refused.

### Errors

Every answer is the API's usual `{code, message}` with the status below; the
sentence for a code is in [error-codes.json](error-codes.json), and the
interface shows it in the reader's language.

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | The body is not valid JSON, has an unknown key, or a field breaks its rule above. The message names which. |
| 400 | `invalid_contest_id` | The contest id in the path is not a UUID. |
| 401 | `unauthenticated` | No valid session. |
| 403 | `forbidden` | The account lacks `contest.monitor` on this contest. |
| 403 | `results_email_forbidden` | `email` was asked for by an account without `users.manage`. |
| 404 | `not_found` | The contest does not exist. Only an account holding `contest.admin_all` can be told so: for anyone else the permission check answers first, with 403 `forbidden`. |
| 409 | `results_too_many_rows` | The contest has more than 2000 registrations. The export is refused, never cut: a cut file would look complete. |
| 422 | `results_unknown_registration` | A listed registration is not on this contest's table. A draft contest has no table, so every list names unknown registrations. |
| 429 | `results_export_too_often` | This account's previous results export is still being written. One at a time; try again when it has finished. |
| 500 | `internal_error` | The report could not be built (a read failed, or the export's one-minute deadline passed) or could not be recorded in the audit trail. Nothing was exported: the headers are written only after the build and the audit. |
| 503 | `too_many_exports` | As many downloads are being written at once as the installation allows. `Retry-After` says how long to wait at most. |

## The response

On success the status is 200 and the headers are:

| Header | Value |
|---|---|
| `Content-Type` | `text/csv; charset=utf-8` |
| `Content-Disposition` | `attachment; filename="results-<contestID>-<YYYYMMDDTHHMMSSZ>.csv"`. The stamp is the moment the report was made, in UTC. |
| `X-Results-Format` | `1`, the version of this document. Sent with the CSV only. |

## Encoding

- RFC 4180: UTF-8, a comma between cells, CRLF at the end of every line,
  including the last.
- The first line is the header. There is no preamble, no notice row and no
  blank line anywhere.
- A cell is wrapped in double quotes when it contains the separator, a double
  quote or a line break, and a double quote inside it is doubled. A quoted
  cell can span lines, so a record is not always a line. A writer may also
  quote a cell that does not need it; a reader must accept both.
- Column names are fixed English `snake_case` and are never translated.
- Times are UTC, written `2006-01-02T15:04:05Z`.
- Numbers are plain integers. Booleans are `0` and `1`.
- An empty cell means "none": no place, no prize, no email, no value that
  applies to this mode.

## The columns

Summary columns, in this order. Identity columns appear only when asked for,
and always in the order `full_name`, `login`, `email`.

| Column | Meaning |
|---|---|
| `place` | The place on the public table, shared on a tie (1, 1, 3). Empty for a disqualified row and for a row with no answer. In winner mode only the winner has a place, `1`; every other row is empty, answers or not. It is not renumbered for the rows chosen: a file of the second and fourth rows says 2 and 4. |
| `prize` | `1`, `2` or `3`, the prize tier the place wins under the contest's prize counts. Empty for no prize. In winner mode, `1` for the winner alone. |
| `registration_id` | The stable key of the participant on this contest. |
| `full_name` | If requested. The account's full name. |
| `login` | If requested. The account's login. |
| `email` | If requested and allowed. Empty if the account has none. |
| `disqualified` | `1` if the participant is disqualified, otherwise `0`. |
| `solved` | The number of questions solved. |
| `penalty` | ICPC scoring: the penalty in minutes. Empty in points and winner mode. |
| `points` | Points and winner mode: the total points. Empty in ICPC mode. |
| `last_solved_at` | ICPC scoring: the time of the participant's last solve. Otherwise the moment the row reached its score. Empty if there is none. |
| `bonus_credit` | Points scoring, when the contest has a bonus question: the credit the participant's bonus questions gave back. Empty in every other case. |
| `status` | How settled the results are: `preliminary`, `final` or `archived`. The same on every row of the file. See below. |

`full_name`, `login` and `email` are free text; see
[Reading the text cells](#reading-the-text-cells). The account of a deleted
participant is gone, so its `full_name`, `login` and `email` are empty; the
row, its place and its numbers are not.

### Status

- `preliminary`: the contest is a draft, published or running. The results
  may still change.
- `final`: the contest has finished. The rows are complete, but a
  disqualification or a change of the prize counts can still move them until
  the contest is archived.
- `archived`: the contest is archived. Nothing can change.

### Detailed columns

With `detail: "detailed"` the summary columns are followed, for each question
in the contest's order, by three more. `N` is the question's 1-based
position, so the columns are `q1_…`, `q2_…` and so on. The names are the same
in every scoring mode, including ICPC, where the question the screen calls
`A` is `q1`.

ICPC scoring:

| Column | Meaning |
|---|---|
| `qN_solved` | `1` if the participant solved the question, otherwise `0`. |
| `qN_minute` | The minute of the solve, counted from the participant's start as on the screen. Empty if unsolved. |
| `qN_wrong` | The wrong attempts before the solve, or all the wrong attempts if the question is unsolved. |

Points and winner mode:

| Column | Meaning |
|---|---|
| `qN_solved` | `1` if the participant solved the question, otherwise `0`. |
| `qN_points` | The points the question gave, after penalties for wrong attempts. Empty for a bonus question, which gives nothing by itself. |
| `qN_attempts` | All the attempts, the correct one included. |

The questions are the ones the totals count. Under ICPC scoring those are the
visible questions, as the grid shows them. In the other modes it is every
question, hidden ones included, since a question hidden later keeps the points
it gave. A position is counted among the questions the file covers.

In points mode the totals add up: `points` equals the sum of the `qN_points`
plus `bonus_credit`. A bonus question's credit is the smaller of the face
value of the bonus questions solved and the points lost to penalties, which
is why it is a column of its own and not part of any question.

## Rows

- A file lists the requested registrations and no others, in the order of the
  table: the participants with a place, by place with ties adjacent, then the
  rest of the participants who are not disqualified, then the disqualified.
  Rely on `place` for the ranking, not on the position of a line.
- In winner mode only the winner has a place, `1`, and is listed first.
  Everyone else follows in points order with an empty `place`, even with
  answers; that order is not a ranking the contest gives.
- A tie shares its place and each row keeps its own numbers.
- A disqualified participant is listed with `disqualified` `1`, no place and
  no prize, and is not counted when the places of the others are given: the
  place is the one the public table shows.
- During a freeze the file holds the real current results, the ones the jury
  sees, not the table participants see. A CSV cannot carry a note without
  breaking its format, so the page warns the organiser before the download.

## Reading the text cells

A spreadsheet reads a cell that starts with `=`, `+`, `-` or `@` as a
formula, and a value from a participant's account can start with any of them.
So the three free-text columns, `full_name`, `login` and `email`, are written
with a leading apostrophe `'` when the value begins with `=`, `+`, `-`, `@`, a
tab or a carriage return. The other columns never need it.

To get the original value back, strip a leading `'` when the character after
it is one of those six. This is ambiguous only for an original value that
itself starts with `'` followed by one of them, which cannot be told from a
value that was protected. A system that does not open the file in a
spreadsheet may still strip it, and should.

## The Excel flavour

With `csv.excel: true` the columns and rows are the same, but the file starts
with a UTF-8 byte order mark (`EF BB BF`) and the separator is a semicolon.
Excel in a Romanian or Russian locale splits cells on `;` and reads UTF-8
without a byte order mark as ANSI. This flavour is for opening by hand, not
for import: a program should ask for the default flavour, which has no byte
order mark and a comma.

## Versioning

The version is the response header `X-Results-Format` together with the set
of columns described here.

- Removing or renaming a column, changing what a column means or the format
  of a value, or moving a column is a new version, `2`.
- Adding a column at the end is not. The new column appears after the ones
  documented here, and the version stays `1`.
- So a consumer selects columns by their names from the header line, never by
  position, and ignores the names it does not know.

Which identity columns are present, and whether the per-question columns are
present, depend on the request; a reader finds them in the header.

## Examples

The lines are shown ended by a line feed. The file ends every line, the last
included, with CRLF. The test `TestTheDocumentedExamplesAreWhatTheWriterWrites`
builds both reports and compares the writer's output with these blocks
byte for byte, so they cannot drift from the code.

An ICPC contest that has finished, summary rows, with the full name and the
login. The first two participants tie on place 1, so the next place is 3, and
the third row holds the third prize. The fourth never answered and has no
place. The fifth was disqualified: no place, though they solved more
questions than anyone.

<!-- example: icpc-summary -->
```csv
place,prize,registration_id,full_name,login,disqualified,solved,penalty,points,last_solved_at,bonus_credit,status
1,1,0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a51,"Popescu, Ana",ana,0,3,214,,2026-11-02T10:58:00Z,,final
1,1,0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a52,Ion Rusu,ion,0,3,214,,2026-11-02T11:02:00Z,,final
3,3,0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a53,Maria Ceban,maria,0,2,187,,2026-11-02T11:20:00Z,,final
,,0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a54,Vlad Lupu,vlad,0,0,0,,,,final
,,0b6f4a1e-2d3c-4e5f-8a9b-0c1d2e3f4a55,Cezar Munteanu,cezar,1,4,120,,2026-11-02T10:30:00Z,,final
```

A points contest that is still running, detailed rows, with the login only.
The third question is a bonus question: its `q3_points` is empty and the
credit is in `bonus_credit`, so for the first row `4 + 5 + 1` is the `10`
points. The second login, `@dina`, is written `'@dina`. The third row has not
answered anything.

<!-- example: points-detailed -->
```csv
place,prize,registration_id,login,disqualified,solved,penalty,points,last_solved_at,bonus_credit,status,q1_solved,q1_points,q1_attempts,q2_solved,q2_points,q2_attempts,q3_solved,q3_points,q3_attempts
1,1,1c7a5b2f-3e4d-4f60-9bac-1d2e3f4a5b61,ana,0,3,,10,2026-11-02T10:41:00Z,1,preliminary,1,4,2,1,5,1,1,,1
2,2,1c7a5b2f-3e4d-4f60-9bac-1d2e3f4a5b62,'@dina,0,1,,5,2026-11-02T10:12:00Z,0,preliminary,1,5,1,0,0,2,0,,0
,,1c7a5b2f-3e4d-4f60-9bac-1d2e3f4a5b63,mihai,0,0,,0,,0,preliminary,0,0,0,0,0,0,0,,0
```

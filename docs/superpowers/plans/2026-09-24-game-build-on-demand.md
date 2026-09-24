# Game Build On Demand Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Data an organiser puts into the table builder must reach the database participants copy, through a build the contest's staff can ask for once the data is in.

**Architecture:** A new `game_templates.data_changed_at` column records the moment a contest's table data last changed; the three service calls that change it set it inside their own transaction, and `FinishBuild` clears it only when it has not moved since the build claimed the template. A new `POST /contests/{id}/game/build` raises the version and puts the template back to `pending`, which the existing background job already acts on. The organiser's game screen shows the mark and offers the button.

**Tech Stack:** Go (chi, pgx v5), PostgreSQL, golang-migrate files under `backend/migrations`, Next.js App Router + Tailwind v4, vitest.

**Spec:** `docs/superpowers/specs/2026-09-24-game-build-on-demand-design.md`

## Global Constraints

- Commit messages, PR titles and descriptions, and everything in source — identifiers, comments, test names, log messages — are in English. Prose under `docs/` is Russian; this plan and its spec are the exception the project already makes for planning documents, which are Russian.
- History stays linear. Never `git merge`; a branch that falls behind is caught up with `git rebase`.
- Stage files explicitly by path. Never `git add -A` or `git add .`.
- Every error a service hands to the HTTP layer is a declared sentinel, mapped in the handler's `fail` switch, published in `docs/api/error-codes.json` via `make api-contract`, translated in `en`/`ru`/`ro`, and covered by a handler test asserting the 4xx (CLAUDE.md rule 1).
- A migration file numbered 34 or higher opens with `SET lock_timeout = '5s';` — `backend/migrations/migrations_test.go` fails the build otherwise. A file using `CREATE INDEX CONCURRENTLY` must hold exactly one statement and carry no timeout; this plan's migration uses neither.
- Test files mirror their source file: `foo.go` is tested by `foo_test.go` (CLAUDE.md Go layout rule 5).
- Domain packages never import a database driver; `internal/postgres` implements the interfaces the domain declares (Go layout rule 3).
- Never connect to `dbcontest_core` (port 5432) or `pg-game` (5433). Repository tests run against `dbcontest_core_test` through `make test-db`, which supplies `CORE_DB_DSN` itself.
- Never run `make run`, `make runner`, `make dev-up`, `make dev-down`, and never start, stop, create or remove a container you did not create. Never edit `deploy/.env`.
- Nothing under `.superpowers/` is ever committed.

---

### Task 1: The column, the domain field, and the two repository writes that move it

**Files:**
- Create: `backend/migrations/000039_game_template_data_changed.up.sql`
- Create: `backend/migrations/000039_game_template_data_changed.down.sql`
- Modify: `backend/internal/provisioning/template.go` (the `Template` struct near line 206; the `TemplateRepository` interface near line 251)
- Modify: `backend/internal/postgres/gametemplates.go` (`templateColumns` line 23, `templateStatusColumns`, `scanTemplate`, `scanTemplateStatus`, `FinishBuild` line 219)
- Modify: `backend/internal/postgres/gametabledata.go` (new `MarkTableDataChanged`)
- Test: `backend/internal/postgres/gametemplates_test.go`, `backend/internal/provisioning/template_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `provisioning.Template.DataChangedAt *time.Time`
  - `func (t Template) NeedsBuild() bool`
  - `TemplateRepository.MarkTableDataChanged(ctx context.Context, contestID uuid.UUID) error`
  - `TemplateRepository.FinishBuild(ctx context.Context, contestID uuid.UUID, version int, buildError string, claimedAt time.Time) error` — one argument wider than today.

- [ ] **Step 1: Write the migration**

`backend/migrations/000039_game_template_data_changed.up.sql`:

```sql
-- When a contest's table-builder data last changed, and NULL when it has not
-- changed since the build.
--
-- The data an organiser enters in the table builder arrives *after* the game
-- is built and cannot arrive before it: the build runs seconds after the
-- schema is saved, and a row may not be typed into a table that is not yet in
-- the saved schema. Nothing marked the built template out of date, so the
-- rows were stored, never loaded, and every participant copied an empty
-- database. This column is the mark, and internal/provisioning's RequestBuild
-- is what acts on it.
--
-- Not a status. The template built before the edit is still `ready` and still
-- usable — participants get the previous data — so this sits beside the
-- status rather than inside it, and every reader that does not care about
-- freshness goes on reading `status` alone.

-- Waiting is the dangerous half of a migration: DDL queued for a lock makes
-- every request needing the same table queue behind it. This file gives up
-- after five seconds rather than joining that queue — longer than any query
-- the API is allowed to run, so a wait past it is a wait on something else.
-- The file runs as one implicit transaction (cmd/migrate hands it over as one
-- string), so this covers every statement below it.
SET lock_timeout = '5s';

ALTER TABLE game_templates ADD COLUMN data_changed_at timestamptz;

-- Every game that already exists was built before this column did, and the
-- data it holds — if it is a builder game at all — was entered after that
-- build and never loaded. Marking those rows is not cosmetic: it is the only
-- way the organiser of an olympiad prepared last week is told that the
-- database their participants will copy is empty.
UPDATE game_templates
SET data_changed_at = now()
WHERE source = 'builder'
  AND status = 'ready'
  AND EXISTS (
      SELECT 1 FROM game_table_data d
      WHERE d.contest_id = game_templates.contest_id AND d.status = 'complete'
  );
```

`backend/migrations/000039_game_template_data_changed.down.sql`:

```sql
-- The mark is derived state: it says the built database no longer matches the
-- data, and dropping it loses only the knowledge, never the data itself.
SET lock_timeout = '5s';

ALTER TABLE game_templates DROP COLUMN IF EXISTS data_changed_at;
```

- [ ] **Step 2: Write the failing repository test**

Append to `backend/internal/postgres/gametemplates_test.go`:

```go
// TestFinishBuildClearsTheDataMarkOnlyWhenNothingChangedDuringTheBuild is the
// half of this feature that a fake cannot prove: two writers — the build
// finishing and an organiser adding a row — meeting on one row, arbitrated by
// a comparison PostgreSQL makes.
func TestFinishBuildClearsTheDataMarkOnlyWhenNothingChangedDuringTheBuild(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	repo := postgres.NewGameInstances(testPool)
	contest := contestRow(t, t.Context())

	saved, err := repo.SaveDefinition(t.Context(), contest, "game_"+contest.String()[:8], suspectsDefinition())
	if err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	if err := repo.MarkTableDataChanged(t.Context(), contest); err != nil {
		t.Fatalf("mark the data changed: %v", err)
	}

	claimed, err := repo.ClaimBuild(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("claim the build: %v", err)
	}

	// A row typed while the build runs: the mark moves past the claim.
	if err := repo.MarkTableDataChanged(t.Context(), contest); err != nil {
		t.Fatalf("mark the data changed during the build: %v", err)
	}
	if err := repo.FinishBuild(t.Context(), contest, saved.Version, "", claimed.UpdatedAt); err != nil {
		t.Fatalf("finish the build: %v", err)
	}

	after, err := repo.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	if after.DataChangedAt == nil {
		t.Fatal("the build cleared a mark left by a row added while it ran; that row will never be built")
	}

	// A second build, with nothing changing under it, does clear the mark.
	second, err := repo.ClaimBuild(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("claim the second build: %v", err)
	}
	if err := repo.FinishBuild(t.Context(), contest, second.Version, "", second.UpdatedAt); err != nil {
		t.Fatalf("finish the second build: %v", err)
	}
	settled, err := repo.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template again: %v", err)
	}
	if settled.DataChangedAt != nil {
		t.Fatalf("the mark survived a build that saw every change: %v", settled.DataChangedAt)
	}
}
```

If `contestRow` and `suspectsDefinition` do not already exist in this package's `support_test.go`, add them there — `contestRow` inserts a user and a contest and returns the contest id with a `t.Cleanup` that deletes both (copy the shape of `provisioning`'s own `contestFor`, `support_test.go:491`); `suspectsDefinition` returns a one-table `provisioning.Definition` with an integer `id` primary key and a text `name`. Check first: this package already has helpers for both in its own tests.

`ClaimBuild` takes the oldest `pending` template of any contest, so the two claims above can return another test's row. Both claims must be checked: if `claimed.ContestID != contest`, keep claiming (bounded, at most ten times) and finish each foreign claim back with its own `claimed.UpdatedAt` so nothing is left stuck in `building`. Write that as a helper `claimBuildFor(t, repo, contest)` in the same file rather than repeating it.

- [ ] **Step 3: Run it and watch it fail**

```bash
make test-db
```

Expected: a compile failure — `MarkTableDataChanged` undefined, `FinishBuild` takes four arguments, `DataChangedAt` undefined. That is the right failure: nothing exists yet.

- [ ] **Step 4: Add the domain field and the interface methods**

In `backend/internal/provisioning/template.go`, inside `type Template struct`, after `BuildError`:

```go
	// DataChangedAt is when this game's table data last changed, and nil when
	// nothing has changed since the build.
	//
	// The table builder's data arrives after the game is built and cannot
	// arrive before it — the build runs seconds after the definition is
	// saved, and AppendTableRow refuses a table that is not in the saved
	// definition. Without this mark the rows were stored and never loaded,
	// and every participant copied an empty database.
	DataChangedAt *time.Time
```

Below `Building()`:

```go
// NeedsBuild reports a game whose built database no longer holds the data an
// organiser has since put into it — the one state RequestBuild exists for.
//
// Only a game that is `ready`: a build already waiting or running will pick
// the data up on its own, and a failed build's own error is the thing to
// show rather than an invitation to press a button that would replace it.
func (t Template) NeedsBuild() bool {
	return t.Status == TemplateReady && t.DataChangedAt != nil
}
```

In `TemplateRepository`, widen `FinishBuild` and add the mark:

```go
	// FinishBuild records the outcome against the version that was built. The
	// version is what keeps a slow build from marking a newer script ready:
	// a script saved while the build ran has already bumped it.
	//
	// claimedAt is what ClaimBuild wrote as the row's updated_at, and it
	// arbitrates the data mark: the build may only clear a change it
	// actually saw, so a row an organiser added while this build ran keeps
	// its mark and is picked up by the next one.
	FinishBuild(ctx context.Context, contestID uuid.UUID, version int, buildError string, claimedAt time.Time) error

	// MarkTableDataChanged records that this contest's table data no longer
	// matches the database its game was built into. Called by the three
	// writes that change a table's rows, inside their own transaction, so a
	// change that rolled back leaves no request to build behind it and a
	// change that landed never loses one.
	MarkTableDataChanged(ctx context.Context, contestID uuid.UUID) error
```

- [ ] **Step 5: Implement the repository side**

In `backend/internal/postgres/gametemplates.go`, add `data_changed_at` to both column lists and both scanners (the struct field is `*time.Time`; pgx scans a nullable `timestamptz` into it directly). Then:

```go
func (r *GameInstances) FinishBuild(
	ctx context.Context, contestID uuid.UUID, version int, buildError string, claimedAt time.Time,
) error {
	if _, err := r.querier(ctx).Exec(ctx, `
		UPDATE game_templates
		SET status          = CASE WHEN $3 = '' THEN 'ready' ELSE 'failed' END,
		    build_error     = nullif($3, ''),
		    -- Only a change this build could have seen. A row added while it
		    -- ran moved the mark past $4, and clearing it here would lose
		    -- that row for good: no later build would know to look.
		    data_changed_at = CASE WHEN data_changed_at <= $4 THEN NULL ELSE data_changed_at END,
		    updated_at      = now()
		WHERE contest_id = $1 AND version = $2 AND status = 'building'`,
		contestID, version, buildError, claimedAt); err != nil {
		return fmt.Errorf("record the build's outcome: %w", err)
	}
	return nil
}
```

In `backend/internal/postgres/gametabledata.go`:

```go
// MarkTableDataChanged records that this contest's built game no longer holds
// the data its tables do. A contest with no game row at all is not an error:
// the data is stored, the game will be written later, and the build that
// writes it loads everything there is.
func (r *GameInstances) MarkTableDataChanged(ctx context.Context, contestID uuid.UUID) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`UPDATE game_templates SET data_changed_at = now() WHERE contest_id = $1`, contestID); err != nil {
		return fmt.Errorf("mark the contest's table data changed: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Fix every other caller of FinishBuild**

`Games.Build` in `backend/internal/provisioning/template.go` calls `FinishBuild`; pass the claimed template's `UpdatedAt`. The fake in `backend/internal/provisioning/support_test.go` (`templateStore.FinishBuild`, line 720) takes the new argument and must actually honour it, or every service test of the mark proves nothing:

```go
func (s *templateStore) FinishBuild(
	_ context.Context, _ uuid.UUID, version int, buildError string, claimedAt time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, finish{version: version, err: buildError})
	if s.template.DataChangedAt != nil && !s.template.DataChangedAt.After(claimedAt) {
		s.template.DataChangedAt = nil
	}
	return nil
}
```

`templateStore.ClaimBuild` (line 708) must also set `updated_at` on the row it returns, or `claimedAt` is the zero time and nothing is ever cleared:

```go
	s.claims++
	now := time.Now()
	s.template.Status = provisioning.TemplateBuilding
	s.template.UpdatedAt = now
	claimed := s.template
	return claimed, nil
```

Add `MarkTableDataChanged` to the fake too:

```go
func (s *templateStore) MarkTableDataChanged(_ context.Context, _ uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now()
	s.template.DataChangedAt = &at
	return nil
}
```

Check for other implementations of `TemplateRepository`: `grep -rn "FinishBuild" backend/internal` and fix every one.

- [ ] **Step 7: Run the repository tests**

```bash
make test-db
```

Expected: PASS, including the new test. If the test database is not running, say so plainly rather than claiming the step passed — the migration and the SQL above are not proven until this command has been run and read.

- [ ] **Step 8: Run the whole backend suite and commit**

```bash
cd backend && gofmt -l -s . && go vet ./... && go test ./...
```

```bash
git add backend/migrations/000039_game_template_data_changed.up.sql backend/migrations/000039_game_template_data_changed.down.sql backend/internal/provisioning/template.go backend/internal/provisioning/support_test.go backend/internal/postgres/gametemplates.go backend/internal/postgres/gametabledata.go backend/internal/postgres/gametemplates_test.go backend/internal/postgres/support_test.go
git commit -m "feat(game): a template remembers that its data moved on without it

The table builder's rows are stored after the build and cannot be stored
before it, so a built game had no way to know it was already out of date.
data_changed_at is that knowledge, and FinishBuild clears it only for a
change the build actually saw — a row typed while it ran keeps its mark.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: The three writes that set the mark

**Files:**
- Modify: `backend/internal/provisioning/tabledata.go` (`AppendTableRow` ~line 746, `bootstrapTableRow` ~line 984, `completeTableDataAndAudit` ~line 536, `DeleteTableRow` ~line 1235)
- Test: `backend/internal/provisioning/tabledata_test.go`

**Interfaces:**
- Consumes: `TemplateRepository.MarkTableDataChanged`, `Template.NeedsBuild()`, the fake's honest `FinishBuild`/`ClaimBuild` from Task 1.
- Produces: nothing new; the four call sites above now mark the template.

- [ ] **Step 1: Point the existing red test at the new name**

`backend/internal/provisioning/tabledata_test.go` already holds `TestARowAddedAfterTheGameWasBuiltLeavesTheTemplateWaitingToBeBuiltAgain` (written before this plan, currently failing). Change its assertion from `after.Building()` to `after.NeedsBuild()` and its message accordingly:

```go
	if !after.NeedsBuild() {
		t.Fatalf("after a row was added the template is %q with no data mark; the row will never be built into a database",
			after.Status)
	}
```

- [ ] **Step 2: Write the three sibling tests**

In the same file, beside it:

```go
// TestACompletedTableUploadMarksTheGameOutOfDate is the same guarantee for the
// other way rows arrive: a whole CSV, uploaded in chunks.
func TestACompletedTableUploadMarksTheGameOutOfDate(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)
	markBuilt(store)

	const csv = "id,name,nickname\n1,Margot,\n"
	data := beginTableUploadWithContent(t, service, contest, "suspects", csv)
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("complete the upload: %v", err)
	}

	after, err := store.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	if !after.NeedsBuild() {
		t.Fatal("a completed CSV upload left the built game looking current")
	}
}

// TestADeletedRowMarksTheGameOutOfDate: a tombstone changes what a build
// loads exactly as much as a new row does.
func TestADeletedRowMarksTheGameOutOfDate(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects",
		[]string{"1", "Margot", ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	markBuilt(store)

	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 1); err != nil {
		t.Fatalf("delete the row: %v", err)
	}

	after, err := store.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	if !after.NeedsBuild() {
		t.Fatal("a deleted row left the built game looking current")
	}
}

// TestBeginningATableUploadDoesNotMarkTheGameOutOfDate: an upload that has
// only been reserved has changed no row a build would read, and a button
// offered for it would rebuild the same database again.
func TestBeginningATableUploadDoesNotMarkTheGameOutOfDate(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)
	markBuilt(store)

	if _, err := service.BeginTableUpload(t.Context(), contest, "suspects", 32); err != nil {
		t.Fatalf("begin: %v", err)
	}

	after, err := store.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	if after.NeedsBuild() {
		t.Fatal("a reserved upload that has changed no row marked the game out of date")
	}
}
```

`markBuilt` already exists beside the first test (added with it). Leave it where it is.

- [ ] **Step 3: Run them and watch them fail**

```bash
cd backend && go test -run 'TestARowAddedAfterTheGameWasBuilt|TestACompletedTableUpload|TestADeletedRowMarks|TestBeginningATableUpload' ./internal/provisioning/
```

Expected: the first three FAIL ("left the built game looking current"), the fourth PASSES already. A fourth test that passes before the change is the control: it proves the other three are failing for the reason claimed and not because the fake never marks anything.

- [ ] **Step 4: Mark the template at the three sites**

In `AppendTableRow`, inside the `run` closure, after `AppendTableDataRow` succeeds and before the audit write:

```go
		if err := g.repo.MarkTableDataChanged(ctx, contestID); err != nil {
			return fmt.Errorf("mark the contest's data changed: %w", err)
		}
```

The same three lines go into `bootstrapTableRow`'s `run` (after `CreateReadyTableData`), `completeTableDataAndAudit`'s `run` (after `CompleteTableData`), and `DeleteTableRow`'s `run` (after `DeleteTableDataRow`). Each goes *before* the `if g.audit == nil { return nil }` early return — a service assembled without audit still has to mark the template, and putting the call after that line silently skips it.

- [ ] **Step 5: Run the tests**

```bash
cd backend && go test ./internal/provisioning/
```

Expected: PASS, all four.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/provisioning/tabledata.go backend/internal/provisioning/tabledata_test.go
git commit -m "feat(game): every row that changes marks the game out of date

A row typed, a CSV completed, a row tombstoned: each changes what a build
would load, and each now says so inside the transaction that made it. A
reserved upload does not, because it has changed nothing yet.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: RequestBuild

**Files:**
- Modify: `backend/internal/provisioning/template.go` (new `Games.RequestBuild`, `TemplateRepository.RequestBuild`)
- Modify: `backend/internal/postgres/gametemplates.go` (the repository side)
- Modify: `backend/internal/audit/audit.go` (new action constant, added to `Actions()`)
- Modify: `backend/internal/provisioning/support_test.go` (the fake)
- Test: `backend/internal/provisioning/template_test.go`

**Interfaces:**
- Consumes: `Template.NeedsBuild()`, `MarkTableDataChanged` (Task 1).
- Produces:
  - `func (g *Games) RequestBuild(ctx context.Context, actorID, contestID uuid.UUID) (Template, error)` — returns `ErrGameNotEditable`, `ErrNoGame`, `ErrBuildInProgress`.
  - `TemplateRepository.RequestBuild(ctx context.Context, contestID uuid.UUID) (Template, error)` — returns `ErrBuildInProgress` when the row was not in a state to be asked.
  - `audit.ActionGameBuildRequested = "contest.game_build_requested"`.

- [ ] **Step 1: Write the failing service tests**

Append to `backend/internal/provisioning/template_test.go`:

```go
// TestRequestBuildPutsAReadyGameBackToPendingAndRaisesItsVersion is the button
// itself: the data is in, and the organiser asks for the database to be made
// again from it.
//
// The version has to rise. Every copy carries the version it was made from,
// and only a higher one makes the existing copies stale — a rebuild that left
// it alone would build a template nobody is ever given.
func TestRequestBuildPutsAReadyGameBackToPendingAndRaisesItsVersion(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, suspectsOnly()); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	before, err := store.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	markBuilt(store)

	asked, err := service.RequestBuild(t.Context(), uuid.New(), contest)
	if err != nil {
		t.Fatalf("RequestBuild: %v", err)
	}
	if asked.Status != provisioning.TemplatePending {
		t.Fatalf("the game is %q after a build was asked for, want %q", asked.Status, provisioning.TemplatePending)
	}
	if asked.Version <= before.Version {
		t.Fatalf("the version is %d, want more than %d: the copies made from the old one stay current",
			asked.Version, before.Version)
	}
}

// TestRequestBuildIsRefusedOnceTheContestIsRunning: raising the version drops
// and remakes every participant's copy, which in a running olympiad is every
// participant losing their database at once.
func TestRequestBuildIsRefusedOnceTheContestIsRunning(t *testing.T) {
	t.Parallel()
	service, store, _ := games(false)
	contest := uuid.New()
	store.present = true
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_x", Version: 3,
		Status: provisioning.TemplateReady, Source: provisioning.SourceBuilder,
	}

	if _, err := service.RequestBuild(t.Context(), uuid.New(), contest); !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Fatalf("RequestBuild = %v, want ErrGameNotEditable", err)
	}
}

// TestRequestBuildIsRefusedWhileOneIsAlreadyRunning: two organisers on the
// same screen, or one impatient double click. The second is told so rather
// than racing CREATE DATABASE against the first.
func TestRequestBuildIsRefusedWhileOneIsAlreadyRunning(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, suspectsOnly()); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	// SaveDefinition leaves the game 'pending': a build is already waiting.

	if _, err := service.RequestBuild(t.Context(), uuid.New(), contest); !errors.Is(err, provisioning.ErrBuildInProgress) {
		t.Fatalf("RequestBuild = %v, want ErrBuildInProgress", err)
	}
}

// TestRequestBuildWithoutAGameSaysSo: nothing has been written to build.
func TestRequestBuildWithoutAGameSaysSo(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)

	if _, err := service.RequestBuild(t.Context(), uuid.New(), uuid.New()); !errors.Is(err, provisioning.ErrNoGame) {
		t.Fatalf("RequestBuild = %v, want ErrNoGame", err)
	}
}
```

`suspectsOnly()` returns `provisioning.Definition{Tables: []provisioning.TableDefinition{suspectsTable()}}`; `suspectsTable()` already exists in `tabledata_test.go`. If a helper of that shape is already in `template_test.go`, use it instead of adding a second.

- [ ] **Step 2: Run them and watch them fail**

```bash
cd backend && go test -run TestRequestBuild ./internal/provisioning/
```

Expected: a compile failure — `RequestBuild` undefined. Then, once the method exists but the repository does not, the tests fail on behaviour. Both are the right failure in their turn.

- [ ] **Step 3: Add the audit action**

In `backend/internal/audit/audit.go`, beside `ActionGameBuilt`:

```go
	// ActionGameBuildRequested records an organiser asking for the game to be
	// built again — the button the table builder needs, because the data a
	// game is filled with arrives after the build that would have loaded it.
	// Apart from ActionGameBuilt, which is the build's own outcome: the two
	// answer "who asked" and "how did it end", and a trail that only carried
	// the second could not say whether a rebuild was anybody's decision.
	ActionGameBuildRequested = "contest.game_build_requested"
```

Add it to the slice returned by `Actions()` (line ~191, beside `ActionGameBuilt`). Then:

```bash
cd backend && go run ./cmd/auditcontract
```

and add the three translations for the new action to `frontend/lib/i18n/dictionaries/{en,ru,ro}.ts` — `frontend/lib/i18n/dictionary.test.ts` checks them against `docs/api/audit-actions.json` and fails otherwise. English: "Asked for the game to be built again". Russian: «Запросил пересборку игры». Romanian: "A cerut reconstruirea jocului". Match the surrounding entries' wording and casing rather than these strings if the file's own convention differs.

- [ ] **Step 4: Implement the repository side**

In `backend/internal/postgres/gametemplates.go`:

```go
// RequestBuild puts a finished game back to pending so the build job takes it
// again, raising the version the way SaveScript's own upsert does.
//
// The content is not touched. This is a request to build what is already
// stored, not a replacement of it, so init_script, definition_json and
// upload_id stay exactly as they are — which is also what makes it safe to
// offer for all three sources rather than the builder alone.
//
// The status condition is the race arbiter, and the reason the caller's own
// earlier check is not enough: two organisers pressing the button in the same
// second both read 'ready', and only one of them may raise the version. The
// other gets no row back, which the caller reads as ErrBuildInProgress.
//
// The cached schema goes for the reason upsertGame clears it: it describes the
// build being replaced, and migration 22 constrains the two columns to be null
// together.
func (r *GameInstances) RequestBuild(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error) {
	template, err := scanTemplate(r.querier(ctx).QueryRow(ctx, `
		UPDATE game_templates
		SET status         = 'pending',
		    version        = version + 1,
		    build_error    = NULL,
		    schema_json    = NULL,
		    schema_version = NULL,
		    updated_at     = now()
		WHERE contest_id = $1 AND status IN ('ready', 'failed')
		RETURNING `+templateColumns, contestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return provisioning.Template{}, provisioning.ErrBuildInProgress
	}
	if err != nil {
		return provisioning.Template{}, fmt.Errorf("ask for the game to be built: %w", err)
	}
	return template, nil
}
```

Declare it on `TemplateRepository` in `template.go` with a doc comment saying the same in two sentences, and add it to the fake in `support_test.go`:

```go
func (s *templateStore) RequestBuild(_ context.Context, _ uuid.UUID) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.present {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	if s.template.Status != provisioning.TemplateReady && s.template.Status != provisioning.TemplateFailed {
		return provisioning.Template{}, provisioning.ErrBuildInProgress
	}
	s.template.Status = provisioning.TemplatePending
	s.template.Version++
	s.template.BuildError = ""
	return s.template, nil
}
```

- [ ] **Step 5: Implement the service**

In `backend/internal/provisioning/template.go`, beside `SetDefinition`:

```go
// RequestBuild asks for a contest's game to be built again from what is
// already stored — the table builder's own missing half.
//
// The data an organiser fills a builder game with arrives after the build
// that would have loaded it, and cannot arrive before it: a row may only be
// typed into a table the saved definition already names, and saving the
// definition is what starts the build. Without this call the rows are stored
// and never loaded, and every participant copies an empty database.
//
// Refused once the contest is running, by the same gate that refuses a
// replacement: the version rises, every copy becomes stale, and a stale copy
// is dropped and made again. Carrying an edit into databases participants are
// already working in is a different mechanism and not this one.
func (g *Games) RequestBuild(ctx context.Context, actorID, contestID uuid.UUID) (Template, error) {
	current, err := g.repo.TemplateStatus(ctx, contestID)
	if err != nil {
		return Template{}, err // ErrNoGame travels as itself
	}
	if current.Building() {
		// Told apart from the repository's own refusal below so that "a build
		// is already under way" does not read as "somebody beat you to the
		// button" — they are the same sentence to the organiser, and this one
		// costs no write.
		return Template{}, ErrBuildInProgress
	}

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return Template{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return Template{}, ErrGameNotEditable
	}

	var asked Template
	run := func(ctx context.Context) error {
		var err error
		asked, err = g.repo.RequestBuild(ctx, contestID)
		if err != nil {
			return err
		}
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameBuildRequested,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"version": asked.Version},
		})
	}
	if g.uow != nil {
		err = g.uow.Do(ctx, run)
	} else {
		err = run(ctx)
	}
	if err != nil {
		return Template{}, err
	}
	return asked, nil
}
```

- [ ] **Step 6: Run the tests**

```bash
cd backend && go test ./internal/provisioning/ ./internal/audit/ && go test ./cmd/auditcontract/
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/provisioning/template.go backend/internal/provisioning/template_test.go backend/internal/provisioning/support_test.go backend/internal/postgres/gametemplates.go backend/internal/audit/audit.go docs/api/audit-actions.json frontend/lib/i18n/dictionaries/en.ts frontend/lib/i18n/dictionaries/ru.ts frontend/lib/i18n/dictionaries/ro.ts
git commit -m "feat(game): staff can ask for the game to be built again

What is stored is built again, and nothing about the game is replaced: the
script, the definition and the upload stay as they are, only the version
rises so the copies made from the previous build become stale. Refused
while the contest runs, for the reason replacing a game is.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: The route

**Files:**
- Modify: `backend/internal/api/game_handler.go` (`Mount` ~line 170, new `requestBuild` handler, `gameResponse` ~line 406, `gameView`, `fail` ~line 1708)
- Modify: `backend/internal/api/codes.go`
- Modify: `docs/api/error-codes.json` (generated)
- Modify: `frontend/lib/i18n/dictionaries/{en,ru,ro}.ts`
- Test: `backend/internal/api/game_handler_test.go`

**Interfaces:**
- Consumes: `provisioning.Games.RequestBuild`, `Template.NeedsBuild()`.
- Produces: `POST /contests/{id}/game/build` → 202 with the same `gameResponse` body the status route sends; `gameResponse.NeedsBuild bool` on `json:"needs_build"`.

- [ ] **Step 1: Write the failing handler tests**

In `backend/internal/api/game_handler_test.go`, following the shape of the tests already there for `setDefinition` (find them with `grep -n "definition" backend/internal/api/game_handler_test.go`). This package's tests drive the mounted router through a fake `games` service; read one neighbouring test first and reuse its exact harness rather than the sketch of it below.

```go
// TestAskingForABuildAnswersTheGameItWillBuild is the button's happy path.
// 202, because nothing is built by the time this answers, and a body the
// polling screen can carry on from.
func TestAskingForABuildAnswersTheGameItWillBuild(t *testing.T) {
	contest := uuid.New()
	games := &gamesStub{requested: provisioning.Template{
		ContestID: contest, Database: "game_x", Version: 4,
		Status: provisioning.TemplatePending, Source: provisioning.SourceBuilder,
	}}
	router := mountGameHandler(t, games)

	rec := doRequest(t, router, http.MethodPost, "/contests/"+contest.String()+"/game/build", nil)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	var body struct {
		Status     string `json:"status"`
		Version    int    `json:"version"`
		NeedsBuild bool   `json:"needs_build"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "pending" || body.Version != 4 {
		t.Fatalf("body = %+v, want the pending game at version 4", body)
	}
	if body.NeedsBuild {
		t.Fatal("the game still reads as needing a build after one was asked for")
	}
}

// TestAskingForABuildWhileTheContestRunsIs409GameNotEditable: raising the
// version takes every participant's database at once, which is why the
// service refuses it and why the code — not the sentence — is the contract.
func TestAskingForABuildWhileTheContestRunsIs409GameNotEditable(t *testing.T) {
	contest := uuid.New()
	router := mountGameHandler(t, &gamesStub{requestErr: provisioning.ErrGameNotEditable})

	rec := doRequest(t, router, http.MethodPost, "/contests/"+contest.String()+"/game/build", nil)

	assertRefusal(t, rec, http.StatusConflict, "game_not_editable")
}
```

Write `TestAskingForABuildWhileOneRunsIs409BuildInProgress` and `TestAskingForABuildWithNoGameIs404NoGameYet` to the same shape, with `provisioning.ErrBuildInProgress` → 409 `build_in_progress` and `provisioning.ErrNoGame` → 404 `no_game_yet`. `mountGameHandler`, `doRequest`, `assertRefusal` and `gamesStub` stand in for whatever this file already calls them; use its own names and add `requested`/`requestErr` (or the equivalent) to its existing stub rather than writing a second one.

Assert the code, never the message: the message is prose that may be reworded, the code is what a client switches on.

- [ ] **Step 2: Run them and watch them fail**

```bash
cd backend && go test -run TestAskingForABuild ./internal/api/
```

Expected: 404 from chi for an unmounted route, or a compile failure for the missing fake method. Either is the right failure.

- [ ] **Step 3: Add the code**

In `backend/internal/api/codes.go`, beside `codeGameNotEditable`:

```go
	codeBuildInProgress = httpx.NewCode("build_in_progress",
		"The contest's game is already being built, or is waiting to be. The build that is running will load everything stored up to the moment it started, and a change made after that leaves the game marked out of date again, so a second request is never needed to avoid losing work — it would only build the same thing twice.")
```

Then regenerate and translate:

```bash
make api-contract
```

Add `build_in_progress` to `frontend/lib/i18n/dictionaries/{en,ru,ro}.ts` beside `game_not_editable`. English: "The game is already being built." Russian: «Игра уже собирается.» Romanian: "Jocul este deja în construcție." Follow the file's own sentence style if it differs.

- [ ] **Step 4: Mount the route and answer it**

In `Mount`, beside the script and definition routes:

```go
		// Building again what is already stored. ContestEdit, like the two
		// writes above it: this replaces nobody's content, and whoever may
		// replace the game entirely may certainly ask for it to be built.
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).Post("/build", h.requestBuild)
```

```go
// requestBuild asks for the contest's game to be built again from what is
// already stored — the table builder's rows arrive after the build that would
// have loaded them, so without this they never reach a database.
//
// 202 and not 200: nothing is built by the time this answers. The build is a
// background job, and the body is the game as it now stands — pending, with
// the version a copy will be made from — which is exactly what the screen
// polling the status needs to carry on from.
func (h *GameHandler) requestBuild(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	// <the actor lookup, spelled exactly as setDefinition spells it in this
	// same file — read it and copy those lines; this handler needs the actor
	// for the audit entry and nothing more>
	asked, err := h.games.RequestBuild(r.Context(), actorID, contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusAccepted, h.gameView(r.Context(), asked))
}
```

The actor lookup is the one line deliberately not spelled out here: `setDefinition` in this same file already does it, behind the same `Authenticate` middleware, and guessing at the accessor's name would put a symbol in this plan that may not exist. Read those lines and use them.

In `fail`, beside the `ErrGameNotEditable` case:

```go
	case errors.Is(err, provisioning.ErrBuildInProgress):
		httpx.Error(w, r, http.StatusConflict, codeBuildInProgress,
			"The game is already being built, or is waiting to be")
	case errors.Is(err, provisioning.ErrNoGame):
		httpx.Error(w, r, http.StatusNotFound, codeNoGameYet,
			"This contest has no game to build yet")
```

Check first whether `fail` already handles `ErrNoGame`; the status route answers it before reaching `fail`, so it may not.

- [ ] **Step 5: Publish the mark to the client**

Add to `gameResponse`:

```go
	// NeedsBuild is a game whose built database no longer holds the data an
	// organiser has since put into it — provisioning.Template.NeedsBuild.
	//
	// Sent rather than derived on the client from a timestamp: what counts as
	// "out of date" is a domain rule (a `ready` game with a data mark, and
	// not a failed or building one), and a second copy of it in the bundle
	// would go on disagreeing with the server the day the rule moves
	// (CLAUDE.md rule 11).
	NeedsBuild bool `json:"needs_build"`
```

Set it in `gameView` from `template.NeedsBuild()`. The synthetic `"absent"` answer in `status` leaves it `false`, which is correct: a contest with no game has nothing out of date.

- [ ] **Step 6: Run the tests**

```bash
cd backend && go test ./internal/api/ && make api-contract && git diff --exit-code docs/api/error-codes.json
```

Expected: PASS, and no diff — the contract file is already regenerated and committed in step 3.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/api/game_handler.go backend/internal/api/game_handler_test.go backend/internal/api/codes.go docs/api/error-codes.json frontend/lib/i18n/dictionaries/en.ts frontend/lib/i18n/dictionaries/ru.ts frontend/lib/i18n/dictionaries/ro.ts
git commit -m "feat(api): POST /contests/{id}/game/build, and a game that says it is stale

build_in_progress reaches a client for the first time: the sentinel was
declared for exactly this button and had never been returned from anywhere,
so it had no code, no translation and no test until now.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: The button on the organiser's screen

**Files:**
- Modify: `frontend/lib/api/game.ts` (`gameSchema` ~line 104)
- Modify: `frontend/app/(admin)/contests/[contestId]/game/actions.ts`
- Create: `frontend/app/(admin)/contests/[contestId]/game/game-build.tsx`
- Create: `frontend/app/(admin)/contests/[contestId]/game/game-build.test.tsx`
- Modify: `frontend/app/(admin)/contests/[contestId]/game/page.tsx`
- Modify: `frontend/lib/i18n/dictionaries/{en,ru,ro}.ts`
- Test: `frontend/app/(admin)/contests/[contestId]/game/actions.test.ts`

**Interfaces:**
- Consumes: `POST /contests/{id}/game/build`, `game.needs_build`.
- Produces: `requestGameBuildAction(contestId: string): Promise<UploadActionResult<Game>>`; `<GameBuild game={…} editable={…} dict={…} />`.

- [ ] **Step 1: Write the failing component test**

`game-build.test.tsx`, following the shape of `game-upload.test.tsx` in the same directory:

```tsx
describe("GameBuild", () => {
  it("offers the build when the data has moved on without it", () => { /* needs_build: true, editable: true → the button is in the document */ });
  it("says nothing when the built game holds the current data", () => { /* needs_build: false → no button, no notice */ });
  it("offers nothing once the contest is running", () => { /* editable: false, needs_build: true → no button: the API would refuse it */ });
  it("disables the button while a build is running", () => { /* status: "building" → the button is present and disabled */ });
});
```

Write the assertions out in full against the real component, using the same testing-library helpers the neighbouring tests use. Query by role and accessible name, never by class.

- [ ] **Step 2: Run it and watch it fail**

```bash
cd frontend && npx vitest run app/\(admin\)/contests/\[contestId\]/game/game-build.test.tsx
```

Expected: FAIL — the module does not exist.

- [ ] **Step 3: Carry the field and the action**

In `frontend/lib/api/game.ts`, inside `gameSchema`:

```ts
    // Defaulted rather than required, the reason every field here is: an
    // older API that does not send it must not fail the whole render, and
    // `false` reads as "the server did not say the game is stale".
    needs_build: z.boolean().default(false),
```

In `actions.ts`, beside `saveGameDefinitionAction`:

```ts
/**
 * Asking for the game to be built again — `POST .../game/build`.
 *
 * The table builder's rows are stored after the build that would have loaded
 * them and cannot be stored before it, so this is the only way data an
 * organiser typed reaches a database. 202 with the game as it now stands;
 * `refusal` carries a named "no" (the contest is running, a build is already
 * under way, there is no game yet) through to the screen.
 */
export async function requestGameBuildAction(contestId: string): Promise<UploadActionResult<Game>> {
  if (!isId(contestId)) return { code: "invalid_contest_id" };

  let value: Game;
  try {
    value = gameSchema.parse(await serverRequest(`/contests/${contestId}/game/build`, { method: "POST" }));
  } catch (error) {
    return refusal(error);
  }

  revalidatePath(`/contests/${contestId}`, "layout");
  return { value };
}
```

Add an `actions.test.ts` case beside the existing ones asserting that a refused request comes back as `{ code: "build_in_progress" }` rather than throwing.

- [ ] **Step 4: Write the component**

```tsx
"use client";

import { useState, useTransition } from "react";

import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import type { Game } from "@/lib/api/game";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { requestGameBuildAction } from "./actions";

/**
 * Asking for the game to be built again, and the one sentence that explains
 * why anybody would.
 *
 * The table builder's rows are stored after the build that would have loaded
 * them and cannot be stored before it — a row may only be typed into a table
 * the saved definition already names, and saving the definition is what
 * starts the build. So a builder game is normally built empty first and
 * filled afterwards, and this is how the filling reaches a database.
 *
 * Nothing is rendered once the contest is running. The API refuses the
 * request there (raising the version drops and remakes every participant's
 * copy), and a button that exists to be refused is worse than no button:
 * principle 4 of the design spec.
 */
export function GameBuild({
  game,
  editable,
  dict,
}: {
  dict: Dictionary;
  /** `contentEditable(contest)` — false once the olympiad is running. */
  editable: boolean;
  game: Game;
}) {
  const t = dict.contests.game.build;
  const errors = dict.errors;
  const [refusal, setRefusal] = useState<string | null>(null);
  const [pending, startTransition] = useTransition();

  const building = game.status === "building" || game.status === "pending";
  if (!editable || (!game.needs_build && !building)) return null;

  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-3 border-t border-line pt-4">
      <p className="text-small text-ink-2">{building ? t.building : t.notice}</p>
      <button
        type="button"
        disabled={building || pending}
        className={cn(buttonVariants({ variant: "primary" }))}
        onClick={() => {
          startTransition(async () => {
            const result = await requestGameBuildAction(game.contest_id);
            setRefusal("code" in result ? (errors[result.code] ?? errors.fallback) : null);
          });
        }}
      >
        {t.action}
      </button>
      {refusal ? <p className="text-small text-ink-3">{refusal}</p> : null}
    </div>
  );
}
```

Two things to settle against the real code rather than this sketch: `Game` carries no `contest_id` today, so pass the contest id as its own prop instead of reading it off the game; and `buttonVariants`, `cn` and the errors dictionary must be imported exactly as `game-upload.tsx` in the same directory imports them. Follow that file for the notice-and-button shape — it is the pattern this screen already uses.

No new colours, no box, no `font-bold`, no arbitrary Tailwind values: the ESLint rule bans them, and the design spec bans what the rule does not.

Render it from `page.tsx` directly below `<GameBuilder …/>`, passing the game it already loads and `contentEditable(contest)`.

- [ ] **Step 5: Add the three translations**

Under the game section of each dictionary, a `build` block: `notice` ("The data has changed since this game was built"), `action` ("Build again"), `building` ("Building…"). Russian: «Данные изменились после последней сборки», «Собрать заново», «Собирается…». Romanian: "Datele s-au schimbat de la ultima construire", "Construiește din nou", "Se construiește…". `dictionary.test.ts` requires all three locales to carry the same keys.

- [ ] **Step 6: Run the frontend checks**

```bash
cd frontend && npm run check
```

Expected: PASS — lint, types, and the whole vitest suite including the four new cases.

- [ ] **Step 7: Commit**

```bash
git add frontend/lib/api/game.ts "frontend/app/(admin)/contests/[contestId]/game/actions.ts" "frontend/app/(admin)/contests/[contestId]/game/actions.test.ts" "frontend/app/(admin)/contests/[contestId]/game/game-build.tsx" "frontend/app/(admin)/contests/[contestId]/game/game-build.test.tsx" "frontend/app/(admin)/contests/[contestId]/game/page.tsx" frontend/lib/i18n/dictionaries/en.ts frontend/lib/i18n/dictionaries/ru.ts frontend/lib/i18n/dictionaries/ro.ts
git commit -m "feat(game): the screen says the data moved on, and offers to build again

Shown only where it can be acted on: never once the contest is running,
where the API would refuse the request and offering the button would be a
door onto a refusal.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: Prove it on the order a deployment runs in

**Files:**
- Modify: `backend/internal/provisioning/game_integration_test.go` (`TestAScriptSavedInTheCoreDatabaseFromATableBuilderDefinitionWithCSVDataBuildsRowsOnTheGameCluster`, line 487)

**Interfaces:**
- Consumes: `Games.RequestBuild` (Task 3), the marks from Task 2.
- Produces: nothing.

- [ ] **Step 1: Reorder the test to the deployment's own order**

Today the test uploads every row and *then* calls `games.Build` once. On a deployment `Build` is called by the background job, seconds after `SetDefinition` and long before any row exists — which is why this test passed for a year while the feature did not work. Rewrite the middle of it so that, after `SetDefinition`:

1. `built, err := games.Build(t.Context(), time.Minute)` — the first build, which must finish `ready` and whose tables must be empty. Assert `SELECT count(*) FROM suspects` is `0`; without that assertion the test no longer proves which build loaded the rows.
2. The CSV upload, the three typed rows and the tombstone, exactly as they are today.
3. `asked, err := games.RequestBuild(t.Context(), uuid.New(), contest.ID)` — assert no error and that `asked.Version > built.Version`.
4. `second, err := games.Build(t.Context(), time.Minute)` — assert `ready`.
5. Every existing assertion about the rows, run against `second.Database` rather than `built.Database`.

Both databases need `t.Cleanup(func() { gamedbtest.Drop(...) })`: the rebuild makes a second one, and a test that dropped only the first leaves a database on the cluster after every run.

- [ ] **Step 2: Run it**

This needs the game cluster. If `GAME_DB_DSN` is not set the test skips, and a skip is not a pass — say so explicitly rather than reporting success. CI runs it with a real cluster (`.github/workflows/backend.yml` starts `pg-game-test` and runs `go test -race ./...`), so the branch's CI is where this is proven if it cannot run locally.

```bash
cd backend && go test -run TestAScriptSavedInTheCoreDatabaseFromATableBuilderDefinitionWithCSVData ./internal/provisioning/ -v
```

- [ ] **Step 3: Commit**

```bash
git add backend/internal/provisioning/game_integration_test.go
git commit -m "test(game): build first, fill second — the order a deployment runs in

The test used to load every row and only then build, which is the one
order a deployment never takes: the build runs seconds after the schema is
saved, and a row cannot be typed into a table the saved schema does not
name. It proved a path nobody walks, and the defect hid behind it
(CLAUDE.md rule 10).

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Final verification

Before opening the pull request, run all of it and read the output:

```bash
cd backend && gofmt -l -s . && go vet ./... && go test ./... && cd .. && make test-db && make sec && make api-contract && make audit-contract && git diff --exit-code docs/api/ && cd frontend && npm run check
```

Then a browser pass on the game screen: the notice and the button appear after a row is typed, the button is disabled while the build runs, the notice is gone once it finishes, and nothing is offered on a running contest. Both themes, and at least the Russian locale.

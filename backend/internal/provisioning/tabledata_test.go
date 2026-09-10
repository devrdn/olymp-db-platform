package provisioning_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// Most of these run against the fake templateStore and buildCluster (see
// games() in template_test.go) and a real gamefile.Store on a temp
// directory — no CORE_DB_DSN, exactly the brief's own "parsing and
// boundaries — without a database". The four rules PostgreSQL alone holds are the exception, and
// they run against the real repository instead (tableDataGamesOnRepo, near
// the end of this file). The assembly-with-real-data path that needs a real cluster is
// TestAScriptSavedInTheCoreDatabase... in game_integration_test.go, the one
// `make test-game-build` runs.

// suspectsTable is the small definition every test below builds against:
// one primary key column, one required column, one nullable one — enough to
// exercise NOT NULL and every one of the scalar types this package checks.
func suspectsTable() provisioning.TableDefinition {
	return provisioning.TableDefinition{
		Name: "suspects",
		Columns: []provisioning.ColumnDefinition{
			{Name: "id", Type: provisioning.ColumnInteger},
			{Name: "name", Type: provisioning.ColumnText},
			{Name: "nickname", Type: provisioning.ColumnText, Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}
}

// tableDataGames assembles a *provisioning.Games with the fake repository
// and cluster (games(), template_test.go) plus a real gamefile.Store on a
// fresh temp directory for the table builder's own per-table files —
// uploadsGames' own shape, for WithTableData instead of WithUploads.
func tableDataGames(t testing.TB, editable bool) (*provisioning.Games, *templateStore, *buildCluster, *gamefile.Store) {
	t.Helper()
	service, store, cluster, files, _ := tableDataGamesOnDisk(t, editable)
	return service, store, cluster, files
}

// tableDataLimits are the ceilings the table builder's own store runs under
// in these tests. Named rather than inline because the janitor's tests open a
// second handle on the same directory (tableStoreOn) and two handles
// disagreeing about MaxDirBytes would be a difference nothing here is about.
var tableDataLimits = gamefile.Limits{MaxFileBytes: 8 << 20, MaxDirBytes: 32 << 20, MaxChunkBytes: 4 << 20}

// tableDataGamesOnDisk is tableDataGames with the volume's own directory
// handed back too — what the janitor's tests need and a *gamefile.Store
// alone cannot give them: planting a file behind the service's back is
// Store.Begin on a second handle, but ageing one is os.Chtimes on a path.
func tableDataGamesOnDisk(t testing.TB, editable bool) (*provisioning.Games, *templateStore, *buildCluster, *gamefile.Store, string) {
	t.Helper()
	service, store, cluster := games(editable)
	dir := t.TempDir()
	files, err := gamefile.NewStore(dir, tableDataLimits)
	if err != nil {
		t.Fatalf("opening the table data store: %v", err)
	}
	service.WithTableData(files, tableDataLimits)
	return service, store, cluster, files, dir
}

// tableDataGamesOnRepo assembles a *provisioning.Games against the *real*
// core database (this package's own standing pool) and a real
// gamefile.Store — gamesWithUploads' own shape (upload_test.go), for the
// table builder's volume instead of the dump's.
//
// The fake above is right for everything about parsing, bounds and file
// mechanics, and wrong for four rules this feature has that PostgreSQL holds
// and nothing else does: game_table_data_one_receiving_idx,
// game_table_data_one_complete_idx, the GREATEST floors in
// AppendTableDataRow, and migration 26's own source/definition pairing
// CHECK. A fake can only reinvent those in Go — which is the check the index
// exists to make unnecessary — so they are proved here, on the path a
// deployment uses (CLAUDE.md rule 10), exactly as the dump's own
// TestASecondUploadForTheSameContestIsRejectedByTheDatabaseNotByGoCode does.
func tableDataGamesOnRepo(t *testing.T, editable bool) (*provisioning.Games, *gamefile.Store) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	service := provisioning.NewGames(postgres.NewGameInstances(testPool), &buildCluster{}, authoring{editable: editable}).
		WithTableData(files, tableDataLimits)
	return service, files
}

// tableStoreOn opens a second handle on a table-data directory a
// *provisioning.Games is already using — storeOn's own job (upload_test.go)
// under this store's own ceilings rather than the dump store's.
func tableStoreOn(t testing.TB, dir string) *gamefile.Store {
	t.Helper()
	store, err := gamefile.NewStore(dir, tableDataLimits)
	if err != nil {
		t.Fatalf("open a second handle on the table data directory: %v", err)
	}
	return store
}

// anyAge is a cut-off no file a test has just written can be younger than —
// what gamefile.Store.UploadIDs takes to list every id it holds. Its floor is
// a floor and nothing else (its own doc): a zero Time lists nothing at all,
// which is what "no file was left behind" used to be asserted against here.
func anyAge() time.Time { return time.Now().Add(time.Hour) }

// withSuspects saves a builder definition holding only suspectsTable, so a
// call to BeginTableUpload or AppendTableRow finds a table to check its
// upload against.
func withSuspects(t testing.TB, service *provisioning.Games, contest uuid.UUID) {
	t.Helper()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest,
		provisioning.Definition{Tables: []provisioning.TableDefinition{suspectsTable()}}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
}

// beginTableUploadWithContent begins a table upload sized exactly to
// content and appends all of it in one chunk — beginWithContent's own shape
// (upload_test.go) for a table's file instead of a whole dump.
func beginTableUploadWithContent(t testing.TB, service *provisioning.Games, contest uuid.UUID, table, content string) provisioning.TableData {
	t.Helper()
	data, err := service.BeginTableUpload(t.Context(), contest, table, int64(len(content)))
	if err != nil {
		t.Fatalf("begin table upload: %v", err)
	}
	if _, err := service.AppendTableChunk(t.Context(), contest, data.ID, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("append table chunk: %v", err)
	}
	return data
}

// TestCurrentTableDataFindsAnUploadStillReceiving is CurrentTableData's own
// happy path — Games.CurrentUpload's own doc, mirrored here: a reloaded
// page's way to find a table's own chunked upload still in progress and
// resume it.
func TestCurrentTableDataFindsAnUploadStillReceiving(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	begun, err := service.BeginTableUpload(t.Context(), contest, "suspects", 32)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	found, err := service.CurrentTableData(t.Context(), contest, "suspects")
	if err != nil {
		t.Fatalf("CurrentTableData: %v", err)
	}
	if found.ID != begun.ID {
		t.Fatalf("found %v, want %v", found.ID, begun.ID)
	}
}

// BeginTableUpload reserves the file before it writes the row, so a refused
// row leaves both the file and the reservation Store.Begin counted for it
// against the directory (gamefile committedBytes' own doc). Left behind,
// they are the whole volume's budget spent by uploads nobody will ever send
// a byte of — and this store's ceiling is the smaller of the two, so a
// couple of refusals are enough. BeginUpload has the same defect and
// bootstrapTableRow, a few dozen lines below the code under test, already
// had the fix.
func TestATableUploadRowTheDatabaseRefusesGivesBackTheSpaceItReserved(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	// Every begin after the first is refused while that one is still
	// receiving — migration 27's own partial index, and the fake repository
	// answers exactly as it does.
	for i := 0; i < 3; i++ {
		if _, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024); !errors.Is(err, provisioning.ErrTableDataInProgress) {
			t.Fatalf("begin %d answered %v, want ErrTableDataInProgress", i+2, err)
		}
	}

	ids, err := files.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("list the table data volume: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("the volume holds %d file(s) (%v), want only the one upload that has a row", len(ids), ids)
	}
}

// A table with nothing receiving answers ErrTableDataNotFound — the same
// sentinel CurrentUpload itself answers with, which is what lets the
// handler serve the shared "absent" shape either way.
func TestCurrentTableDataAnswersNotFoundWhenNothingIsReceiving(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	_, err := service.CurrentTableData(t.Context(), contest, "suspects")
	if !errors.Is(err, provisioning.ErrTableDataNotFound) {
		t.Fatalf("error = %v, want ErrTableDataNotFound", err)
	}
}

// A table that is not part of the contest's current definition is
// ErrTableUnknown, exactly as BeginTableUpload itself already refuses it —
// currentDefinitionTable's own doc: every table-data method that is not a
// pure id lookup calls it first.
func TestCurrentTableDataRefusesATableOutsideTheDefinition(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	_, err := service.CurrentTableData(t.Context(), contest, "ghosts")
	if !errors.Is(err, provisioning.ErrTableUnknown) {
		t.Fatalf("error = %v, want ErrTableUnknown", err)
	}
}

// An installation with no table-data volume configured (WithTableData never
// called) answers ErrTableDataDisabled for this method too, the identical
// convention every other table-data method already follows.
func TestCurrentTableDataIsDisabledWithoutATableDataVolume(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)

	_, err := service.CurrentTableData(t.Context(), uuid.New(), "suspects")
	if !errors.Is(err, provisioning.ErrTableDataDisabled) {
		t.Fatalf("error = %v, want ErrTableDataDisabled", err)
	}
}

// TestAppendTableChunkRefusesAMismatchedHeaderOnTheFirstChunk is the brief's
// own requirement read literally: "a header that does not match the
// description's own columns is refused before the first byte of data is
// accepted. Accepting a gigabyte and only then saying 'wrong columns' is
// seven minutes of somebody else's time." The one chunk sent here is the
// file's entirety, so if the check ran only at CompleteTableUpload (as it
// did before this test existed), this call would succeed and only the
// explicit CompleteTableUpload below would see the mismatch — this asserts
// the rejection happens right here, on AppendTableChunk, one chunk in.
func TestAppendTableChunkRefusesAMismatchedHeaderOnTheFirstChunk(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,extra\n1,A,x\n" // wrong header; the row itself would be fine
	data, err := service.BeginTableUpload(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = service.AppendTableChunk(t.Context(), contest, data.ID, 0, strings.NewReader(content))
	if !errors.Is(err, provisioning.ErrTableHeaderMismatch) {
		t.Fatalf("AppendTableChunk error = %v, want ErrTableHeaderMismatch on the first chunk itself", err)
	}
}

// TestAppendTableChunkAcceptsAHeaderSplitAcrossChunksWithoutRefusing is the
// edge case the brief's own timing requirement runs into and must not
// mishandle: a header line too long to fit inside one chunk. The first
// chunk here ends mid-column-name, with no newline anywhere in it at all —
// there is nothing yet for the early check to compare, and that must not be
// mistaken for a mismatch (or for the chunk's own last line). The second
// chunk completes the (correct) header and the one data row; the whole
// upload must complete cleanly.
func TestAppendTableChunkAcceptsAHeaderSplitAcrossChunksWithoutRefusing(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	first := "id,na" // no newline at all: the header has not arrived yet
	second := "me,nickname\n1,Margot,\n"
	content := first + second

	data, err := service.BeginTableUpload(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := service.AppendTableChunk(t.Context(), contest, data.ID, 0, strings.NewReader(first)); err != nil {
		t.Fatalf("first chunk (no newline yet) was refused: %v", err)
	}
	if _, err := service.AppendTableChunk(t.Context(), contest, data.ID, int64(len(first)), strings.NewReader(second)); err != nil {
		t.Fatalf("second chunk: %v", err)
	}

	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// TestAppendTableChunkDoesNotRecheckAResentFirstChunk is the third edge
// case: a chunk upload resumed after a dropped connection resends its first
// chunk verbatim. gamefile.Store.Append's own idempotent-retry rule treats
// that as a no-op (its own doc), and the early header check must ride along
// with that rule rather than run a second time — a bad header must still be
// caught, but exactly once, and a resend of a good one must not somehow
// start failing on its second delivery.
func TestAppendTableChunkDoesNotRecheckAResentFirstChunk(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot,\n"
	data, err := service.BeginTableUpload(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := service.AppendTableChunk(t.Context(), contest, data.ID, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("first send: %v", err)
	}
	// The same chunk, resent at the same offset — exactly what a client that
	// never saw the first response would do.
	if _, err := service.AppendTableChunk(t.Context(), contest, data.ID, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("resend of the same first chunk was refused: %v", err)
	}

	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// TestCompleteTableUploadRefusesTheDeclaredLengthNotMatchingWhatArrived
// mirrors gameuploads' own length check — the brief lists file size among
// the bounds this feature owns, and this is the one that comes from
// gamefile.Store rather than a number this package invented.
func TestCompleteTableUploadRefusesTheDeclaredLengthNotMatchingWhatArrived(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data, err := service.BeginTableUpload(t.Context(), contest, "suspects", 100)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := files.Append(data.ID.String(), 0, strings.NewReader("id,name,nickname\n1,A,\n")); err != nil {
		t.Fatalf("append directly: %v", err)
	}
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); !errors.Is(err, provisioning.ErrTableDataLengthMismatch) {
		t.Fatalf("error = %v, want ErrTableDataLengthMismatch", err)
	}
}

// TestAppendTableRowBootstrapsAndAppendsToTheSameFile is the brief's own
// requirement that a row typed into a form and a row from a chunked upload
// land in one file: here there is no upload at all, and the first row has
// to create the file (with its own header) before the second is simply
// appended to it.
func TestAppendTableRowBootstrapsAndAppendsToTheSameFile(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	first, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""})
	if err != nil {
		t.Fatalf("append first row: %v", err)
	}
	if first.ActiveRows() != 1 {
		t.Fatalf("active rows = %d, want 1", first.ActiveRows())
	}

	second, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"2", "Someone", "Sparrow"})
	if err != nil {
		t.Fatalf("append second row: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("the second row landed in a different file (%s) than the first (%s)", second.ID, first.ID)
	}
	if second.ActiveRows() != 2 {
		t.Fatalf("active rows = %d, want 2", second.ActiveRows())
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window.Rows) != 2 {
		t.Fatalf("window returned %d rows, want 2: %+v", len(window.Rows), window.Rows)
	}
	if window.Rows[0].Fields[1] != "Margot" || window.Rows[1].Fields[1] != "Someone" {
		t.Fatalf("window = %+v", window.Rows)
	}
}

// TestAppendTableRowAfterAFileWhoseLastLineHasNoNewline is the join between
// the two ways a table's rows arrive. Plenty of exporters end a CSV without a
// trailing newline, and validateTableFile accepts that file — its last line
// is a whole row, tableLineScanner's own doc says so. A row added from the
// form afterwards must still land as its own line rather than being glued to
// the end of that last one, which would turn two rows into a single one of
// five fields in a three-column table: garbage in the organiser's own window,
// and `extra data after last expected column` on the build that follows.
func TestAppendTableRowAfterAFileWhoseLastLineHasNoNewline(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot," // no trailing newline, exactly as many exporters write it
	data := beginTableUploadWithContent(t, service, contest, "suspects", content)
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	appended, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"2", "Someone", "Sparrow"})
	if err != nil {
		t.Fatalf("append a row after a file with no trailing newline: %v", err)
	}
	if appended.Lines != 2 {
		t.Fatalf("lines = %d, want 2", appended.Lines)
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window.Rows) != 2 {
		t.Fatalf("window returned %d row(s), want 2: %+v", len(window.Rows), window.Rows)
	}
	if got := window.Rows[0].Fields; len(got) != 3 || got[1] != "Margot" {
		t.Fatalf("row 1 = %q, want the three fields of Margot's own row", got)
	}
	if got := window.Rows[1].Fields; len(got) != 3 || got[1] != "Someone" {
		t.Fatalf("row 2 = %q, want the three fields of the appended row", got)
	}
}

// TestAppendTableRowWritesAnEmptyValueAsNullNotAsAnEmptyString is the other
// half of the same promise AppendTableRow's own validation makes: an empty
// value is checked as a NULL (it is refused outright in a NOT NULL column),
// so it has to reach PostgreSQL as one. COPY ... WITH (FORMAT csv) reads a
// bare empty field as NULL and a quoted one ("") as the empty string, and the
// writer used to quote it — which for `age integer` is
// `invalid input syntax for type integer: ""`, a build failure for a row the
// API answered 201 to, and for a nullable text column an empty string stored
// where the organiser meant nothing at all, so that the IS NULL a task asks
// about finds no rows.
func TestAppendTableRowWritesAnEmptyValueAsNullNotAsAnEmptyString(t *testing.T) {
	t.Parallel()
	service, _, cluster, _ := tableDataGames(t, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{{
			Name: "witnesses",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger},
				{Name: "name", Type: provisioning.ColumnText},
				{Name: "age", Type: provisioning.ColumnInteger, Nullable: true},
			},
		}},
	}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "witnesses", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("append a row with an empty nullable value: %v", err)
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("build finished as %q: %s", built.Status, built.BuildError)
	}
	got := cluster.tableData[built.Database+".witnesses"]
	want := "1,Margot,\n"
	if got != want {
		t.Fatalf("loaded table data = %q, want %q — a quoted empty field is the empty string to COPY, not NULL", got, want)
	}
}

// TestAppendTableRowRefusesPastMaxTableDataRows is the same bound on the
// other path: MaxTableDataRows is what this service publishes as max_rows and
// what validateTableFile enforces for an uploaded file, so the form must not
// be the way past it. Only the bookkeeping is grown to the limit — writing
// two hundred thousand rows to prove a check that never reads them would be
// the test's own cost and nobody else's. Unlike MaxTableDeletedRows, this
// bound really is one this package checks — the comparison is in
// AppendTableRow — so seeding the count is staging an input, not standing in
// for the check itself.
func TestAppendTableRowRefusesPastMaxTableDataRows(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "A", ""})
	if err != nil {
		t.Fatalf("row 1: %v", err)
	}
	store.mu.Lock()
	seeded := store.tableData[data.ID]
	seeded.Lines = provisioning.MaxTableDataRows
	store.tableData[data.ID] = seeded
	store.mu.Unlock()

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects",
		[]string{"2", "B", ""}); !errors.Is(err, provisioning.ErrTableTooManyRows) {
		t.Fatalf("error = %v, want ErrTableTooManyRows", err)
	}
}

// TestAppendTableRowRefusesWhileAChunkedUploadIsReceiving is the race this
// package's own doc names: the two paths must not both compute an append
// offset from the same bookkeeping at once.
func TestAppendTableRowRefusesWhileAChunkedUploadIsReceiving(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "A", ""}); !errors.Is(err, provisioning.ErrTableDataInProgress) {
		t.Fatalf("error = %v, want ErrTableDataInProgress", err)
	}
}

// TestTwoFormsAppendingAtOnceLoseNoRowAndDoNotWedgeTheTable is the race
// AppendTableRow's own doc did not cover: not the form against a chunked
// upload (that one is refused outright), but two forms against each other.
// Both read the same "the file is N bytes long" and both write there;
// gamefile.Store.Append answers the second one with the idempotent-retry
// success its own doc promises, having written nothing, and the second row is
// gone while its author is told 201. Worse, the length written back is one no
// file has, so every later append is ErrTableDataChunkOutOfOrder for ever.
//
// The gate makes that deterministic rather than hoping the scheduler
// interleaves: both callers are held until both have read the table's current
// data, which is exactly the state two browser tabs are in.
func TestTwoFormsAppendingAtOnceLoseNoRowAndDoNotWedgeTheTable(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "A", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}

	store.gateTableDataReads(2)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects",
				[]string{strconv.Itoa(i + 2), "B", ""})
		}()
	}
	wg.Wait()

	var accepted int
	for _, err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, provisioning.ErrTableDataChanged):
		default:
			t.Fatalf("a concurrent append failed with %v, want either success or ErrTableDataChanged", err)
		}
	}
	if accepted == 0 {
		t.Fatal("neither concurrent append was accepted; one of them was writing at the file's end")
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	// The invariant, whichever way the two interleaved: every append that was
	// accepted is in the file, every one that was refused is not, and the
	// bookkeeping counts exactly what is there.
	if len(window.Rows) != 1+accepted {
		t.Fatalf("the file holds %d row(s) after 1 + %d accepted appends: %+v", len(window.Rows), accepted, window.Rows)
	}
	if int64(len(window.Rows)) != window.TotalRows {
		t.Fatalf("the file holds %d row(s), the bookkeeping says %d", len(window.Rows), window.TotalRows)
	}

	// And the table is not wedged: the next row still lands.
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"9", "C", ""}); err != nil {
		t.Fatalf("the table stopped taking rows after the race: %v", err)
	}
}

// TestAppendTableRowReconcilesBookkeepingThatLagsTheFile is the same
// desynchronisation with no race at all: the bytes reached the file and the
// transaction that was to record them rolled back afterwards (a failed audit
// write is enough). The bookkeeping then names an offset the file is already
// past, and an append taken from it is written nowhere — gamefile.Store.Append
// reports the retry success its own doc promises. The row must not be lost,
// and the table must not be stuck.
func TestAppendTableRowReconcilesBookkeepingThatLagsTheFile(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""})
	if err != nil {
		t.Fatalf("row 1: %v", err)
	}
	// Back to what the row said before that append committed: the header
	// alone, no data rows — the file itself keeps Margot.
	store.mu.Lock()
	rolledBack := store.tableData[data.ID]
	rolledBack.ReceivedBytes, rolledBack.Lines = int64(len("id,name,nickname\n")), 0
	store.tableData[data.ID] = rolledBack
	store.mu.Unlock()

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"2", "Someone", "Sparrow"}); err != nil {
		t.Fatalf("append after a rolled-back transaction: %v", err)
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window.Rows) != 2 {
		t.Fatalf("window returned %d row(s), want both: %+v", len(window.Rows), window.Rows)
	}
	if window.Rows[1].Fields[1] != "Someone" {
		t.Fatalf("the appended row is not in the file: %+v", window.Rows)
	}
	if int64(len(window.Rows)) != window.TotalRows {
		t.Fatalf("the file holds %d row(s), the bookkeeping says %d", len(window.Rows), window.TotalRows)
	}
}

// TestAGameThatStopsBeingBuilderSourcedDiscardsItsTablesData closes the way
// round ErrDefinitionTableLocked that its own doc describes and its own check
// did not cover. The refusal is honest while the game stays builder-sourced —
// but saving any script at all in the editor made the check skip itself
// ("this game names no table such a row could belong to"), and nothing
// anywhere deleted the rows, so the same file was still there when the
// organiser came back and saved the table with another column type. The next
// build then loaded values validated as text into a numeric column, silently,
// because the file's header names columns and never their types.
//
// A game that is not built by the table builder has no tables for that data
// to belong to, so the data goes when the game does — the same thing
// replaceGame already does to a dump the new game displaces.
func TestAGameThatStopsBeingBuilderSourcedDiscardsItsTablesData(t *testing.T) {
	t.Parallel()
	service, _, cluster, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}

	// The way round: the organiser meets the honest refusal, saves a script
	// instead, and comes back to the builder with the table redescribed.
	if _, err := service.SetScript(t.Context(), uuid.New(), contest, "SELECT 1;"); err != nil {
		t.Fatalf("save a script: %v", err)
	}
	ids, err := files.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("list the table data volume: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("%d table data file(s) outlived the game they belonged to", len(ids))
	}

	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{{
			Name: "suspects",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger},
				{Name: "name", Type: provisioning.ColumnNumeric}, // the type the old rows were never validated against
				{Name: "nickname", Type: provisioning.ColumnText, Nullable: true},
			},
		}},
	}); err != nil {
		t.Fatalf("save the redescribed definition: %v", err)
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window.Rows) != 0 || window.TotalRows != 0 {
		t.Fatalf("the redescribed table still holds %d row(s) (total %d): %+v", len(window.Rows), window.TotalRows, window.Rows)
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if loaded, was := cluster.tableData[built.Database+".suspects"]; was {
		t.Fatalf("the build loaded %q into a table the organiser redescribed", loaded)
	}
}

// AbortTableUpload's own path: the bytes go first, then the row, and a
// second abort of the same upload is told the upload is no longer
// 'receiving' rather than removing anything twice.
func TestAbortTableUploadRemovesTheFileAndMarksTheRowAborted(t *testing.T) {
	t.Parallel()
	service, store, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	begun := beginTableUploadWithContent(t, service, contest, "suspects", "id,name,nickname\n1,A,\n")

	aborted, err := service.AbortTableUpload(t.Context(), uuid.New(), contest, begun.ID)
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborted.Status != provisioning.TableDataAborted {
		t.Fatalf("status = %q, want aborted", aborted.Status)
	}
	if _, err := files.Received(begun.ID.String()); !errors.Is(err, gamefile.ErrNotFound) {
		t.Fatalf("the aborted upload's file is still on disk: %v", err)
	}
	store.mu.Lock()
	row := store.tableData[begun.ID]
	store.mu.Unlock()
	if row.Status != provisioning.TableDataAborted {
		t.Fatalf("the stored row is %q, want aborted", row.Status)
	}

	if _, err := service.AbortTableUpload(t.Context(), uuid.New(), contest, begun.ID); !errors.Is(err, provisioning.ErrTableDataAlreadyComplete) {
		t.Fatalf("second abort = %v, want ErrTableDataAlreadyComplete", err)
	}
	// A table freed by the abort takes a fresh upload: nothing is left
	// 'receiving' behind migration 27's own partial index.
	if _, err := service.BeginTableUpload(t.Context(), contest, "suspects", 32); err != nil {
		t.Fatalf("begin after an abort: %v", err)
	}
}

// The janitor's two sweeps on the table builder's own volume — the branch of
// SweepUploads that WithTableData turns on. Every test of that janitor used
// to assemble the service with WithUploads only, so this whole half of it
// (sweepAbandonedTableData, sweepOrphanTableFiles, abortTableData) had never
// once run: the leak SweepUploads' own doc calls the more dangerous of the
// two — bytes on the volume no row names — was written for the table builder
// and never executed.
func TestSweepUploadsAbandonsAStaleTableUploadAndRemovesATableFileWithNoRow(t *testing.T) {
	t.Parallel()
	service, store, _, files, dir := tableDataGamesOnDisk(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	stale, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Aged rather than waited for, the same convention the dump janitor's own
	// test uses against the real column.
	store.ageTableData(stale.ID, 48*time.Hour)

	// A file with no row at all: Store.Begin on a second handle, bypassing
	// Games entirely — what a crash between the reservation and the INSERT
	// leaves behind.
	orphanID := uuid.New()
	if err := tableStoreOn(t, dir).Begin(orphanID.String(), 1<<16); err != nil {
		t.Fatalf("reserve an orphan table file: %v", err)
	}
	age(t, dir, orphanID, time.Hour)

	result, err := service.SweepUploads(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Abandoned != 1 {
		t.Fatalf("abandoned = %d, want 1", result.Abandoned)
	}
	if result.OrphanFiles != 1 {
		t.Fatalf("orphan files = %d, want 1", result.OrphanFiles)
	}
	if _, err := files.Received(stale.ID.String()); !errors.Is(err, gamefile.ErrNotFound) {
		t.Fatalf("the abandoned table upload's file is still on disk: %v", err)
	}
	if _, err := files.Received(orphanID.String()); !errors.Is(err, gamefile.ErrNotFound) {
		t.Fatalf("the orphan table file — no row ever named it — is still on disk: %v", err)
	}

	store.mu.Lock()
	swept := store.tableData[stale.ID]
	store.mu.Unlock()
	if swept.Status != provisioning.TableDataAborted {
		t.Fatalf("the swept row is %q, want aborted", swept.Status)
	}
}

// The other side of the same two cut-offs: an upload begun moments ago, and a
// file reserved moments ago, are both entirely ordinary and must survive.
// Without this, a sweep with no cut-off at all — or one reading a zero
// timestamp — passes the test above.
func TestSweepUploadsLeavesARecentTableUploadAndAYoungTableFileAlone(t *testing.T) {
	t.Parallel()
	service, _, _, files, dir := tableDataGamesOnDisk(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	fresh, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Reserved this instant and never aged: the window BeginTableUpload opens
	// between Store.Begin and its own INSERT, which orphanFileGrace exists to
	// keep the sweep out of.
	youngOrphan := uuid.New()
	if err := tableStoreOn(t, dir).Begin(youngOrphan.String(), 1<<16); err != nil {
		t.Fatalf("reserve a young orphan table file: %v", err)
	}

	result, err := service.SweepUploads(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Abandoned != 0 {
		t.Fatalf("abandoned = %d, want 0 for an upload begun moments ago", result.Abandoned)
	}
	if result.OrphanFiles != 0 {
		t.Fatalf("orphan files = %d, want 0 for a file reserved moments ago", result.OrphanFiles)
	}
	if _, err := files.Received(fresh.ID.String()); err != nil {
		t.Fatalf("a fresh table upload's file was removed by the sweep: %v", err)
	}
	if _, err := files.Received(youngOrphan.String()); err != nil {
		t.Fatalf("a file younger than orphanFileGrace was removed by the sweep: %v", err)
	}
}

// wrapTableFileErr translates internal/gamefile's own refusals into this
// package's sentinels, and until this test not one of its branches had ever
// run: every one of them would have reached a handler as "internal error"
// (CLAUDE.md rule 1) and no test would have noticed. Reached through the
// public methods rather than the function directly, which is the only way a
// caller ever reaches it.
func TestGamefileRefusalsReachTheCallerAsTableDataSentinels(t *testing.T) {
	t.Parallel()
	service, _, _, files, _ := tableDataGamesOnDisk(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	// ErrFileTooLarge: a declared length past the store's own ceiling.
	if _, err := service.BeginTableUpload(t.Context(), contest, "suspects",
		tableDataLimits.MaxFileBytes+1); !errors.Is(err, provisioning.ErrTableDataTooLarge) {
		t.Fatalf("an oversized declaration = %v, want ErrTableDataTooLarge", err)
	}

	begun, err := service.BeginTableUpload(t.Context(), contest, "suspects", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	// ErrChunkOutOfOrder: an offset that does not continue the file.
	if _, err := service.AppendTableChunk(t.Context(), contest, begun.ID, 9,
		strings.NewReader("id,name,nickname\n")); !errors.Is(err, provisioning.ErrTableDataChunkOutOfOrder) {
		t.Fatalf("an out-of-order chunk = %v, want ErrTableDataChunkOutOfOrder", err)
	}

	// ErrLengthMismatch: completing with fewer bytes than were declared.
	if _, err := service.AppendTableChunk(t.Context(), contest, begun.ID, 0,
		strings.NewReader("id,name,nickname\n1,A,\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest,
		begun.ID); !errors.Is(err, provisioning.ErrTableDataLengthMismatch) {
		t.Fatalf("completing short = %v, want ErrTableDataLengthMismatch", err)
	}

	// ErrNotFound: a row whose bytes the volume no longer has. The row is left
	// in place so the bookkeeping's own "no such upload" cannot be what
	// answers — only the file is taken away.
	if _, err := service.AbortTableUpload(t.Context(), uuid.New(), contest, begun.ID); err != nil {
		t.Fatalf("abort the short upload: %v", err)
	}
	missing := beginTableUploadWithContent(t, service, contest, "suspects", "id,name,nickname\n2,B,\n")
	if err := files.Abort(missing.ID.String()); err != nil {
		t.Fatalf("remove the file behind the row: %v", err)
	}
	if _, err := service.AppendTableChunk(t.Context(), contest, missing.ID, 0,
		strings.NewReader("x")); !errors.Is(err, provisioning.ErrTableDataNotFound) {
		t.Fatalf("appending to a file the volume lost = %v, want ErrTableDataNotFound", err)
	}
}

// TableDataLimits reports the table store's own ceilings, never the dump
// store's — the whole reason WithTableData takes a second, independent
// gamefile.Store. internal/api publishes these to the editor (CLAUDE.md
// rule 11), so an installation whose two volumes are configured differently
// must not be told the wrong one.
func TestTableDataLimitsReportsTheTableStoresOwnCeilings(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)

	limits, ok := service.TableDataLimits()
	if !ok {
		t.Fatal("TableDataLimits reported no table data volume on a service given one")
	}
	if limits != tableDataLimits {
		t.Fatalf("limits = %+v, want %+v", limits, tableDataLimits)
	}

	bare, _, _ := games(true)
	if _, ok := bare.TableDataLimits(); ok {
		t.Fatal("a service never given WithTableData reported a table data volume")
	}
}

// The audit trail for a table's own data, action by action.
//
// This is a published contract — every action code is in audit.Actions, and
// the journal is append-only, so an entry that stops being written cannot be
// recovered afterwards from anything. Five of the six recording sites this
// feature added were asserted by nothing at all: an early return, a `g.audit
// == nil` that stopped being false, a Record moved outside the unit of work
// — none of them failed a test.
//
// One test rather than five, because what has to hold is the same thing five
// times, and because a sixth site added later is a row added to this table.
func TestEveryTableDataChangeIsRecordedInTheAuditTrail(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail), directly{})
	contest := uuid.New()
	withSuspects(t, service, contest)
	actor := uuid.New()

	// One recording site each, in the order an organiser would reach them.
	// The first row goes through bootstrapTableRow and the second through
	// AppendTableRow's own append: two Record calls in two places, both
	// writing ActionGameTableDataRowAdd.
	if _, err := service.AppendTableRow(t.Context(), actor, contest, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("bootstrap row: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), actor, contest, "suspects", []string{"2", "Sparrow", ""}); err != nil {
		t.Fatalf("append row: %v", err)
	}
	if err := service.DeleteTableRow(t.Context(), actor, contest, "suspects", 2); err != nil {
		t.Fatalf("delete row: %v", err)
	}
	abandoned, err := service.BeginTableUpload(t.Context(), contest, "suspects", 32)
	if err != nil {
		t.Fatalf("begin the upload to abort: %v", err)
	}
	if _, err := service.AbortTableUpload(t.Context(), actor, contest, abandoned.ID); err != nil {
		t.Fatalf("abort: %v", err)
	}
	replacing := beginTableUploadWithContent(t, service, contest, "suspects", "id,name,nickname\n9,Uploaded,\n")
	if _, err := service.CompleteTableUpload(t.Context(), actor, contest, replacing.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	want := []struct {
		action  string
		payload map[string]any
	}{
		{audit.ActionGameTableDataRowAdd, map[string]any{"table": "suspects", "row": int64(1)}},
		{audit.ActionGameTableDataRowAdd, map[string]any{"table": "suspects", "row": int64(2)}},
		{audit.ActionGameTableDataRowDelete, map[string]any{"table": "suspects", "row": int64(2)}},
		{audit.ActionGameTableDataUploadAbort, map[string]any{"table": "suspects", "bytes": int64(0)}},
		{audit.ActionGameTableDataUpload, map[string]any{"table": "suspects", "rows": int64(1), "bytes": int64(29)}},
	}
	// SetDefinition's own entry (contest.game_definition_set, asserted in
	// template_test.go) is the first thing withSuspects above wrote, and is
	// skipped here rather than restated.
	entries := trail.entries[1:]
	if len(entries) != len(want) {
		t.Fatalf("the trail holds %d table-data entr(ies), want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		got := entries[i]
		if got.Action != w.action {
			t.Fatalf("entry %d is %q, want %q", i, got.Action, w.action)
		}
		if got.Entity != "contest" || got.EntityID != contest.String() {
			t.Fatalf("entry %d points at %s/%s, want contest/%s", i, got.Entity, got.EntityID, contest)
		}
		if got.ActorID == nil || *got.ActorID != actor {
			t.Fatalf("entry %d names actor %v, want %s", i, got.ActorID, actor)
		}
		for key, value := range w.payload {
			if got.Payload[key] != value {
				t.Fatalf("entry %d (%s) payload[%q] = %v (%T), want %v", i, got.Action, key, got.Payload[key], got.Payload[key], value)
			}
		}
	}
}

// The janitor's own abort is a system event: the entry is written with no
// actor, the same convention the dump janitor keeps — an operator reading
// "who cancelled this upload" must not be shown somebody who did not.
func TestTheJanitorsOwnTableUploadAbortIsRecordedWithNoActor(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail), directly{})
	contest := uuid.New()
	withSuspects(t, service, contest)

	stale, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	store.ageTableData(stale.ID, 48*time.Hour)

	if _, err := service.SweepUploads(t.Context(), 24*time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	last := trail.entries[len(trail.entries)-1]
	if last.Action != audit.ActionGameTableDataUploadAbort {
		t.Fatalf("the sweep recorded %q, want %q", last.Action, audit.ActionGameTableDataUploadAbort)
	}
	if last.ActorID != nil {
		t.Fatalf("the janitor's own abort names actor %s; it must name nobody", last.ActorID)
	}
}

// TestDeleteTableRowTombstonesWithoutTouchingTheFile is the brief's own
// requirement for delete: the row disappears from a window read, but the
// bytes on disk are untouched — no rewrite, whatever the file's size.
func TestDeleteTableRowTombstonesWithoutTouchingTheFile(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "A", ""})
	if err != nil {
		t.Fatalf("row 1: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"2", "B", ""}); err != nil {
		t.Fatalf("row 2: %v", err)
	}
	before, err := files.Received(data.ID.String())
	if err != nil {
		t.Fatalf("stat the file: %v", err)
	}

	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 1); err != nil {
		t.Fatalf("delete row 1: %v", err)
	}

	after, err := files.Received(data.ID.String())
	if err != nil {
		t.Fatalf("stat the file: %v", err)
	}
	if after != before {
		t.Fatalf("the file changed size (%d -> %d) on a delete", before, after)
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window.Rows) != 1 || window.Rows[0].Row != 2 {
		t.Fatalf("window = %+v, want only row 2", window.Rows)
	}

	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 1); !errors.Is(err, provisioning.ErrTableRowAlreadyDeleted) {
		t.Fatalf("re-deleting row 1 = %v, want ErrTableRowAlreadyDeleted", err)
	}
	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 99); !errors.Is(err, provisioning.ErrTableRowNotFound) {
		t.Fatalf("deleting row 99 = %v, want ErrTableRowNotFound", err)
	}
}

// MaxTableDeletedRows is not a bound this package checks: DeleteTableRow
// holds no comparison against it at all. The bound is migration 27's own
// CHECK on deleted_rows, and turning a constraint violation into
// ErrTooManyDeletedRows is postgres.GameInstances.DeleteTableDataRow's job —
// which is where it is proved, against a real database
// (TestDeleteTableDataRowTombstonesAndRefusesADuplicateOrAnOverflow).
//
// What this level can honestly claim is the half the service does own: the
// database's refusal reaches the caller as itself. DeleteTableRow wraps the
// call in an audit entry and a unit of work, and both are places a sentinel
// can be swallowed or rewritten into a bare 500 — so the fake is told to
// answer exactly what the repository answers, and the assertion is that
// nothing on the way out changed it.
//
// Deliberately not a fake reimplementing the CHECK in Go: a bound the test's
// own stand-in invents is a test of the stand-in. Remove the CHECK from the
// migration, or break the SQLSTATE mapping, and that version stayed green.
func TestDeleteTableRowSurfacesTheDatabasesOwnRefusalPastMaxTableDeletedRows(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "A", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}

	store.mu.Lock()
	store.deleteRowErr = provisioning.ErrTooManyDeletedRows
	store.mu.Unlock()

	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 1); !errors.Is(err, provisioning.ErrTooManyDeletedRows) {
		t.Fatalf("error = %v, want ErrTooManyDeletedRows", err)
	}
}

// TestBuildingATableBuilderGameLoadsEachTablesDataMinusItsHeaderAndTombstones
// is the assembly half, without a real cluster: BuildTemplate is a fake that
// only records bytes, but loadTableData's own filtering — dropping the
// header line and any deleted row — is this package's own code, and this is
// what proves a mutation that skipped it (or that stopped filtering) would
// be caught without ever touching a real database.
func TestBuildingATableBuilderGameLoadsEachTablesDataMinusItsHeaderAndTombstones(t *testing.T) {
	t.Parallel()
	service, _, cluster, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"2", "Someone", "Sparrow"}); err != nil {
		t.Fatalf("row 2: %v", err)
	}
	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 1); err != nil {
		t.Fatalf("delete row 1: %v", err)
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("build finished as %q: %s", built.Status, built.BuildError)
	}

	got := cluster.tableData[built.Database+".suspects"]
	want := "2,Someone,Sparrow\n"
	if got != want {
		t.Fatalf("loaded table data = %q, want %q (header stripped, row 1 tombstoned)", got, want)
	}

	// A table nobody uploaded data for is left empty, not refused — the
	// brief's own words.
	if _, wasLoaded := cluster.tableData[built.Database+".nonexistent"]; wasLoaded {
		t.Fatal("LoadTableData was called for a table that was never given any data")
	}
}

// TestABuildToreDownTheTemplateWhenLoadingTableDataFailed proves
// finishDefinitionBuild's own teardown: a data load that fails after the
// schema already built must not leave a half-loaded template behind.
func TestABuildToreDownTheTemplateWhenLoadingTableDataFailed(t *testing.T) {
	t.Parallel()
	service, _, cluster, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}
	cluster.tableDataFail = errors.New("the game cluster refused the COPY")

	built, err := service.Build(t.Context(), time.Minute)
	if err == nil {
		t.Fatal("a data-load failure was reported as a clean tick")
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("build finished as %q, want failed", built.Status)
	}
	if built.BuildError != provisioning.BuildFailedInternally {
		t.Fatalf("build error = %q, want the fixed sentence", built.BuildError)
	}
	if len(cluster.dropped) != 1 || cluster.dropped[0] != built.Database {
		t.Fatalf("dropped = %v, want exactly %q torn down", cluster.dropped, built.Database)
	}
}

// TestTableDataWindowReturnsATruncatedRowRatherThanNoneAtAll is the fix for
// a window whose byte budget is smaller than one legitimate row: a table
// of wide text columns can have a single row past what a caller's own
// maxBytes allows (MaxTableFieldBytes alone lets one row reach megabytes),
// and returning an empty page for that — rows: [], truncated: true — reads
// identically to "there is nothing left to see" even though total_rows
// says otherwise, and the "next" button (windowFrom + len(rows)) computes
// the very page it is already on. gamefile.Store.Window never does this to
// a dump's own line window (TestWindowTruncatedByByteBudgetOnALongLine):
// asked for more than its budget allows, it still returns one line, cut to
// the budget, with Truncated set. This can't cut the line itself the same
// way — a sliced CSV row would parse as fields nothing about the table's
// real data, which is worse than a page with one row over budget — so a
// row already collected is never discarded to fit; the first row of a page
// is let through whole even when it alone is larger than maxBytes, exactly
// so a table's own window can never show fewer than one row of data that
// exists.
func TestTableDataWindowReturnsATruncatedRowRatherThanNoneAtAll(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	longName := strings.Repeat("x", 200)
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", longName, ""}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"2", "Someone", "Sparrow"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Comfortably below the first row's own line length (id + a 200-byte
	// name + the empty nickname field, plus separators), the way the brief's
	// own scenario has max_field_bytes-sized columns exceed maxTableWindowBytes.
	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 50)
	if err != nil {
		t.Fatalf("TableDataWindow: %v", err)
	}
	if len(window.Rows) != 1 {
		t.Fatalf("got %d rows, want 1 (the over-budget row itself, not an empty page)", len(window.Rows))
	}
	if !window.Truncated {
		t.Error("Truncated = false, want true (the budget stopped the window after one row)")
	}
	if window.Rows[0].Row != 1 || window.Rows[0].Fields[1] != longName {
		t.Fatalf("row = %+v, want row 1 with its name kept whole, not cut to the byte budget", window.Rows[0])
	}
}

// The rules PostgreSQL holds, on the service path a deployment uses.
//
// Everything above this line runs against the fake repository, which
// reinvents each of these in Go — a map scan under a mutex where the schema
// has a unique partial index, a `max` where the statement has GREATEST. That
// is precisely the check-then-write the index exists to make unnecessary, so
// those tests say nothing at all about the real schema: drop either index
// and every one of them stays green. These four do not (CLAUDE.md rule 10).

// The guarantee migration 27's own game_table_data_one_receiving_idx exists
// for, at the service level rather than the repository's —
// TestASecondUploadForTheSameContestIsRejectedByTheDatabaseNotByGoCode's own
// doc, at the finer grain of one table.
func TestASecondUploadForTheSameTableIsRejectedByTheDatabaseNotByGoCode(t *testing.T) {
	service, files := tableDataGamesOnRepo(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	withTwoTables(t, service, contest.ID)

	first, err := service.BeginTableUpload(t.Context(), contest.ID, "suspects", 1024)
	if err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if _, err := service.BeginTableUpload(t.Context(), contest.ID, "suspects", 1024); !errors.Is(err, provisioning.ErrTableDataInProgress) {
		t.Fatalf("a second upload for the same table = %v, want ErrTableDataInProgress", err)
	}
	if _, err := files.Received(first.ID.String()); err != nil {
		t.Fatalf("the first, still-receiving upload's file disappeared: %v", err)
	}

	// The index is per (contest, table), not per contest: a second table of
	// the same definition may be uploaded at the same time.
	if _, err := service.BeginTableUpload(t.Context(), contest.ID, "sightings", 1024); err != nil {
		t.Fatalf("a second table's own upload was refused: %v", err)
	}
}

// Completing a second upload for a table retires the first in the one
// statement, and the bytes of the displaced file are freed rather than
// stranded on the volume.
//
// Deliberately not claimed as a test of game_table_data_one_complete_idx:
// this path never reaches the index, because tableDataToDisplace has already
// named the row to retire and CompleteTableData retires it in the same
// statement. Dropping the index leaves this test green, which is how it was
// found out — the race the index is actually for is the one below.
func TestCompletingASecondUploadForATableRetiresTheFirstAndFreesItsBytes(t *testing.T) {
	service, files := tableDataGamesOnRepo(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	withTwoTables(t, service, contest.ID)

	const firstCSV = "id,name,nickname\n1,Margot,\n"
	first := beginTableUploadWithContent(t, service, contest.ID, "suspects", firstCSV)
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest.ID, first.ID); err != nil {
		t.Fatalf("complete the first: %v", err)
	}

	const secondCSV = "id,name,nickname\n2,Sparrow,Bird\n3,Someone,\n"
	second := beginTableUploadWithContent(t, service, contest.ID, "suspects", secondCSV)
	completed, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest.ID, second.ID)
	if err != nil {
		t.Fatalf("complete the second: %v", err)
	}
	if completed.Lines != 2 {
		t.Fatalf("the current file holds %d row(s), want 2 (the second upload's)", completed.Lines)
	}

	// The displaced file is gone from the volume, not merely unreferenced:
	// this is the leak SweepUploads' own doc calls the more dangerous half,
	// closed at the moment it is created rather than a tick later.
	if _, err := files.Received(first.ID.String()); !errors.Is(err, gamefile.ErrNotFound) {
		t.Fatalf("the displaced upload's file is still on the volume: %v", err)
	}
	window, err := service.TableDataWindow(t.Context(), contest.ID, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window.Rows) != 2 || window.Rows[0].Fields[1] != "Sparrow" {
		t.Fatalf("the table reads back as %+v, want the second upload's two rows", window.Rows)
	}
}

// game_table_data_one_complete_idx, on the one path that really reaches it:
// two forms adding the very first row of the same table at the same moment.
// Neither has an upload to displace — bootstrapTableRow inserts a 'complete'
// row outright — so the only thing that can stop the table ending up with
// two current files, two sets of bytes and a build loading whichever the
// query happened to pick, is the index.
//
// The two are made to read "no data yet" together rather than left to the
// scheduler: heldReady releases its callers only once both have arrived,
// which is exactly the snapshot two browser tabs share.
func TestTwoFormsBootstrappingTheSameTableAtOnceLeaveOneCurrentFileNotTwo(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	dir := t.TempDir()
	files, err := gamefile.NewStore(dir, tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	repo := &heldReady{
		TemplateRepository: postgres.NewGameInstances(testPool),
		gate:               &arrivalGate{remaining: 2, open: make(chan struct{})},
	}
	service := provisioning.NewGames(repo, &buildCluster{}, authoring{editable: true}).
		WithTableData(files, tableDataLimits)

	contest, _ := contestFor(t, t.Context(), 0)
	withTwoTables(t, service, contest.ID)

	results := make(chan error, 2)
	for i, name := range []string{"Margot", "Sparrow"} {
		go func() {
			_, err := service.AppendTableRow(context.Background(), uuid.New(), contest.ID, "suspects",
				[]string{strconv.Itoa(i + 1), name, ""})
			results <- err
		}()
	}

	var won, refused int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			won++
		case errors.Is(err, provisioning.ErrTableDataInProgress):
			refused++
		default:
			t.Fatalf("bootstrapping the first row = %v, want nil or ErrTableDataInProgress", err)
		}
	}
	if won != 1 || refused != 1 {
		t.Fatalf("%d caller(s) bootstrapped the table and %d were refused, want 1 and 1", won, refused)
	}

	// One current file, and one file on the volume: the refused caller's own
	// bytes were written before its row was refused, and bootstrapTableRow
	// gives them back rather than leaving them for the janitor.
	window, err := service.TableDataWindow(t.Context(), contest.ID, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window.Rows) != 1 {
		t.Fatalf("the table holds %d row(s), want 1: %+v", len(window.Rows), window.Rows)
	}
	ids, err := tableStoreOn(t, dir).UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("list the volume: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("the volume holds %d file(s), want 1", len(ids))
	}
}

// heldReady is the real repository with ReadyTableData gated: every caller
// takes its answer and then waits until as many of them have arrived as the
// gate was armed for. Everything else is the real statement, so what the
// test above proves is the schema's own behaviour, not a stand-in's.
type heldReady struct {
	provisioning.TemplateRepository
	gate *arrivalGate
}

func (h *heldReady) ReadyTableData(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	data, err := h.TemplateRepository.ReadyTableData(ctx, contestID, table)
	h.gate.arrive()
	return data, err
}

// The GREATEST floors of postgres.GameInstances.AppendTableDataRow, reached
// the way production reaches them: two forms adding a row to the same table,
// recording their counts in the opposite order to the one they wrote in.
//
// AppendTableRow's own doc is explicit that this is what the floors are for —
// "the two callers reaching storage in the opposite order to the one they
// read in cannot leave the bookkeeping describing the shorter file" — and
// until this ran on the real statement, nothing said so anywhere but in a
// fake's own `max`. A plain assignment leaves the row counting three rows as
// two, and every later append then writes at an offset the file already has,
// which gamefile.Store.Append reports as a retry: the table takes no more
// rows, for ever.
//
// The inversion is staged rather than raced: heldAppend holds the first
// caller between writing its bytes and recording them, which is a window the
// scheduler would otherwise open only now and then.
func TestTwoFormsRecordingOutOfOrderCannotMakeTheBookkeepingDescribeAShorterFile(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	held := &heldAppend{
		TemplateRepository: postgres.NewGameInstances(testPool),
		arrived:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	service := provisioning.NewGames(held, &buildCluster{}, authoring{editable: true}).
		WithTableData(files, tableDataLimits)

	contest, _ := contestFor(t, t.Context(), 0)
	withTwoTables(t, service, contest.ID)

	// Row 1 bootstraps the file (CreateReadyTableData, not the statement
	// under test), so the two rows below are the first two appends.
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}

	slow := make(chan error, 1)
	go func() {
		_, err := service.AppendTableRow(context.Background(), uuid.New(), contest.ID, "suspects", []string{"2", "Sparrow", ""})
		slow <- err
	}()
	<-held.arrived // row 2's bytes are on disk; its count is not recorded yet

	// Row 3 reads the file rather than the stale bookkeeping (tableFileState),
	// so it appends after row 2 and records three rows.
	third, err := service.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"3", "Someone", ""})
	if err != nil {
		t.Fatalf("row 3: %v", err)
	}
	if third.Lines != 3 {
		t.Fatalf("row 3 recorded %d line(s), want 3", third.Lines)
	}

	close(held.release) // row 2 now records its own, smaller pair
	if err := <-slow; err != nil {
		t.Fatalf("row 2: %v", err)
	}

	current, err := postgres.NewGameInstances(testPool).ReadyTableData(t.Context(), contest.ID, "suspects")
	if err != nil {
		t.Fatalf("read the table's current data: %v", err)
	}
	onVolume, err := files.Received(current.ID.String())
	if err != nil {
		t.Fatalf("stat the file: %v", err)
	}
	if current.Lines != 3 || current.ReceivedBytes != onVolume {
		t.Fatalf("the bookkeeping says %d row(s) / %d bytes, the file holds 3 rows / %d bytes",
			current.Lines, current.ReceivedBytes, onVolume)
	}

	// And the table still takes rows: a bookkeeping that went backwards would
	// send this append to an offset the file already covers, which
	// gamefile.Store.Append answers as a retry — no row, no error, for ever.
	fourth, err := service.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"4", "Later", ""})
	if err != nil {
		t.Fatalf("row 4: %v", err)
	}
	if fourth.Lines != 4 {
		t.Fatalf("row 4 recorded %d line(s), want 4 — the table was wedged", fourth.Lines)
	}
}

// heldAppend is the real repository with one call held open: the first
// AppendTableDataRow waits on release, having announced itself on arrived.
// Everything else — every statement, every constraint — is the real one, so
// what the test below proves is the SQL's own behaviour and not a stand-in's.
type heldAppend struct {
	provisioning.TemplateRepository
	once    sync.Once
	arrived chan struct{}
	release chan struct{}
}

func (h *heldAppend) AppendTableDataRow(ctx context.Context, id uuid.UUID, receivedBytes, lines int64) (provisioning.TableData, error) {
	first := false
	h.once.Do(func() { first = true })
	if first {
		close(h.arrived)
		<-h.release
	}
	return h.TemplateRepository.AppendTableDataRow(ctx, id, receivedBytes, lines)
}

// Migration 26's own source/definition pairing CHECK, on the service path:
// a row may be 'builder' with a definition or 'editor'/'file' without one,
// and never a leftover of the source it was replaced from. Nothing in Go
// enforces that — SaveDefinition and SaveScript each clear the column the
// other uses, and the constraint is what notices when one of them stops.
func TestSwitchingASourceClearsWhatTheOtherOneLeftBehind(t *testing.T) {
	service, _ := tableDataGamesOnRepo(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	withTwoTables(t, service, contest.ID)

	stored, err := postgres.NewGameInstances(testPool).Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the builder game back: %v", err)
	}
	if len(stored.Definition.Tables) != 2 {
		t.Fatalf("the builder game stored %d table(s), want 2", len(stored.Definition.Tables))
	}

	// An editor script over a builder game: the definition has to go, or the
	// CHECK refuses the upsert outright.
	if _, err := service.SetScript(t.Context(), uuid.New(), contest.ID, "CREATE TABLE t (id integer);"); err != nil {
		t.Fatalf("switch to an editor script: %v", err)
	}
	stored, err = postgres.NewGameInstances(testPool).Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the editor game back: %v", err)
	}
	if len(stored.Definition.Tables) != 0 {
		t.Fatalf("the editor game still carries a definition: %+v", stored.Definition)
	}

	// And back again: the definition returns, and the table the organiser had
	// uploaded data for is theirs to fill again.
	withTwoTables(t, service, contest.ID)
	if _, err := service.BeginTableUpload(t.Context(), contest.ID, "suspects", 32); err != nil {
		t.Fatalf("upload into the restored definition: %v", err)
	}
}

// withTwoTables saves the definition the real-repository tests above work
// against: suspects, plus a second table so a per-(contest, table) index can
// be told from a per-contest one.
func withTwoTables(t *testing.T, service *provisioning.Games, contest uuid.UUID) {
	t.Helper()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{
			suspectsTable(),
			{
				Name: "sightings",
				Columns: []provisioning.ColumnDefinition{
					{Name: "suspect_id", Type: provisioning.ColumnInteger},
					{Name: "seen_at", Type: provisioning.ColumnTimestamp},
				},
			},
		},
	}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
}

// benchmarkTable is ten columns of mixed type — the shape the review measured
// the validation pass against, and wider than any of the tests' own tables.
func benchmarkTable() provisioning.TableDefinition {
	return provisioning.TableDefinition{
		Name: "records",
		Columns: []provisioning.ColumnDefinition{
			{Name: "id", Type: provisioning.ColumnInteger},
			{Name: "amount", Type: provisioning.ColumnNumeric},
			{Name: "ratio", Type: provisioning.ColumnNumeric},
			{Name: "name", Type: provisioning.ColumnText},
			{Name: "city", Type: provisioning.ColumnText},
			{Name: "note", Type: provisioning.ColumnText},
			{Name: "active", Type: provisioning.ColumnBoolean},
			{Name: "seen_on", Type: provisioning.ColumnDate},
			{Name: "seen_at", Type: provisioning.ColumnTimestamp},
			{Name: "rank", Type: provisioning.ColumnInteger},
		},
		PrimaryKey: []string{"id"},
	}
}

// benchmarkCSV is rows of benchmarkTable, header included.
func benchmarkCSV(rows int) string {
	var b strings.Builder
	b.WriteString("id,amount,ratio,name,city,note,active,seen_on,seen_at,rank\n")
	for i := range rows {
		fmt.Fprintf(&b, "%d,12345.6789,-0.5e3,Ionescu Vasile,Chisinau,an ordinary note,true,2024-01-02,2024-01-02 10:11:12,%d\n", i, i%97)
	}
	return b.String()
}

// The pass CompleteTableUpload runs inside the HTTP request that finishes an
// upload: the header, then every data row's field count and every field's
// type. It is the whole of finding 5 — measured by the review at 178 ms and
// 166 MB of garbage for two hundred thousand rows of ten columns, with 42% of
// the time inside one regular expression.
//
// Run with `go test -bench CompleteTableUpload -benchmem ./internal/provisioning/`.
func BenchmarkCompleteTableUpload(b *testing.B) {
	const rows = 30_000
	content := benchmarkCSV(rows)

	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		service, _, _, _ := tableDataGames(b, true)
		contest := uuid.New()
		if _, err := service.SetDefinition(b.Context(), uuid.New(), contest,
			provisioning.Definition{Tables: []provisioning.TableDefinition{benchmarkTable()}}); err != nil {
			b.Fatalf("save the definition: %v", err)
		}
		data := beginTableUploadWithContent(b, service, contest, "records", content)
		b.StartTimer()

		if _, err := service.CompleteTableUpload(b.Context(), uuid.New(), contest, data.ID); err != nil {
			b.Fatalf("CompleteTableUpload: %v", err)
		}
	}
}

// Paging through a table's rows: the last page of a file is what finding 6 is
// about, since a scan that always starts at row 1 makes it cost the whole
// file.
//
// Run with `go test -bench TableDataWindow -benchmem ./internal/provisioning/`.
func BenchmarkTableDataWindowLastPage(b *testing.B) {
	const rows = 30_000
	content := benchmarkCSV(rows)

	service, _, _, _ := tableDataGames(b, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(b.Context(), uuid.New(), contest,
		provisioning.Definition{Tables: []provisioning.TableDefinition{benchmarkTable()}}); err != nil {
		b.Fatalf("save the definition: %v", err)
	}
	data := beginTableUploadWithContent(b, service, contest, "records", content)
	if _, err := service.CompleteTableUpload(b.Context(), uuid.New(), contest, data.ID); err != nil {
		b.Fatalf("CompleteTableUpload: %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		window, err := service.TableDataWindow(b.Context(), contest, "records", rows-99, 100, 1<<20)
		if err != nil {
			b.Fatalf("TableDataWindow: %v", err)
		}
		if len(window.Rows) != 100 {
			b.Fatalf("got %d rows, want 100", len(window.Rows))
		}
	}
}

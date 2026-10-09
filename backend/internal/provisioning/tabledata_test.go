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

// Most of these run against the fake repository and cluster with a real
// gamefile.Store on a temp directory. Rules only PostgreSQL holds run against
// the real repository (tableDataGamesOnRepo).

// suspectsTable has a primary key, a required column and a nullable one.
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

func tableDataGames(t testing.TB, editable bool) (*provisioning.Games, *templateStore, *buildCluster, *gamefile.Store) {
	t.Helper()
	service, store, cluster, files, _ := tableDataGamesOnDisk(t, editable)
	return service, store, cluster, files
}

// tableDataLimits is shared so a second handle (tableStoreOn) agrees with the
// service's store.
var tableDataLimits = gamefile.Limits{MaxFileBytes: 8 << 20, MaxDirBytes: 32 << 20, MaxChunkBytes: 4 << 20}

// tableDataGamesOnDisk also returns the directory, so janitor tests can age
// files with os.Chtimes.
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

// tableDataGamesOnRepo assembles Games against the real core database, for
// rules only PostgreSQL holds: the one-receiving and one-complete indexes, the
// GREATEST floors in AppendTableDataRow, and the source/definition CHECK
// (CLAUDE.md rule 10).
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

// tableStoreOn opens a second handle on a table-data directory already in use.
func tableStoreOn(t testing.TB, dir string) *gamefile.Store {
	t.Helper()
	store, err := gamefile.NewStore(dir, tableDataLimits)
	if err != nil {
		t.Fatalf("open a second handle on the table data directory: %v", err)
	}
	return store
}

// anyAge is a cut-off that makes UploadIDs list every file; a zero Time would
// list none.
func anyAge() time.Time { return time.Now().Add(time.Hour) }

// withSuspects saves a builder definition holding only suspectsTable.
func withSuspects(t testing.TB, service *provisioning.Games, contest uuid.UUID) {
	t.Helper()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest,
		provisioning.Definition{Tables: []provisioning.TableDefinition{suspectsTable()}}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
}

// beginTableUploadWithContent begins an upload sized to content and appends
// it in one chunk.
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

// A refused row must not leave its file and directory reservation behind.
func TestATableUploadRowTheDatabaseRefusesGivesBackTheSpaceItReserved(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	// Refused while the first is still receiving.
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

// The same sentinel CurrentUpload uses, so the handler serves one "absent"
// shape.
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

func TestCurrentTableDataIsDisabledWithoutATableDataVolume(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)

	_, err := service.CurrentTableData(t.Context(), uuid.New(), "suspects")
	if !errors.Is(err, provisioning.ErrTableDataDisabled) {
		t.Fatalf("error = %v, want ErrTableDataDisabled", err)
	}
}

// The refusal must come from AppendTableChunk, not wait for
// CompleteTableUpload.
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

// The first chunk ends mid-header with no newline; that is not a mismatch.
func TestAppendTableChunkAcceptsAHeaderSplitAcrossChunksWithoutRefusing(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	first := "id,na"
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

// A resumed upload resends its first chunk; Append treats it as a retry and
// the resend must not fail.
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
	if _, err := service.AppendTableChunk(t.Context(), contest, data.ID, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("resend of the same first chunk was refused: %v", err)
	}

	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

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

// With no upload, the first row creates the file and the second appends to it.
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

// A form row after an upload with no trailing newline must land on its own
// line, not glued to the last row.
func TestAppendTableRowAfterAFileWhoseLastLineHasNoNewline(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot," // no trailing newline
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

// An empty value is validated as NULL, so it must be written as a bare empty
// field: COPY csv reads a quoted "" as the empty string, which fails for an
// integer column.
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

// Only the bookkeeping is grown to the limit; the check never reads the rows.
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

// The gate holds both callers until both have read the table's data, so they
// write at the same offset deterministically.
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
	// Accepted rows are in the file, refused ones are not, and the
	// bookkeeping counts what is there.
	if len(window.Rows) != 1+accepted {
		t.Fatalf("the file holds %d row(s) after 1 + %d accepted appends: %+v", len(window.Rows), accepted, window.Rows)
	}
	if int64(len(window.Rows)) != window.TotalRows {
		t.Fatalf("the file holds %d row(s), the bookkeeping says %d", len(window.Rows), window.TotalRows)
	}

	// The table is not wedged.
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"9", "C", ""}); err != nil {
		t.Fatalf("the table stopped taking rows after the race: %v", err)
	}
}

// The bytes reached the file but the transaction recording them rolled back,
// so the bookkeeping's offset is behind the file.
func TestAppendTableRowReconcilesBookkeepingThatLagsTheFile(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""})
	if err != nil {
		t.Fatalf("row 1: %v", err)
	}
	// Roll the bookkeeping back to the header alone; the file keeps Margot.
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

// Saving a script in between must not let old rows survive into a table
// redescribed with another column type: the file's header names columns, not
// types, so the build would load them unchecked.
func TestAGameThatStopsBeingBuilderSourcedDiscardsItsTablesData(t *testing.T) {
	t.Parallel()
	service, _, cluster, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}

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
				{Name: "name", Type: provisioning.ColumnNumeric}, // the old rows were never validated as numeric
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
	// Nothing is left 'receiving', so a fresh upload can begin.
	if _, err := service.BeginTableUpload(t.Context(), contest, "suspects", 32); err != nil {
		t.Fatalf("begin after an abort: %v", err)
	}
}

// The janitor's two sweeps on the table-data volume.
func TestSweepUploadsAbandonsAStaleTableUploadAndRemovesATableFileWithNoRow(t *testing.T) {
	t.Parallel()
	service, store, _, files, dir := tableDataGamesOnDisk(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	stale, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	store.ageTableData(stale.ID, 48*time.Hour)

	// A file with no row, as a crash between reservation and INSERT leaves.
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

// Without this, a sweep that ignores its cut-offs passes the test above.
func TestSweepUploadsLeavesARecentTableUploadAndAYoungTableFileAlone(t *testing.T) {
	t.Parallel()
	service, _, _, files, dir := tableDataGamesOnDisk(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	fresh, err := service.BeginTableUpload(t.Context(), contest, "suspects", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Never aged: inside orphanFileGrace, like a file between Store.Begin and
	// its INSERT.
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

// Exercises wrapTableFileErr through the public methods (CLAUDE.md rule 1).
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

	// ErrNotFound: only the file is removed, so the answer comes from the
	// volume and not the bookkeeping.
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

// A missing audit entry cannot be recovered later, so every recording site is
// asserted here; a new site is a new row in want.
func TestEveryTableDataChangeIsRecordedInTheAuditTrail(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail), directly{})
	contest := uuid.New()
	withSuspects(t, service, contest)
	actor := uuid.New()

	// The first row goes through bootstrapTableRow, the second through the
	// append path: two recording sites for the same action.
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
	// Skip withSuspects' SetDefinition entry.
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

// The bound itself is a database CHECK, proved against PostgreSQL in the
// postgres package. This asserts only that the repository's refusal survives
// the audit and unit-of-work wrapping unchanged.
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

// The fake cluster records the bytes loaded, which proves the header and
// tombstone filtering without a real database.
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

	// A table with no data is left empty, not refused.
	if _, wasLoaded := cluster.tableData[built.Database+".nonexistent"]; wasLoaded {
		t.Fatal("LoadTableData was called for a table that was never given any data")
	}
}

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

// A first row larger than maxBytes is returned whole rather than as an empty
// page, whose "next" offset would point back at itself.
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

	// Well below the first row's length (over 200 bytes).
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

// The rules PostgreSQL holds, on the real repository. The fake above
// reimplements them in Go, so only these tests notice if the schema loses
// them (CLAUDE.md rule 10).

// game_table_data_one_receiving_idx, per (contest, table).
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

	// Another table of the same contest may upload at the same time.
	if _, err := service.BeginTableUpload(t.Context(), contest.ID, "sightings", 1024); err != nil {
		t.Fatalf("a second table's own upload was refused: %v", err)
	}
}

// This path never reaches game_table_data_one_complete_idx, since
// CompleteTableData retires the displaced row in the same statement; the
// index's race is tested below.
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

	// The displaced file is removed now, not left for the janitor.
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

// game_table_data_one_complete_idx: two forms bootstrapping the same table
// both insert a 'complete' row, and only the index stops two current files.
// heldReady makes both read "no data yet" before either writes.
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

	// The refused caller's file was removed by bootstrapTableRow.
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

// heldReady is the real repository with ReadyTableData gated: each caller gets
// its answer, then waits until the gate's count of callers has arrived.
type heldReady struct {
	provisioning.TemplateRepository
	gate *arrivalGate
}

func (h *heldReady) ReadyTableData(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	data, err := h.TemplateRepository.ReadyTableData(ctx, contestID, table)
	h.gate.arrive()
	return data, err
}

// The GREATEST floors in AppendTableDataRow: two forms record their counts in
// the opposite order to their writes. heldAppend holds the first caller
// between writing its bytes and recording them.
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

	// Row 1 bootstraps the file, so rows 2 and 3 are the appends under test.
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}

	slow := make(chan error, 1)
	go func() {
		_, err := service.AppendTableRow(context.Background(), uuid.New(), contest.ID, "suspects", []string{"2", "Sparrow", ""})
		slow <- err
	}()
	<-held.arrived // row 2's bytes are on disk; its count is not recorded yet

	// Row 3 reads the file (tableFileState), appends after row 2 and records
	// three rows.
	third, err := service.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"3", "Someone", ""})
	if err != nil {
		t.Fatalf("row 3: %v", err)
	}
	if third.Lines != 3 {
		t.Fatalf("row 3 recorded %d line(s), want 3", third.Lines)
	}

	close(held.release) // row 2 now records its smaller pair
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

	// The table still takes rows.
	fourth, err := service.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"4", "Later", ""})
	if err != nil {
		t.Fatalf("row 4: %v", err)
	}
	if fourth.Lines != 4 {
		t.Fatalf("row 4 recorded %d line(s), want 4 — the table was wedged", fourth.Lines)
	}
}

// heldAppend is the real repository whose first AppendTableDataRow closes
// arrived and then waits on release.
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

// The source/definition CHECK: only 'builder' carries a definition. It catches
// SaveDefinition or SaveScript failing to clear the other's column.
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

	// The definition must be cleared, or the CHECK refuses the upsert.
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

	// Back to the builder: the table accepts uploads again.
	withTwoTables(t, service, contest.ID)
	if _, err := service.BeginTableUpload(t.Context(), contest.ID, "suspects", 32); err != nil {
		t.Fatalf("upload into the restored definition: %v", err)
	}
}

// withTwoTables saves suspects plus a second table, so a per-(contest, table)
// index can be told from a per-contest one.
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

// benchmarkTable is ten columns of mixed type.
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

// The validation pass CompleteTableUpload runs inside the request.
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

// The last page is the worst case for a walk from the top of the file.
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

// The definition is saved, and so built, before any row can be typed, so
// every later row must mark the template out of date (CLAUDE.md rule 10).
func TestARowAddedAfterTheGameWasBuiltLeavesTheTemplateWaitingToBeBuiltAgain(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)
	markBuilt(store)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects",
		[]string{"1", "Margot", ""}); err != nil {
		t.Fatalf("append a row from the form: %v", err)
	}

	after, err := store.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	if !after.NeedsBuild() {
		t.Fatalf("after a row was added the template is %q with no data mark; the row will never be built into a database",
			after.Status)
	}
}

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

// A reserved upload changes no row a build reads.
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

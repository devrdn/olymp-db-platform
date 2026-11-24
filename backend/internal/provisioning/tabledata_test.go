package provisioning_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/gamefile"
	"github.com/devrdn/olymp-db-platform/backend/internal/gamefile/gamefiletest"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/postgres"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
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
var tableDataLimits = gamefile.Limits{MaxFileBytes: 8 << 20, MaxDirBytes: 32 << 20, MaxChunkBytes: 4 << 20, BufferedChunks: 4}

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
// rules only PostgreSQL holds: the one-receiving and one-complete indexes and
// the source/definition CHECK (CLAUDE.md rule 10).
func tableDataGamesOnRepo(t *testing.T, editable bool) (*provisioning.Games, *gamefile.Store) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	service := provisioning.NewGames(onPostgres(postgres.NewGameInstances(testPool)), &buildCluster{}, authoring{editable: editable}, noContest{}, directly{}).
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
	data, err := service.TableUploads().Begin(t.Context(), contest, table, int64(len(content)))
	if err != nil {
		t.Fatalf("begin table upload: %v", err)
	}
	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), contest, data.ID, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("append table chunk: %v", err)
	}
	return data
}

func TestCurrentTableDataFindsAnUploadStillReceiving(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	begun, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 32)
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

	if _, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 1024); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	// Refused while the first is still receiving.
	for i := 0; i < 3; i++ {
		if _, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 1024); !errors.Is(err, provisioning.ErrUploadInProgress) {
			t.Fatalf("begin %d answered %v, want ErrUploadInProgress", i+2, err)
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
	if !errors.Is(err, provisioning.ErrUploadNotFound) {
		t.Fatalf("error = %v, want ErrUploadNotFound", err)
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
	if !errors.Is(err, provisioning.ErrUploadsDisabled) {
		t.Fatalf("error = %v, want ErrUploadsDisabled", err)
	}
}

// The refusal must come from the first chunk, not wait for
// CompleteTableUpload.
func TestATableUploadRefusesAMismatchedHeaderOnTheFirstChunk(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,extra\n1,A,x\n" // wrong header; the row itself would be fine
	data, err := service.TableUploads().Begin(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = service.TableUploads().Append(t.Context(), uuid.New(), contest, data.ID, 0, strings.NewReader(content))
	if !errors.Is(err, provisioning.ErrTableHeaderMismatch) {
		t.Fatalf("Append() = %v, want ErrTableHeaderMismatch on the first chunk itself", err)
	}
}

// The first chunk ends mid-header with no newline; that is not a mismatch.
func TestATableUploadAcceptsAHeaderSplitAcrossChunksWithoutRefusing(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	first := "id,na"
	second := "me,nickname\n1,Margot,\n"
	content := first + second

	data, err := service.TableUploads().Begin(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), contest, data.ID, 0, strings.NewReader(first)); err != nil {
		t.Fatalf("first chunk (no newline yet) was refused: %v", err)
	}
	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), contest, data.ID, int64(len(first)), strings.NewReader(second)); err != nil {
		t.Fatalf("second chunk: %v", err)
	}

	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// A resumed upload resends its first chunk; Append treats it as a retry and
// the resend must not fail.
func TestATableUploadDoesNotRecheckAResentFirstChunk(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot,\n"
	data, err := service.TableUploads().Begin(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), contest, data.ID, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), contest, data.ID, 0, strings.NewReader(content)); err != nil {
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

	data, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 100)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := files.Append(data.ID.String(), 0, strings.NewReader("id,name,nickname\n1,A,\n")); err != nil {
		t.Fatalf("append directly: %v", err)
	}
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); !errors.Is(err, provisioning.ErrUploadLengthMismatch) {
		t.Fatalf("error = %v, want ErrUploadLengthMismatch", err)
	}
}

// Bytes on disk past what the upload's row records were never acknowledged to
// the client, so completing the upload neither validates nor counts them.
func TestCompleteTableUploadCountsOnlyTheBytesItsRecordHolds(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)
	const content = "id,name,nickname\n1,Margot,\n"
	upload := beginTableUploadWithContent(t, service, contest, "suspects", content)
	if _, err := files.Append(upload.ID.String(), int64(len(content)), strings.NewReader("2,Unrecorded,\n")); err != nil {
		t.Fatalf("write past the record: %v", err)
	}

	completed, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, upload.ID)

	if err != nil || completed.Lines != 1 {
		t.Fatalf("CompleteTableUpload() = %d row(s), %v, want the 1 its record holds", completed.Lines, err)
	}
	assertWindowShows(t, service, contest, "1,Margot,")
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

	if _, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 1024); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "A", ""}); !errors.Is(err, provisioning.ErrUploadInProgress) {
		t.Fatalf("error = %v, want ErrUploadInProgress", err)
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
	if accepted != 1 {
		t.Fatalf("%d concurrent appends were accepted, want the one whose row was recorded first", accepted)
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

// A missing audit entry cannot be recovered later, so every recording site is
// asserted here; a new site is a new row in want.
func TestEveryTableDataChangeIsRecordedInTheAuditTrail(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail))
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
	abandoned, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 32)
	if err != nil {
		t.Fatalf("begin the upload to abort: %v", err)
	}
	if _, err := service.TableUploads().Abort(t.Context(), actor, contest, abandoned.ID); err != nil {
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
	service = service.WithAudit(audit.New(trail))
	contest := uuid.New()
	withSuspects(t, service, contest)

	stale, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	store.ageUpload(stale.ID, 48*time.Hour)

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
	before, err := gamefiletest.StoredBytes(files, data.ID.String())
	if err != nil {
		t.Fatalf("stat the file: %v", err)
	}

	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 1); err != nil {
		t.Fatalf("delete row 1: %v", err)
	}

	after, err := gamefiletest.StoredBytes(files, data.ID.String())
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

// A value is refused rather than written: a line break inside a row would be
// two physical lines to every reader that counts them, and the count stored
// alongside would be off by one from there on.
func TestAppendTableRowRefusesAValueWithALineBreakAndWritesNothing(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	for name, value := range map[string]string{"a newline": "Mar\ngot", "a carriage return": "Mar\rgot"} {
		_, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", value, ""})
		if !errors.Is(err, provisioning.ErrTableValueInvalid) {
			t.Fatalf("%s: error = %v, want ErrTableValueInvalid", name, err)
		}
		if !strings.Contains(err.Error(), `"name"`) {
			t.Fatalf("%s: error = %q, does not name the column", name, err)
		}
	}
	ids, err := files.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("list the table data volume: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a refused row still left %d file(s) on disk", len(ids))
	}
	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("TableDataWindow: %v", err)
	}
	if window.TotalRows != 0 {
		t.Fatalf("the table counts %d row(s) after two refused values", window.TotalRows)
	}
}

// COPY reads a bare `\.` line as the end of the data even in CSV format, so
// a one-column text table holding that value would load every row before it
// and none after, with no error. The reader quotes that one line.
func TestBuildingATableBuilderGameQuotesARowCopyWouldReadAsEndOfData(t *testing.T) {
	t.Parallel()
	service, _, cluster, _ := tableDataGames(t, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{{
			Name:    "notes",
			Columns: []provisioning.ColumnDefinition{{Name: "body", Type: provisioning.ColumnText}},
		}},
	}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	for _, body := range []string{`\.`, `\.x`, "after"} {
		if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "notes", []string{body}); err != nil {
			t.Fatalf("row %q: %v", body, err)
		}
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("build finished as %q: %s", built.Status, built.BuildError)
	}

	got := cluster.tableData[built.Database+".notes"]
	want := "\"\\.\"\n\\.x\nafter\n"
	if got != want {
		t.Fatalf("loaded table data = %q, want %q (only the bare end-of-data line quoted)", got, want)
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

// The rows load into the template the script built, under the same limit.
func TestABuildWhoseTableDataPassesTheTemplateSizeLimitIsTooLargeAndTornDown(t *testing.T) {
	t.Parallel()
	service, _, cluster, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("row 1: %v", err)
	}
	cluster.tableDataFail = fmt.Errorf("%w: past the limit", provisioning.ErrTemplateTooLarge)

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("table data too large for the cluster was reported as the tick's own failure: %v", err)
	}
	if built.Status != provisioning.TemplateFailed || !built.TooLarge() {
		t.Fatalf("finished as %q with %q, want failed as too large", built.Status, built.BuildError)
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

	first, err := service.TableUploads().Begin(t.Context(), contest.ID, "suspects", 1024)
	if err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if _, err := service.TableUploads().Begin(t.Context(), contest.ID, "suspects", 1024); !errors.Is(err, provisioning.ErrUploadInProgress) {
		t.Fatalf("a second upload for the same table = %v, want ErrUploadInProgress", err)
	}
	if _, err := gamefiletest.StoredBytes(files, first.ID.String()); err != nil {
		t.Fatalf("the first, still-receiving upload's file disappeared: %v", err)
	}

	// Another table of the same contest may upload at the same time.
	if _, err := service.TableUploads().Begin(t.Context(), contest.ID, "sightings", 1024); err != nil {
		t.Fatalf("a second table's own upload was refused: %v", err)
	}
}

// This path never reaches game_table_data_one_complete_idx, since the table
// rows' Complete retires the displaced row in the same statement; the index's
// race is tested below.
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
	if _, err := gamefiletest.StoredBytes(files, first.ID.String()); !errors.Is(err, gamefile.ErrNotFound) {
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
	stores := onPostgres(postgres.NewGameInstances(testPool))
	stores.TableData = &heldReady{
		TableDataRows: stores.TableData,
		gate:          &arrivalGate{remaining: 2, open: make(chan struct{})},
	}
	service := provisioning.NewGames(stores, &buildCluster{}, authoring{editable: true}, noContest{}, directly{}).
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
		case errors.Is(err, provisioning.ErrUploadInProgress):
			refused++
		default:
			t.Fatalf("bootstrapping the first row = %v, want nil or ErrUploadInProgress", err)
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

// heldReady is the real rows with Ready gated: each caller gets its answer,
// then waits until the gate's count of callers has arrived.
type heldReady struct {
	provisioning.TableDataRows
	gate *arrivalGate
}

func (h *heldReady) Ready(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	data, err := h.TableDataRows.Ready(ctx, contestID, table)
	h.gate.arrive()
	return data, err
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
	if _, err := service.TableUploads().Begin(t.Context(), contest.ID, "suspects", 32); err != nil {
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

	if _, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 32); err != nil {
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

// A table's data is the game's content: once the contest has started, under
// the contest row's lock, every write to it is refused, whatever an earlier
// check outside the transaction said.
func TestTableDataWritesAreRefusedOnceTheContestHasStarted(t *testing.T) {
	t.Parallel()
	rows := &contestRows{status: contests.StatusPublished}
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	store := &templateStore{policy: sqlpolicy.ReadOnly()}
	service := provisioning.NewGames(store.stores(), &buildCluster{},
		authoring{editable: true}, rows, directly{}).WithTableData(files, tableDataLimits)
	contest, actor := uuid.New(), uuid.New()
	withSuspects(t, service, contest)
	if _, err := service.AppendTableRow(t.Context(), actor, contest, "suspects", []string{"1", "Margot", ""}); err != nil {
		t.Fatalf("the first row: %v", err)
	}
	rows.status = contests.StatusRunning

	if _, err := service.AppendTableRow(t.Context(), actor, contest, "suspects", []string{"2", "Sparrow", ""}); !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Errorf("AppendTableRow() = %v, want ErrGameNotEditable", err)
	}
	if err := service.DeleteTableRow(t.Context(), actor, contest, "suspects", 1); !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Errorf("DeleteTableRow() = %v, want ErrGameNotEditable", err)
	}
	upload := beginTableUploadWithContent(t, service, contest, "suspects", "id,name,nickname\n9,Uploaded,\n")
	if _, err := service.CompleteTableUpload(t.Context(), actor, contest, upload.ID); !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Errorf("CompleteTableUpload() = %v, want ErrGameNotEditable", err)
	}
}

// transaction is the unit of work NewGames takes.
type transaction interface {
	Do(ctx context.Context, fn func(context.Context) error) error
}

var errCommit = errors.New("commit: connection reset")

// commitLost answers a failed commit for a transaction that did commit.
type commitLost struct{ inner transaction }

func (c commitLost) Do(ctx context.Context, fn func(context.Context) error) error {
	if err := c.inner.Do(ctx, fn); err != nil {
		return err
	}
	return errCommit
}

// commitFailed rolls back a transaction that ran to its end.
type commitFailed struct{ inner transaction }

func (c commitFailed) Do(ctx context.Context, fn func(context.Context) error) error {
	return c.inner.Do(ctx, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		return errCommit
	})
}

// undoing is a unit of work over the fake repository that puts its table data
// back when fn fails, as a rolled-back transaction does.
type undoing struct{ store *templateStore }

func (u undoing) Do(ctx context.Context, fn func(context.Context) error) error {
	u.store.mu.Lock()
	saved := maps.Clone(u.store.tableData)
	u.store.mu.Unlock()

	err := fn(ctx)
	if err != nil {
		u.store.mu.Lock()
		u.store.tableData = saved
		u.store.mu.Unlock()
	}
	return err
}

// staleReads answers Ready with the pinned row while one is set: the table as
// a form read it before another form's row was recorded. Calls made one after
// another then decide exactly as forms submitted at once do.
type staleReads struct {
	provisioning.TableDataRows
	pinned *provisioning.TableData
}

func (s *staleReads) Ready(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	if s.pinned != nil {
		return *s.pinned, nil
	}
	return s.TableDataRows.Ready(ctx, contestID, table)
}

// refusingFirst is contests.ContentRows for a contest the first edit
// finds running and every later one finds published.
type refusingFirst struct{ edits int }

func (r *refusingFirst) LockContest(context.Context, uuid.UUID) error { return nil }

func (r *refusingFirst) ByID(_ context.Context, id uuid.UUID) (contests.Contest, error) {
	r.edits++
	if r.edits == 1 {
		return contests.Contest{ID: id, Status: contests.StatusRunning}, nil
	}
	return contests.Contest{ID: id, Status: contests.StatusPublished}, nil
}

func (r *refusingFirst) MarkContentChanged(_ context.Context, read contests.Contest) (contests.Contest, error) {
	return read, nil
}

// suspectsOnDisk is a suspects table holding Margot, over the fake repository
// and a real file store.
type suspectsOnDisk struct {
	store   *templateStore
	repo    *staleReads
	cluster *buildCluster
	files   *gamefile.Store
	contest uuid.UUID
	first   provisioning.TableData
}

func newSuspectsOnDisk(t *testing.T) *suspectsOnDisk {
	t.Helper()
	store := &templateStore{policy: sqlpolicy.ReadOnly()}
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	s := &suspectsOnDisk{
		store: store, repo: &staleReads{TableDataRows: store.tableRows()}, cluster: &buildCluster{},
		files: files, contest: uuid.New(),
	}
	service := s.games(noContest{}, undoing{store})
	withSuspects(t, service, s.contest)
	s.first, err = service.AppendTableRow(t.Context(), uuid.New(), s.contest, "suspects", []string{"1", "Margot", ""})
	if err != nil {
		t.Fatalf("the first row: %v", err)
	}
	return s
}

func (s *suspectsOnDisk) games(rows contests.ContentRows, uow transaction) *provisioning.Games {
	stores := s.store.stores()
	stores.TableData = s.repo
	return provisioning.NewGames(stores, s.cluster, authoring{editable: true}, rows, uow).
		WithTableData(s.files, tableDataLimits)
}

func (s *suspectsOnDisk) add(t *testing.T, service *provisioning.Games, name string) error {
	t.Helper()
	_, err := service.AppendTableRow(t.Context(), uuid.New(), s.contest, "suspects", []string{"2", name, ""})
	return err
}

// assertEveryReaderSees checks the organiser's window and the build's load
// against the rows given as CSV lines, in order. The game must be waiting to
// be built.
func assertEveryReaderSees(t *testing.T, service *provisioning.Games, cluster *buildCluster, contest uuid.UUID, rows ...string) {
	t.Helper()
	assertWindowShows(t, service, contest, rows...)

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil || built.Status != provisioning.TemplateReady {
		t.Fatalf("Build() = %q (%s), %v", built.Status, built.BuildError, err)
	}
	want := strings.Join(rows, "\n") + "\n"
	cluster.mu.Lock()
	loaded := cluster.tableData[built.Database+".suspects"]
	cluster.mu.Unlock()
	if loaded != want {
		t.Errorf("the build loaded %q, want %q", loaded, want)
	}
}

func assertWindowShows(t *testing.T, service *provisioning.Games, contest uuid.UUID, rows ...string) {
	t.Helper()
	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 100, 1<<20)
	if err != nil {
		t.Fatalf("TableDataWindow() = %v", err)
	}
	var seen []string
	for _, row := range window.Rows {
		seen = append(seen, strings.Join(row.Fields, ","))
	}
	if !slices.Equal(seen, rows) || window.TotalRows != int64(len(rows)) {
		t.Errorf("the window shows %q of %d row(s), want %q", seen, window.TotalRows, rows)
	}
}

// assertNoTail checks the file is exactly as long as its record.
func assertNoTail(t *testing.T, files *gamefile.Store, data provisioning.TableData) {
	t.Helper()
	if onDisk, err := gamefiletest.StoredBytes(files, data.ID.String()); err != nil || onDisk != data.ReceivedBytes {
		t.Errorf("the file holds %d bytes (%v), its record counts %d", onDisk, err, data.ReceivedBytes)
	}
}

// A row whose transaction did not commit stays in the file as a tail the
// record does not count. No reader may show it.
func TestARowWhoseTransactionRolledBackIsReadByNothing(t *testing.T) {
	t.Parallel()
	s := newSuspectsOnDisk(t)

	err := s.add(t, s.games(noContest{}, commitFailed{undoing{s.store}}), "Sparrow")

	if !errors.Is(err, errCommit) {
		t.Fatalf("AppendTableRow() = %v, want the commit's failure", err)
	}
	assertEveryReaderSees(t, s.games(noContest{}, undoing{s.store}), s.cluster, s.contest, "1,Margot,")
}

// The next row is written where the record ends, over whatever an uncommitted
// row left there.
func TestTheNextRowIsWrittenOverAnUncommittedTail(t *testing.T) {
	t.Parallel()
	s := newSuspectsOnDisk(t)
	if err := s.add(t, s.games(noContest{}, commitFailed{undoing{s.store}}), "A much longer name than the next"); !errors.Is(err, errCommit) {
		t.Fatalf("the uncommitted row = %v, want the commit's failure", err)
	}
	service := s.games(noContest{}, undoing{s.store})

	if err := s.add(t, service, "Sparrow"); err != nil {
		t.Fatalf("AppendTableRow() = %v", err)
	}

	current, err := s.store.tableRows().Ready(t.Context(), s.contest, "suspects")
	if err != nil {
		t.Fatalf("Ready() = %v", err)
	}
	if current.Lines != 2 || current.ReceivedBytes != s.first.ReceivedBytes+int64(len("2,Sparrow,\n")) {
		t.Errorf("the record counts %d row(s) in %d bytes, want 2 in %d",
			current.Lines, current.ReceivedBytes, s.first.ReceivedBytes+int64(len("2,Sparrow,\n")))
	}
	assertNoTail(t, s.files, current)
	assertEveryReaderSees(t, service, s.cluster, s.contest, "1,Margot,", "2,Sparrow,")
}

// Forms submitted at once all read the same end of the table. One row is
// recorded; every other form is told the table changed and writes nothing.
// The later forms carry other bytes, so a write that came before the refusal
// would show in the file.
func TestOfRowsSubmittedAtOnceOneIsRecordedAndTheRestAreToldTheTableChanged(t *testing.T) {
	t.Parallel()
	for _, forms := range []int{2, 3} {
		t.Run(strconv.Itoa(forms), func(t *testing.T) {
			t.Parallel()
			s := newSuspectsOnDisk(t)
			service := s.games(noContest{}, undoing{s.store})
			s.repo.pinned = &s.first

			if err := s.add(t, service, "Sparrow"); err != nil {
				t.Fatalf("the first form = %v, want its row recorded", err)
			}
			for range forms - 1 {
				if err := s.add(t, service, "Someone else"); !errors.Is(err, provisioning.ErrTableDataChanged) {
					t.Fatalf("a later form = %v, want ErrTableDataChanged", err)
				}
			}

			s.repo.pinned = nil
			current, err := s.store.tableRows().Ready(t.Context(), s.contest, "suspects")
			if err != nil {
				t.Fatalf("Ready() = %v", err)
			}
			assertNoTail(t, s.files, current)
			assertEveryReaderSees(t, service, s.cluster, s.contest, "1,Margot,", "2,Sparrow,")
		})
	}
}

// Two forms submit at once and the first is not recorded: refused because the
// contest started, or rolled back. The second form's row is recorded, and
// nothing of the first one's is read.
func TestARowSubmittedBesideOneThatWasNotRecordedIsRecordedOnce(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		rows    func() contests.ContentRows
		first   func(*templateStore) transaction
		wantErr error
	}{
		"refused": {
			rows:    func() contests.ContentRows { return &refusingFirst{} },
			first:   func(store *templateStore) transaction { return undoing{store} },
			wantErr: provisioning.ErrGameNotEditable,
		},
		"rolled back": {
			rows:    func() contests.ContentRows { return noContest{} },
			first:   func(store *templateStore) transaction { return commitFailed{undoing{store}} },
			wantErr: errCommit,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newSuspectsOnDisk(t)
			rows := tt.rows()
			s.repo.pinned = &s.first

			if err := s.add(t, s.games(rows, tt.first(s.store)), "Never recorded"); !errors.Is(err, tt.wantErr) {
				t.Fatalf("the first form = %v, want %v", err, tt.wantErr)
			}
			service := s.games(rows, undoing{s.store})
			if err := s.add(t, service, "Sparrow"); err != nil {
				t.Fatalf("the second form = %v, want its row recorded", err)
			}

			s.repo.pinned = nil
			current, err := s.store.tableRows().Ready(t.Context(), s.contest, "suspects")
			if err != nil {
				t.Fatalf("Ready() = %v", err)
			}
			if current.Lines != 2 {
				t.Errorf("the record counts %d row(s), want 2", current.Lines)
			}
			assertNoTail(t, s.files, current)
			assertEveryReaderSees(t, service, s.cluster, s.contest, "1,Margot,", "2,Sparrow,")
		})
	}
}

// suspectsOnPostgres is a suspects table holding Margot in a draft contest,
// over the real repository, contest rows and file store (CLAUDE.md rule 10).
type suspectsOnPostgres struct {
	stores  provisioning.GameStores
	cluster *buildCluster
	files   *gamefile.Store
	contest uuid.UUID
	first   provisioning.TableData
}

func newSuspectsOnPostgres(t *testing.T) *suspectsOnPostgres {
	t.Helper()
	contest, _ := contestFor(t, t.Context(), 0)
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	s := &suspectsOnPostgres{stores: onPostgres(ownBuilds(contest.ID)), cluster: &buildCluster{}, files: files, contest: contest.ID}
	service := s.games(storage.NewUnitOfWork(testPool))
	withSuspects(t, service, s.contest)
	s.first, err = service.AppendTableRow(t.Context(), uuid.New(), s.contest, "suspects", []string{"1", "Margot", ""})
	if err != nil {
		t.Fatalf("the first row: %v", err)
	}
	return s
}

func (s *suspectsOnPostgres) games(uow transaction) *provisioning.Games {
	return provisioning.NewGames(s.stores, s.cluster, authoring{editable: true}, postgres.NewContests(testPool), uow).
		WithTableData(s.files, tableDataLimits)
}

func (s *suspectsOnPostgres) record(t *testing.T) provisioning.TableData {
	t.Helper()
	current, err := s.stores.TableData.Ready(t.Context(), s.contest, "suspects")
	if err != nil {
		t.Fatalf("Ready() = %v", err)
	}
	return current
}

// A row decided before the contest started and refused inside its
// transaction is in nothing a reader sees, the build included.
func TestARowRefusedBecauseTheContestStartedIsReadByNothing(t *testing.T) {
	s := newSuspectsOnPostgres(t)
	service := s.games(storage.NewUnitOfWork(testPool))
	if _, err := testPool.Exec(t.Context(), `UPDATE contests SET status = 'running' WHERE id = $1`, s.contest); err != nil {
		t.Fatalf("start the contest = %v", err)
	}

	_, err := service.AppendTableRow(t.Context(), uuid.New(), s.contest, "suspects", []string{"2", "Sparrow", ""})

	if !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Fatalf("AppendTableRow() = %v, want ErrGameNotEditable", err)
	}
	if current := s.record(t); current.ReceivedBytes != s.first.ReceivedBytes || current.Lines != 1 {
		t.Errorf("the record counts %d row(s) in %d bytes, want 1 in %d", current.Lines, current.ReceivedBytes, s.first.ReceivedBytes)
	}
	assertEveryReaderSees(t, service, s.cluster, s.contest, "1,Margot,")
}

// A commit that fails says nothing of whether it landed. Landed, the row is
// counted and read; not landed, nothing reads it and the next row is written
// over it.
func TestARowWhoseCommitFailedIsEitherCountedOrDiscarded(t *testing.T) {
	rowBytes := int64(len("2,Sparrow,\n"))

	t.Run("the commit landed", func(t *testing.T) {
		s := newSuspectsOnPostgres(t)
		real := storage.NewUnitOfWork(testPool)

		_, err := s.games(commitLost{real}).AppendTableRow(t.Context(), uuid.New(), s.contest, "suspects", []string{"2", "Sparrow", ""})

		if !errors.Is(err, errCommit) {
			t.Fatalf("AppendTableRow() = %v, want the commit's failure", err)
		}
		current := s.record(t)
		if current.Lines != 2 || current.ReceivedBytes != s.first.ReceivedBytes+rowBytes {
			t.Errorf("the record counts %d row(s) in %d bytes, want 2 in %d", current.Lines, current.ReceivedBytes, s.first.ReceivedBytes+rowBytes)
		}
		assertNoTail(t, s.files, current)
		assertEveryReaderSees(t, s.games(real), s.cluster, s.contest, "1,Margot,", "2,Sparrow,")
	})

	t.Run("the commit did not land", func(t *testing.T) {
		s := newSuspectsOnPostgres(t)
		real := storage.NewUnitOfWork(testPool)

		_, err := s.games(commitFailed{real}).AppendTableRow(t.Context(), uuid.New(), s.contest, "suspects", []string{"2", "Sparrow", ""})

		if !errors.Is(err, errCommit) {
			t.Fatalf("AppendTableRow() = %v, want the commit's failure", err)
		}
		if current := s.record(t); current.Lines != 1 || current.ReceivedBytes != s.first.ReceivedBytes {
			t.Errorf("the record counts %d row(s) in %d bytes, want 1 in %d", current.Lines, current.ReceivedBytes, s.first.ReceivedBytes)
		}
		if onDisk, _ := gamefiletest.StoredBytes(s.files, s.first.ID.String()); onDisk != s.first.ReceivedBytes+rowBytes {
			t.Fatalf("the file holds %d bytes, want the uncommitted row as its tail (%d)", onDisk, s.first.ReceivedBytes+rowBytes)
		}
		service := s.games(real)
		assertEveryReaderSees(t, service, s.cluster, s.contest, "1,Margot,")

		if _, err := service.AppendTableRow(t.Context(), uuid.New(), s.contest, "suspects", []string{"2", "Later", ""}); err != nil {
			t.Fatalf("the next row = %v", err)
		}
		current := s.record(t)
		if current.Lines != 2 {
			t.Errorf("the record counts %d row(s) after the next row, want 2", current.Lines)
		}
		assertNoTail(t, s.files, current)
		assertWindowShows(t, service, s.contest, "1,Margot,", "2,Later,")
	})
}

// completingReader is a chunk body that completes its own upload before it
// yields a byte: a chunk admitted while the upload was 'receiving' whose body
// arrives after the upload completed.
type completingReader struct {
	complete func()
	body     io.Reader
	once     sync.Once
}

func (c *completingReader) Read(p []byte) (int, error) {
	c.once.Do(c.complete)
	return c.body.Read(p)
}

// A chunk that lands after its upload completed is refused, and nothing it
// carried is recorded or read, however valid it looks.
func TestAChunkThatLandsAfterItsUploadCompletedIsReadByNothing(t *testing.T) {
	contest, _ := contestFor(t, t.Context(), 0)
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	cluster := &buildCluster{}
	service := provisioning.NewGames(onPostgres(ownBuilds(contest.ID)), cluster, authoring{editable: true},
		postgres.NewContests(testPool), storage.NewUnitOfWork(testPool)).WithTableData(files, tableDataLimits)
	withSuspects(t, service, contest.ID)
	const content = "id,name,nickname\n1,Margot,\n"
	upload := beginTableUploadWithContent(t, service, contest.ID, "suspects", content)
	late := &completingReader{
		body: strings.NewReader("2,Unvalidated,\n"),
		complete: func() {
			if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest.ID, upload.ID); err != nil {
				t.Errorf("CompleteTableUpload() = %v", err)
			}
		},
	}

	_, err = service.TableUploads().Append(t.Context(), uuid.New(), contest.ID, upload.ID, int64(len(content)), late)

	if !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
		t.Fatalf("Append() = %v, want ErrUploadAlreadyComplete", err)
	}
	current, err := postgres.NewGameTableData(testPool).Ready(t.Context(), contest.ID, "suspects")
	if err != nil {
		t.Fatalf("Ready() = %v", err)
	}
	if current.ReceivedBytes != int64(len(content)) || current.Lines != 1 {
		t.Errorf("the record counts %d row(s) in %d bytes, want 1 in %d", current.Lines, current.ReceivedBytes, len(content))
	}
	assertEveryReaderSees(t, service, cluster, contest.ID, "1,Margot,")
}

// heldRecord is the rows whose first AppendRow, once it has run and so holds
// the table's row, closes recorded and waits for release.
type heldRecord struct {
	provisioning.TableDataRows
	records  atomic.Int32
	recorded chan struct{}
	release  chan struct{}
}

func (h *heldRecord) AppendRow(ctx context.Context, id uuid.UUID, at, rowBytes int64) (provisioning.TableData, error) {
	data, err := h.TableDataRows.AppendRow(ctx, id, at, rowBytes)
	if h.records.Add(1) == 1 {
		close(h.recorded)
		<-h.release
	}
	return data, err
}

// waitForABlockedTableDataWrite returns once a session of this database is
// waiting on a lock to update a table-data row.
func waitForABlockedTableDataWrite(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := testPool.QueryRow(t.Context(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%UPDATE game_table_data%'`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no session came to wait on the table's row")
}

// Without the contest's lock, the table's row is what two forms race for. The
// second waits on it, finds the length it read no longer the record, and
// writes nothing.
func TestARowRecordedWhileAnotherFormWaitsOnTheTableIsNotOverwritten(t *testing.T) {
	s := newSuspectsOnPostgres(t)
	held := &heldRecord{TableDataRows: s.stores.TableData, recorded: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(held.release) })
	t.Cleanup(release) // a failed wait must not leave the first form parked in its transaction
	stores := s.stores
	stores.TableData = held
	service := provisioning.NewGames(stores, s.cluster, authoring{editable: true}, noContest{}, storage.NewUnitOfWork(testPool)).
		WithTableData(s.files, tableDataLimits)

	winner := make(chan error, 1)
	go func() {
		_, err := service.AppendTableRow(context.Background(), uuid.New(), s.contest, "suspects", []string{"2", "Sparrow", ""})
		winner <- err
	}()
	<-held.recorded
	loser := make(chan error, 1)
	go func() {
		_, err := service.AppendTableRow(context.Background(), uuid.New(), s.contest, "suspects", []string{"2", "Someone else", ""})
		loser <- err
	}()
	waitForABlockedTableDataWrite(t)
	release()

	if err := <-winner; err != nil {
		t.Fatalf("the first form = %v, want its row recorded", err)
	}
	if err := <-loser; !errors.Is(err, provisioning.ErrTableDataChanged) {
		t.Fatalf("the second form = %v, want ErrTableDataChanged", err)
	}
	assertNoTail(t, s.files, s.record(t))
	assertEveryReaderSees(t, service, s.cluster, s.contest, "1,Margot,", "2,Sparrow,")
}

// The same guard when the chunk's progress and the completion run at once:
// the progress write waits on the row the completion holds, and then finds
// the upload no longer receiving.
func TestAChunksProgressThatWaitedOnTheCompletionLeavesTheRecordAlone(t *testing.T) {
	contest, _ := contestFor(t, t.Context(), 0)
	repo := postgres.NewGameTableData(testPool)
	id := uuid.New()
	if _, err := repo.Begin(t.Context(), id, contest.ID, "suspects", 40); err != nil {
		t.Fatalf("Begin() = %v", err)
	}
	if err := repo.UpdateReceived(t.Context(), id, 40); err != nil {
		t.Fatalf("UpdateReceived() = %v", err)
	}
	completed, commit := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(commit) })
	t.Cleanup(release)
	completion := make(chan error, 1)
	go func() {
		completion <- storage.NewUnitOfWork(testPool).Do(context.Background(), func(ctx context.Context) error {
			_, err := repo.Complete(ctx, contest.ID, "suspects", id, 40, 2, nil)
			close(completed)
			<-commit
			return err
		})
	}()
	<-completed

	late := make(chan error, 1)
	go func() { late <- repo.UpdateReceived(context.Background(), id, 55) }()
	waitForABlockedTableDataWrite(t)
	release()

	if err := <-completion; err != nil {
		t.Fatalf("the completion = %v", err)
	}
	if err := <-late; !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
		t.Fatalf("the chunk's progress = %v, want ErrUploadAlreadyComplete", err)
	}
	if current, err := repo.ByID(t.Context(), id); err != nil || current.ReceivedBytes != 40 {
		t.Fatalf("the record counts %d bytes (%v), want the 40 the upload completed with", current.ReceivedBytes, err)
	}
}

// stalledChunk is a chunk body that yields nothing until released.
type stalledChunk struct {
	asked   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *stalledChunk) Read([]byte) (int, error) {
	s.once.Do(func() { close(s.asked) })
	<-s.release
	return 0, io.EOF
}

// stallAChunk begins an upload for table in contest and has actor send it a
// chunk whose body never arrives until the test ends.
func stallAChunk(t *testing.T, service *provisioning.Games, actor, contest uuid.UUID, table string) {
	t.Helper()
	upload, err := service.TableUploads().Begin(t.Context(), contest, table, 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stalled := &stalledChunk{asked: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = service.TableUploads().Append(context.Background(), actor, contest, upload.ID, 0, stalled)
	}()
	t.Cleanup(func() {
		close(stalled.release)
		<-done
	})
	<-stalled.asked
}

// tableUploadsOf is the service over a table store holding chunks buffers,
// with a two-table game in each of contests.
func tableUploadsOf(t *testing.T, chunks int, contests ...uuid.UUID) *provisioning.Games {
	t.Helper()
	limits := tableDataLimits
	limits.BufferedChunks = chunks
	files, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	service, _, _ := games(true)
	service.WithTableData(files, limits)
	for _, contest := range contests {
		withTwoTables(t, service, contest)
	}
	return service
}

// The table store holds a bounded number of chunks in memory, and one more is
// told the store is busy rather than made to wait.
func TestAChunkBeyondWhatTheTableStoreHoldsInMemoryIsToldItIsBusy(t *testing.T) {
	t.Parallel()
	slow, other := uuid.New(), uuid.New()
	service := tableUploadsOf(t, 1, slow, other)
	stallAChunk(t, service, uuid.New(), slow, "suspects")
	upload, err := service.TableUploads().Begin(t.Context(), other, "suspects", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = service.TableUploads().Append(t.Context(), uuid.New(), other, upload.ID, 0, strings.NewReader("x"))

	if !errors.Is(err, provisioning.ErrUploadBusy) {
		t.Fatalf("Append() = %v, want ErrUploadBusy", err)
	}
}

// unreadableRows is the rows whose ByID fails, as in an outage.
type unreadableRows struct {
	provisioning.TableDataRows
}

func (unreadableRows) ByID(context.Context, uuid.UUID) (provisioning.TableData, error) {
	return provisioning.TableData{}, errors.New("connection refused")
}

// A missing file is judged by reading the table's row again. When that read
// fails, the answer is the failure: neither damage nor a missing upload.
func TestAMissingFileWhoseRowCannotBeReadAgainIsAnOutage(t *testing.T) {
	t.Parallel()
	store := &templateStore{policy: sqlpolicy.ReadOnly()}
	dir := t.TempDir()
	files, err := gamefile.NewStore(dir, tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	games := func(rows provisioning.TableDataRows) *provisioning.Games {
		stores := store.stores()
		stores.TableData = rows
		return provisioning.NewGames(stores, &buildCluster{}, authoring{editable: true}, noContest{}, directly{}).
			WithTableData(files, tableDataLimits)
	}
	contest := uuid.New()
	withSuspects(t, games(store.tableRows()), contest)
	first, err := games(store.tableRows()).AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""})
	if err != nil {
		t.Fatalf("the first row: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, first.ID.String()+".data")); err != nil {
		t.Fatalf("remove the file: %v", err)
	}

	_, err = games(unreadableRows{store.tableRows()}).TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)

	if err == nil || errors.Is(err, provisioning.ErrTableDataDamaged) || errors.Is(err, provisioning.ErrUploadNotFound) {
		t.Fatalf("TableDataWindow() = %v, want the read's own failure", err)
	}
}

// One contest takes one of the installation's buffers at a time, however many
// of its tables are uploading, so one organiser cannot hold them all.
func TestAContestHasOneChunkInMemoryAtATime(t *testing.T) {
	t.Parallel()
	slow, other := uuid.New(), uuid.New()
	service := tableUploadsOf(t, 4, slow, other)
	stallAChunk(t, service, uuid.New(), slow, "suspects")
	sameContest, err := service.TableUploads().Begin(t.Context(), slow, "sightings", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	otherContest, err := service.TableUploads().Begin(t.Context(), other, "suspects", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), slow, sameContest.ID, 0, strings.NewReader("x")); !errors.Is(err, provisioning.ErrUploadBusy) {
		t.Errorf("a second chunk of the same contest = %v, want ErrUploadBusy", err)
	}
	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), other, otherContest.ID, 0, strings.NewReader("id,name,nickname\n")); err != nil {
		t.Errorf("another contest's chunk = %v, want it written", err)
	}
}

// One account takes one buffer at a time too, however many contests it
// edits, and another account is not held up by it.
func TestAnAccountHasOneChunkInMemoryAtATime(t *testing.T) {
	t.Parallel()
	slow, other := uuid.New(), uuid.New()
	service := tableUploadsOf(t, 4, slow, other)
	organiser := uuid.New()
	stallAChunk(t, service, organiser, slow, "suspects")
	upload, err := service.TableUploads().Begin(t.Context(), other, "suspects", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	if _, err := service.TableUploads().Append(t.Context(), organiser, other, upload.ID, 0, strings.NewReader("x")); !errors.Is(err, provisioning.ErrUploadBusy) {
		t.Errorf("the same account's chunk for another contest = %v, want ErrUploadBusy", err)
	}
	if _, err := service.TableUploads().Append(t.Context(), uuid.New(), other, upload.ID, 0, strings.NewReader("id,name,nickname\n")); err != nil {
		t.Errorf("another account's chunk = %v, want it written", err)
	}
}

// A chunk is charged to the contest's owner as well as to its sender, so the
// contests one organiser owns share one buffer whoever uploads to them: the
// managers an owner appoints do not multiply the owner's share.
func TestTheContestsOfOneOwnerHaveOneChunkInMemoryAtATime(t *testing.T) {
	t.Parallel()
	limits := tableDataLimits
	files, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	owner := uuid.New()
	service := provisioning.NewGames((&templateStore{policy: sqlpolicy.ReadOnly(), ownerOfAll: owner}).stores(), &buildCluster{},
		authoring{editable: true}, noContest{}, directly{}).WithTableData(files, limits)
	first, second := uuid.New(), uuid.New()
	withTwoTables(t, service, first)
	withTwoTables(t, service, second)
	stallAChunk(t, service, uuid.New(), first, "suspects") // a manager of the first contest
	upload, err := service.TableUploads().Begin(t.Context(), second, "suspects", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	for who, actor := range map[string]uuid.UUID{"another manager": uuid.New(), "the owner": owner} {
		if _, err := service.TableUploads().Append(t.Context(), actor, second, upload.ID, 0, strings.NewReader("x")); !errors.Is(err, provisioning.ErrUploadBusy) {
			t.Errorf("%s's chunk for the owner's other contest = %v, want ErrUploadBusy", who, err)
		}
	}
}

// ownerUnreadable is the rows whose read of an upload and its contest's owner
// fails, as in an outage.
type ownerUnreadable struct {
	provisioning.TableDataRows
}

func (ownerUnreadable) WithOwner(context.Context, uuid.UUID) (provisioning.TableData, uuid.UUID, error) {
	return provisioning.TableData{}, uuid.Nil, errors.New("connection refused")
}

// neverRead is a chunk body that must not be asked for a byte.
type neverRead struct{ t *testing.T }

func (n neverRead) Read([]byte) (int, error) {
	n.t.Error("the chunk's body was read")
	return 0, io.EOF
}

// Without the upload's row and its owner there is nobody to charge the chunk
// to, so it is refused as the failure it is, before its body is read.
func TestAChunkWhoseOwnerCannotBeReadIsRefusedUnread(t *testing.T) {
	t.Parallel()
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	stores := (&templateStore{policy: sqlpolicy.ReadOnly()}).stores()
	stores.TableData = ownerUnreadable{stores.TableData}
	service := provisioning.NewGames(stores, &buildCluster{},
		authoring{editable: true}, noContest{}, directly{}).WithTableData(files, tableDataLimits)

	_, err = service.TableUploads().Append(t.Context(), uuid.New(), uuid.New(), uuid.New(), 0, neverRead{t})

	if err == nil || errors.Is(err, provisioning.ErrUploadNotFound) || errors.Is(err, provisioning.ErrUploadBusy) {
		t.Fatalf("Append() = %v, want the read's own failure", err)
	}
}

// An upload is addressed through its contest's route, which is what the
// caller's permission was checked on. Naming another contest's upload reads
// as a missing one, so its existence does not leak, and its body is not read.
func TestAChunkForAnotherContestsUploadIsNotFoundAndUnread(t *testing.T) {
	t.Parallel()
	mine, theirs := uuid.New(), uuid.New()
	service := tableUploadsOf(t, 4, mine, theirs)
	upload, err := service.TableUploads().Begin(t.Context(), theirs, "suspects", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = service.TableUploads().Append(t.Context(), uuid.New(), mine, upload.ID, 0, neverRead{t})

	if !errors.Is(err, provisioning.ErrUploadNotFound) {
		t.Fatalf("Append() = %v, want ErrUploadNotFound", err)
	}
}

// panicking is a chunk body whose Read panics.
type panicking struct{}

func (panicking) Read([]byte) (int, error) { panic("the body reader failed") }

// A chunk that ends in a panic, which the HTTP server recovers from, must
// not leave its contest and its account refused for the life of the process.
func TestAChunkThatPanicsLeavesItsContestAndAccountFree(t *testing.T) {
	t.Parallel()
	contest, organiser := uuid.New(), uuid.New()
	service := tableUploadsOf(t, 4, contest)
	upload, err := service.TableUploads().Begin(t.Context(), contest, "suspects", 64)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the chunk did not panic")
			}
		}()
		_, _ = service.TableUploads().Append(t.Context(), organiser, contest, upload.ID, 0, panicking{})
	}()

	if _, err := service.TableUploads().Append(t.Context(), organiser, contest, upload.ID, 0, strings.NewReader("id,name,nickname\n")); err != nil {
		t.Fatalf("the next chunk of the same contest and account = %v, want it written", err)
	}
}

// A table's file is written from inside a transaction, so its store must be
// one that buffers chunks; a streaming store is a wiring mistake.
func TestTableDataRefusesAStoreThatStreamsItsChunks(t *testing.T) {
	t.Parallel()
	limits := tableDataLimits
	limits.BufferedChunks = 0
	files, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	service, _, _ := games(true)
	defer func() {
		if recover() == nil {
			t.Fatal("WithTableData accepted a store that streams its chunks")
		}
	}()

	service.WithTableData(files, limits)
}

// A file that lost bytes its record counts is told apart from every other
// failure, on each path that reads or extends it, and uploading the table's
// data again puts the table right.
func TestATableWhoseFileLostRecordedBytesIsDamagedUntilItIsUploadedAgain(t *testing.T) {
	t.Parallel()
	service, _, _, _, dir := tableDataGamesOnDisk(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)
	first, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""})
	if err != nil {
		t.Fatalf("the first row: %v", err)
	}
	if err := os.Truncate(filepath.Join(dir, first.ID.String()+".data"), first.ReceivedBytes-3); err != nil {
		t.Fatalf("cut the file short: %v", err)
	}

	if _, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20); !errors.Is(err, provisioning.ErrTableDataDamaged) {
		t.Errorf("TableDataWindow() = %v, want ErrTableDataDamaged", err)
	}
	_, err = service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"2", "Sparrow", ""})
	if !errors.Is(err, provisioning.ErrTableDataDamaged) {
		t.Fatalf("AppendTableRow() = %v, want ErrTableDataDamaged", err)
	}
	if !strings.Contains(err.Error(), first.ID.String()) {
		t.Errorf("the error %q does not name the file, so the log cannot either", err)
	}

	// The build tells the organiser what to do and hands the cause to the log.
	built, err := service.Build(t.Context(), time.Minute)
	if !errors.Is(err, provisioning.ErrTableDataDamaged) || built.Status != provisioning.TemplateFailed ||
		built.BuildError != provisioning.BuildTableDataDamaged {
		t.Errorf("Build() = %q (%q), %v, want it failed telling the organiser to upload the data again", built.Status, built.BuildError, err)
	}

	// The file gone altogether is the same damage, not a missing upload.
	if err := os.Remove(filepath.Join(dir, first.ID.String()+".data")); err != nil {
		t.Fatalf("remove the file: %v", err)
	}
	if _, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20); !errors.Is(err, provisioning.ErrTableDataDamaged) {
		t.Errorf("TableDataWindow() with no file = %v, want ErrTableDataDamaged", err)
	}

	upload := beginTableUploadWithContent(t, service, contest, "suspects", "id,name,nickname\n9,Uploaded,\n")
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, upload.ID); err != nil {
		t.Fatalf("upload the table's data again: %v", err)
	}
	assertWindowShows(t, service, contest, "9,Uploaded,")
}

// A table's first row whose commit failed may have been recorded, so its file
// stays: removing it would leave a record with nothing behind it.
func TestAFirstRowWhoseCommitFailedKeepsItsFile(t *testing.T) {
	t.Parallel()
	store := &templateStore{policy: sqlpolicy.ReadOnly()}
	files, err := gamefile.NewStore(t.TempDir(), tableDataLimits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	games := func(uow transaction) *provisioning.Games {
		return provisioning.NewGames(store.stores(), &buildCluster{}, authoring{editable: true}, noContest{}, uow).
			WithTableData(files, tableDataLimits)
	}
	contest := uuid.New()
	withSuspects(t, games(directly{}), contest)

	_, err = games(commitLost{directly{}}).AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Margot", ""})

	if !errors.Is(err, errCommit) {
		t.Fatalf("AppendTableRow() = %v, want the commit's failure", err)
	}
	assertWindowShows(t, games(directly{}), contest, "1,Margot,")
}

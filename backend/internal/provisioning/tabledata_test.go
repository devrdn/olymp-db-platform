package provisioning_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// These tests run against the fake templateStore and buildCluster (see
// games() in template_test.go) and a real gamefile.Store on a temp
// directory — no CORE_DB_DSN, exactly the brief's own "разбор и границы —
// без базы". The assembly-with-real-data path that needs a real cluster is
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
func tableDataGames(t *testing.T, editable bool) (*provisioning.Games, *templateStore, *buildCluster, *gamefile.Store) {
	t.Helper()
	service, store, cluster := games(editable)
	limits := gamefile.Limits{MaxFileBytes: 8 << 20, MaxDirBytes: 32 << 20, MaxChunkBytes: 4 << 20}
	files, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("opening the table data store: %v", err)
	}
	service.WithTableData(files, limits)
	return service, store, cluster, files
}

// withSuspects saves a builder definition holding only suspectsTable, so a
// call to BeginTableUpload or AppendTableRow finds a table to check its
// upload against.
func withSuspects(t *testing.T, service *provisioning.Games, contest uuid.UUID) {
	t.Helper()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest,
		provisioning.Definition{Tables: []provisioning.TableDefinition{suspectsTable()}}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
}

// beginTableUploadWithContent begins a table upload sized exactly to
// content and appends all of it in one chunk — beginWithContent's own shape
// (upload_test.go) for a table's file instead of a whole dump.
func beginTableUploadWithContent(t *testing.T, service *provisioning.Games, contest uuid.UUID, table, content string) provisioning.TableData {
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

// TestCompleteTableUploadRefusesAMismatchedHeaderBeforeValidatingAnyRow is
// the brief's own requirement for the full, end-of-upload pass: a header
// that does not name the table's own columns is refused, and refused for
// that reason alone — the row after it, itself invalid for an unrelated
// reason (a non-numeric id), is never even reached.
//
// The content is written straight to the store rather than through
// service.AppendTableChunk (beginTableUploadWithContent's own helper), the
// same way TestCompleteTableUploadRefusesTheDeclaredLengthNotMatchingWhatArrived
// does: AppendTableChunk now catches most mismatched headers itself, on the
// very first chunk (TestAppendTableChunkRefusesAMismatchedHeaderOnTheFirstChunk
// below) — this test is about the fallback for a file that reached the
// store some other way, so it must not go through the same early check it
// is not testing.
func TestCompleteTableUploadRefusesAMismatchedHeaderBeforeValidatingAnyRow(t *testing.T) {
	t.Parallel()
	service, store, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,extra\nnot-a-number,A,x\n" // wrong header AND a row that would also fail
	data, err := service.BeginTableUpload(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := files.Append(data.ID.String(), 0, strings.NewReader(content)); err != nil {
		t.Fatalf("append directly: %v", err)
	}
	// Written straight to the store rather than through AppendTableChunk, so
	// the fake repository's own bookkeeping of how many bytes have arrived
	// is brought up to date by hand — otherwise CompleteTableUpload would
	// stop at its length check, never reaching the header check this test
	// is actually about.
	store.mu.Lock()
	seeded := store.tableData[data.ID]
	seeded.ReceivedBytes = int64(len(content))
	store.tableData[data.ID] = seeded
	store.mu.Unlock()

	_, err = service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableHeaderMismatch) {
		t.Fatalf("error = %v, want ErrTableHeaderMismatch", err)
	}
	if errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatal("the row's own mistake was reported — the header should have stopped this first")
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

// TestCompleteTableUploadNamesTheRowWhoseFieldCountDoesNotMatch is the
// brief's other named requirement: a row with the wrong field count is
// refused with its own row number, not silently skipped.
func TestCompleteTableUploadNamesTheRowWhoseFieldCountDoesNotMatch(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot,\n2,Duplicate\n" // row 2 has only 2 fields
	data := beginTableUploadWithContent(t, service, contest, "suspects", content)

	_, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableRowFieldCount) {
		t.Fatalf("error = %v, want ErrTableRowFieldCount", err)
	}
	if !strings.Contains(err.Error(), "row 2") {
		t.Fatalf("error = %q, does not name row 2", err)
	}
}

// TestCompleteTableUploadNamesTheRowAndColumnOfAValueThatDoesNotParse checks
// the second named refusal: a value that does not fit its column's type
// names the row and the column, not just "invalid".
func TestCompleteTableUploadNamesTheRowAndColumnOfAValueThatDoesNotParse(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot,\nnot-a-number,Someone,\n"
	data := beginTableUploadWithContent(t, service, contest, "suspects", content)

	_, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("error = %v, want ErrTableValueInvalid", err)
	}
	if !strings.Contains(err.Error(), "row 2") || !strings.Contains(err.Error(), `"id"`) {
		t.Fatalf("error = %q, does not name row 2's id column", err)
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

// A row that does not match the table's own columns is refused before
// anything is written — a value with the wrong number of fields, or one
// that will not parse as its column's type, must not reach the file at all.
func TestAppendTableRowRefusesAValueThatDoesNotMatchItsColumn(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"not-a-number", "A", ""}); !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("error = %v, want ErrTableValueInvalid", err)
	}
	ids, err := files.UploadIDs(time.Time{})
	if err != nil {
		t.Fatalf("list the table data volume: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a refused row still left %d file(s) on disk", len(ids))
	}
}

// TestAppendTableRowValidatesNumericAsDecimalSyntaxNotAsAFloat is the other
// defect this package's numeric column check had: a PostgreSQL numeric is an
// exact decimal of arbitrary precision, and strconv.ParseFloat is not a
// stand-in for it in either direction.
//
//   - "0x1p-2" is a hexadecimal float literal ParseFloat happily parses to
//     0.25 — and numeric_in has never accepted one (checked against a live
//     PostgreSQL 16 instance, this platform's own target). A value that
//     clears this check and only fails inside PostgreSQL's own COPY,
//     minutes into a build, is exactly the failure this whole pre-check
//     exists to prevent.
//   - "1e400" is a value numeric holds exactly, arbitrary precision being
//     the type's entire point, but ParseFloat reports a range error for it
//     because it does not fit a 64-bit float — refusing an organiser's
//     perfectly good row for a limit that belongs to Go's float type, not
//     to the column's own.
//   - "NaN", numeric's own special value (and, since PostgreSQL 14,
//     signed Infinity/Inf, also checked against that live instance), must
//     keep working now that the check is decimal syntax rather than a
//     float parse.
func TestAppendTableRowValidatesNumericAsDecimalSyntaxNotAsAFloat(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{{
			Name: "prices",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger},
				{Name: "amount", Type: provisioning.ColumnNumeric},
			},
		}},
	}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"1", "0x1p-2"}); !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("hex float literal: error = %v, want ErrTableValueInvalid", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"2", "1e400"}); err != nil {
		t.Fatalf("a value numeric holds exactly was refused as if it had to fit a 64-bit float: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"3", "NaN"}); err != nil {
		t.Fatalf("NaN, numeric's own special value, was refused: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"4", "-Infinity"}); err != nil {
		t.Fatalf("-Infinity, numeric's own special value since PostgreSQL 14, was refused: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"5", "1_000.5"}); err != nil {
		t.Fatalf("an underscore digit separator, valid decimal syntax, was refused: %v", err)
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

// TestDeleteTableRowRefusesPastMaxTableDeletedRows is the boundary test the
// brief asks for on the bound this package invents (MaxTableDeletedRows).
// The file itself is never grown to that size — DeleteTableRow's own cost is
// independent of the file, which is the point being tested — only the
// bookkeeping the fake repository holds is, directly, the same way a real
// database's row would be past migration 27's own CHECK.
func TestDeleteTableRowRefusesPastMaxTableDeletedRows(t *testing.T) {
	t.Parallel()
	service, store, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "A", ""})
	if err != nil {
		t.Fatalf("row 1: %v", err)
	}

	already := make([]int64, provisioning.MaxTableDeletedRows)
	for i := range already {
		already[i] = int64(i + 100) // none of these collide with row 1 or row 2 below
	}
	store.mu.Lock()
	seeded := store.tableData[data.ID]
	seeded.Lines = int64(provisioning.MaxTableDeletedRows) + 100
	seeded.DeletedRows = already
	store.tableData[data.ID] = seeded
	store.mu.Unlock()

	if err := service.DeleteTableRow(t.Context(), uuid.New(), contest, "suspects", 2); !errors.Is(err, provisioning.ErrTooManyDeletedRows) {
		t.Fatalf("error = %v, want ErrTooManyDeletedRows", err)
	}
}

// TestCompleteTableUploadRefusesOneRowPastMaxTableDataRows is the boundary
// test for the row-count bound: MaxTableDataRows rows complete, one more is
// refused. A one-column table keeps the file small enough that both halves
// of this test run in well under a second.
func TestCompleteTableUploadRefusesOneRowPastMaxTableDataRows(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{{
			Name:    "codes",
			Columns: []provisioning.ColumnDefinition{{Name: "id", Type: provisioning.ColumnInteger}},
		}},
	}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}

	var atLimit strings.Builder
	atLimit.WriteString("id\n")
	for i := 0; i < provisioning.MaxTableDataRows; i++ {
		atLimit.WriteString("1\n")
	}
	data := beginTableUploadWithContent(t, service, contest, "codes", atLimit.String())
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("exactly MaxTableDataRows rows was refused: %v", err)
	}

	overLimit := atLimit.String() + "1\n"
	data2 := beginTableUploadWithContent(t, service, contest, "codes", overLimit)
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data2.ID); !errors.Is(err, provisioning.ErrTableTooManyRows) {
		t.Fatalf("error = %v, want ErrTableTooManyRows", err)
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

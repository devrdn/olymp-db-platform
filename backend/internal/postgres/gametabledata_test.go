package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

func TestBeginningTableDataCreatesARowInReceiving(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		data, err := repo.BeginTableData(ctx, id, contest, "suspects", 1024)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if data.ID != id || data.ContestID != contest || data.Table != "suspects" {
			t.Fatalf("data = %+v, want id %s contest %s table suspects", data, id, contest)
		}
		if data.Status != provisioning.TableDataReceiving {
			t.Fatalf("status = %q, want receiving", data.Status)
		}
		if data.ReceivedBytes != 0 || len(data.DeletedRows) != 0 {
			t.Fatalf("data = %+v, want zero received bytes and no deleted rows", data)
		}
	})
}

// The guarantee migration 27's own partial index gives, at the finer grain
// of one table: two uploads 'receiving' at once for the *same* table of the
// *same* contest are refused by the database, never by a check-then-insert
// in Go.
func TestASecondTableUploadForTheSameTableWhileOneIsReceivingIsRejectedByTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.BeginTableData(ctx, uuid.New(), contest, "suspects", 1024); err != nil {
			t.Fatalf("first begin: %v", err)
		}
		// A different table of the same contest is untouched — the index is
		// scoped to (contest_id, lower(table_name)), not to the contest alone.
		// Checked *before* the conflict below: a unique violation leaves the
		// whole test transaction unusable for anything that follows it
		// (PostgreSQL's own "current transaction is aborted" rule), so the
		// statement that is meant to succeed has to run first.
		if _, err := repo.BeginTableData(ctx, uuid.New(), contest, "sightings", 1024); err != nil {
			t.Fatalf("begin for a different table of the same contest: %v", err)
		}
		if _, err := repo.BeginTableData(ctx, uuid.New(), contest, "suspects", 2048); !errors.Is(err, provisioning.ErrTableDataInProgress) {
			t.Fatalf("second begin = %v, want ErrTableDataInProgress", err)
		}
	})
}

func TestCompletingTableDataMarksItCompleteAndDisplacesThePrevious(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		first := uuid.New()
		if _, err := repo.BeginTableData(ctx, first, contest, "suspects", 20); err != nil {
			t.Fatalf("begin first: %v", err)
		}
		if _, err := repo.CompleteTableData(ctx, contest, "suspects", first, 20, 2, nil); err != nil {
			t.Fatalf("complete first: %v", err)
		}

		second := uuid.New()
		if _, err := repo.BeginTableData(ctx, second, contest, "suspects", 30); err != nil {
			t.Fatalf("begin second: %v", err)
		}
		completed, err := repo.CompleteTableData(ctx, contest, "suspects", second, 30, 3, &first)
		if err != nil {
			t.Fatalf("complete second: %v", err)
		}
		if completed.Status != provisioning.TableDataComplete || completed.Lines != 3 {
			t.Fatalf("completed = %+v, want complete with 3 lines", completed)
		}

		previous, err := repo.TableDataByID(ctx, first)
		if err != nil {
			t.Fatalf("read the displaced row: %v", err)
		}
		if previous.Status != provisioning.TableDataAborted {
			t.Fatalf("the displaced row's status is %q, want aborted", previous.Status)
		}

		ready, err := repo.ReadyTableData(ctx, contest, "suspects")
		if err != nil {
			t.Fatalf("read the ready row: %v", err)
		}
		if ready.ID != second {
			t.Fatalf("ready table data = %s, want the second upload %s", ready.ID, second)
		}
	})
}

// CreateReadyTableData is the bootstrap path (AppendTableRow's own doc): a
// file that starts life already 'complete'. The one-ready-file-per-table
// index refuses a second bootstrap racing the first.
func TestCreateReadyTableDataBootstrapsAndTheCompleteIndexRefusesASecondOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		data, err := repo.CreateReadyTableData(ctx, id, contest, "suspects", 10, 1)
		if err != nil {
			t.Fatalf("create ready: %v", err)
		}
		if data.Status != provisioning.TableDataComplete || data.Lines != 1 {
			t.Fatalf("data = %+v, want complete with 1 line", data)
		}

		if _, err := repo.CreateReadyTableData(ctx, uuid.New(), contest, "suspects", 5, 1); err == nil {
			t.Fatal("a second bootstrap for a table that already has a ready file was not refused")
		}
	})
}

func TestAppendTableDataRowUpdatesBytesAndLinesTogether(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		if _, err := repo.CreateReadyTableData(ctx, id, contest, "suspects", 10, 1); err != nil {
			t.Fatalf("create ready: %v", err)
		}

		updated, err := repo.AppendTableDataRow(ctx, id, 25, 2)
		if err != nil {
			t.Fatalf("append row: %v", err)
		}
		if updated.ReceivedBytes != 25 || updated.Lines != 2 {
			t.Fatalf("updated = %+v, want bytes 25 lines 2", updated)
		}
	})
}

func TestAbortingTableDataMarksItAborted(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		if _, err := repo.BeginTableData(ctx, id, contest, "suspects", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := repo.AbortTableData(ctx, id); err != nil {
			t.Fatalf("abort: %v", err)
		}
		data, err := repo.TableDataByID(ctx, id)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if data.Status != provisioning.TableDataAborted {
			t.Fatalf("status = %q, want aborted", data.Status)
		}
	})
}

// DeleteTableDataRow's own three cases: a fresh delete succeeds, deleting
// the same row twice is told apart from the first, and — the real boundary
// migration 27's own CHECK enforces — a table already at MaxTableDeletedRows
// refuses a delete past it rather than growing the array without limit.
func TestDeleteTableDataRowTombstonesAndRefusesADuplicateOrAnOverflow(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		if _, err := repo.CreateReadyTableData(ctx, id, contest, "suspects", 10, 5); err != nil {
			t.Fatalf("create ready: %v", err)
		}

		if err := repo.DeleteTableDataRow(ctx, id, 2); err != nil {
			t.Fatalf("delete row 2: %v", err)
		}
		data, err := repo.TableDataByID(ctx, id)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if len(data.DeletedRows) != 1 || data.DeletedRows[0] != 2 {
			t.Fatalf("deleted_rows = %v, want [2]", data.DeletedRows)
		}

		if err := repo.DeleteTableDataRow(ctx, id, 2); !errors.Is(err, provisioning.ErrTableRowAlreadyDeleted) {
			t.Fatalf("deleting row 2 again = %v, want ErrTableRowAlreadyDeleted", err)
		}

		// Seed the array up to migration 27's own bound directly in SQL —
		// generate_series is far cheaper than 10,000 round trips through
		// this repository — then ask this method to add one more.
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_table_data
			 SET deleted_rows = (SELECT array_agg(x) FROM generate_series(1000, 1000 + $2 - 1) x)
			 WHERE id = $1`, id, provisioning.MaxTableDeletedRows); err != nil {
			t.Fatalf("seed the array to the bound: %v", err)
		}
		if err := repo.DeleteTableDataRow(ctx, id, 3); !errors.Is(err, provisioning.ErrTooManyDeletedRows) {
			t.Fatalf("deleting one row past the bound = %v, want ErrTooManyDeletedRows", err)
		}
	})
}

func TestAbandonedTableDataListsOnlyReceivingRowsOlderThanTheCutoff(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		stale := uuid.New()
		if _, err := repo.BeginTableData(ctx, stale, contest, "suspects", 10); err != nil {
			t.Fatalf("begin stale: %v", err)
		}
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_table_data SET updated_at = now() - interval '2 days' WHERE id = $1`, stale); err != nil {
			t.Fatalf("age the row: %v", err)
		}

		fresh := uuid.New()
		if _, err := repo.BeginTableData(ctx, fresh, contest, "sightings", 10); err != nil {
			t.Fatalf("begin fresh: %v", err)
		}

		abandoned, err := repo.AbandonedTableData(ctx, time.Now().Add(-24*time.Hour), 100)
		if err != nil {
			t.Fatalf("list abandoned: %v", err)
		}
		listed := make(map[uuid.UUID]bool, len(abandoned))
		for _, d := range abandoned {
			listed[d.ID] = true
		}
		if !listed[stale] {
			t.Fatal("the stale table upload was not listed as abandoned")
		}
		if listed[fresh] {
			t.Fatal("a table upload begun moments ago was listed as abandoned")
		}
	})
}

// The question the janitor's orphan sweep asks of a table-data file it found
// on the volume — TableDataInUse's own doc, UploadInUse's own shape.
func TestTableDataInUseSeparatesAFileSomethingNeedsFromOneNothingDoes(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if inUse, err := repo.TableDataInUse(ctx, uuid.New()); err != nil || inUse {
			t.Fatalf("in use = %v, err = %v, want false for an id no row ever named", inUse, err)
		}

		id := uuid.New()
		if _, err := repo.BeginTableData(ctx, id, contest, "suspects", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if inUse, err := repo.TableDataInUse(ctx, id); err != nil || !inUse {
			t.Fatalf("in use = %v, err = %v, want true while receiving", inUse, err)
		}

		if _, err := repo.CompleteTableData(ctx, contest, "suspects", id, 10, 1, nil); err != nil {
			t.Fatalf("complete: %v", err)
		}
		if inUse, err := repo.TableDataInUse(ctx, id); err != nil || !inUse {
			t.Fatalf("in use = %v, err = %v, want true for a ready file", inUse, err)
		}

		if err := repo.AbortTableData(ctx, id); err != nil {
			t.Fatalf("abort: %v", err)
		}
		if inUse, err := repo.TableDataInUse(ctx, id); err != nil || inUse {
			t.Fatalf("in use = %v, err = %v, want false once aborted", inUse, err)
		}
	})
}

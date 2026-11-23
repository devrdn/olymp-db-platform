package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
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

func TestASecondTableUploadForTheSameTableWhileOneIsReceivingIsRejectedByTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.BeginTableData(ctx, uuid.New(), contest, "suspects", 1024); err != nil {
			t.Fatalf("first begin: %v", err)
		}
		// The index is per (contest_id, lower(table_name)). This runs before
		// the conflict below, which aborts the test transaction.
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

func TestTableDataWithOwnerReadsTheRowAndTheContestsOwnerTogether(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		owner := makeUser(t, ctx, "owner-"+uuid.NewString()[:8])
		contest := makeContest(t, ctx, owner.ID)
		repo := NewGameInstances(testPool)
		id := uuid.New()
		if _, err := repo.BeginTableData(ctx, id, contest, "suspects", 40); err != nil {
			t.Fatalf("begin: %v", err)
		}

		data, gotOwner, err := repo.TableDataWithOwner(ctx, id)

		if err != nil {
			t.Fatalf("TableDataWithOwner() = %v", err)
		}
		if data.ID != id || data.ContestID != contest || data.Status != provisioning.TableDataReceiving || data.DeclaredBytes != 40 {
			t.Errorf("data = %+v, want the row begun", data)
		}
		if gotOwner != owner.ID {
			t.Errorf("owner = %s, want the contest's creator %s", gotOwner, owner.ID)
		}
		if _, _, err := repo.TableDataWithOwner(ctx, uuid.New()); !errors.Is(err, provisioning.ErrTableDataNotFound) {
			t.Errorf("an unknown id = %v, want ErrTableDataNotFound", err)
		}
	})
}

// A chunk can still be arriving when its upload completes. Once the file is
// 'complete' its recorded length is what every reader trusts, so a chunk's
// progress may no longer move it.
func TestUpdateTableDataReceivedLeavesACompletedFilesLengthAlone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		if _, err := repo.BeginTableData(ctx, id, contest, "suspects", 40); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := repo.UpdateTableDataReceived(ctx, id, 40); err != nil {
			t.Fatalf("record progress: %v", err)
		}
		// A delayed write of an earlier chunk must not move the length back.
		if err := repo.UpdateTableDataReceived(ctx, id, 25); err != nil {
			t.Fatalf("record an earlier chunk's progress: %v", err)
		}
		if current, err := repo.TableDataByID(ctx, id); err != nil || current.ReceivedBytes != 40 {
			t.Fatalf("received = %d (%v), want the 40 already recorded", current.ReceivedBytes, err)
		}
		if _, err := repo.CompleteTableData(ctx, contest, "suspects", id, 40, 2, nil); err != nil {
			t.Fatalf("complete: %v", err)
		}

		for _, late := range []int64{55, 25} {
			if err := repo.UpdateTableDataReceived(ctx, id, late); !errors.Is(err, provisioning.ErrTableDataAlreadyComplete) {
				t.Fatalf("progress of %d after completion = %v, want ErrTableDataAlreadyComplete", late, err)
			}
		}
		if current, err := repo.TableDataByID(ctx, id); err != nil || current.ReceivedBytes != 40 {
			t.Fatalf("received = %d (%v), want the 40 the upload completed with", current.ReceivedBytes, err)
		}
	})
}

func TestAppendTableDataRowCountsTheRowFromTheLengthItWasReadAt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		if _, err := repo.CreateReadyTableData(ctx, id, contest, "suspects", 10, 1); err != nil {
			t.Fatalf("create ready: %v", err)
		}

		updated, err := repo.AppendTableDataRow(ctx, id, 10, 15)
		if err != nil {
			t.Fatalf("append row: %v", err)
		}
		if updated.ReceivedBytes != 25 || updated.Lines != 2 {
			t.Fatalf("updated = %+v, want bytes 25 lines 2", updated)
		}
	})
}

// A form that read the file before another form's row was recorded would
// write its own row over that one; the recorded length is what refuses it.
func TestAppendTableDataRowRefusesALengthThatIsNoLongerTheRecord(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		if _, err := repo.CreateReadyTableData(ctx, id, contest, "suspects", 10, 1); err != nil {
			t.Fatalf("create ready: %v", err)
		}
		if _, err := repo.AppendTableDataRow(ctx, id, 10, 15); err != nil {
			t.Fatalf("append row: %v", err)
		}

		if _, err := repo.AppendTableDataRow(ctx, id, 10, 15); !errors.Is(err, provisioning.ErrTableDataChanged) {
			t.Fatalf("a second row from the same length = %v, want ErrTableDataChanged", err)
		}
		current, err := repo.TableDataByID(ctx, id)
		if err != nil {
			t.Fatalf("read the row: %v", err)
		}
		if current.ReceivedBytes != 25 || current.Lines != 2 {
			t.Fatalf("data = %+v, want the file still described as 25 bytes and 2 rows", current)
		}

		// A row no longer current (the game was replaced) is refused.
		if err := repo.AbortTableData(ctx, id); err != nil {
			t.Fatalf("abort: %v", err)
		}
		if _, err := repo.AppendTableDataRow(ctx, id, 25, 15); !errors.Is(err, provisioning.ErrTableDataChanged) {
			t.Fatalf("error = %v, want ErrTableDataChanged", err)
		}
	})
}

// TestDiscardTableDataRetiresEveryFileTheContestStillHas: a row already
// 'aborted' is not returned again, since an id returned twice would be a file
// removed twice.
func TestDiscardTableDataRetiresEveryFileTheContestStillHas(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		other := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		ready := uuid.New()
		if _, err := repo.CreateReadyTableData(ctx, ready, contest, "suspects", 10, 1); err != nil {
			t.Fatalf("create ready: %v", err)
		}
		receiving := uuid.New()
		if _, err := repo.BeginTableData(ctx, receiving, contest, "witnesses", 64); err != nil {
			t.Fatalf("begin: %v", err)
		}
		alreadyGone := uuid.New()
		if _, err := repo.BeginTableData(ctx, alreadyGone, contest, "evidence", 64); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := repo.AbortTableData(ctx, alreadyGone); err != nil {
			t.Fatalf("abort: %v", err)
		}
		untouched := uuid.New()
		if _, err := repo.CreateReadyTableData(ctx, untouched, other, "suspects", 10, 1); err != nil {
			t.Fatalf("create ready for another contest: %v", err)
		}

		ids, err := repo.DiscardTableData(ctx, contest)
		if err != nil {
			t.Fatalf("discard: %v", err)
		}
		got := map[uuid.UUID]bool{}
		for _, id := range ids {
			if got[id] {
				t.Fatalf("id %s was returned twice", id)
			}
			got[id] = true
		}
		if len(got) != 2 || !got[ready] || !got[receiving] {
			t.Fatalf("discarded %v, want exactly the ready file and the receiving upload", ids)
		}

		for _, id := range []uuid.UUID{ready, receiving} {
			data, err := repo.TableDataByID(ctx, id)
			if err != nil {
				t.Fatalf("read back %s: %v", id, err)
			}
			if data.Status != provisioning.TableDataAborted {
				t.Fatalf("%s is %q, want aborted", id, data.Status)
			}
		}
		if _, err := repo.ReadyTableData(ctx, other, "suspects"); err != nil {
			t.Fatalf("another contest's own data was discarded too: %v", err)
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

		// Seed the array to the bound in SQL, cheaper than one call per row.
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

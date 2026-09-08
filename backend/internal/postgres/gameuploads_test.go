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

func TestBeginningAnUploadCreatesARowInReceiving(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		id := uuid.New()
		upload, err := repo.BeginUpload(ctx, id, contest, "dump.sql", 1024)
		if err != nil {
			t.Fatalf("begin upload: %v", err)
		}
		if upload.ID != id || upload.ContestID != contest {
			t.Fatalf("upload = %+v, want id %s contest %s", upload, id, contest)
		}
		if upload.Status != provisioning.UploadReceiving {
			t.Fatalf("status = %q, want receiving", upload.Status)
		}
		if upload.Filename != "dump.sql" || upload.DeclaredBytes != 1024 {
			t.Fatalf("upload = %+v", upload)
		}
		if upload.ReceivedBytes != 0 {
			t.Fatalf("received_bytes = %d, want 0 for a fresh upload", upload.ReceivedBytes)
		}
	})
}

// The guarantee migration 24's own unique partial index exists for: between
// a SELECT and an INSERT in Go there is always room for a second request, and
// only the database itself closes that gap.
func TestASecondBeginForTheSameContestWhileOneIsReceivingIsRejectedByTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.BeginUpload(ctx, uuid.New(), contest, "first.sql", 1024); err != nil {
			t.Fatalf("first begin: %v", err)
		}

		_, err := repo.BeginUpload(ctx, uuid.New(), contest, "second.sql", 2048)
		if !errors.Is(err, provisioning.ErrUploadInProgress) {
			t.Fatalf("second begin answered %v, want ErrUploadInProgress", err)
		}
	})
}

// A different contest's own upload is untouched by the index above — it is
// scoped to (contest_id) WHERE status = 'receiving', not installation-wide.
func TestTwoDifferentContestsMayEachHaveAnUploadReceiving(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		a, b := aContest(t, ctx), aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.BeginUpload(ctx, uuid.New(), a, "a.sql", 1024); err != nil {
			t.Fatalf("begin for a: %v", err)
		}
		if _, err := repo.BeginUpload(ctx, uuid.New(), b, "b.sql", 1024); err != nil {
			t.Fatalf("begin for b, a different contest: %v", err)
		}
	})
}

func TestCurrentUploadReadsTheOneReceivingRowAndNoneWhenThereIsNone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.CurrentUpload(ctx, contest); !errors.Is(err, provisioning.ErrUploadNotFound) {
			t.Fatalf("a contest with no upload answered %v, want ErrUploadNotFound", err)
		}

		id := uuid.New()
		if _, err := repo.BeginUpload(ctx, id, contest, "dump.sql", 1024); err != nil {
			t.Fatalf("begin: %v", err)
		}

		current, err := repo.CurrentUpload(ctx, contest)
		if err != nil {
			t.Fatalf("current upload: %v", err)
		}
		if current.ID != id {
			t.Fatalf("current upload = %s, want %s", current.ID, id)
		}
	})
}

func TestUpdateReceivedRecordsProgress(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		id := uuid.New()
		if _, err := repo.BeginUpload(ctx, id, contest, "dump.sql", 4096); err != nil {
			t.Fatalf("begin: %v", err)
		}

		if err := repo.UpdateReceived(ctx, id, 2048); err != nil {
			t.Fatalf("update received: %v", err)
		}

		upload, err := repo.Upload(ctx, id)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if upload.ReceivedBytes != 2048 {
			t.Fatalf("received_bytes = %d, want 2048", upload.ReceivedBytes)
		}
	})
}

// Completing an upload is the same event SaveScript records for the editor
// path: the version bumps, the game goes back to pending, and this time the
// row also says where it came from.
func TestCompletingAnUploadMarksItCompleteAndReplacesTheGame(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		id := uuid.New()
		if _, err := repo.BeginUpload(ctx, id, contest, "dump.sql", 4096); err != nil {
			t.Fatalf("begin: %v", err)
		}

		template, err := repo.CompleteUpload(ctx, contest, id, "game_tpl_cabc",
			provisioning.UploadSummary{Bytes: 4096, SHA256: "deadbeef", Lines: 7}, nil)
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if template.Version != 1 || template.Status != provisioning.TemplatePending {
			t.Fatalf("template = %+v, want version 1, pending", template)
		}
		if template.Source != provisioning.SourceFile {
			t.Fatalf("source = %q, want file", template.Source)
		}
		if template.UploadID == nil || *template.UploadID != id {
			t.Fatalf("upload_id = %v, want %s", template.UploadID, id)
		}
		if template.Script != "" {
			t.Fatalf("script = %q, want empty for a file-sourced game", template.Script)
		}

		upload, err := repo.Upload(ctx, id)
		if err != nil {
			t.Fatalf("read the upload back: %v", err)
		}
		if upload.Status != provisioning.UploadComplete {
			t.Fatalf("upload status = %q, want complete", upload.Status)
		}
		if upload.SHA256 != "deadbeef" || upload.Lines != 7 || upload.ReceivedBytes != 4096 {
			t.Fatalf("upload = %+v", upload)
		}
	})
}

// The row a new completed upload displaces is retired here — marked
// 'aborted' — in the same statement group that replaces the game, once its
// file has already been removed from disk by the caller
// (provisioning.Games.CompleteUpload's own doc explains the order).
func TestCompletingAnUploadRetiresThePreviousOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		first := uuid.New()
		if _, err := repo.BeginUpload(ctx, first, contest, "first.sql", 10); err != nil {
			t.Fatalf("begin first: %v", err)
		}
		if _, err := repo.CompleteUpload(ctx, contest, first, "game_tpl_cabc",
			provisioning.UploadSummary{Bytes: 10, SHA256: "aaaa", Lines: 1}, nil); err != nil {
			t.Fatalf("complete first: %v", err)
		}

		second := uuid.New()
		if _, err := repo.BeginUpload(ctx, second, contest, "second.sql", 20); err != nil {
			t.Fatalf("begin second: %v", err)
		}
		template, err := repo.CompleteUpload(ctx, contest, second, "game_tpl_cabc",
			provisioning.UploadSummary{Bytes: 20, SHA256: "bbbb", Lines: 2}, &first)
		if err != nil {
			t.Fatalf("complete second: %v", err)
		}
		if template.Version != 2 {
			t.Fatalf("version = %d, want 2", template.Version)
		}
		if template.UploadID == nil || *template.UploadID != second {
			t.Fatalf("upload_id = %v, want %s", template.UploadID, second)
		}

		previous, err := repo.Upload(ctx, first)
		if err != nil {
			t.Fatalf("read the displaced upload back: %v", err)
		}
		if previous.Status != provisioning.UploadAborted {
			t.Fatalf("the displaced upload's status is %q, want aborted", previous.Status)
		}
	})
}

func TestAbortingAnUploadMarksItAbortedAndNeverTouchesTheGame(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		id := uuid.New()
		if _, err := repo.BeginUpload(ctx, id, contest, "dump.sql", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}

		if err := repo.AbortUpload(ctx, id); err != nil {
			t.Fatalf("abort: %v", err)
		}

		upload, err := repo.Upload(ctx, id)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if upload.Status != provisioning.UploadAborted {
			t.Fatalf("status = %q, want aborted", upload.Status)
		}
		if _, err := repo.Template(ctx, contest); !errors.Is(err, provisioning.ErrNoGame) {
			t.Fatalf("an aborted upload created a game: %v", err)
		}
	})
}

func TestAbandonedUploadsListsOnlyReceivingRowsOlderThanTheCutoff(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		stale := uuid.New()
		if _, err := repo.BeginUpload(ctx, stale, contest, "stale.sql", 10); err != nil {
			t.Fatalf("begin stale: %v", err)
		}
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_uploads SET updated_at = now() - interval '2 days' WHERE id = $1`, stale); err != nil {
			t.Fatalf("age the row: %v", err)
		}

		fresh := uuid.New()
		freshContest := aContest(t, ctx)
		if _, err := repo.BeginUpload(ctx, fresh, freshContest, "fresh.sql", 10); err != nil {
			t.Fatalf("begin fresh: %v", err)
		}

		abandoned, err := repo.AbandonedUploads(ctx, time.Now().Add(-24*time.Hour), 100)
		if err != nil {
			t.Fatalf("list abandoned: %v", err)
		}
		var sawStale, sawFresh bool
		for _, u := range abandoned {
			if u.ID == stale {
				sawStale = true
			}
			if u.ID == fresh {
				sawFresh = true
			}
		}
		if !sawStale {
			t.Fatal("the stale upload was not listed as abandoned")
		}
		if sawFresh {
			t.Fatal("an upload updated moments ago was listed as abandoned")
		}
	})
}

func TestUploadExistsTellsARowApartFromNoRowAtAll(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if exists, err := repo.UploadExists(ctx, uuid.New()); err != nil || exists {
			t.Fatalf("exists = %v, err = %v, want false for an id nothing named", exists, err)
		}

		id := uuid.New()
		if _, err := repo.BeginUpload(ctx, id, contest, "dump.sql", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if exists, err := repo.UploadExists(ctx, id); err != nil || !exists {
			t.Fatalf("exists = %v, err = %v, want true", exists, err)
		}
	})
}

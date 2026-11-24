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

func TestBeginningAnUploadCreatesARowInReceiving(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		uploads := NewGameUploads(testPool)

		id := uuid.New()
		upload, err := uploads.Begin(ctx, id, contest, "dump.sql", 1024)
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

func TestASecondBeginForTheSameContestWhileOneIsReceivingIsRejectedByTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		uploads := NewGameUploads(testPool)

		if _, err := uploads.Begin(ctx, uuid.New(), contest, "first.sql", 1024); err != nil {
			t.Fatalf("first begin: %v", err)
		}

		_, err := uploads.Begin(ctx, uuid.New(), contest, "second.sql", 2048)
		if !errors.Is(err, provisioning.ErrUploadInProgress) {
			t.Fatalf("second begin answered %v, want ErrUploadInProgress", err)
		}
	})
}

func TestTwoDifferentContestsMayEachHaveAnUploadReceiving(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		a, b := aContest(t, ctx), aContest(t, ctx)
		uploads := NewGameUploads(testPool)

		if _, err := uploads.Begin(ctx, uuid.New(), a, "a.sql", 1024); err != nil {
			t.Fatalf("begin for a: %v", err)
		}
		if _, err := uploads.Begin(ctx, uuid.New(), b, "b.sql", 1024); err != nil {
			t.Fatalf("begin for b, a different contest: %v", err)
		}
	})
}

func TestCurrentUploadReadsTheOneReceivingRowAndNoneWhenThereIsNone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		uploads := NewGameUploads(testPool)

		if _, err := uploads.Current(ctx, contest); !errors.Is(err, provisioning.ErrUploadNotFound) {
			t.Fatalf("a contest with no upload answered %v, want ErrUploadNotFound", err)
		}

		id := uuid.New()
		if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 1024); err != nil {
			t.Fatalf("begin: %v", err)
		}

		current, err := uploads.Current(ctx, contest)
		if err != nil {
			t.Fatalf("current upload: %v", err)
		}
		if current.ID != id {
			t.Fatalf("current upload = %s, want %s", current.ID, id)
		}
	})
}

// A chunk's progress write can arrive late: after a later chunk's, or after
// the upload was completed or cancelled. It never lowers the length and never
// touches an upload that is no longer receiving.
func TestUpdateReceivedNeverLowersTheLengthNorTouchesAFinishedUpload(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		uploads := NewGameUploads(testPool)
		id := uuid.New()
		if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 4096); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := uploads.UpdateReceived(ctx, id, 2048); err != nil {
			t.Fatalf("update received: %v", err)
		}

		if err := uploads.UpdateReceived(ctx, id, 1024); err != nil {
			t.Fatalf("an earlier chunk's progress: %v", err)
		}
		if upload, err := uploads.ByID(ctx, id); err != nil || upload.ReceivedBytes != 2048 {
			t.Fatalf("received_bytes = %d (%v), want the 2048 already recorded", upload.ReceivedBytes, err)
		}

		if err := uploads.Abort(ctx, id); err != nil {
			t.Fatalf("abort: %v", err)
		}
		if err := uploads.UpdateReceived(ctx, id, 4096); !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
			t.Fatalf("progress after the upload was cancelled = %v, want ErrUploadAlreadyComplete", err)
		}
		if upload, err := uploads.ByID(ctx, id); err != nil || upload.ReceivedBytes != 2048 {
			t.Fatalf("received_bytes = %d (%v), want the 2048 the upload ended with", upload.ReceivedBytes, err)
		}
	})
}

func TestUpdateReceivedRecordsProgress(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		uploads := NewGameUploads(testPool)
		id := uuid.New()
		if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 4096); err != nil {
			t.Fatalf("begin: %v", err)
		}

		if err := uploads.UpdateReceived(ctx, id, 2048); err != nil {
			t.Fatalf("update received: %v", err)
		}

		upload, err := uploads.ByID(ctx, id)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if upload.ReceivedBytes != 2048 {
			t.Fatalf("received_bytes = %d, want 2048", upload.ReceivedBytes)
		}
	})
}

func TestCompletingAnUploadMarksItCompleteAndReplacesTheGame(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)
		id := uuid.New()
		if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 4096); err != nil {
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

		upload, err := uploads.ByID(ctx, id)
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

func TestCompletingAnUploadRetiresThePreviousOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)

		first := uuid.New()
		if _, err := uploads.Begin(ctx, first, contest, "first.sql", 10); err != nil {
			t.Fatalf("begin first: %v", err)
		}
		if _, err := repo.CompleteUpload(ctx, contest, first, "game_tpl_cabc",
			provisioning.UploadSummary{Bytes: 10, SHA256: "aaaa", Lines: 1}, nil); err != nil {
			t.Fatalf("complete first: %v", err)
		}

		second := uuid.New()
		if _, err := uploads.Begin(ctx, second, contest, "second.sql", 20); err != nil {
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

		previous, err := uploads.ByID(ctx, first)
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
		repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)
		id := uuid.New()
		if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}

		if err := uploads.Abort(ctx, id); err != nil {
			t.Fatalf("abort: %v", err)
		}

		upload, err := uploads.ByID(ctx, id)
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

// An abort and a completion that both read 'receiving' race for the row; the
// one that writes second must find it moved and change nothing.
func TestAnAbortAndACompletionOfOneDumpCannotBothWin(t *testing.T) {
	summary := provisioning.UploadSummary{Bytes: 10, SHA256: "ab", Lines: 1}

	t.Run("an abort after the completion", func(t *testing.T) {
		withTx(t, func(ctx context.Context) {
			contest := aContest(t, ctx)
			repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)
			id := uuid.New()
			if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 10); err != nil {
				t.Fatalf("begin: %v", err)
			}
			if _, err := repo.CompleteUpload(ctx, contest, id, "game_tpl_cabc", summary, nil); err != nil {
				t.Fatalf("complete: %v", err)
			}

			if err := uploads.Abort(ctx, id); !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
				t.Fatalf("abort = %v, want ErrUploadAlreadyComplete", err)
			}
			if upload, err := uploads.ByID(ctx, id); err != nil || upload.Status != provisioning.UploadComplete {
				t.Fatalf("read back %q, %v; want still complete", upload.Status, err)
			}
		})
	})

	t.Run("a completion after the abort", func(t *testing.T) {
		withTx(t, func(ctx context.Context) {
			contest := aContest(t, ctx)
			repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)
			id := uuid.New()
			if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 10); err != nil {
				t.Fatalf("begin: %v", err)
			}
			if err := uploads.Abort(ctx, id); err != nil {
				t.Fatalf("abort: %v", err)
			}

			if _, err := repo.CompleteUpload(ctx, contest, id, "game_tpl_cabc", summary, nil); !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
				t.Fatalf("complete = %v, want ErrUploadAlreadyComplete", err)
			}
			if _, err := repo.Template(ctx, contest); !errors.Is(err, provisioning.ErrNoGame) {
				t.Fatalf("an aborted upload became the game: %v", err)
			}
		})
	})
}

// The same two orders across two connections: the second operation waits on
// the row the first holds, and finds it moved once the first commits.
func TestAnAbortAndACompletionOfOneDumpRacingCannotBothWin(t *testing.T) {
	summary := provisioning.UploadSummary{Bytes: 10, SHA256: "ab", Lines: 1}
	begun := func(t *testing.T) (contest, id uuid.UUID) {
		t.Helper()
		contest, id = contestRow(t, context.Background()), uuid.New()
		if _, err := NewGameUploads(testPool).Begin(context.Background(), id, contest, "dump.sql", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}
		return contest, id
	}
	repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)

	t.Run("an abort waiting on a completion", func(t *testing.T) {
		contest, id := begun(t)
		err := raceOnTwoConnections(t,
			func(ctx context.Context) error {
				_, err := repo.CompleteUpload(ctx, contest, id, "race_"+id.String()[:8], summary, nil)
				return err
			},
			func(ctx context.Context) error { return uploads.Abort(ctx, id) },
			"SET status = 'aborted'")
		if !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
			t.Fatalf("abort = %v, want ErrUploadAlreadyComplete", err)
		}
		if upload, err := uploads.ByID(context.Background(), id); err != nil || upload.Status != provisioning.UploadComplete {
			t.Fatalf("read back %q, %v; want still complete", upload.Status, err)
		}
	})

	t.Run("a completion waiting on an abort", func(t *testing.T) {
		contest, id := begun(t)
		err := raceOnTwoConnections(t,
			func(ctx context.Context) error { return uploads.Abort(ctx, id) },
			func(ctx context.Context) error {
				_, err := repo.CompleteUpload(ctx, contest, id, "race_"+id.String()[:8], summary, nil)
				return err
			},
			"SET status = 'complete'")
		if !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
			t.Fatalf("complete = %v, want ErrUploadAlreadyComplete", err)
		}
		if _, err := repo.Template(context.Background(), contest); !errors.Is(err, provisioning.ErrNoGame) {
			t.Fatalf("an aborted upload became the game: %v", err)
		}
	})
}

func TestAbandonedDumpsAreOnlyReceivingRowsOlderThanTheCutoff(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		uploads := NewGameUploads(testPool)

		stale := uuid.New()
		if _, err := uploads.Begin(ctx, stale, contest, "stale.sql", 10); err != nil {
			t.Fatalf("begin stale: %v", err)
		}
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_uploads SET updated_at = now() - interval '2 days' WHERE id = $1`, stale); err != nil {
			t.Fatalf("age the row: %v", err)
		}

		fresh := uuid.New()
		freshContest := aContest(t, ctx)
		if _, err := uploads.Begin(ctx, fresh, freshContest, "fresh.sql", 10); err != nil {
			t.Fatalf("begin fresh: %v", err)
		}

		// Old enough but not 'receiving': listing these would let the janitor
		// delete the file a built game needs. One row per other status, since
		// a predicate that catches only one of them is as wrong as none.
		settled := map[string]uuid.UUID{"complete": uuid.New(), "aborted": uuid.New()}
		for status, id := range settled {
			contest := aContest(t, ctx)
			if _, err := uploads.Begin(ctx, id, contest, status+".sql", 10); err != nil {
				t.Fatalf("begin the %s upload: %v", status, err)
			}
			if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
				`UPDATE game_uploads SET status = $2, updated_at = now() - interval '2 days' WHERE id = $1`,
				id, status); err != nil {
				t.Fatalf("settle and age the %s upload: %v", status, err)
			}
		}

		abandoned, err := uploads.Abandoned(ctx, time.Now().Add(-24*time.Hour), 100)
		if err != nil {
			t.Fatalf("list abandoned: %v", err)
		}
		listed := make(map[uuid.UUID]bool, len(abandoned))
		for _, u := range abandoned {
			listed[u.ID] = true
			if u.Status != provisioning.UploadReceiving {
				t.Fatalf("upload %s came back as abandoned with status %q", u.ID, u.Status)
			}
		}
		if !listed[stale] {
			t.Fatal("the stale upload was not listed as abandoned")
		}
		if listed[fresh] {
			t.Fatal("an upload updated moments ago was listed as abandoned")
		}
		for status, id := range settled {
			if listed[id] {
				t.Fatalf("a %s upload two days old was listed as abandoned — the janitor would delete "+
					"the file a built game still needs", status)
			}
		}
	})
}

func TestAnInUseDumpIsAFileSomethingNeedsAndNotOneNothingDoes(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)
		name := "game_tpl_c" + uuid.NewString()[:12]

		if inUse, err := uploads.InUse(ctx, uuid.New()); err != nil || inUse {
			t.Fatalf("in use = %v, err = %v, want false for an id no row ever named", inUse, err)
		}

		id := uuid.New()
		if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if inUse, err := uploads.InUse(ctx, id); err != nil || !inUse {
			t.Fatalf("in use = %v, err = %v, want true while the upload is still receiving", inUse, err)
		}

		summary := provisioning.UploadSummary{Bytes: 10, SHA256: "d0", Lines: 1}
		if _, err := repo.CompleteUpload(ctx, contest, id, name, summary, nil); err != nil {
			t.Fatalf("complete: %v", err)
		}
		if inUse, err := uploads.InUse(ctx, id); err != nil || !inUse {
			t.Fatalf("in use = %v, err = %v, want true for the upload the game is built from", inUse, err)
		}

		// A script replaces the game: the row stays 'complete' but no game
		// names the file.
		if _, err := repo.SaveScript(ctx, contest, name, `SELECT 1`); err != nil {
			t.Fatalf("save script: %v", err)
		}
		if inUse, err := uploads.InUse(ctx, id); err != nil || inUse {
			t.Fatalf("in use = %v, err = %v, want false once no game names the upload", inUse, err)
		}
	})
}

// The CHECK game_templates_source_pairing requires a 'file' game to name an
// upload, so upload_id cannot be ON DELETE SET NULL: deleting the upload must
// cascade to the game rather than rely on RI trigger order.
func TestDeletingAnUploadAFileSourcedGameNamesTakesTheGameWithIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo, uploads := NewGameInstances(testPool), NewGameUploads(testPool)

		id := uuid.New()
		if _, err := uploads.Begin(ctx, id, contest, "dump.sql", 10); err != nil {
			t.Fatalf("begin: %v", err)
		}
		summary := provisioning.UploadSummary{Bytes: 10, SHA256: "d0", Lines: 1}
		if _, err := repo.CompleteUpload(ctx, contest, id, "game_tpl_cabc", summary, nil); err != nil {
			t.Fatalf("complete: %v", err)
		}

		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`DELETE FROM game_uploads WHERE id = $1`, id); err != nil {
			t.Fatalf("deleting the upload a file-sourced game names: %v", err)
		}

		if _, err := repo.Template(ctx, contest); !errors.Is(err, provisioning.ErrNoGame) {
			t.Fatalf("the game outlived the upload it is built from: %v", err)
		}
	})
}

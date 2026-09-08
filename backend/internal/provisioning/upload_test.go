package provisioning_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// uploadLimits are deliberately small: these tests write real bytes to a
// real gamefile.Store on a real temp directory, and the point is the
// behaviour, not the size.
var uploadLimits = gamefile.Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 4 << 20, MaxChunkBytes: 256 << 10}

// gamesWithUploads assembles a *provisioning.Games against the real database
// (this package's own standing pool, testPool) and a real gamefile.Store on
// a fresh temp directory — never a fake for either. The behaviour these
// tests exist to prove — the unique partial index, the order a file is
// removed in, a file the volume holds that no row names — is a property of
// the real schema and the real disk, and a fake of either would prove
// nothing about them (the same reasoning instances_test.go's own doc gives
// for using the real repository rather than a stand-in).
func gamesWithUploads(t *testing.T, editable bool) (*provisioning.Games, string) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	dir := t.TempDir()
	store, err := gamefile.NewStore(dir, uploadLimits)
	if err != nil {
		t.Fatalf("open the upload store: %v", err)
	}
	games := provisioning.NewGames(postgres.NewGameInstances(testPool), &buildCluster{}, authoring{editable: editable}).
		WithUploads(store, dir, uploadLimits)
	return games, dir
}

// gamesWithUploadsAndAudit is gamesWithUploads plus the trail wired in, for
// the tests that check what completing or cancelling an upload records.
func gamesWithUploadsAndAudit(t *testing.T) (*provisioning.Games, *sink) {
	t.Helper()
	games, _ := gamesWithUploads(t, true)
	s := &sink{}
	games = games.WithAudit(audit.New(s), storage.NewUnitOfWork(testPool))
	return games, s
}

// onDisk reports whether id's data file still exists in dir — the ground
// truth these tests check the database's own bookkeeping against.
func onDisk(t *testing.T, dir string, id uuid.UUID) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, id.String()+".data"))
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat the upload's file: %v", err)
	return false
}

func TestBeginningAnUploadReservesItOnDiskAndInTheDatabase(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	upload, err := games.BeginUpload(t.Context(), contest.ID, "dump.sql", 1024)
	if err != nil {
		t.Fatalf("begin upload: %v", err)
	}
	if upload.Status != provisioning.UploadReceiving {
		t.Fatalf("status = %q, want receiving", upload.Status)
	}
	if !onDisk(t, dir, upload.ID) {
		t.Fatal("no file was reserved on disk for the new upload")
	}
}

// Starting to receive gigabytes into a contest that is already running is
// time and disk nobody gets back, found out only at the very end — so
// BeginUpload checks GameEditable itself rather than leaving that to
// CompleteUpload, and refuses before it ever touches the disk.
func TestBeginningAnUploadForAContestThatIsNotEditableIsRefusedBeforeTouchingDisk(t *testing.T) {
	games, dir := gamesWithUploads(t, false)
	contest, _ := contestFor(t, t.Context(), 0)

	if _, err := games.BeginUpload(t.Context(), contest.ID, "dump.sql", 1024); !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Fatalf("error = %v, want ErrGameNotEditable", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the upload directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the refused begin still left %d file(s) on disk", len(entries))
	}
}

func TestADeclaredLengthPastTheConfiguredCeilingIsRefused(t *testing.T) {
	games, _ := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	_, err := games.BeginUpload(t.Context(), contest.ID, "dump.sql", uploadLimits.MaxFileBytes+1)
	if !errors.Is(err, provisioning.ErrUploadTooLarge) {
		t.Fatalf("error = %v, want ErrUploadTooLarge", err)
	}
}

// The guarantee migration 24's own unique partial index exists for. A check
// in Go between reading and writing always leaves room for a second request
// to land in between; only the database closes that gap for good.
func TestASecondUploadForTheSameContestIsRejectedByTheDatabaseNotByGoCode(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	first, err := games.BeginUpload(t.Context(), contest.ID, "first.sql", 1024)
	if err != nil {
		t.Fatalf("first begin: %v", err)
	}

	_, err = games.BeginUpload(t.Context(), contest.ID, "second.sql", 1024)
	if !errors.Is(err, provisioning.ErrUploadInProgress) {
		t.Fatalf("second begin answered %v, want ErrUploadInProgress", err)
	}
	if !onDisk(t, dir, first.ID) {
		t.Fatal("the first, still-receiving upload's file disappeared")
	}
}

// beginWithContent begins an upload sized exactly to content and appends all
// of it in one chunk, leaving it 'receiving' and ready for CompleteUpload —
// what every test below that needs a specific, known upload starts from.
func beginWithContent(t *testing.T, games *provisioning.Games, contest uuid.UUID, filename, content string) provisioning.Upload {
	t.Helper()
	upload, err := games.BeginUpload(t.Context(), contest, filename, int64(len(content)))
	if err != nil {
		t.Fatalf("begin upload: %v", err)
	}
	if _, err := games.AppendChunk(t.Context(), contest, upload.ID, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("append chunk: %v", err)
	}
	return upload
}

// Completing an upload is the same event SetScript records for a script an
// organiser wrote directly: the version bumps and the game is queued to
// build again.
func TestCompletingAnUploadBumpsTheVersionAndQueuesTheBuild(t *testing.T) {
	games, _ := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	actor := uuid.New()

	upload := beginWithContent(t, games, contest.ID, "dump.sql", "CREATE X;\n")
	template, err := games.CompleteUpload(t.Context(), actor, contest.ID, upload.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if template.Version != 1 {
		t.Fatalf("version = %d, want 1 for a contest's first game", template.Version)
	}
	if template.Status != provisioning.TemplatePending {
		t.Fatalf("status = %q, want pending — a build should now be queued", template.Status)
	}
	if template.Source != provisioning.SourceFile {
		t.Fatalf("source = %q, want file", template.Source)
	}

	// A second upload, completed the same way, bumps the version again —
	// exactly the mechanism a second SetScript call uses, because this is
	// the same path.
	second := beginWithContent(t, games, contest.ID, "dump2.sql", "CREATE Y;\n")
	again, err := games.CompleteUpload(t.Context(), actor, contest.ID, second.ID)
	if err != nil {
		t.Fatalf("complete second: %v", err)
	}
	if again.Version != 2 {
		t.Fatalf("version = %d, want 2 after a second upload replaced the game", again.Version)
	}
}

// The order Instances.DropInstance already uses: the real object goes first.
// A newly completed upload displaces the previous one, and its file is gone
// from disk once completion succeeds.
func TestCompletingAReplacementUploadDeletesTheOldFile(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	actor := uuid.New()

	first := beginWithContent(t, games, contest.ID, "first.sql", "A;\n")
	if _, err := games.CompleteUpload(t.Context(), actor, contest.ID, first.ID); err != nil {
		t.Fatalf("complete first: %v", err)
	}
	if !onDisk(t, dir, first.ID) {
		t.Fatal("the first upload's own file is gone right after it completed")
	}

	second := beginWithContent(t, games, contest.ID, "second.sql", "C;D;\n")
	if _, err := games.CompleteUpload(t.Context(), actor, contest.ID, second.ID); err != nil {
		t.Fatalf("complete second: %v", err)
	}

	if onDisk(t, dir, first.ID) {
		t.Fatal("the displaced upload's file is still on disk — gigabytes nobody is looking for")
	}
	if !onDisk(t, dir, second.ID) {
		t.Fatal("the new upload's own file is missing")
	}
}

// An organiser's own cancel removes the file too — the same "real object
// first" order, and the row records nothing that never existed as a game.
func TestAbortingAnUploadDeletesTheFileAndLeavesNoGame(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	upload, err := games.BeginUpload(t.Context(), contest.ID, "dump.sql", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !onDisk(t, dir, upload.ID) {
		t.Fatal("nothing was reserved on disk to abort")
	}

	aborted, err := games.AbortUpload(t.Context(), uuid.New(), contest.ID, upload.ID)
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborted.Status != provisioning.UploadAborted {
		t.Fatalf("status = %q, want aborted", aborted.Status)
	}
	if onDisk(t, dir, upload.ID) {
		t.Fatal("the aborted upload's file is still on disk")
	}
	if _, err := games.Of(t.Context(), contest.ID); !errors.Is(err, provisioning.ErrNoGame) {
		t.Fatalf("an aborted upload left a game behind: %v", err)
	}
}

func TestAbortingAnUploadRecordsFilenameAndLengthNeverContent(t *testing.T) {
	games, s := gamesWithUploadsAndAudit(t)
	contest, _ := contestFor(t, t.Context(), 0)

	upload := beginWithContent(t, games, contest.ID, "confidential.sql", "SECRET ROWS HERE")

	actor := uuid.New()
	if _, err := games.AbortUpload(t.Context(), actor, contest.ID, upload.ID); err != nil {
		t.Fatalf("abort: %v", err)
	}

	entries := entriesFor(s, contest.ID)
	if len(entries) != 1 {
		t.Fatalf("%d entries recorded, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Action != audit.ActionGameUploadAbort {
		t.Fatalf("action = %q, want %q", entry.Action, audit.ActionGameUploadAbort)
	}
	if entry.ActorID == nil || *entry.ActorID != actor {
		t.Fatalf("actor = %v, want %v", entry.ActorID, actor)
	}
	if entry.Payload["filename"] != "confidential.sql" {
		t.Fatalf("payload does not name the file: %+v", entry.Payload)
	}
	for _, v := range entry.Payload {
		if s, ok := v.(string); ok && strings.Contains(s, "SECRET ROWS") {
			t.Fatalf("the audit payload leaked the upload's content: %+v", entry.Payload)
		}
	}
}

// The janitor's two sweeps: a 'receiving' row nobody has appended to in a
// while is aborted, and a file the volume holds that no row names at all —
// the more dangerous half, since nothing else in this package ever asks the
// volume what it holds — is removed too.
func TestSweepUploadsAbandonsAStaleUploadAndRemovesAFileWithNoRow(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	stale, err := games.BeginUpload(t.Context(), contest.ID, "forgotten.sql", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Aged deliberately rather than waited for, the same convention
	// gametemplates_test.go's own stale-build test uses.
	if _, err := storage.QuerierFrom(t.Context(), testPool).Exec(t.Context(),
		`UPDATE game_uploads SET updated_at = now() - interval '2 days' WHERE id = $1`, stale.ID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	// A file with no row at all: gamefile.Store.Begin makes the reservation
	// directly, bypassing Games so no game_uploads row is ever written for
	// it — exactly what a crash between the two would leave behind.
	orphanID := uuid.New()
	orphanStore, err := gamefile.NewStore(dir, uploadLimits)
	if err != nil {
		t.Fatalf("open a second handle on the same directory: %v", err)
	}
	if err := orphanStore.Begin(orphanID.String()); err != nil {
		t.Fatalf("reserve an orphan file: %v", err)
	}

	result, err := games.SweepUploads(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Abandoned != 1 {
		t.Fatalf("abandoned = %d, want 1", result.Abandoned)
	}
	if result.OrphanFiles != 1 {
		t.Fatalf("orphan files = %d, want 1", result.OrphanFiles)
	}
	if onDisk(t, dir, stale.ID) {
		t.Fatal("the abandoned upload's file is still on disk")
	}
	if onDisk(t, dir, orphanID) {
		t.Fatal("the orphan file — no row ever named it — is still on disk")
	}

	upload, err := postgres.NewGameInstances(testPool).Upload(t.Context(), stale.ID)
	if err != nil {
		t.Fatalf("read the swept upload back: %v", err)
	}
	if upload.Status != provisioning.UploadAborted {
		t.Fatalf("status = %q, want aborted", upload.Status)
	}
}

// A fresh upload — nothing appended in the last few seconds is entirely
// ordinary — must survive a sweep whose cutoff it has not reached yet.
func TestSweepUploadsLeavesARecentUploadAlone(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	fresh, err := games.BeginUpload(t.Context(), contest.ID, "dump.sql", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	result, err := games.SweepUploads(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Abandoned != 0 {
		t.Fatalf("abandoned = %d, want 0 for an upload begun moments ago", result.Abandoned)
	}
	if !onDisk(t, dir, fresh.ID) {
		t.Fatal("a fresh upload's file was removed by the sweep")
	}
}

// The order proper, not merely its usual outcome: a reordering that marked
// the row first would only be visible when the file removal that follows it
// actually fails — the harmless case (nothing fails) looks identical either
// way. So this forces the removal to fail (a read-only directory refuses the
// unlink) and checks the one thing "real object first" is actually for: a
// row that stays 'receiving' — not 'aborted' over a file still on disk —
// when the disk half did not go through. See Instances.DropInstance's own
// doc for the same guarantee proved the same way for a database drop.
func TestAbortLeavesTheRowReceivingWhenTheFileCannotBeRemoved(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	upload, err := games.BeginUpload(t.Context(), contest.ID, "dump.sql", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	// Unlinking a file needs write permission on its directory, not on the
	// file itself — this is what makes the directory (not the file) the
	// thing to lock down.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("make the upload directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if _, err := games.AbortUpload(t.Context(), uuid.New(), contest.ID, upload.ID); err == nil {
		t.Fatal("abort succeeded although the file could not be removed")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("restore the upload directory's permissions: %v", err)
	}

	stored, err := postgres.NewGameInstances(testPool).Upload(t.Context(), upload.ID)
	if err != nil {
		t.Fatalf("read the upload back: %v", err)
	}
	if stored.Status != provisioning.UploadReceiving {
		t.Fatalf("status = %q, want still receiving — the file removal that failed must not have been marked done anyway", stored.Status)
	}
	if !onDisk(t, dir, upload.ID) {
		t.Fatal("the file is gone even though its removal was made to fail")
	}

	// And the guarantee is repairable: a retry with the directory writable
	// again finishes what the first attempt could not.
	if _, err := games.AbortUpload(t.Context(), uuid.New(), contest.ID, upload.ID); err != nil {
		t.Fatalf("retry after restoring permissions: %v", err)
	}
	if onDisk(t, dir, upload.ID) {
		t.Fatal("the retried abort left the file behind")
	}
}

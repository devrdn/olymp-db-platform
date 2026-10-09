package provisioning_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// uploadLimits are small: these tests write real bytes to a temp directory.
var uploadLimits = gamefile.Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 4 << 20, MaxChunkBytes: 256 << 10}

// gamesWithUploads uses the real database (testPool) and a real
// gamefile.Store on a temp directory: the unique index, the removal order and
// orphan files are properties of the real schema and disk.
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
		WithUploads(store, uploadLimits)
	return games, dir
}

func gamesWithUploadsAndAudit(t *testing.T) (*provisioning.Games, *sink) {
	t.Helper()
	games, _ := gamesWithUploads(t, true)
	s := &sink{}
	games = games.WithAudit(audit.New(s), storage.NewUnitOfWork(testPool))
	return games, s
}

// onDisk reports whether id's upload still exists in dir, asking a fresh
// gamefile.Store rather than knowing its file layout.
func onDisk(t *testing.T, dir string, id uuid.UUID) bool {
	t.Helper()
	store, err := gamefile.NewStore(dir, uploadLimits)
	if err != nil {
		t.Fatalf("open the upload store: %v", err)
	}
	_, err = store.Received(id.String())
	if err == nil {
		return true
	}
	if errors.Is(err, gamefile.ErrNotFound) {
		return false
	}
	t.Fatalf("check the upload's status: %v", err)
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

// A NUL would make the INSERT fail after Store.Begin created the file
// (see validUploadFilename).
func TestBeginningAnUploadRefusesAFilenameWithAControlCharacter(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	for _, name := range []string{"dump\x00.sql", "dump\n.sql", "dump\x1b[2J.sql"} {
		if _, err := games.BeginUpload(t.Context(), contest.ID, name, 1024); !errors.Is(err, provisioning.ErrUploadFilenameInvalid) {
			t.Fatalf("BeginUpload(%q) = %v, want ErrUploadFilenameInvalid", name, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the upload directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the refused begins left %d file(s) on disk", len(entries))
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

// Only the unique partial index closes the race a check in Go would leave.
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

// Store.Begin counts each declared length against MaxDirBytes, so refused
// begins that kept their reservation would fill the budget for every contest.
func TestAnUploadRowTheDatabaseRefusesGivesBackTheSpaceItReserved(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	// One receiving upload, so every later begin is refused.
	quarter := uploadLimits.MaxDirBytes / 4
	if _, err := games.BeginUpload(t.Context(), contest.ID, "first.sql", quarter); err != nil {
		t.Fatalf("first begin: %v", err)
	}

	// Three refusals of a quarter each: held, they are the whole directory.
	for i := 0; i < 3; i++ {
		if _, err := games.BeginUpload(t.Context(), contest.ID, "again.sql", quarter); !errors.Is(err, provisioning.ErrUploadInProgress) {
			t.Fatalf("begin %d answered %v, want ErrUploadInProgress", i+2, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the upload directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the volume holds %d file(s), want only the one upload that has a row", len(entries))
	}

	other, _ := contestFor(t, t.Context(), 0)
	if _, err := games.BeginUpload(t.Context(), other.ID, "elsewhere.sql", quarter); err != nil {
		t.Fatalf("a second contest's upload was refused %v — the refused begins are still "+
			"holding the directory budget", err)
	}
}

// beginWithContent begins an upload sized to content and appends it in one
// chunk, leaving it ready for CompleteUpload.
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

	second := beginWithContent(t, games, contest.ID, "dump2.sql", "CREATE Y;\n")
	again, err := games.CompleteUpload(t.Context(), actor, contest.ID, second.ID)
	if err != nil {
		t.Fatalf("complete second: %v", err)
	}
	if again.Version != 2 {
		t.Fatalf("version = %d, want 2 after a second upload replaced the game", again.Version)
	}
}

// The API resolves a file-sourced Template.UploadID through Games.Upload
// (CLAUDE.md rule 11).
func TestUploadResolvesTheRowAFileSourcedTemplatesUploadIDNames(t *testing.T) {
	games, _ := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	actor := uuid.New()

	begun := beginWithContent(t, games, contest.ID, "dump.sql", "CREATE X;\nCREATE Y;\n")
	template, err := games.CompleteUpload(t.Context(), actor, contest.ID, begun.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if template.UploadID == nil {
		t.Fatal("a file-sourced template carries no upload id")
	}

	resolved, err := games.Upload(t.Context(), contest.ID, *template.UploadID)
	if err != nil {
		t.Fatalf("Upload(): %v", err)
	}
	if resolved.ID != begun.ID || resolved.Filename != "dump.sql" {
		t.Fatalf("resolved = %+v, want id %s and filename dump.sql", resolved, begun.ID)
	}
	if resolved.ReceivedBytes != int64(len("CREATE X;\nCREATE Y;\n")) {
		t.Fatalf("received_bytes = %d, want %d", resolved.ReceivedBytes, len("CREATE X;\nCREATE Y;\n"))
	}
	if resolved.Lines != 2 {
		t.Fatalf("lines = %d, want 2", resolved.Lines)
	}
	if resolved.Status != provisioning.UploadComplete {
		t.Fatalf("status = %q, want complete", resolved.Status)
	}

	// Another contest's upload answers "no such upload", not leaking the row.
	other, _ := contestFor(t, t.Context(), 0)
	if _, err := games.Upload(t.Context(), other.ID, *template.UploadID); !errors.Is(err, provisioning.ErrUploadNotFound) {
		t.Fatalf("cross-contest Upload() = %v, want ErrUploadNotFound", err)
	}
}

// The one test that touches gamefile's layout on disk: Store offers no way to
// damage an index.
func TestACorruptLineIndexIsNamedRatherThanCollapsedIntoAnInternalError(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	upload := beginWithContent(t, games, contest.ID, "dump.sql", "CREATE X;\nCREATE Y;\n")
	if _, err := games.CompleteUpload(t.Context(), uuid.New(), contest.ID, upload.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// One byte short of what its header declares, like a truncated write.
	index := filepath.Join(dir, upload.ID.String()+".idx")
	info, err := os.Stat(index)
	if err != nil {
		t.Fatalf("stat the line index: %v", err)
	}
	if err := os.Truncate(index, info.Size()-1); err != nil {
		t.Fatalf("damage the line index: %v", err)
	}

	_, err = games.UploadWindow(t.Context(), contest.ID, upload.ID, 1, 10, 4096)
	if !errors.Is(err, provisioning.ErrUploadIndexCorrupt) {
		t.Fatalf("a damaged line index answered %v, want ErrUploadIndexCorrupt", err)
	}
}

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

func TestSweepUploadsAbandonsAStaleUploadAndRemovesAFileWithNoRow(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	stale, err := games.BeginUpload(t.Context(), contest.ID, "forgotten.sql", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Aged rather than waited for.
	if _, err := storage.QuerierFrom(t.Context(), testPool).Exec(t.Context(),
		`UPDATE game_uploads SET updated_at = now() - interval '2 days' WHERE id = $1`, stale.ID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	// A file with no row, as a crash between Store.Begin and the INSERT
	// would leave.
	orphanID := uuid.New()
	if err := storeOn(t, dir).Begin(orphanID.String(), 1<<16); err != nil {
		t.Fatalf("reserve an orphan file: %v", err)
	}
	// Aged past orphanFileGrace.
	age(t, dir, orphanID, time.Hour)

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

// The file goes before the row is marked. Only a failing removal shows the
// order, so a read-only directory makes the unlink fail.
func TestAbortLeavesTheRowReceivingWhenTheFileCannotBeRemoved(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)

	upload, err := games.BeginUpload(t.Context(), contest.ID, "dump.sql", 1024)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	// Unlinking needs write permission on the directory, not the file.
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

	// A retry with the directory writable again finishes the abort.
	if _, err := games.AbortUpload(t.Context(), uuid.New(), contest.ID, upload.ID); err != nil {
		t.Fatalf("retry after restoring permissions: %v", err)
	}
	if onDisk(t, dir, upload.ID) {
		t.Fatal("the retried abort left the file behind")
	}
}

// editableUntil answers GameEditable true for its first left calls and false
// afterwards. With left: 1 a contest starts between CompleteUpload's own
// check and replaceGame's.
type editableUntil struct {
	mu   sync.Mutex
	left int
}

func (e *editableUntil) GameEditable(context.Context, uuid.UUID) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.left <= 0 {
		return false, nil
	}
	e.left--
	return true, nil
}

// storeOn opens a second handle on the directory, to plant a file behind the
// service's back.
func storeOn(t *testing.T, dir string) *gamefile.Store {
	t.Helper()
	store, err := gamefile.NewStore(dir, uploadLimits)
	if err != nil {
		t.Fatalf("open a second handle on the upload directory: %v", err)
	}
	return store
}

// age backdates an upload's file so the sweep's minimum file age is passed.
func age(t *testing.T, dir string, id uuid.UUID, by time.Duration) {
	t.Helper()
	when := time.Now().Add(-by)
	if err := os.Chtimes(filepath.Join(dir, id.String()+".data"), when, when); err != nil {
		t.Fatalf("age the upload's file: %v", err)
	}
}

// A file left behind here fills GAME_UPLOAD_MAX_DIR_BYTES for the whole
// installation after a few switches.
func TestSwitchingToTheEditorRetiresTheUploadedFileItReplaces(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	actor := uuid.New()

	upload := beginWithContent(t, games, contest.ID, "dump.sql", "A;\n")
	if _, err := games.CompleteUpload(t.Context(), actor, contest.ID, upload.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	template, err := games.SetScript(t.Context(), actor, contest.ID, "CREATE TABLE t (id int);\n")
	if err != nil {
		t.Fatalf("set script: %v", err)
	}
	if template.Source != provisioning.SourceEditor {
		t.Fatalf("source = %q, want editor", template.Source)
	}
	if onDisk(t, dir, upload.ID) {
		t.Fatal("the file the editor's script displaced is still on disk, and nothing else will ever remove it")
	}
}

// The displaced file goes only after the replacing transaction commits. A
// contest that starts while the file is hashed keeps its old game, which
// still needs the old file for every rebuild.
func TestAReplacementRefusedInsideItsTransactionLeavesTheDisplacedFileAlone(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	actor := uuid.New()

	first := beginWithContent(t, games, contest.ID, "first.sql", "A;\n")
	if _, err := games.CompleteUpload(t.Context(), actor, contest.ID, first.ID); err != nil {
		t.Fatalf("complete first: %v", err)
	}
	second := beginWithContent(t, games, contest.ID, "second.sql", "B;\n")

	racing := provisioning.NewGames(postgres.NewGameInstances(testPool), &buildCluster{}, &editableUntil{left: 1}).
		WithUploads(storeOn(t, dir), uploadLimits)

	if _, err := racing.CompleteUpload(t.Context(), actor, contest.ID, second.ID); !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Fatalf("complete second = %v, want ErrGameNotEditable", err)
	}

	template, err := games.Of(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	if template.UploadID == nil || *template.UploadID != first.ID {
		t.Fatalf("the game names upload %v, want the undisplaced %s", template.UploadID, first.ID)
	}
	if !onDisk(t, dir, first.ID) {
		t.Fatal("the displaced file was removed although the transaction that would have replaced it was refused — " +
			"the game still names it, and every rebuild from here fails")
	}
}

// A crash between the commit and the unlink leaves a file whose row exists
// but which no game names.
func TestSweepUploadsRemovesAFileNoGameNamesAnyMore(t *testing.T) {
	games, dir := gamesWithUploads(t, true)
	contest, _ := contestFor(t, t.Context(), 0)
	actor := uuid.New()

	upload := beginWithContent(t, games, contest.ID, "dump.sql", "A;\n")
	if _, err := games.CompleteUpload(t.Context(), actor, contest.ID, upload.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := games.SetScript(t.Context(), actor, contest.ID, "CREATE TABLE t (id int);\n"); err != nil {
		t.Fatalf("set script: %v", err)
	}

	// Put the file back as such a crash would have left it.
	if err := storeOn(t, dir).Begin(upload.ID.String(), 1<<16); err != nil {
		t.Fatalf("plant the file a crash would have left: %v", err)
	}
	age(t, dir, upload.ID, time.Hour)

	result, err := games.SweepUploads(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.OrphanFiles != 1 {
		t.Fatalf("orphan files = %d, want 1", result.OrphanFiles)
	}
	if onDisk(t, dir, upload.ID) {
		t.Fatal("a file no contest's game names any more survived the sweep because its row still exists")
	}
}

// BeginUpload reserves the file before its row commits; see orphanFileGrace.
func TestSweepUploadsLeavesAFileTooYoungToBeAnOrphan(t *testing.T) {
	games, dir := gamesWithUploads(t, true)

	fresh := uuid.New()
	if err := storeOn(t, dir).Begin(fresh.String(), 1<<16); err != nil {
		t.Fatalf("reserve a file the way BeginUpload does: %v", err)
	}

	result, err := games.SweepUploads(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.OrphanFiles != 0 {
		t.Fatalf("orphan files = %d, want 0 — a reservation made moments ago is not an orphan", result.OrphanFiles)
	}
	if !onDisk(t, dir, fresh) {
		t.Fatal("a file reserved moments ago was swept away; BeginUpload's own INSERT had not even committed yet")
	}
}

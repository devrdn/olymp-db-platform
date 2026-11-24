package provisioning_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/gamefile"
	"github.com/devrdn/olymp-db-platform/backend/internal/gamefile/gamefiletest"
	"github.com/devrdn/olymp-db-platform/backend/internal/postgres"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/google/uuid"
)

// chunkedKind is one kind of chunked upload as the lifecycle suite drives it:
// a dump or a table's CSV, over the in-memory rows and a real gamefile.Store
// on a temp directory.
type chunkedKind[R provisioning.UploadRow[R]] struct {
	// limits is what open gives the kind's store.
	limits gamefile.Limits
	// open assembles Games with this kind's volume on dir, or with none when
	// dir is empty, and a contest an upload of name may begin in.
	open func(t *testing.T, dir string) (games *provisioning.Games, store *templateStore, contest uuid.UUID)
	// contest makes another contest of games an upload of name may begin in.
	contest func(t *testing.T, games *provisioning.Games) uuid.UUID
	// uploads is the kind's lifecycle on games.
	uploads func(games *provisioning.Games) *provisioning.Uploads[R]
	// name is what an upload of this kind is of.
	name string
	// state reads what the suite asserts on.
	state func(R) (id uuid.UUID, received int64, status provisioning.UploadStatus)
	// stored reads the row as the store holds it.
	stored func(store *templateStore, id uuid.UUID) R
}

// The two kinds' limits are small, so the ceilings can be reached. The table
// store buffers its chunks, as the deployed one must.
var (
	dumpLimits  = gamefile.Limits{MaxFileBytes: 1 << 10, MaxDirBytes: 4 << 10, MaxChunkBytes: 64}
	tableLimits = gamefile.Limits{MaxFileBytes: 1 << 10, MaxDirBytes: 4 << 10, MaxChunkBytes: 64, BufferedChunks: 2}
)

var (
	dumpKind = chunkedKind[provisioning.Upload]{
		limits: dumpLimits,
		open: func(t *testing.T, dir string) (*provisioning.Games, *templateStore, uuid.UUID) {
			games, store, _ := games(true)
			if dir != "" {
				games.WithUploads(chunkedStoreOn(t, dir, dumpLimits), dumpLimits)
			}
			return games, store, uuid.New()
		},
		contest: func(*testing.T, *provisioning.Games) uuid.UUID { return uuid.New() },
		uploads: func(g *provisioning.Games) *provisioning.Uploads[provisioning.Upload] { return g.DumpUploads().Uploads },
		name:    "dump.sql",
		state: func(u provisioning.Upload) (uuid.UUID, int64, provisioning.UploadStatus) {
			return u.ID, u.ReceivedBytes, u.Status
		},
		stored: func(store *templateStore, id uuid.UUID) provisioning.Upload {
			store.mu.Lock()
			defer store.mu.Unlock()
			return store.uploads[id]
		},
	}

	tableKind = chunkedKind[provisioning.TableData]{
		limits: tableLimits,
		open: func(t *testing.T, dir string) (*provisioning.Games, *templateStore, uuid.UUID) {
			games, store, _ := games(true)
			if dir != "" {
				games.WithTableData(chunkedStoreOn(t, dir, tableLimits), tableLimits)
			}
			return games, store, suspectsContest(t, games)
		},
		contest: suspectsContest,
		uploads: (*provisioning.Games).TableUploads,
		name:    "suspects",
		state: func(d provisioning.TableData) (uuid.UUID, int64, provisioning.UploadStatus) {
			return d.ID, d.ReceivedBytes, d.Status
		},
		stored: func(store *templateStore, id uuid.UUID) provisioning.TableData {
			store.mu.Lock()
			defer store.mu.Unlock()
			return store.tableData[id]
		},
	}
)

// suspectsContest is a contest of games whose definition holds suspectsTable.
func suspectsContest(t *testing.T, games *provisioning.Games) uuid.UUID {
	contest := uuid.New()
	withSuspects(t, games, contest)
	return contest
}

func chunkedStoreOn(t *testing.T, dir string, limits gamefile.Limits) *gamefile.Store {
	t.Helper()
	store, err := gamefile.NewStore(dir, limits)
	if err != nil {
		t.Fatalf("open the upload store: %v", err)
	}
	return store
}

// mustBegin begins an upload of name a case goes on to use, so a refused
// begin fails the case rather than leaving it to test uuid.Nil.
func mustBegin[R provisioning.UploadRow[R]](t *testing.T, uploads *provisioning.Uploads[R], contest uuid.UUID, name string) R {
	t.Helper()
	begun, err := uploads.Begin(t.Context(), contest, name, 100)
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}
	return begun
}

// filesIn counts what dir holds, so a refusal can be shown to reserve nothing.
func filesIn(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the upload directory: %v", err)
	}
	return len(entries)
}

// A dump and a table's CSV are the same lifecycle; each case runs for both.
func TestEveryKindOfUploadKeepsTheSameLifecycle(t *testing.T) {
	t.Run("dump", func(t *testing.T) { lifecycleSuite(t, dumpKind) })
	t.Run("table data", func(t *testing.T) { lifecycleSuite(t, tableKind) })
}

func lifecycleSuite[R provisioning.UploadRow[R]](t *testing.T, kind chunkedKind[R]) {
	// The header line of a table's CSV is checked on the first chunk, so the
	// bytes sent are a header the table accepts, which any dump is too.
	const header = "id,name,nickname\n"

	t.Run("a begun upload has a file and a row and has received nothing", func(t *testing.T) {
		dir := t.TempDir()
		games, store, contest := kind.open(t, dir)

		begun, err := kind.uploads(games).Begin(t.Context(), contest, kind.name, 100)
		if err != nil {
			t.Fatalf("Begin() = %v", err)
		}
		id, received, status := kind.state(begun)
		if status != provisioning.UploadReceiving || received != 0 {
			t.Fatalf("begun as %q with %d bytes, want receiving with none", status, received)
		}
		if _, _, stored := kind.state(kind.stored(store, id)); stored != provisioning.UploadReceiving {
			t.Fatalf("the stored row is %q, want receiving", stored)
		}
		if filesIn(t, dir) == 0 {
			t.Fatal("no file was reserved for the upload")
		}
		if limits, enabled := kind.uploads(games).Limits(); !enabled || limits != kind.limits {
			t.Fatalf("Limits() = %+v, %v; want the store's own %+v", limits, enabled, kind.limits)
		}
	})

	t.Run("a retried chunk writes no record", func(t *testing.T) {
		games, store, contest := kind.open(t, t.TempDir())
		uploads := kind.uploads(games)
		begun, err := uploads.Begin(t.Context(), contest, kind.name, int64(len(header)))
		if err != nil {
			t.Fatalf("Begin() = %v", err)
		}
		id, _, _ := kind.state(begun)

		for attempt := range 2 {
			received, err := uploads.Append(t.Context(), uuid.New(), contest, id, 0, strings.NewReader(header))
			if err != nil || received != int64(len(header)) {
				t.Fatalf("attempt %d: Append() = %d, %v; want %d", attempt+1, received, err, len(header))
			}
		}

		if writes := store.progressRecorded(); writes != 1 {
			t.Fatalf("the progress was recorded %d times, want once: the retry had nothing to record", writes)
		}
		if _, received, _ := kind.state(kind.stored(store, id)); received != int64(len(header)) {
			t.Fatalf("the record holds %d bytes, want %d", received, len(header))
		}
	})

	t.Run("a chunk that does not continue the upload is refused and recorded nowhere", func(t *testing.T) {
		games, store, contest := kind.open(t, t.TempDir())
		uploads := kind.uploads(games)
		begun := mustBegin(t, uploads, contest, kind.name)
		id, _, _ := kind.state(begun)

		_, err := uploads.Append(t.Context(), uuid.New(), contest, id, 10, strings.NewReader("ahead"))
		if !errors.Is(err, provisioning.ErrUploadChunkOutOfOrder) {
			t.Fatalf("Append() past the end = %v, want ErrUploadChunkOutOfOrder", err)
		}
		if writes := store.progressRecorded(); writes != 0 {
			t.Fatalf("a refused chunk recorded progress %d times", writes)
		}
	})

	t.Run("a chunk past the chunk ceiling is refused", func(t *testing.T) {
		games, _, contest := kind.open(t, t.TempDir())
		uploads := kind.uploads(games)
		begun := mustBegin(t, uploads, contest, kind.name)
		id, _, _ := kind.state(begun)

		tooLong := strings.Repeat("x", int(kind.limits.MaxChunkBytes)+1)
		if _, err := uploads.Append(t.Context(), uuid.New(), contest, id, 0, strings.NewReader(tooLong)); !errors.Is(err, provisioning.ErrUploadChunkTooLarge) {
			t.Fatalf("Append() = %v, want ErrUploadChunkTooLarge", err)
		}
	})

	// internal/api caps a chunk's body at the chunk ceiling too (CLAUDE.md
	// rule 12). Only a read one byte past the ceiling meets the cap, and the
	// cause must survive the mapping, or the route would answer "send it
	// again" for a chunk that can never fit.
	t.Run("a body the transport cut off at the chunk ceiling keeps nothing and says so", func(t *testing.T) {
		dir := t.TempDir()
		games, _, contest := kind.open(t, dir)
		uploads := kind.uploads(games)
		begun := mustBegin(t, uploads, contest, kind.name)
		id, _, _ := kind.state(begun)

		body := http.MaxBytesReader(nil, io.NopCloser(strings.NewReader(strings.Repeat("x", int(kind.limits.MaxChunkBytes)+1))),
			kind.limits.MaxChunkBytes)
		_, err := uploads.Append(t.Context(), uuid.New(), contest, id, 0, body)

		var capped *http.MaxBytesError
		if !errors.As(err, &capped) && !errors.Is(err, provisioning.ErrUploadChunkTooLarge) {
			t.Fatalf("Append() = %v, want the transport's MaxBytesError or ErrUploadChunkTooLarge", err)
		}
		if stored, err := gamefiletest.StoredBytes(chunkedStoreOn(t, dir, kind.limits), id.String()); err != nil || stored != 0 {
			t.Fatalf("the store kept %d bytes (%v) of a chunk it refused, want 0", stored, err)
		}
	})

	t.Run("a declared length past the file ceiling reserves nothing", func(t *testing.T) {
		dir := t.TempDir()
		games, _, contest := kind.open(t, dir)

		_, err := kind.uploads(games).Begin(t.Context(), contest, kind.name, kind.limits.MaxFileBytes+1)
		if !errors.Is(err, provisioning.ErrUploadTooLarge) {
			t.Fatalf("Begin() = %v, want ErrUploadTooLarge", err)
		}
		if n := filesIn(t, dir); n != 0 {
			t.Fatalf("the refused begin left %d file(s)", n)
		}
	})

	t.Run("a second upload while one is receiving is refused and gives back its reservation", func(t *testing.T) {
		dir := t.TempDir()
		games, _, contest := kind.open(t, dir)
		uploads := kind.uploads(games)
		if _, err := uploads.Begin(t.Context(), contest, kind.name, 100); err != nil {
			t.Fatalf("first Begin() = %v", err)
		}
		before := filesIn(t, dir)

		if _, err := uploads.Begin(t.Context(), contest, kind.name, 100); !errors.Is(err, provisioning.ErrUploadInProgress) {
			t.Fatalf("second Begin() = %v, want ErrUploadInProgress", err)
		}
		if after := filesIn(t, dir); after != before {
			t.Fatalf("the directory went from %d to %d entries: the refused begin kept its file", before, after)
		}
	})

	t.Run("another contest's upload is not found", func(t *testing.T) {
		games, _, contest := kind.open(t, t.TempDir())
		uploads := kind.uploads(games)
		begun := mustBegin(t, uploads, contest, kind.name)
		id, _, _ := kind.state(begun)
		other := uuid.New()

		if _, err := uploads.Append(t.Context(), uuid.New(), other, id, 0, strings.NewReader(header)); !errors.Is(err, provisioning.ErrUploadNotFound) {
			t.Fatalf("Append() from another contest = %v, want ErrUploadNotFound", err)
		}
		if _, err := uploads.Abort(t.Context(), uuid.New(), other, id); !errors.Is(err, provisioning.ErrUploadNotFound) {
			t.Fatalf("Abort() from another contest = %v, want ErrUploadNotFound", err)
		}
		if _, err := uploads.Get(t.Context(), other, id); !errors.Is(err, provisioning.ErrUploadNotFound) {
			t.Fatalf("Get() from another contest = %v, want ErrUploadNotFound", err)
		}
	})

	t.Run("a chunk for a file the volume lost is not found", func(t *testing.T) {
		dir := t.TempDir()
		games, _, contest := kind.open(t, dir)
		uploads := kind.uploads(games)
		begun := mustBegin(t, uploads, contest, kind.name)
		id, _, _ := kind.state(begun)
		if err := chunkedStoreOn(t, dir, kind.limits).Abort(id.String()); err != nil {
			t.Fatalf("remove the file behind the row: %v", err)
		}

		if _, err := uploads.Append(t.Context(), uuid.New(), contest, id, 0, strings.NewReader(header)); !errors.Is(err, provisioning.ErrUploadNotFound) {
			t.Fatalf("Append() = %v, want ErrUploadNotFound", err)
		}
	})

	t.Run("an aborted upload loses its file and takes nothing more", func(t *testing.T) {
		games, store, contest := kind.open(t, t.TempDir())
		uploads := kind.uploads(games)
		begun := mustBegin(t, uploads, contest, kind.name)
		id, _, _ := kind.state(begun)

		aborted, err := uploads.Abort(t.Context(), uuid.New(), contest, id)
		if err != nil {
			t.Fatalf("Abort() = %v", err)
		}
		if _, _, status := kind.state(aborted); status != provisioning.UploadAborted {
			t.Fatalf("Abort() answered %q, want aborted", status)
		}
		if _, _, status := kind.state(kind.stored(store, id)); status != provisioning.UploadAborted {
			t.Fatalf("the stored row is %q, want aborted", status)
		}
		if _, err := uploads.Append(t.Context(), uuid.New(), contest, id, 0, strings.NewReader(header)); !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
			t.Fatalf("Append() after the abort = %v, want ErrUploadAlreadyComplete", err)
		}
		if _, err := uploads.Abort(t.Context(), uuid.New(), contest, id); !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
			t.Fatalf("a second Abort() = %v, want ErrUploadAlreadyComplete", err)
		}
		if _, err := uploads.Begin(t.Context(), contest, kind.name, 100); err != nil {
			t.Fatalf("Begin() after the abort = %v; nothing is receiving any more", err)
		}
	})

	t.Run("the janitor aborts an abandoned upload and removes an old file no row names, and nothing younger", func(t *testing.T) {
		dir := t.TempDir()
		games, store, contest := kind.open(t, dir)
		uploads := kind.uploads(games)
		stale := mustBegin(t, uploads, contest, kind.name)
		staleID, _, _ := kind.state(stale)
		store.ageUpload(staleID, 48*time.Hour)
		fresh, err := uploads.Begin(t.Context(), kind.contest(t, games), kind.name, 100)
		if err != nil {
			t.Fatalf("Begin() in another contest = %v", err)
		}
		freshID, _, _ := kind.state(fresh)

		// A file with no row, as a crash between reservation and INSERT
		// leaves, and one too young to tell from an INSERT still to commit.
		orphan, youngOrphan := uuid.New(), uuid.New()
		for _, id := range []uuid.UUID{orphan, youngOrphan} {
			if err := chunkedStoreOn(t, dir, kind.limits).Begin(id.String(), 10); err != nil {
				t.Fatalf("reserve an orphan file: %v", err)
			}
		}
		age(t, dir, orphan, time.Hour)

		result, err := games.SweepUploads(t.Context(), 24*time.Hour)
		if err != nil {
			t.Fatalf("SweepUploads() = %v", err)
		}
		if result.Abandoned != 1 || result.OrphanFiles != 1 {
			t.Fatalf("swept %+v, want one abandoned upload and one orphan file", result)
		}
		files := chunkedStoreOn(t, dir, kind.limits)
		for _, gone := range []uuid.UUID{staleID, orphan} {
			if _, err := gamefiletest.StoredBytes(files, gone.String()); !errors.Is(err, gamefile.ErrNotFound) {
				t.Errorf("file %s is still on the volume: %v", gone, err)
			}
		}
		for name, kept := range map[string]uuid.UUID{"a fresh upload's": freshID, "a young orphan's": youngOrphan} {
			if _, err := gamefiletest.StoredBytes(files, kept.String()); err != nil {
				t.Errorf("%s file was swept: %v", name, err)
			}
		}
		if _, _, status := kind.state(kind.stored(store, staleID)); status != provisioning.UploadAborted {
			t.Errorf("the abandoned row is %q, want aborted", status)
		}
	})

	t.Run("without a volume every call is refused as disabled", func(t *testing.T) {
		games, _, contest := kind.open(t, "")
		uploads := kind.uploads(games)

		if _, enabled := uploads.Limits(); enabled {
			t.Error("Limits() reports a volume that was never configured")
		}
		if _, err := uploads.Begin(t.Context(), contest, kind.name, 100); !errors.Is(err, provisioning.ErrUploadsDisabled) {
			t.Errorf("Begin() = %v, want ErrUploadsDisabled", err)
		}
		if _, err := uploads.Append(t.Context(), uuid.New(), contest, uuid.New(), 0, strings.NewReader(header)); !errors.Is(err, provisioning.ErrUploadsDisabled) {
			t.Errorf("Append() = %v, want ErrUploadsDisabled", err)
		}
		if _, err := uploads.Abort(t.Context(), uuid.New(), contest, uuid.New()); !errors.Is(err, provisioning.ErrUploadsDisabled) {
			t.Errorf("Abort() = %v, want ErrUploadsDisabled", err)
		}
		if result, err := games.SweepUploads(t.Context(), time.Hour); err != nil || result != (provisioning.UploadCleanupResult{}) {
			t.Errorf("SweepUploads() = %+v, %v; want nothing swept and no error", result, err)
		}
	})
}

// The two kinds share the module, not their rows or their stores. Production
// keeps the table store in a directory inside the dump's, as here.
func TestTheTwoKindsOfUploadNeverReachEachOthersUploads(t *testing.T) {
	dumpDir := t.TempDir()
	tableDir := filepath.Join(dumpDir, "tables")
	games, store, _ := games(true)
	games.WithUploads(chunkedStoreOn(t, dumpDir, dumpLimits), dumpLimits)
	games.WithTableData(chunkedStoreOn(t, tableDir, tableLimits), tableLimits)
	dumps, tables := games.DumpUploads(), games.TableUploads()

	mine, other := suspectsContest(t, games), suspectsContest(t, games)
	dump := mustBegin(t, dumps.Uploads, mine, "dump.sql")
	table := mustBegin(t, tables, mine, "suspects")
	otherDump := mustBegin(t, dumps.Uploads, other, "dump.sql")
	otherTable := mustBegin(t, tables, other, "suspects")

	t.Run("an id of the other kind is not found", func(t *testing.T) {
		body := strings.NewReader("id,name,nickname\n")
		for name, err := range map[string]error{
			"a dump's chunk on the table route": func() error {
				_, err := tables.Append(t.Context(), uuid.New(), mine, dump.ID, 0, body)
				return err
			}(),
			"a dump's abort on the table route": func() error {
				_, err := tables.Abort(t.Context(), uuid.New(), mine, dump.ID)
				return err
			}(),
			"a dump completed on the table route": func() error {
				_, err := games.CompleteTableUpload(t.Context(), uuid.New(), mine, dump.ID)
				return err
			}(),
			"a table's chunk on the dump route": func() error {
				_, err := dumps.Append(t.Context(), uuid.New(), mine, table.ID, 0, body)
				return err
			}(),
			"a table's abort on the dump route": func() error {
				_, err := dumps.Abort(t.Context(), uuid.New(), mine, table.ID)
				return err
			}(),
			"a table completed on the dump route": func() error {
				_, err := games.CompleteUpload(t.Context(), uuid.New(), mine, table.ID)
				return err
			}(),
			"a table's window on the dump route": func() error {
				_, err := dumps.Window(t.Context(), mine, table.ID, 1, 10, 1<<10)
				return err
			}(),
		} {
			if !errors.Is(err, provisioning.ErrUploadNotFound) {
				t.Errorf("%s = %v, want ErrUploadNotFound", name, err)
			}
		}
	})

	t.Run("each sweep leaves the other kind's and other contests' files", func(t *testing.T) {
		// Every file is old enough to be an orphan if its own kind's rows did
		// not name it, and one dump is abandoned.
		for _, id := range []uuid.UUID{dump.ID, otherDump.ID} {
			age(t, dumpDir, id, time.Hour)
		}
		for _, id := range []uuid.UUID{table.ID, otherTable.ID} {
			age(t, tableDir, id, time.Hour)
		}
		store.ageUpload(otherDump.ID, 48*time.Hour)

		result, err := games.SweepUploads(t.Context(), 24*time.Hour)
		if err != nil {
			t.Fatalf("SweepUploads() = %v", err)
		}
		if result.Abandoned != 1 || result.OrphanFiles != 0 {
			t.Fatalf("swept %+v, want the one abandoned dump and no orphan", result)
		}
		dumpFiles, tableFiles := chunkedStoreOn(t, dumpDir, dumpLimits), chunkedStoreOn(t, tableDir, tableLimits)
		if _, err := gamefiletest.StoredBytes(dumpFiles, otherDump.ID.String()); !errors.Is(err, gamefile.ErrNotFound) {
			t.Errorf("the abandoned dump's file is still there: %v", err)
		}
		for name, kept := range map[string]struct {
			files *gamefile.Store
			id    uuid.UUID
		}{
			"this contest's dump":       {dumpFiles, dump.ID},
			"this contest's table":      {tableFiles, table.ID},
			"the other contest's table": {tableFiles, otherTable.ID},
		} {
			if _, err := gamefiletest.StoredBytes(kept.files, kept.id.String()); err != nil {
				t.Errorf("%s file was swept: %v", name, err)
			}
		}
	})
}

// uploadRowsTarget is one implementation of provisioning.UploadRows, with what
// the contract needs to set its cases up.
type uploadRowsTarget[R any] struct {
	rows provisioning.UploadRows[R]
	// withOwner, set for the kind that bounds chunks by owner, is its
	// TableDataRows.WithOwner.
	withOwner func(ctx context.Context, id uuid.UUID) (R, uuid.UUID, error)
	// newContest makes a contest uploads may belong to, and names its creator.
	newContest func() (contest, owner uuid.UUID)
	// age moves an upload's last activity back by by.
	age   func(id uuid.UUID, by time.Duration)
	state func(R) (id, contest uuid.UUID, name string, received int64, status provisioning.UploadStatus)
}

// The in-memory rows the lifecycle suite runs on are held to what PostgreSQL
// does, for both kinds, so the suite cannot pass on a fake that drifted.
func TestUploadRowsInMemoryKeepThePostgresContract(t *testing.T) {
	dumpState := func(u provisioning.Upload) (uuid.UUID, uuid.UUID, string, int64, provisioning.UploadStatus) {
		return u.ID, u.ContestID, u.Filename, u.ReceivedBytes, u.Status
	}
	tableState := func(d provisioning.TableData) (uuid.UUID, uuid.UUID, string, int64, provisioning.UploadStatus) {
		return d.ID, d.ContestID, d.Table, d.ReceivedBytes, d.Status
	}
	// In memory every contest is its own owner (templateStore.ownerOf).
	inMemory := func() (uuid.UUID, uuid.UUID) {
		contest := uuid.New()
		return contest, contest
	}

	t.Run("dumps in memory", func(t *testing.T) {
		uploadRowsContract(t, "dump.sql", func(t *testing.T, run func(context.Context, uploadRowsTarget[provisioning.Upload])) {
			store := &templateStore{}
			run(t.Context(), uploadRowsTarget[provisioning.Upload]{
				rows: store.dumpRows(), newContest: inMemory, age: store.ageUpload, state: dumpState,
			})
		})
	})
	t.Run("table data in memory", func(t *testing.T) {
		uploadRowsContract(t, "suspects", func(t *testing.T, run func(context.Context, uploadRowsTarget[provisioning.TableData])) {
			store := &templateStore{}
			run(t.Context(), uploadRowsTarget[provisioning.TableData]{
				rows: store.tableRows(), withOwner: store.tableRows().WithOwner,
				newContest: inMemory, age: store.ageUpload, state: tableState,
			})
		})
	})
	// Committed rather than rolled back: a refused INSERT ends the transaction
	// it runs in, and contestFor removes each contest, its uploads with it.
	t.Run("dumps on postgres", func(t *testing.T) {
		uploadRowsContract(t, "dump.sql", func(t *testing.T, run func(context.Context, uploadRowsTarget[provisioning.Upload])) {
			skipWithoutDatabase(t)
			run(t.Context(), uploadRowsTarget[provisioning.Upload]{
				rows: postgres.NewGameUploads(testPool), newContest: contestsIn(t),
				age: agedIn(t, "game_uploads"), state: dumpState,
			})
		})
	})
	t.Run("table data on postgres", func(t *testing.T) {
		uploadRowsContract(t, "suspects", func(t *testing.T, run func(context.Context, uploadRowsTarget[provisioning.TableData])) {
			skipWithoutDatabase(t)
			rows := postgres.NewGameTableData(testPool)
			run(t.Context(), uploadRowsTarget[provisioning.TableData]{
				rows: rows, withOwner: rows.WithOwner, newContest: contestsIn(t),
				age: agedIn(t, "game_table_data"), state: tableState,
			})
		})
	})
}

func skipWithoutDatabase(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
}

// contestsIn makes contests on the test database for the length of t.
func contestsIn(t *testing.T) func() (uuid.UUID, uuid.UUID) {
	return func() (uuid.UUID, uuid.UUID) {
		t.Helper()
		contest, _ := contestFor(t, t.Context(), 0)
		var owner uuid.UUID
		if err := testPool.QueryRow(t.Context(),
			`SELECT created_by FROM contests WHERE id = $1`, contest.ID).Scan(&owner); err != nil {
			t.Fatalf("read the contest's owner: %v", err)
		}
		return contest.ID, owner
	}
}

// agedIn ages a row of table, one of the two upload tables.
func agedIn(t *testing.T, table string) func(uuid.UUID, time.Duration) {
	return func(id uuid.UUID, by time.Duration) {
		t.Helper()
		if _, err := testPool.Exec(t.Context(),
			`UPDATE `+table+` SET updated_at = updated_at - $2 * interval '1 second' WHERE id = $1`,
			id, by.Seconds()); err != nil {
			t.Fatalf("age the row: %v", err)
		}
	}
}

// uploadRowsContract is what every provisioning.UploadRows must do; name is
// what an upload of the kind is of. each prepares a fresh target for one case
// and calls run with it. Abandoned lists installation-wide, so its case looks
// only at the rows it made.
func uploadRowsContract[R any](t *testing.T, name string, each func(t *testing.T, run func(context.Context, uploadRowsTarget[R]))) {
	t.Run("a begun upload reads back receiving with nothing received, and with its contest's owner where the kind reads one", func(t *testing.T) {
		each(t, func(ctx context.Context, target uploadRowsTarget[R]) {
			contest, wantOwner := target.newContest()
			id := uuid.New()
			if _, err := target.rows.Begin(ctx, id, contest, name, 100); err != nil {
				t.Fatalf("Begin() = %v", err)
			}

			row, err := target.rows.ByID(ctx, id)
			if err != nil {
				t.Fatalf("ByID() = %v", err)
			}
			gotID, gotContest, gotName, received, status := target.state(row)
			if gotID != id || gotContest != contest || gotName != name || received != 0 || status != provisioning.UploadReceiving {
				t.Errorf("read back %v %v %q %d %q, want %v %v %q 0 receiving",
					gotID, gotContest, gotName, received, status, id, contest, name)
			}
			if target.withOwner == nil {
				return
			}
			withOwner, owner, err := target.withOwner(ctx, id)
			if err != nil {
				t.Fatalf("WithOwner() = %v", err)
			}
			if ownedID, _, _, _, _ := target.state(withOwner); ownedID != id || owner != wantOwner {
				t.Errorf("WithOwner() = %v owned by %v, want %v owned by %v", ownedID, owner, id, wantOwner)
			}
		})
	})

	t.Run("an id nothing began is not found", func(t *testing.T) {
		each(t, func(ctx context.Context, target uploadRowsTarget[R]) {
			if _, err := target.rows.ByID(ctx, uuid.New()); !errors.Is(err, provisioning.ErrUploadNotFound) {
				t.Errorf("ByID() = %v, want ErrUploadNotFound", err)
			}
			if target.withOwner == nil {
				return
			}
			if _, _, err := target.withOwner(ctx, uuid.New()); !errors.Is(err, provisioning.ErrUploadNotFound) {
				t.Errorf("WithOwner() = %v, want ErrUploadNotFound", err)
			}
		})
	})

	t.Run("one upload of a thing receives at a time", func(t *testing.T) {
		each(t, func(ctx context.Context, target uploadRowsTarget[R]) {
			contest, _ := target.newContest()
			other, _ := target.newContest()
			first := uuid.New()
			if _, err := target.rows.Begin(ctx, first, contest, name, 100); err != nil {
				t.Fatalf("first Begin() = %v", err)
			}
			if _, err := target.rows.Begin(ctx, uuid.New(), contest, name, 100); !errors.Is(err, provisioning.ErrUploadInProgress) {
				t.Fatalf("second Begin() = %v, want ErrUploadInProgress", err)
			}
			if _, err := target.rows.Begin(ctx, uuid.New(), other, name, 100); err != nil {
				t.Fatalf("another contest's Begin() = %v", err)
			}
			if err := target.rows.Abort(ctx, first); err != nil {
				t.Fatalf("Abort() = %v", err)
			}
			if _, err := target.rows.Begin(ctx, uuid.New(), contest, name, 100); err != nil {
				t.Fatalf("Begin() after the abort = %v", err)
			}
		})
	})

	t.Run("progress only moves forward and stops once the upload no longer receives", func(t *testing.T) {
		each(t, func(ctx context.Context, target uploadRowsTarget[R]) {
			contest, _ := target.newContest()
			id := uuid.New()
			if _, err := target.rows.Begin(ctx, id, contest, name, 100); err != nil {
				t.Fatalf("Begin() = %v", err)
			}
			for _, received := range []int64{40, 25} {
				if err := target.rows.UpdateReceived(ctx, id, received); err != nil {
					t.Fatalf("UpdateReceived(%d) = %v", received, err)
				}
			}
			if err := target.rows.Abort(ctx, id); err != nil {
				t.Fatalf("Abort() = %v", err)
			}
			if err := target.rows.UpdateReceived(ctx, id, 60); !errors.Is(err, provisioning.ErrUploadAlreadyComplete) {
				t.Fatalf("UpdateReceived() after the abort = %v, want ErrUploadAlreadyComplete", err)
			}

			row, err := target.rows.ByID(ctx, id)
			if err != nil {
				t.Fatalf("ByID() = %v", err)
			}
			if _, _, _, received, status := target.state(row); received != 40 || status != provisioning.UploadAborted {
				t.Errorf("read back %d bytes, %q; want 40, aborted", received, status)
			}
		})
	})

	t.Run("only an upload idle since before the cutoff is abandoned", func(t *testing.T) {
		each(t, func(ctx context.Context, target uploadRowsTarget[R]) {
			begin := func() uuid.UUID {
				t.Helper()
				contest, _ := target.newContest()
				id := uuid.New()
				if _, err := target.rows.Begin(ctx, id, contest, name, 100); err != nil {
					t.Fatalf("Begin() = %v", err)
				}
				return id
			}
			stale, fresh, resumed, aborted := begin(), begin(), begin(), begin()
			for _, id := range []uuid.UUID{stale, resumed, aborted} {
				target.age(id, 48*time.Hour)
			}
			// A chunk is activity, and an aborted upload is nobody's to sweep.
			if err := target.rows.UpdateReceived(ctx, resumed, 10); err != nil {
				t.Fatalf("UpdateReceived() = %v", err)
			}
			if err := target.rows.Abort(ctx, aborted); err != nil {
				t.Fatalf("Abort() = %v", err)
			}
			target.age(aborted, 48*time.Hour)

			rows, err := target.rows.Abandoned(ctx, time.Now().Add(-24*time.Hour), 1000)
			if err != nil {
				t.Fatalf("Abandoned() = %v", err)
			}
			listed := map[uuid.UUID]bool{}
			for _, row := range rows {
				id, _, _, _, _ := target.state(row)
				listed[id] = true
			}
			if !listed[stale] || listed[fresh] || listed[resumed] || listed[aborted] {
				t.Errorf("abandoned: stale %v, fresh %v, resumed %v, aborted %v; want only stale",
					listed[stale], listed[fresh], listed[resumed], listed[aborted])
			}
		})
	})

	t.Run("a receiving upload is in use and an aborted one is not", func(t *testing.T) {
		each(t, func(ctx context.Context, target uploadRowsTarget[R]) {
			contest, _ := target.newContest()
			id := uuid.New()
			if inUse, err := target.rows.InUse(ctx, id); err != nil || inUse {
				t.Fatalf("InUse() of an id nothing began = %v, %v; want false", inUse, err)
			}
			if _, err := target.rows.Begin(ctx, id, contest, name, 100); err != nil {
				t.Fatalf("Begin() = %v", err)
			}
			if inUse, err := target.rows.InUse(ctx, id); err != nil || !inUse {
				t.Fatalf("InUse() while receiving = %v, %v; want true", inUse, err)
			}
			if err := target.rows.Abort(ctx, id); err != nil {
				t.Fatalf("Abort() = %v", err)
			}
			if inUse, err := target.rows.InUse(ctx, id); err != nil || inUse {
				t.Fatalf("InUse() once aborted = %v, %v; want false", inUse, err)
			}
		})
	})
}

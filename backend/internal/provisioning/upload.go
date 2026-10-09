package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/google/uuid"
)

// MaxUploadFilenameBytes bounds an upload's filename (CLAUDE.md rule 2). The
// name is only shown to the organiser, never used as a path. A CHECK in
// migration 24 enforces the same bound.
const MaxUploadFilenameBytes = 255

// validUploadFilename reports whether a name may be stored and shown.
//
// Control characters are refused. PostgreSQL rejects a NUL in text, which
// would make BeginUpload's INSERT a 500 after the file already exists
// (CLAUDE.md rule 1), and other control characters can forge log lines. The
// name is never a path, so nothing else needs sanitising.
func validUploadFilename(name string) bool {
	if name == "" || len(name) > MaxUploadFilenameBytes {
		return false
	}
	for _, r := range name {
		// Checked on the decoded rune, so U+0085 and the C1 block are refused
		// too, and a byte inside a multi-byte rune is never mistaken for one.
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// UploadStatus is where one upload has got to.
type UploadStatus string

const (
	// UploadReceiving is an upload still taking chunks, or waiting between
	// them.
	UploadReceiving UploadStatus = "receiving"
	// UploadComplete is a sealed upload that became a contest's game.
	UploadComplete UploadStatus = "complete"
	// UploadAborted is an upload cancelled, by an organiser or by the
	// janitor, before it became a game.
	UploadAborted UploadStatus = "aborted"
)

// Upload is the database's record of one file-based game upload. Its bytes
// live in gamefile.Store, under the row's ID.
type Upload struct {
	ID            uuid.UUID
	ContestID     uuid.UUID
	Filename      string
	DeclaredBytes int64
	ReceivedBytes int64
	SHA256        string
	Lines         int64
	Status        UploadStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UploadSummary is what Store.Complete measured, carried into the repository
// without depending on gamefile.Summary's shape.
type UploadSummary struct {
	Bytes  int64
	SHA256 string
	Lines  int64
}

// Why an upload could not be received, completed or read back (CLAUDE.md
// rule 1).
var (
	// ErrUploadsDisabled is every upload method's answer when no
	// GAME_UPLOAD_DIR is configured (Games was never given WithUploads).
	ErrUploadsDisabled = errors.New("file uploads are not configured on this installation")
	// ErrUploadFilenameInvalid is an empty filename, one past
	// MaxUploadFilenameBytes, or one carrying a control character.
	ErrUploadFilenameInvalid = errors.New("the upload's filename is invalid")
	// ErrUploadTooLarge is a declared length past MaxFileBytes, or actual
	// bytes that exceeded it in Store.Append.
	ErrUploadTooLarge = errors.New("the upload exceeds the maximum file size")
	// ErrUploadStoreFull is the upload directory already holding its
	// configured MaxDirBytes of other uploads.
	ErrUploadStoreFull = errors.New("the upload directory is full")
	// ErrUploadChunkOutOfOrder is a chunk whose offset does not continue
	// where the upload actually left off.
	ErrUploadChunkOutOfOrder = errors.New("the chunk does not continue where the upload left off")
	// ErrUploadChunkTooLarge is one chunk past the configured MaxChunkBytes.
	ErrUploadChunkTooLarge = errors.New("the chunk exceeds the maximum chunk size")
	// ErrUploadChunkIncomplete is a chunk body cut off before the store had
	// all of it (a dropped connection, an expired read deadline). Nothing of
	// the chunk was kept, so the client sends the same chunk again.
	ErrUploadChunkIncomplete = errors.New("the chunk body was not received in full")
	// ErrUploadLengthMismatch is Complete finding that what actually landed
	// on disk does not match what the browser declared at Begin.
	ErrUploadLengthMismatch = errors.New("the received bytes do not match the declared length")
	// ErrUploadNotFound is an id that names no upload of this contest.
	ErrUploadNotFound = errors.New("no such upload")
	// ErrUploadInProgress is a second Begin for a contest that already has an
	// upload 'receiving', refused by the game_uploads_one_receiving_idx index.
	ErrUploadInProgress = errors.New("this contest already has an upload in progress")
	// ErrUploadAlreadyComplete is an Append, Complete or Abort against an
	// upload no longer 'receiving' — sealed already, or already cancelled.
	ErrUploadAlreadyComplete = errors.New("the upload has already been completed or cancelled")
	// ErrUploadIncomplete is a Window asked of an upload Complete has not
	// sealed yet — there is no line index to read.
	ErrUploadIncomplete = errors.New("the upload has not been completed yet")
	// ErrUploadIndexCorrupt is a completed upload whose line index no longer
	// matches its file (a truncated write, a damaged disk). The organiser has
	// to upload the file again; retrying the read does not help.
	ErrUploadIndexCorrupt = errors.New("the upload's line index is damaged")
	// ErrUploadWindowUnreachable is a preview page that starts further past
	// the nearest index mark than one read may walk. The request was valid;
	// the organiser can page from a line nearer a mark.
	ErrUploadWindowUnreachable = errors.New("that line is too far into a file with lines this long to preview")
)

// wrapGamefileErr maps internal/gamefile's sentinels to this package's, so
// the HTTP layer never needs to know gamefile. Anything unrecognised is a bug
// or a disk failure and is wrapped with context.
func wrapGamefileErr(err error) error {
	switch {
	case errors.Is(err, gamefile.ErrNotFound), errors.Is(err, gamefile.ErrBadUploadID):
		return ErrUploadNotFound
	case errors.Is(err, gamefile.ErrUploadSealed):
		return ErrUploadAlreadyComplete
	case errors.Is(err, gamefile.ErrIncomplete):
		return ErrUploadIncomplete
	case errors.Is(err, gamefile.ErrCorruptIndex):
		return ErrUploadIndexCorrupt
	case errors.Is(err, gamefile.ErrWindowUnreachable):
		return ErrUploadWindowUnreachable
	case errors.Is(err, gamefile.ErrFileTooLarge):
		return ErrUploadTooLarge
	case errors.Is(err, gamefile.ErrStoreFull):
		return ErrUploadStoreFull
	case errors.Is(err, gamefile.ErrChunkOutOfOrder):
		return ErrUploadChunkOutOfOrder
	case errors.Is(err, gamefile.ErrChunkTooLarge):
		return ErrUploadChunkTooLarge
	case errors.Is(err, gamefile.ErrChunkIncomplete):
		// Wrapped, not replaced: the cause may be an http.MaxBytesError, which
		// internal/api answers as "chunk too large". A bare sentinel would tell
		// the client to retry, for ever, a chunk that is simply too big.
		return fmt.Errorf("%w: %w", ErrUploadChunkIncomplete, err)
	case errors.Is(err, gamefile.ErrLengthMismatch):
		return ErrUploadLengthMismatch
	default:
		return fmt.Errorf("gamefile: %w", err)
	}
}

// BeginUpload reserves a new upload for a contest's game.
//
// Editability and declaredBytes are checked before anything is reserved, so a
// running contest or an oversized file is refused at the start rather than
// after the transfer (CLAUDE.md rule 12). The file is reserved before the
// database row: a refused INSERT (most often ErrUploadInProgress) leaves at
// worst a file with no row, which sweepOrphanFiles finds, rather than a row
// with nothing on disk.
func (g *Games) BeginUpload(ctx context.Context, contestID uuid.UUID, filename string, declaredBytes int64) (Upload, error) {
	if g.files == nil {
		return Upload{}, ErrUploadsDisabled
	}
	if !validUploadFilename(filename) {
		return Upload{}, ErrUploadFilenameInvalid
	}
	if declaredBytes <= 0 || declaredBytes > g.limits.MaxFileBytes {
		return Upload{}, ErrUploadTooLarge
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return Upload{}, err
	}

	id := uuid.New()
	// Store.Begin checks declaredBytes against what the directory has left, so
	// a full volume refuses now rather than mid-transfer (CLAUDE.md rule 11).
	if err := g.files.Begin(id.String(), declaredBytes); err != nil {
		return Upload{}, wrapGamefileErr(err)
	}

	upload, err := g.repo.BeginUpload(ctx, id, contestID, filename, declaredBytes)
	if err != nil {
		// Release the reservation now: Store.Begin counts declaredBytes against
		// the directory until the upload ends, so a few refused begins would
		// spend the volume's budget for every contest until the janitor runs.
		// Best effort, since the janitor is still behind it.
		_ = g.retireUploadFile(id)
		if errors.Is(err, ErrUploadInProgress) {
			return Upload{}, ErrUploadInProgress
		}
		return Upload{}, fmt.Errorf("record the upload: %w", err)
	}
	return upload, nil
}

// currentContestUpload reads uploadID and checks it belongs to contestID,
// which is the authorisation boundary. An upload of another contest answers
// ErrUploadNotFound, so its existence does not leak.
func (g *Games) currentContestUpload(ctx context.Context, contestID, uploadID uuid.UUID) (Upload, error) {
	upload, err := g.repo.Upload(ctx, uploadID)
	if err != nil {
		return Upload{}, err
	}
	if upload.ContestID != contestID {
		return Upload{}, ErrUploadNotFound
	}
	return upload, nil
}

// AppendChunk writes one chunk of an upload already begun.
func (g *Games) AppendChunk(ctx context.Context, contestID, uploadID uuid.UUID, offset int64, r io.Reader) (int64, error) {
	if g.files == nil {
		return 0, ErrUploadsDisabled
	}
	upload, err := g.currentContestUpload(ctx, contestID, uploadID)
	if err != nil {
		return 0, err
	}
	if upload.Status != UploadReceiving {
		return 0, ErrUploadAlreadyComplete
	}

	received, err := g.files.Append(uploadID.String(), offset, r)
	if err != nil {
		return received, wrapGamefileErr(err)
	}
	if received == upload.ReceivedBytes {
		// A retried chunk the store already had: nothing to record
		// (CLAUDE.md rule 6).
		return received, nil
	}
	if err := g.repo.UpdateReceived(ctx, uploadID, received); err != nil {
		return received, fmt.Errorf("record the upload's progress: %w", err)
	}
	return received, nil
}

// CurrentUpload reads a contest's one upload still 'receiving', or
// ErrUploadNotFound when there is none, so a reloaded page can resume it.
func (g *Games) CurrentUpload(ctx context.Context, contestID uuid.UUID) (Upload, error) {
	return g.repo.CurrentUpload(ctx, contestID)
}

// Upload reads one upload of this contest by id, whatever its status. The API
// reads a file-sourced game's filename, length and line count from here
// rather than Template carrying a copy (CLAUDE.md rule 11).
func (g *Games) Upload(ctx context.Context, contestID, uploadID uuid.UUID) (Upload, error) {
	return g.currentContestUpload(ctx, contestID, uploadID)
}

// UploadWindow reads a slice of a completed upload's lines, for the console's
// preview of a script it will not run yet.
func (g *Games) UploadWindow(ctx context.Context, contestID, uploadID uuid.UUID, fromLine, maxLines int, maxBytes int64) (gamefile.Window, error) {
	if g.files == nil {
		return gamefile.Window{}, ErrUploadsDisabled
	}
	if _, err := g.currentContestUpload(ctx, contestID, uploadID); err != nil {
		return gamefile.Window{}, err
	}
	// ctx lets a page the organiser left stop its read of a large file.
	window, err := g.files.Window(ctx, uploadID.String(), fromLine, maxLines, maxBytes)
	if err != nil {
		return gamefile.Window{}, wrapGamefileErr(err)
	}
	return window, nil
}

// retireUploadFile removes one upload's file from disk. It is idempotent: a
// cancel, a displacement and the janitor can each retire the same id twice (a
// retried request, or a crash half way), so gamefile.ErrNotFound is success.
func (g *Games) retireUploadFile(id uuid.UUID) error {
	if err := g.files.Abort(id.String()); err != nil && !errors.Is(err, gamefile.ErrNotFound) {
		return wrapGamefileErr(err)
	}
	return nil
}

// displacedUpload names the upload whose file this contest's next game will
// leave behind: the current game's own, when that game is file-sourced.
//
// It is nil when there is nothing to retire: no game yet (ErrNoGame), a game
// written in the editor, a game that already names keeping, or no upload
// volume. Call it before the game is written, since SaveScript and
// CompleteUpload overwrite upload_id.
func (g *Games) displacedUpload(ctx context.Context, contestID uuid.UUID, keeping *uuid.UUID) (*uuid.UUID, error) {
	if g.files == nil {
		return nil, nil
	}

	existing, err := g.repo.Template(ctx, contestID)
	switch {
	case errors.Is(err, ErrNoGame):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read the contest's current game: %w", err)
	}

	if existing.Source != SourceFile || existing.UploadID == nil {
		return nil, nil
	}
	if keeping != nil && *existing.UploadID == *keeping {
		return nil, nil
	}
	return existing.UploadID, nil
}

// CompleteUpload seals an upload begun with BeginUpload, replaces the
// contest's game with it, and retires whichever upload it displaces.
//
// Hashing and indexing the file (Store.Complete) runs before any transaction
// opens, so no core-database transaction is held for that long; only the row
// updates run inside one, through replaceGame, as SetScript does. The contest
// can start during the hashing and the replacement then be refused, so the
// displaced file is removed only after the commit (see replaceGame).
func (g *Games) CompleteUpload(ctx context.Context, actorID, contestID, uploadID uuid.UUID) (Template, error) {
	if g.files == nil {
		return Template{}, ErrUploadsDisabled
	}

	upload, err := g.currentContestUpload(ctx, contestID, uploadID)
	if err != nil {
		return Template{}, err
	}
	if upload.Status != UploadReceiving {
		return Template{}, ErrUploadAlreadyComplete
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return Template{}, err
	}

	summary, err := g.files.Complete(uploadID.String(), upload.DeclaredBytes)
	if err != nil {
		return Template{}, wrapGamefileErr(err)
	}

	// The displaced upload's row is retired inside the transaction below and
	// its file after the commit; a refused transaction touches neither.
	previous, err := g.displacedUpload(ctx, contestID, &uploadID)
	if err != nil {
		return Template{}, err
	}

	database := templateName(contestID)
	storedSummary := UploadSummary{Bytes: summary.Bytes, SHA256: summary.SHA256, Lines: summary.Lines}

	return g.replaceGame(ctx, contestID, previous,
		func(ctx context.Context) (Template, error) {
			return g.repo.CompleteUpload(ctx, contestID, uploadID, database, storedSummary, previous)
		},
		func(saved Template) audit.Entry {
			// Never the content: only what identifies the file and how it measured.
			return audit.Entry{
				ActorID: &actorID, Action: audit.ActionGameUploadComplete,
				Entity: "contest", EntityID: contestID.String(),
				Payload: map[string]any{
					"filename": upload.Filename, "bytes": summary.Bytes,
					"lines": summary.Lines, "version": saved.Version,
				},
			}
		},
	)
}

// abortUpload removes one upload's file, then marks the row 'aborted'. actor
// is nil for the janitor's sweep, a system event.
func (g *Games) abortUpload(ctx context.Context, actor *uuid.UUID, upload Upload) (Upload, error) {
	mark := func(ctx context.Context) error {
		if err := g.repo.AbortUpload(ctx, upload.ID); err != nil {
			return fmt.Errorf("mark the upload aborted: %w", err)
		}
		return g.record(ctx, audit.Entry{
			ActorID: actor, Action: audit.ActionGameUploadAbort,
			Entity: "contest", EntityID: upload.ContestID.String(),
			Payload: map[string]any{"filename": upload.Filename, "bytes": upload.ReceivedBytes},
		})
	}

	if err := g.retireUploadFile(upload.ID); err != nil {
		return Upload{}, fmt.Errorf("remove the upload's file: %w", err)
	}

	err := g.atomically(ctx, mark)
	if err != nil {
		return Upload{}, err
	}
	upload.Status = UploadAborted
	return upload, nil
}

// AbortUpload cancels an organiser's upload before it completes. It never
// touches game_templates: an aborted upload never became a game, so there is
// nothing for GameEditable to gate.
func (g *Games) AbortUpload(ctx context.Context, actorID, contestID, uploadID uuid.UUID) (Upload, error) {
	if g.files == nil {
		return Upload{}, ErrUploadsDisabled
	}
	upload, err := g.currentContestUpload(ctx, contestID, uploadID)
	if err != nil {
		return Upload{}, err
	}
	if upload.Status != UploadReceiving {
		return Upload{}, ErrUploadAlreadyComplete
	}
	return g.abortUpload(ctx, &actorID, upload)
}

// UploadCleanupResult is one janitor pass, summed over both upload stores:
// whole dumps and per-table CSVs (tabledata.go).
type UploadCleanupResult struct {
	// Abandoned counts uploads left 'receiving' past their grace period.
	Abandoned int
	// OrphanFiles counts files on the volume nothing needs any more (see
	// sweepOrphanFiles).
	OrphanFiles int
}

// SweepUploads is the abandoned-upload janitor: every 'receiving' row older
// than olderThan is aborted, and every file nothing needs any more is
// removed.
//
// The file sweep matters more. Every other query in this package starts from
// a database row, so a file no row needs (left by a crash between Store.Begin
// and the INSERT, or between replaceGame's commit and its removal) is found
// by nothing else, and may be a multi-gigabyte dump.
func (g *Games) SweepUploads(ctx context.Context, olderThan time.Duration) (UploadCleanupResult, error) {
	var result UploadCleanupResult
	var failures []error

	if g.files != nil {
		abandoned, err := g.repo.AbandonedUploads(ctx, g.now().Add(-olderThan), abandonedUploadBatchLimit)
		if err != nil {
			failures = append(failures, fmt.Errorf("list abandoned uploads: %w", err))
		}
		for _, upload := range abandoned {
			if _, err := g.abortUpload(ctx, nil, upload); err != nil {
				failures = append(failures, fmt.Errorf("abandon upload %s: %w", upload.ID, err))
				continue
			}
			result.Abandoned++
		}

		removed, err := g.sweepOrphanFiles(ctx)
		result.OrphanFiles += removed
		if err != nil {
			failures = append(failures, err)
		}
	}

	// The table builder's files live in a second gamefile.Store (WithTableData)
	// and leak the same two ways, so the same janitor sweeps them.
	if g.tableFiles != nil {
		abandoned, err := g.sweepAbandonedTableData(ctx, olderThan)
		result.Abandoned += abandoned
		if err != nil {
			failures = append(failures, err)
		}

		removed, err := g.sweepOrphanTableFiles(ctx)
		result.OrphanFiles += removed
		if err != nil {
			failures = append(failures, err)
		}
	}

	return result, errors.Join(failures...)
}

// abandonedUploadBatchLimit bounds one sweep, as ReclaimBatchLimit bounds
// Reclaim, so a fresh deployment's first tick does not abort every upload
// ever left behind in one pass.
const abandonedUploadBatchLimit = 100

// orphanFileGrace is how old a file must be before the orphan sweep considers
// it. BeginUpload reserves the file before it writes the row, so a younger
// file may belong to an upload whose INSERT has not committed yet; removing
// it would break an upload just handed to the organiser. The sweep runs
// every ten minutes (internal/app.abandonedUploads).
const orphanFileGrace = 15 * time.Minute

// sweepOrphanFiles removes every upload file older than orphanFileGrace that
// nothing needs: no game_uploads row names it, or the row is neither still
// receiving nor the one a contest's game is built from (UploadInUse).
//
// The second case catches a displaced upload whose removal after
// replaceGame's commit was interrupted. The ids come from
// gamefile.Store.UploadIDs because the file layout is gamefile's to know.
func (g *Games) sweepOrphanFiles(ctx context.Context) (int, error) {
	ids, err := g.files.UploadIDs(g.now().Add(-orphanFileGrace))
	if err != nil {
		return 0, fmt.Errorf("list the upload volume: %w", err)
	}

	var removed int
	var failures []error
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		inUse, err := g.repo.UploadInUse(ctx, id)
		if err != nil {
			failures = append(failures, fmt.Errorf("check upload %s: %w", id, err))
			continue
		}
		if inUse {
			continue
		}
		if err := g.retireUploadFile(id); err != nil {
			failures = append(failures, fmt.Errorf("remove orphan file %s: %w", id, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(failures...)
}

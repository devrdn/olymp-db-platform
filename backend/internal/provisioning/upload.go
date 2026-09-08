package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/google/uuid"
)

// MaxUploadFilenameBytes bounds the one free-text field an upload carries
// (CLAUDE.md rule 2): the name a browser's file picker reported, kept only
// for an organiser's own screen and never used as a path — migration 24's
// own CHECK enforces the same bound in the schema.
const MaxUploadFilenameBytes = 255

// UploadStatus is where one upload has got to.
type UploadStatus string

const (
	// UploadReceiving is an upload still taking chunks — or waiting between
	// them, since a chunked upload spans more than one request.
	UploadReceiving UploadStatus = "receiving"
	// UploadComplete is a sealed upload that became a contest's game.
	UploadComplete UploadStatus = "complete"
	// UploadAborted is an upload cancelled — by an organiser, or by the
	// abandoned-upload janitor — before it ever became anybody's game.
	UploadAborted UploadStatus = "aborted"
)

// Upload is one organiser's file-based game, as far as the database's own
// bookkeeping goes. Its bytes are never here — internal/gamefile.Store
// holds those, addressed by this row's own ID.
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

// UploadSummary is what Store.Complete measured, carried from Games into the
// repository so the two do not have to agree on gamefile.Summary's own shape.
type UploadSummary struct {
	Bytes  int64
	SHA256 string
	Lines  int64
}

// Why an upload could not be received, completed or read back. Named so a
// handler's fail switch can map each to its own status rather than serving
// "internal error" for a file too large or a chunk sent out of order
// (CLAUDE.md rule 1).
var (
	// ErrUploadsDisabled is every upload method's answer on an installation
	// with no GAME_UPLOAD_DIR configured — Games was never given WithUploads.
	ErrUploadsDisabled = errors.New("file uploads are not configured on this installation")
	// ErrUploadFilenameInvalid is an empty filename or one past
	// MaxUploadFilenameBytes.
	ErrUploadFilenameInvalid = errors.New("the upload's filename is invalid")
	// ErrUploadTooLarge is a declared length past the configured
	// MaxFileBytes, or one Store.Append refused for the same reason once
	// actual bytes exceeded it.
	ErrUploadTooLarge = errors.New("the upload exceeds the maximum file size")
	// ErrUploadStoreFull is the upload directory already holding its
	// configured MaxDirBytes of other uploads.
	ErrUploadStoreFull = errors.New("the upload directory is full")
	// ErrUploadChunkOutOfOrder is a chunk whose offset does not continue
	// where the upload actually left off.
	ErrUploadChunkOutOfOrder = errors.New("the chunk does not continue where the upload left off")
	// ErrUploadChunkTooLarge is one chunk past the configured MaxChunkBytes.
	ErrUploadChunkTooLarge = errors.New("the chunk exceeds the maximum chunk size")
	// ErrUploadLengthMismatch is Complete finding that what actually landed
	// on disk does not match what the browser declared at Begin.
	ErrUploadLengthMismatch = errors.New("the received bytes do not match the declared length")
	// ErrUploadNotFound is an id that names no upload of this contest — or of
	// any contest at all.
	ErrUploadNotFound = errors.New("no such upload")
	// ErrUploadInProgress is a second Begin for a contest that already has
	// one upload 'receiving' — game_uploads_one_receiving_idx's own refusal,
	// not a check this package makes ahead of it (migration 24's own doc).
	ErrUploadInProgress = errors.New("this contest already has an upload in progress")
	// ErrUploadAlreadyComplete is an Append, Complete or Abort against an
	// upload no longer 'receiving' — sealed already, or already cancelled.
	ErrUploadAlreadyComplete = errors.New("the upload has already been completed or cancelled")
	// ErrUploadIncomplete is a Window asked of an upload Complete has not
	// sealed yet — there is no line index to read.
	ErrUploadIncomplete = errors.New("the upload has not been completed yet")
)

// wrapGamefileErr turns one of internal/gamefile's own sentinels into the
// domain's — the handler that eventually serves these must not know that
// package exists (migration 24's brief, in as many words). Anything
// unrecognised is wrapped with context instead of passed through bare: it is
// either a bug in this mapping or a disk failure, and either way it is not
// something an organiser's own fail switch has a branch for.
func wrapGamefileErr(err error) error {
	switch {
	case errors.Is(err, gamefile.ErrNotFound), errors.Is(err, gamefile.ErrBadUploadID):
		return ErrUploadNotFound
	case errors.Is(err, gamefile.ErrUploadSealed):
		return ErrUploadAlreadyComplete
	case errors.Is(err, gamefile.ErrIncomplete):
		return ErrUploadIncomplete
	case errors.Is(err, gamefile.ErrFileTooLarge):
		return ErrUploadTooLarge
	case errors.Is(err, gamefile.ErrStoreFull):
		return ErrUploadStoreFull
	case errors.Is(err, gamefile.ErrChunkOutOfOrder):
		return ErrUploadChunkOutOfOrder
	case errors.Is(err, gamefile.ErrChunkTooLarge):
		return ErrUploadChunkTooLarge
	case errors.Is(err, gamefile.ErrLengthMismatch):
		return ErrUploadLengthMismatch
	default:
		return fmt.Errorf("gamefile: %w", err)
	}
}

// BeginUpload reserves a new upload for a contest's game.
//
// Editability is checked here, not only at CompleteUpload: starting to
// receive gigabytes into a contest that is already running is time and disk
// nobody can get back, found out only at the very end (migration 24's own
// brief). declaredBytes is checked against the configured MaxFileBytes for
// the same reason Store.Append checks it again on every chunk — CLAUDE.md
// rule 12 wants the bound at the moment the bytes (or here, the promise of
// them) arrive, not after a reservation was already made on disk.
//
// The reservation on disk happens before the database row: Store.Begin first,
// then BeginUpload's own INSERT, so that a database refusal (most often
// ErrUploadInProgress, from game_uploads_one_receiving_idx) leaves at worst
// an empty file with no row — exactly what the janitor's orphan-file sweep
// exists to find (sweepOrphanFiles below), rather than a row with nothing
// behind it on disk.
func (g *Games) BeginUpload(ctx context.Context, contestID uuid.UUID, filename string, declaredBytes int64) (Upload, error) {
	if g.files == nil {
		return Upload{}, ErrUploadsDisabled
	}
	if filename == "" || len(filename) > MaxUploadFilenameBytes {
		return Upload{}, ErrUploadFilenameInvalid
	}
	if declaredBytes <= 0 || declaredBytes > g.limits.MaxFileBytes {
		return Upload{}, ErrUploadTooLarge
	}

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return Upload{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return Upload{}, ErrGameNotEditable
	}

	id := uuid.New()
	if err := g.files.Begin(id.String()); err != nil {
		return Upload{}, wrapGamefileErr(err)
	}

	upload, err := g.repo.BeginUpload(ctx, id, contestID, filename, declaredBytes)
	if err != nil {
		if errors.Is(err, ErrUploadInProgress) {
			return Upload{}, ErrUploadInProgress
		}
		return Upload{}, fmt.Errorf("record the upload: %w", err)
	}
	return upload, nil
}

// currentContestUpload reads uploadID and checks it actually belongs to
// contestID — the same contest-scoping InstanceNamed's own doc explains:
// db_name (there) and an upload's id (here) are both unique on their own,
// and checking the contest anyway is the authorisation boundary, not a
// redundancy. A row that exists but names another contest answers
// ErrUploadNotFound, identically to a row that does not exist at all — to
// the caller it is the same fact, and the difference would only leak that
// somebody else's upload exists.
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
		// Store.Append's own idempotent-skip path: a retried chunk the
		// server already had. Writing the same number back would be exactly
		// the reasonless write CLAUDE.md rule 6 asks a hot path to skip.
		return received, nil
	}
	if err := g.repo.UpdateReceived(ctx, uploadID, received); err != nil {
		return received, fmt.Errorf("record the upload's progress: %w", err)
	}
	return received, nil
}

// CurrentUpload reads a contest's one upload still 'receiving', or
// ErrUploadNotFound when there is none — which lets a page reload find an
// upload already in progress and offer to resume it rather than refuse a
// second Begin with no way to explain why.
func (g *Games) CurrentUpload(ctx context.Context, contestID uuid.UUID) (Upload, error) {
	return g.repo.CurrentUpload(ctx, contestID)
}

// UploadWindow reads a slice of a completed upload's lines — the console's
// own preview of a script it will not run yet (Build's own doc, above).
func (g *Games) UploadWindow(ctx context.Context, contestID, uploadID uuid.UUID, fromLine, maxLines int, maxBytes int64) (gamefile.Window, error) {
	if g.files == nil {
		return gamefile.Window{}, ErrUploadsDisabled
	}
	if _, err := g.currentContestUpload(ctx, contestID, uploadID); err != nil {
		return gamefile.Window{}, err
	}
	window, err := g.files.Window(uploadID.String(), fromLine, maxLines, maxBytes)
	if err != nil {
		return gamefile.Window{}, wrapGamefileErr(err)
	}
	return window, nil
}

// retireUploadFile removes one upload's file from disk, tolerating one that
// is already gone.
//
// Idempotent on purpose: this runs both for an organiser's own cancel and
// for the upload a completed one displaces, and either can be asked to
// retire the same id twice — a retried request, or the janitor catching what
// a crash left half done between the file being removed and the row being
// marked (CompleteUpload's own doc explains why that ordering, and this gap,
// exist). A second call finding gamefile.ErrNotFound must read as "already
// retired", the same idempotency Store.Append documents for a repeated
// chunk, not as a failure.
func (g *Games) retireUploadFile(id uuid.UUID) error {
	if err := g.files.Abort(id.String()); err != nil && !errors.Is(err, gamefile.ErrNotFound) {
		return wrapGamefileErr(err)
	}
	return nil
}

// CompleteUpload finishes an upload begun with BeginUpload: seals it on
// disk, replaces the contest's game with it, and retires whichever upload
// this one displaces.
//
// The heavy work — hashing and indexing up to MaxFileBytes of an organiser's
// SQL, Store.Complete's own one sequential pass — happens before any
// database transaction opens. A core-database transaction is not something
// to hold open across the seconds (or, at the configured ceiling, longer)
// that takes; only the row updates below run inside one, through
// replaceGame, the same shape SetScript uses. This is the "same path as
// SetScript" migration 24's own brief asks for: one transaction, one
// GameEditable check, one audit write, not a second parallel one that could
// drift from it.
//
// Displacing the previous upload follows Instances.DropInstance's own
// ordering: the real object goes first. If this contest's current game is
// already file-sourced, its upload's file is removed from disk here, before
// anything is written to the database — marking without removing would
// leave gigabytes nobody is looking for, and this is what stops that rather
// than a promise in a comment.
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

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return Template{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return Template{}, ErrGameNotEditable
	}

	summary, err := g.files.Complete(uploadID.String(), upload.DeclaredBytes)
	if err != nil {
		return Template{}, wrapGamefileErr(err)
	}

	// Whichever upload this one displaces — this contest's current game,
	// only if it is itself file-sourced and is a different upload — is
	// retired now, on disk, before anything is marked. A contest with no
	// game yet, or one whose current game is SourceEditor, has nothing to
	// retire, and ErrNoGame reads as exactly that rather than as a failure.
	var previous *uuid.UUID
	existing, err := g.repo.Template(ctx, contestID)
	switch {
	case err == nil && existing.Source == SourceFile && existing.UploadID != nil && *existing.UploadID != uploadID:
		if err := g.retireUploadFile(*existing.UploadID); err != nil {
			return Template{}, fmt.Errorf("remove the displaced upload's file: %w", err)
		}
		previous = existing.UploadID
	case err != nil && !errors.Is(err, ErrNoGame):
		return Template{}, fmt.Errorf("read the contest's current game: %w", err)
	}

	database := templateName(contestID)
	storedSummary := UploadSummary{Bytes: summary.Bytes, SHA256: summary.SHA256, Lines: summary.Lines}

	return g.replaceGame(ctx, contestID,
		func(ctx context.Context) (Template, error) {
			return g.repo.CompleteUpload(ctx, contestID, uploadID, database, storedSummary, previous)
		},
		func(saved Template) audit.Entry {
			// Never the content — only what identifies which file this was
			// and how it measured (migration 24's own brief).
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

// abortUpload cancels one upload: removes its file from disk, then marks the
// row 'aborted'. actor is nil for the janitor's own sweep (SweepUploads
// below) — a system event nobody asked for, the same convention
// reclaimEntry uses for a database the grace period took rather than an
// organiser.
func (g *Games) abortUpload(ctx context.Context, actor *uuid.UUID, upload Upload) (Upload, error) {
	mark := func(ctx context.Context) error {
		if err := g.repo.AbortUpload(ctx, upload.ID); err != nil {
			return fmt.Errorf("mark the upload aborted: %w", err)
		}
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, audit.Entry{
			ActorID: actor, Action: audit.ActionGameUploadAbort,
			Entity: "contest", EntityID: upload.ContestID.String(),
			Payload: map[string]any{"filename": upload.Filename, "bytes": upload.ReceivedBytes},
		})
	}

	if err := g.retireUploadFile(upload.ID); err != nil {
		return Upload{}, fmt.Errorf("remove the upload's file: %w", err)
	}

	var err error
	if g.uow != nil {
		err = g.uow.Do(ctx, mark)
	} else {
		err = mark(ctx)
	}
	if err != nil {
		return Upload{}, err
	}
	upload.Status = UploadAborted
	return upload, nil
}

// AbortUpload cancels an organiser's own upload before it was completed. It
// never touches game_templates — an aborted upload never became anybody's
// game, so there is nothing to replace and nothing GameEditable needs to
// gate.
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

// UploadCleanupResult is one pass of the janitor's own two sweeps.
type UploadCleanupResult struct {
	// Abandoned counts uploads left 'receiving' past their grace period —
	// nobody appended to them, and nobody is coming back to.
	Abandoned int
	// OrphanFiles counts files the volume holds that no row in game_uploads
	// names at all — the sweep's own doc (SweepUploads below) explains why
	// these are the more dangerous half.
	OrphanFiles int
}

// dataSuffix is internal/gamefile's own naming convention for an upload's
// data file — package-private there, so this is the one place this package
// has to know it rather than ask gamefile for a directory listing, which it
// does not offer (see this task's own report for the gap).
const dataSuffix = ".data"

// SweepUploads is the abandoned-upload janitor: every 'receiving' row older
// than olderThan is aborted, and every file on the volume that no row names
// at all is removed.
//
// Two different leaks, and the second is the more dangerous one. An
// abandoned row at least says so — a contest an organiser can find, an
// updated_at anybody can read. A file with no row is invisible to every
// other query this package makes: Instances, Reclaim, the orphan-database
// sweep (orphans.go) all start from a database row and ask whether the
// object behind it still exists; nothing here ever asks the volume what it
// holds and works backwards. Without this second half, a crash between
// Store.Begin succeeding and BeginUpload's own INSERT — or any other gap
// this package's own comments already call out — leaves bytes nobody will
// ever find again.
func (g *Games) SweepUploads(ctx context.Context, olderThan time.Duration) (UploadCleanupResult, error) {
	if g.files == nil {
		return UploadCleanupResult{}, nil
	}

	var result UploadCleanupResult
	var failures []error

	abandoned, err := g.repo.AbandonedUploads(ctx, g.now().Add(-olderThan), abandonedUploadBatchLimit)
	if err != nil {
		return result, fmt.Errorf("list abandoned uploads: %w", err)
	}
	for _, upload := range abandoned {
		if _, err := g.abortUpload(ctx, nil, upload); err != nil {
			failures = append(failures, fmt.Errorf("abandon upload %s: %w", upload.ID, err))
			continue
		}
		result.Abandoned++
	}

	removed, err := g.sweepOrphanFiles(ctx)
	result.OrphanFiles = removed
	if err != nil {
		failures = append(failures, err)
	}

	return result, errors.Join(failures...)
}

// abandonedUploadBatchLimit bounds one sweep the same way
// ReclaimBatchLimit bounds Reclaim's: a fresh deployment's first tick must
// not try to abort every upload ever left behind in one pass. Uploads are
// rarer than instances by construction — one per contest at a time, at
// most — so a smaller batch is still generous against anything this
// platform's own numbers describe.
const abandonedUploadBatchLimit = 100

// sweepOrphanFiles removes every *.data file the upload volume holds that no
// row in game_uploads names at all, whatever that row's status. A file whose
// row exists but says 'complete' or 'aborted' is not touched here — that is
// ordinary history, or something retireUploadFile has already handled — only
// a file with no row at all, which nothing else in this package will ever
// notice on its own.
func (g *Games) sweepOrphanFiles(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(g.dir)
	if err != nil {
		return 0, fmt.Errorf("list the upload volume: %w", err)
	}

	var removed int
	var failures []error
	for _, entry := range entries {
		idStr, ok := strings.CutSuffix(entry.Name(), dataSuffix)
		if !ok {
			continue
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		exists, err := g.repo.UploadExists(ctx, id)
		if err != nil {
			failures = append(failures, fmt.Errorf("check upload %s: %w", id, err))
			continue
		}
		if exists {
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

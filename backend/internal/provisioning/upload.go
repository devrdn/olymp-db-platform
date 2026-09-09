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

// MaxUploadFilenameBytes bounds the one free-text field an upload carries
// (CLAUDE.md rule 2): the name a browser's file picker reported, kept only
// for an organiser's own screen and never used as a path — migration 24's
// own CHECK enforces the same bound in the schema.
const MaxUploadFilenameBytes = 255

// validUploadFilename reports whether a name may be stored and shown.
//
// Length is not the only bound this field needs. The column is `text`, and
// PostgreSQL refuses a NUL byte in one with SQLSTATE 22021 — so a name
// carrying one turns BeginUpload's INSERT into an internal error *after*
// Store.Begin has already created the file, which is a 500 for the organiser
// and an orphan for the janitor, where a named refusal was available for free
// (CLAUDE.md rule 1). NUL is not special-cased: no control character belongs
// in a name a person typed into a file picker, and one that reaches a log
// line or a console can forge either. Everything else — every alphabet, every
// space, every punctuation mark — is left alone: this name is never a path
// (MaxUploadFilenameBytes' own doc), so there is nothing else to sanitise it
// against.
func validUploadFilename(name string) bool {
	if name == "" || len(name) > MaxUploadFilenameBytes {
		return false
	}
	for _, r := range name {
		// unicode.IsControl over the decoded rune, so this also rejects
		// U+0085 and the C1 block, and never mistakes a byte inside a
		// multi-byte rune for one.
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

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
	// ErrUploadFilenameInvalid is an empty filename, one past
	// MaxUploadFilenameBytes, or one carrying a control character.
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
	// ErrUploadChunkIncomplete is a chunk whose body stopped arriving before
	// the store had all of it: a dropped connection, or a read deadline that
	// expired mid-body. Nothing of the chunk was kept (gamefile.
	// ErrChunkIncomplete's own doc), so the answer to it is to send the same
	// chunk again — which is the whole reason it is a sentinel and not the
	// internal error a wrapped I/O failure would have become. On a route
	// whose body is megabytes over whatever uplink an organiser has, an
	// interrupted transfer is an ordinary event, not a fault of ours.
	ErrUploadChunkIncomplete = errors.New("the chunk body was not received in full")
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
	// ErrUploadIndexCorrupt is a completed upload whose line index no longer
	// describes the file next to it: a truncated write, a damaged disk, or a
	// file substituted underneath the volume (gamefile.ErrCorruptIndex's own
	// doc).
	//
	// A sentinel of its own rather than the internal error it used to become,
	// because it is one of the few failures on this route the organiser can
	// actually act on: the bytes on the API host cannot be trusted to page
	// through any more, so the file has to be uploaded again. Nothing about
	// it is a fault of theirs, and nothing about it is fixed by retrying the
	// same read.
	ErrUploadIndexCorrupt = errors.New("the upload's line index is damaged")
	// ErrUploadWindowUnreachable is a preview page whose first line is
	// further past the file's nearest index mark than one read may walk
	// (gamefile.ErrWindowUnreachable's own doc).
	//
	// Named rather than left as an internal error because the request was
	// right and the organiser has moves: page from a line nearer a mark, or
	// look at the file some other way. It is a fact about a dump whose lines
	// are megabytes long, not a fault of theirs and not a failure of ours.
	ErrUploadWindowUnreachable = errors.New("that line is too far into a file with lines this long to preview")
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
		// The one case that wraps rather than replaces. What interrupted the
		// body is usually the transport's own doing, and the HTTP layer that
		// created that reader has its own name for it (http.MaxBytesError,
		// which internal/api's appendChunk answers as "chunk too large"
		// rather than "send it again"). Replacing the error with a bare
		// sentinel here would throw that away and leave a client being told
		// to retry, for ever, a chunk that is simply too big.
		return fmt.Errorf("%w: %w", ErrUploadChunkIncomplete, err)
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
	if !validUploadFilename(filename) {
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
	// declaredBytes travels on into the store, which is the only thing that
	// knows what the directory has left: the check above is about one file's
	// ceiling, and Store.Begin's is about whether the volume can hold this
	// upload at all — a promise refused now instead of a transfer cut off at
	// its 129th chunk (Store.Begin's own doc, CLAUDE.md rule 11).
	if err := g.files.Begin(id.String(), declaredBytes); err != nil {
		return Upload{}, wrapGamefileErr(err)
	}

	upload, err := g.repo.BeginUpload(ctx, id, contestID, filename, declaredBytes)
	if err != nil {
		// The reservation this call made is nobody's now, and it is not the
		// file alone: Store.Begin also counts declaredBytes against the
		// directory until the upload completes or is aborted
		// (committedBytes' own doc). Left behind, four refused begins of a
		// gibibyte each spend the whole volume's budget — for every contest
		// on the installation, not only this one — until the janitor's own
		// sweep, which is fifteen minutes of grace plus a tick away. The
		// removal is the same one bootstrapTableRow makes on the identical
		// refusal, and best effort for the same reason: the janitor is still
		// behind it, this only stops the wait.
		_ = g.retireUploadFile(id)
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

// Upload reads one upload of this contest by id, whatever its status —
// currentContestUpload already does exactly this internally, exposed here so
// a caller can resolve the row a file-sourced Template.UploadID names.
//
// This is what lets GET /contests/{id}/game describe the file a file-sourced
// game came from (internal/api's gameResponse.Upload) without a second copy
// of the upload's own bookkeeping: the filename, the length, the line count
// all already live on this row, and CLAUDE.md rule 11 is the value that
// decided this — Template only ever kept UploadID, the row it points at is
// where the rest of the fact already lived, so the API layer reads it from
// here rather than this package growing a duplicate field to carry it.
//
// Unlike CurrentUpload, not limited to 'receiving': a file-sourced game's own
// upload is 'complete' by the time anything asks for it this way, and
// currentContestUpload never filtered on status to begin with.
func (g *Games) Upload(ctx context.Context, contestID, uploadID uuid.UUID) (Upload, error) {
	return g.currentContestUpload(ctx, contestID, uploadID)
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
	// ctx travels into the store, which is what lets a console page the
	// organiser navigated away from stop the read it started rather than
	// leaving a goroutine walking a multi-gigabyte file for nobody.
	window, err := g.files.Window(ctx, uploadID.String(), fromLine, maxLines, maxBytes)
	if err != nil {
		return gamefile.Window{}, wrapGamefileErr(err)
	}
	return window, nil
}

// retireUploadFile removes one upload's file from disk, tolerating one that
// is already gone.
//
// Idempotent on purpose: this runs for an organiser's own cancel, for the
// upload a replacement game displaces, and for the janitor's own sweep, and
// any of them can be asked to retire the same id twice — a retried request, or
// the janitor catching what a crash left half done on either side of the row
// being marked. A second call finding gamefile.ErrNotFound must read as
// "already retired", the same idempotency Store.Append documents for a
// repeated chunk, not as a failure.
func (g *Games) retireUploadFile(id uuid.UUID) error {
	if err := g.files.Abort(id.String()); err != nil && !errors.Is(err, gamefile.ErrNotFound) {
		return wrapGamefileErr(err)
	}
	return nil
}

// displacedUpload names the upload whose file this contest's next game will
// leave behind: the current game's own, when that game is file-sourced.
//
// nil when there is nothing to retire — the contest has no game yet, its game
// was written in the editor, the game already names the very upload that is
// replacing it (keeping, non-nil only for CompleteUpload), or this
// installation has no upload volume at all, in which case there is no file to
// speak of and nothing this service could remove. ErrNoGame is one of those
// answers rather than a failure.
//
// Read before the game is written, because afterwards the row no longer says
// which upload it came from — SaveScript and CompleteUpload both overwrite
// upload_id in the same statement.
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
// Displacing the previous upload happens the other way round from
// Instances.DropInstance's "the real object goes first": the row is written
// first and the file removed after the transaction commits. See replaceGame,
// which does the removal, for why this one is the exception — the removal
// here is conditional on a commit that has not happened yet, and this method
// deliberately leaves a window (the hashing and indexing of an organiser's
// whole file) in which the contest can start and the replacement be refused.
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

	// Whichever upload this one displaces — this contest's current game, only
	// if it is itself file-sourced and is a different upload. Its row is
	// retired inside the transaction below and its file after that one
	// commits; nothing about it is touched if the transaction is refused.
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

// UploadCleanupResult is one pass of the janitor's own two sweeps, run for
// both kinds of file this package now owns: a whole dump (upload.go) and
// one table's own CSV (tabledata.go). Two independent gamefile.Store
// directories, so the counts below are summed across both rather than one
// field secretly meaning "whichever kind happened to be found" — the same
// distinction TableData vs Upload already draws everywhere else.
type UploadCleanupResult struct {
	// Abandoned counts uploads left 'receiving' past their grace period —
	// nobody appended to them, and nobody is coming back to.
	Abandoned int
	// OrphanFiles counts files on the volume nothing needs any more — no row
	// names them, or the row is there but no contest's game is built from it
	// — the sweep's own doc (SweepUploads below) explains why these are the
	// more dangerous half.
	OrphanFiles int
}

// SweepUploads is the abandoned-upload janitor: every 'receiving' row older
// than olderThan is aborted, and every file on the volume nothing needs any
// more is removed.
//
// Two different leaks, and the second is the more dangerous one. An
// abandoned row at least says so — a contest an organiser can find, an
// updated_at anybody can read. A file nothing points at is invisible to every
// other query this package makes: Instances, Reclaim, the orphan-database
// sweep (orphans.go) all start from a database row and ask whether the
// object behind it still exists; nothing here ever asks the volume what it
// holds and works backwards. Without this second half, a crash between
// Store.Begin succeeding and BeginUpload's own INSERT, a crash between
// replaceGame's commit and the removal that follows it, or any other gap this
// package's own comments already call out, leaves bytes nobody will ever find
// again — and on this platform one of them is a multi-gigabyte dump.
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

	// The table builder's own per-table files (tabledata.go) live on a
	// second, independent gamefile.Store — see WithTableData's own doc for
	// why — but they leak the identical two ways a dump does, so the same
	// janitor sweeps both rather than internal/app growing a second
	// scheduled task nobody remembers to add when this feature was wired in.
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

// abandonedUploadBatchLimit bounds one sweep the same way
// ReclaimBatchLimit bounds Reclaim's: a fresh deployment's first tick must
// not try to abort every upload ever left behind in one pass. Uploads are
// rarer than instances by construction — one per contest at a time, at
// most — so a smaller batch is still generous against anything this
// platform's own numbers describe.
const abandonedUploadBatchLimit = 100

// orphanFileGrace is how young a file on the volume may be and still be left
// alone by the sweep below.
//
// BeginUpload reserves the file before it writes the row, deliberately — its
// own doc says why — which means there is always an instant in which the
// volume holds a file no row names yet. Without a floor on the file's age the
// sweep does not merely fail to clean that up, it *causes* the damage: a
// ReadDir that catches the reservation and a UploadInUse that lands before the
// INSERT commits delete the bytes of an upload whose id is at that moment
// being handed back to the organiser, and the first chunk then answers "no
// such upload" against a 'receiving' row that blocks every retry.
//
// Fifteen minutes is many orders of magnitude past the one INSERT that window
// is, and short enough that a file genuinely left behind is not held for long
// — the sweep runs every ten minutes (internal/app.abandonedUploads), so
// nothing waits more than a tick or two past the grace.
const orphanFileGrace = 15 * time.Minute

// sweepOrphanFiles removes every upload on the volume that nothing needs any
// more: no row in game_uploads names it at all, or the row is there but the
// upload is neither still receiving chunks nor the one a contest's game is
// built from (TemplateRepository.UploadInUse asks exactly that).
//
// The second half is what makes this a backstop rather than a formality. A
// completed upload stops being needed the moment its game stops naming it —
// a second upload displaced it, or an organiser went back to writing a script
// in the editor — and both of those are followed by a removal that can be
// interrupted: replaceGame removes the file after its transaction commits, so
// a process that dies in between leaves a row and gigabytes of bytes nothing
// will ever ask for again. Asking only whether a row existed answered "keep"
// for every one of those.
//
// Only files older than orphanFileGrace are considered, and the list of ids
// comes from gamefile.Store.UploadIDs rather than this package reading the
// directory itself: which files make up one upload, and how many of them there
// are, is gamefile's own layout to know.
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

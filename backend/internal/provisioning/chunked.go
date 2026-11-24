package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/gamefile"
	"github.com/google/uuid"
)

// A chunked upload is a file an organiser's browser sends in pieces: begun
// with its declared length, appended to at the offset it has reached, then
// completed or aborted, with a janitor for the ones nobody finishes. Its bytes
// live in a gamefile.Store under the row's ID and its bookkeeping in a row.
// Uploads runs that lifecycle; an uploaded dump (upload.go) and a table's CSV
// (tabledata.go) are its two kinds, each with its own store and rows.

// UploadStatus is where one chunked upload has got to.
type UploadStatus string

const (
	// UploadReceiving is an upload still taking chunks, or waiting between
	// them.
	UploadReceiving UploadStatus = "receiving"
	// UploadComplete is a finished upload: a dump that became a contest's
	// game, or a table's validated CSV, the only file a build loads rows from.
	UploadComplete UploadStatus = "complete"
	// UploadAborted is an upload cancelled, by an organiser or by the
	// janitor, or displaced by a later one.
	UploadAborted UploadStatus = "aborted"
)

// Why a chunked upload could not be received, completed or read back
// (CLAUDE.md rule 1). Both kinds return these; the HTTP layer answers them
// under each route's own codes.
var (
	// ErrUploadsDisabled is every call's answer when the kind has no volume
	// (Games was never given WithUploads or WithTableData).
	ErrUploadsDisabled = errors.New("uploads of this kind are not configured on this installation")
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
	// ErrUploadBusy is a chunk arriving while its store already holds as many
	// chunks in memory as it may, or while another chunk is still arriving
	// for the same contest, from the same account, or for a contest of the
	// same owner. The same chunk can be sent again shortly.
	ErrUploadBusy = errors.New("too many chunks are being received at once")
	// ErrUploadLengthMismatch is a completion finding that what actually
	// arrived does not match what the browser declared when it began.
	ErrUploadLengthMismatch = errors.New("the received bytes do not match the declared length")
	// ErrUploadNotFound is an id that names no upload of this contest.
	ErrUploadNotFound = errors.New("no such upload")
	// ErrUploadInProgress is a second Begin while an upload of the same thing
	// (a contest's dump, a contest's table) is 'receiving', refused by the
	// database's unique index.
	ErrUploadInProgress = errors.New("an upload of this is already in progress")
	// ErrUploadAlreadyComplete is an Append, Complete or Abort against an
	// upload no longer 'receiving': sealed already, or already cancelled.
	ErrUploadAlreadyComplete = errors.New("the upload has already been completed or cancelled")
	// ErrUploadIncomplete is a Window asked of an upload no completion has
	// sealed: there is no line index to read.
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
	case errors.Is(err, gamefile.ErrTooManyChunks):
		return ErrUploadBusy
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

// UploadRows is the bookkeeping one kind of chunked upload keeps, a row per
// upload; internal/postgres implements it once per table.
type UploadRows[R any] interface {
	// Begin records a new upload in 'receiving'. The caller generates id so
	// the file on disk can carry it before this row exists, and name is what
	// the upload is of (a dump's filename, a table's name). A second upload
	// in progress of the same thing is refused by a unique index and returned
	// as ErrUploadInProgress.
	Begin(ctx context.Context, id, contestID uuid.UUID, name string, declaredBytes int64) (R, error)
	// ByID reads one upload, whatever its status, or ErrUploadNotFound.
	ByID(ctx context.Context, id uuid.UUID) (R, error)
	// UpdateReceived records how far a 'receiving' upload has got, never
	// lowering it, so a resumed browser knows where to continue. An upload no
	// longer 'receiving' is ErrUploadAlreadyComplete and keeps its length.
	UpdateReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error
	// Abort marks one 'receiving' upload 'aborted'. One no longer receiving,
	// completed since the caller read it, is ErrUploadAlreadyComplete and
	// stays as it is.
	Abort(ctx context.Context, id uuid.UUID) error
	// Abandoned lists up to limit uploads still 'receiving' whose last
	// activity is older than cutoff, oldest first.
	Abandoned(ctx context.Context, cutoff time.Time, limit int) ([]R, error)
	// InUse reports whether anything still needs id's bytes: an upload still
	// receiving, or the file its kind is currently built from. A row existing
	// is not enough, since a displaced upload's row stays as history.
	InUse(ctx context.Context, id uuid.UUID) (bool, error)
}

// UploadRow is what Uploads asks of either kind's row. Its methods are
// unexported, so only this package's Upload and TableData satisfy it.
type UploadRow[R any] interface {
	progress() uploadProgress
	// aborted is the row as Abort leaves it.
	aborted() R
	// abortEntry is the audit entry of an abort by actor, nil for the janitor.
	abortEntry(actor *uuid.UUID) audit.Entry
}

// uploadProgress is the part of a row the lifecycle decides on.
type uploadProgress struct {
	ID            uuid.UUID
	ContestID     uuid.UUID
	ReceivedBytes int64
	Status        UploadStatus
}

// Uploads is one kind of chunked upload: its own gamefile.Store and limits,
// its own rows, and the hooks for what its kind adds to the lifecycle. Games
// builds one for dumps and one for table CSVs; completing either is Games's,
// since that is where the kinds differ (CompleteUpload, CompleteTableUpload).
type Uploads[R UploadRow[R]] struct {
	rows UploadRows[R]
	// files is nil, and every call but Get answers ErrUploadsDisabled, until
	// Games is given this kind's volume.
	files  *gamefile.Store
	limits gamefile.Limits

	// What the lifecycle asks of the game it belongs to: whether the contest
	// may still change it, the transaction an abort and its audit entry share,
	// the audit trail, and the clock the janitor ages uploads by.
	editable func(ctx context.Context, contestID uuid.UUID) error
	uow      unitOfWork
	record   func(ctx context.Context, entry audit.Entry) error
	now      func() time.Time
	// checkName refuses a name this kind cannot take, before anything is
	// reserved.
	checkName func(ctx context.Context, contestID uuid.UUID, name string) error
	// firstChunk, when set, looks at an upload once its chunk at offset 0 is
	// recorded, so a wrong file is refused after one chunk rather than all of
	// them.
	firstChunk func(ctx context.Context, row R) error
	// bound, when set, admits one chunk at a time per contest, sending
	// account and contest owner. Without it a chunk reads only its row.
	bound *chunkBound[R]
}

// chunkBound is the admission a kind that holds its chunks in memory needs: a
// chunk is charged to its contest, its sender and the contest's owner, whom
// owners reads with the row in one statement, since a chunk arrives hundreds
// of times per upload.
type chunkBound[R any] struct {
	owners interface {
		WithOwner(ctx context.Context, id uuid.UUID) (R, uuid.UUID, error)
	}
	inFlight chunksInFlight
}

// use gives the kind its volume. limits must be the Limits files was built
// with: an oversized upload is refused before Store.Begin reserves anything
// (CLAUDE.md rule 12).
func (u *Uploads[R]) use(files *gamefile.Store, limits gamefile.Limits) {
	u.files, u.limits = files, limits
}

// volume is the kind's store, for what Games itself reads and writes there:
// sealing a dump, building from it, and a table's typed rows. nil until use.
func (u *Uploads[R]) volume() *gamefile.Store { return u.files }

// Limits reports the ceilings this kind enforces, so the API publishes the
// values it checks (CLAUDE.md rule 11). The bool is false, with zero Limits,
// when the kind has no volume; disabled is not the same as a limit of zero.
func (u *Uploads[R]) Limits() (gamefile.Limits, bool) {
	return u.limits, u.files != nil
}

func (u *Uploads[R]) enabled() bool { return u.files != nil }

// Begin reserves a new upload of name for a contest's game.
//
// The declared length, the name and editability are checked before anything
// is reserved, so a running contest or an oversized file is refused at the
// start rather than after the transfer (CLAUDE.md rule 12). The file is
// reserved before the row: a refused INSERT (most often ErrUploadInProgress)
// leaves at worst a file with no row, which the janitor finds, rather than a
// row with nothing on disk.
func (u *Uploads[R]) Begin(ctx context.Context, contestID uuid.UUID, name string, declaredBytes int64) (R, error) {
	var none R
	if !u.enabled() {
		return none, ErrUploadsDisabled
	}
	if declaredBytes <= 0 || declaredBytes > u.limits.MaxFileBytes {
		return none, ErrUploadTooLarge
	}
	if err := u.checkName(ctx, contestID, name); err != nil {
		return none, err
	}
	if err := u.editable(ctx, contestID); err != nil {
		return none, err
	}

	id := uuid.New()
	// Store.Begin checks declaredBytes against what the directory has left, so
	// a full volume refuses now rather than mid-transfer (CLAUDE.md rule 11).
	if err := u.files.Begin(id.String(), declaredBytes); err != nil {
		return none, wrapGamefileErr(err)
	}
	row, err := u.rows.Begin(ctx, id, contestID, name, declaredBytes)
	if err != nil {
		// Store.Begin counts declaredBytes against the directory until the
		// upload ends, so a few refused begins would spend the volume's budget
		// for every contest until the janitor runs. Best effort, since the
		// janitor is still behind it.
		_ = u.retire(id)
		if errors.Is(err, ErrUploadInProgress) {
			return none, ErrUploadInProgress
		}
		return none, fmt.Errorf("record the upload: %w", err)
	}
	return row, nil
}

// Append writes one chunk at offset of an upload already begun, for actorID,
// and returns how much of the upload has arrived.
//
// A chunk the store already holds is a retry and records nothing (CLAUDE.md
// rule 6). Progress is recorded only while the upload is still 'receiving',
// so a chunk that finishes arriving after its upload completed leaves the
// record alone and answers ErrUploadAlreadyComplete.
func (u *Uploads[R]) Append(ctx context.Context, actorID, contestID, id uuid.UUID, offset int64, r io.Reader) (int64, error) {
	if !u.enabled() {
		return 0, ErrUploadsDisabled
	}
	row, owner, err := u.chunkRow(ctx, id)
	if err != nil {
		return 0, err
	}
	at := row.progress()
	if at.ContestID != contestID {
		// Another contest's upload reads as missing, so its existence does
		// not leak.
		return 0, ErrUploadNotFound
	}
	if at.Status != UploadReceiving {
		return 0, ErrUploadAlreadyComplete
	}

	if u.bound != nil {
		if !u.bound.inFlight.enter(contestID, actorID, owner) {
			return 0, ErrUploadBusy
		}
		// Deferred: a body reader that panics must not leave the contest and
		// the accounts refused until the process restarts.
		defer u.bound.inFlight.leave(contestID, actorID, owner)
	}
	received, err := u.files.Append(id.String(), offset, r)
	if err != nil {
		return received, wrapGamefileErr(err)
	}
	if received == at.ReceivedBytes {
		return received, nil
	}
	if err := u.rows.UpdateReceived(ctx, id, received); err != nil {
		if errors.Is(err, ErrUploadAlreadyComplete) {
			// Completed or cancelled while this chunk was arriving. Its bytes
			// are past the record, where nothing reads.
			return received, ErrUploadAlreadyComplete
		}
		return received, fmt.Errorf("record the upload's progress: %w", err)
	}

	// Runs at most once per upload: a resent first chunk took the retry
	// return above.
	if offset == 0 && u.firstChunk != nil {
		if err := u.firstChunk(ctx, row); err != nil {
			return received, err
		}
	}
	return received, nil
}

// chunkRow reads the row a chunk is for, and its contest's owner when the
// kind bounds chunks by it.
func (u *Uploads[R]) chunkRow(ctx context.Context, id uuid.UUID) (R, uuid.UUID, error) {
	if u.bound == nil {
		row, err := u.rows.ByID(ctx, id)
		return row, uuid.Nil, err
	}
	return u.bound.owners.WithOwner(ctx, id)
}

// Get reads one upload of this contest by id, whatever its status, with or
// without a volume: a file-sourced game's row outlives the configuration. It
// is the authorisation boundary: an upload of another contest answers
// ErrUploadNotFound, so its existence does not leak.
func (u *Uploads[R]) Get(ctx context.Context, contestID, id uuid.UUID) (R, error) {
	var none R
	row, err := u.rows.ByID(ctx, id)
	if err != nil {
		return none, err
	}
	if row.progress().ContestID != contestID {
		return none, ErrUploadNotFound
	}
	return row, nil
}

// receiving is Get for an upload a caller is about to complete or abort.
func (u *Uploads[R]) receiving(ctx context.Context, contestID, id uuid.UUID) (R, error) {
	var none R
	if !u.enabled() {
		return none, ErrUploadsDisabled
	}
	row, err := u.Get(ctx, contestID, id)
	if err != nil {
		return none, err
	}
	if row.progress().Status != UploadReceiving {
		return none, ErrUploadAlreadyComplete
	}
	return row, nil
}

// Abort cancels an upload before it completed. It never touches the game: an
// aborted upload never became part of it.
func (u *Uploads[R]) Abort(ctx context.Context, actorID, contestID, id uuid.UUID) (R, error) {
	row, err := u.receiving(ctx, contestID, id)
	if err != nil {
		return row, err
	}
	return u.abort(ctx, &actorID, row)
}

// abort marks the row 'aborted', then removes its file. actor is nil for the
// janitor's sweep, a system event.
//
// The row goes first because a completion may have taken it since it was
// read: that abort is refused as ErrUploadAlreadyComplete, and the file the
// completed upload now is stays. A removal that fails after the commit is
// left to the orphan sweep, which an aborted row no longer holds back.
func (u *Uploads[R]) abort(ctx context.Context, actor *uuid.UUID, row R) (R, error) {
	var none R
	at := row.progress()
	err := u.uow.Do(ctx, func(ctx context.Context) error {
		if err := u.rows.Abort(ctx, at.ID); err != nil {
			if errors.Is(err, ErrUploadAlreadyComplete) {
				return ErrUploadAlreadyComplete
			}
			return fmt.Errorf("mark the upload aborted: %w", err)
		}
		return u.record(ctx, row.abortEntry(actor))
	})
	if err != nil {
		return none, err
	}
	_ = u.retire(at.ID)
	return row.aborted(), nil
}

// retire removes one upload's file from disk. It is idempotent: a cancel, a
// displacement and the janitor can each retire the same id twice (a retried
// request, or a crash half way), so gamefile.ErrNotFound is success.
func (u *Uploads[R]) retire(id uuid.UUID) error {
	if err := u.files.Abort(id.String()); err != nil && !errors.Is(err, gamefile.ErrNotFound) {
		return wrapGamefileErr(err)
	}
	return nil
}

// abandonedUploadBatchLimit bounds one sweep, as ReclaimBatchLimit bounds
// Reclaim, so a fresh deployment's first tick does not abort every upload
// ever left behind in one pass.
const abandonedUploadBatchLimit = 100

// orphanFileGrace is how old a file must be before the orphan sweep considers
// it. A file is reserved before the row naming it is written, so a younger
// file may belong to an upload whose INSERT has not committed yet; removing
// it would break an upload just handed to the organiser. The sweep runs
// every ten minutes (internal/app.abandonedUploads).
const orphanFileGrace = 15 * time.Minute

// sweep aborts every upload left 'receiving' past olderThan and removes every
// file nothing needs any more, returning how many of each.
//
// The file sweep matters more. Every other path starts from a row, so a file
// no row needs (left by a crash between Store.Begin and the INSERT, or
// between a replacement's commit and its removal) is found by nothing else,
// and may be a multi-gigabyte dump.
func (u *Uploads[R]) sweep(ctx context.Context, olderThan time.Duration) (abandoned, orphans int, err error) {
	if !u.enabled() {
		return 0, 0, nil
	}
	var failures []error

	rows, err := u.rows.Abandoned(ctx, u.now().Add(-olderThan), abandonedUploadBatchLimit)
	if err != nil {
		failures = append(failures, fmt.Errorf("list abandoned uploads: %w", err))
	}
	for _, row := range rows {
		_, err := u.abort(ctx, nil, row)
		switch {
		case errors.Is(err, ErrUploadAlreadyComplete):
			continue // completed or cancelled since it was listed
		case err != nil:
			failures = append(failures, fmt.Errorf("abandon upload %s: %w", row.progress().ID, err))
			continue
		}
		abandoned++
	}

	orphans, err = u.sweepOrphanFiles(ctx)
	if err != nil {
		failures = append(failures, err)
	}
	return abandoned, orphans, errors.Join(failures...)
}

// sweepOrphanFiles removes every file older than orphanFileGrace that nothing
// needs (UploadRows.InUse). That covers a file no row names and a displaced
// upload whose removal after its replacement's commit was interrupted. The ids
// come from gamefile.Store.UploadIDs because the file layout is gamefile's to
// know.
func (u *Uploads[R]) sweepOrphanFiles(ctx context.Context) (int, error) {
	ids, err := u.files.UploadIDs(u.now().Add(-orphanFileGrace))
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
		inUse, err := u.rows.InUse(ctx, id)
		if err != nil {
			failures = append(failures, fmt.Errorf("check upload %s: %w", id, err))
			continue
		}
		if inUse {
			continue
		}
		if err := u.retire(id); err != nil {
			failures = append(failures, fmt.Errorf("remove orphan file %s: %w", id, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(failures...)
}

// UploadCleanupResult is one janitor pass, summed over both kinds of upload.
type UploadCleanupResult struct {
	// Abandoned counts uploads left 'receiving' past their grace period.
	Abandoned int
	// OrphanFiles counts files on either volume nothing needs any more.
	OrphanFiles int
}

// SweepUploads is the abandoned-upload janitor, over dumps and table CSVs
// alike: every 'receiving' row older than olderThan is aborted, and every file
// nothing needs any more is removed.
func (g *Games) SweepUploads(ctx context.Context, olderThan time.Duration) (UploadCleanupResult, error) {
	dumpsAbandoned, dumpOrphans, dumpsErr := g.dumps.sweep(ctx, olderThan)
	tablesAbandoned, tableOrphans, tablesErr := g.tables.sweep(ctx, olderThan)
	if dumpsErr != nil {
		dumpsErr = fmt.Errorf("dumps: %w", dumpsErr)
	}
	if tablesErr != nil {
		tablesErr = fmt.Errorf("table data: %w", tablesErr)
	}
	return UploadCleanupResult{
		Abandoned:   dumpsAbandoned + tablesAbandoned,
		OrphanFiles: dumpOrphans + tableOrphans,
	}, errors.Join(dumpsErr, tablesErr)
}

// chunksInFlight is who has a chunk arriving: which contests and which
// accounts. A chunk is charged to its contest, to the account sending it and
// to the contest's owner, and each takes one of the store's buffers at a
// time. So one organiser cannot hold every buffer the installation has, with
// however many contests, tables, parallel requests or managers of their own
// appointing. The zero value is ready to use.
type chunksInFlight struct {
	mu       sync.Mutex
	contests map[uuid.UUID]struct{}
	accounts map[uuid.UUID]struct{}
}

// enter reports whether neither contest nor any of accounts had a chunk
// arriving, and marks them all as having one until leave with the same
// arguments.
func (c *chunksInFlight) enter(contest uuid.UUID, accounts ...uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.contests[contest]; busy {
		return false
	}
	for _, account := range accounts {
		if _, busy := c.accounts[account]; busy {
			return false
		}
	}
	if c.contests == nil {
		c.contests, c.accounts = map[uuid.UUID]struct{}{}, map[uuid.UUID]struct{}{}
	}
	c.contests[contest] = struct{}{}
	for _, account := range accounts {
		c.accounts[account] = struct{}{}
	}
	return true
}

func (c *chunksInFlight) leave(contest uuid.UUID, accounts ...uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.contests, contest)
	for _, account := range accounts {
		delete(c.accounts, account)
	}
}

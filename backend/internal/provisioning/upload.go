package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/gamefile"
	"github.com/google/uuid"
)

// MaxUploadFilenameBytes bounds an upload's filename (CLAUDE.md rule 2). The
// name is only shown to the organiser, never used as a path. A CHECK in
// migration 24 enforces the same bound.
const MaxUploadFilenameBytes = 255

// validUploadFilename reports whether a name may be stored and shown.
//
// Control characters are refused. PostgreSQL rejects a NUL in text, which
// would make the row's INSERT a 500 after the file already exists
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

// An uploaded dump is one kind of chunked upload (chunked.go): a finished SQL
// file whose completion replaces the contest's game.

// Upload is the database's record of one uploaded dump. Its bytes live in
// gamefile.Store, under the row's ID.
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

// ErrUploadFilenameInvalid is an empty filename, one past
// MaxUploadFilenameBytes, or one carrying a control character.
var ErrUploadFilenameInvalid = errors.New("the upload's filename is invalid")

// checkUploadFilename is Uploads.checkName for a dump.
func checkUploadFilename(_ context.Context, _ uuid.UUID, name string) error {
	if !validUploadFilename(name) {
		return ErrUploadFilenameInvalid
	}
	return nil
}

func (u Upload) progress() uploadProgress {
	return uploadProgress{ID: u.ID, ContestID: u.ContestID, ReceivedBytes: u.ReceivedBytes, Status: u.Status}
}

func (u Upload) aborted() Upload {
	u.Status = UploadAborted
	return u
}

// abortEntry never carries the content: only what identifies the file.
func (u Upload) abortEntry(actor *uuid.UUID) audit.Entry {
	return audit.Entry{
		ActorID: actor, Action: audit.ActionGameUploadAbort,
		Entity: "contest", EntityID: u.ContestID.String(),
		Payload: map[string]any{"filename": u.Filename, "bytes": u.ReceivedBytes},
	}
}

// DumpRows is UploadRows for uploaded dumps, with the one read only a dump
// has: the contest's upload in progress, of which there is at most one.
type DumpRows interface {
	UploadRows[Upload]
	// Current reads a contest's one 'receiving' upload, or ErrUploadNotFound.
	Current(ctx context.Context, contestID uuid.UUID) (Upload, error)
}

// Dumps is the dump's Uploads, with the one read only a sealed upload has.
type Dumps struct{ *Uploads[Upload] }

// DumpUploads begins, receives, aborts and reads back a contest's uploaded
// dumps; CompleteUpload is what turns one into the game.
func (g *Games) DumpUploads() Dumps { return Dumps{g.dumps} }

// Window reads a slice of a completed dump's lines, for the console's preview
// of a script it will not run yet.
func (d Dumps) Window(ctx context.Context, contestID, id uuid.UUID, fromLine, maxLines int, maxBytes int64) (gamefile.Window, error) {
	if !d.enabled() {
		return gamefile.Window{}, ErrUploadsDisabled
	}
	if _, err := d.Get(ctx, contestID, id); err != nil {
		return gamefile.Window{}, err
	}
	// ctx lets a page the organiser left stop its read of a large file.
	window, err := d.files.Window(ctx, id.String(), fromLine, maxLines, maxBytes)
	if err != nil {
		return gamefile.Window{}, wrapGamefileErr(err)
	}
	return window, nil
}

// CurrentUpload reads a contest's one upload still 'receiving', or
// ErrUploadNotFound when there is none, so a reloaded page can resume it.
func (g *Games) CurrentUpload(ctx context.Context, contestID uuid.UUID) (Upload, error) {
	return g.dumpRows.Current(ctx, contestID)
}

// displacedUpload names the upload whose file this contest's next game will
// leave behind: the current game's own, when that game is file-sourced.
//
// It is nil when there is nothing to retire: no game yet (ErrNoGame), a game
// written in the editor, a game that already names keeping, or no upload
// volume. Call it before the game is written, since SaveScript and
// CompleteUpload overwrite upload_id.
func (g *Games) displacedUpload(ctx context.Context, contestID uuid.UUID, keeping *uuid.UUID) (*uuid.UUID, error) {
	if !g.dumps.enabled() {
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

// CompleteUpload seals an upload begun with DumpUploads, replaces the
// contest's game with it, and retires whichever upload it displaces.
//
// Hashing and indexing the file (Store.Complete) runs before any transaction
// opens, so no core-database transaction is held for that long; only the row
// updates run inside one, through replaceGame, as SetScript does. The contest
// can start during the hashing and the replacement then be refused, so the
// displaced file is removed only after the commit (see replaceGame).
func (g *Games) CompleteUpload(ctx context.Context, actorID, contestID, uploadID uuid.UUID) (Template, error) {
	upload, err := g.dumps.receiving(ctx, contestID, uploadID)
	if err != nil {
		return Template{}, err
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return Template{}, err
	}

	summary, err := g.dumps.volume().Complete(uploadID.String(), upload.DeclaredBytes)
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

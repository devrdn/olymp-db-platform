package provisioning

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// Why a game could not be replaced or built.
var (
	// ErrScriptEmpty is a game with no SQL in it. Refused rather than built,
	// because an empty template produces an empty database and a contest
	// whose every question answers "no such table".
	ErrScriptEmpty = errors.New("the game script is empty")
	// ErrScriptTooLong is a script past MaxScriptBytes.
	ErrScriptTooLong = errors.New("the game script is too long")
	// ErrGameNotEditable is a contest whose game may no longer be replaced.
	//
	// The whole reason this refusal exists: replacing a game bumps its
	// version, every participant's copy is then stale, and a stale copy is
	// dropped and made again from the new template. In a running olympiad
	// that is every participant losing their database at once — so the gate
	// is the contest's own ContentEditable, the same one the story and the
	// SQL policy sit behind.
	ErrGameNotEditable = errors.New("the contest's game can no longer be replaced")
	// ErrBuildInProgress is a build somebody else already claimed.
	ErrBuildInProgress = errors.New("the game is already being built")
)

// ScriptFailure is the one build failure whose words belong to the organiser:
// PostgreSQL's verdict on a statement in their own script.
//
// Declared here, by the consumer that needs the distinction (Go layout rule
// 3), so that this package — which decides what an organiser is told — keeps
// knowing nothing about the cluster or the driver that produced the error.
// gamedb.ScriptError satisfies it, and is produced at the one place an
// organiser's SQL is executed.
//
// The distinction cannot be made here by inspecting the error, which is why it
// is an interface a producer opts into rather than a type switch: pgx nests a
// *pgconn.PgError inside the *pgconn.ConnectError it returns for a refused
// login, so "does this contain a database error?" answers yes for a connection
// that never opened, and the answer names the role, the host and the port
// (internal/rpc.classify, which had to be rewritten around exactly that).
type ScriptFailure interface {
	error
	// ScriptRejection is the text that may be served to whoever wrote the
	// script and kept in the audit trail.
	ScriptRejection() string
}

// BuildFailedInternally is what a failed build tells an organiser when the
// failure was not their script.
//
// A fixed sentence, never the error's own text. Everything on that side of the
// line — a cluster that would not take a connection, a CREATE DATABASE that
// was refused, the grants applied after the script — names the provisioning
// role, the cluster's host and port, or the internal database; and this string
// has two sinks that make it permanent: GET /contests/{id}/game serves it to
// anybody holding contest.view, and Build below writes it into the
// contest.game_built audit payload, which is append-only. The real error is
// returned to the caller instead, for the service log, where an operator can
// have it and a manager cannot.
const BuildFailedInternally = "The game could not be built. The failure was not in the script — ask an administrator to check the service log."

// MaxBuildErrorBytes bounds what a failed build may leave behind about
// itself (CLAUDE.md rule 2).
//
// The one build failure whose text is not ours is a script refusal, and a
// script refusal quotes the organiser's own file back at them — a file this
// service accepts up to GAME_UPLOAD_MAX_FILE_BYTES of and validates nothing
// about. That string has two sinks that keep it: game_templates.build_error,
// an unbounded `text` column, and the contest.game_built audit payload, which
// is append-only. The 1 MiB body limit bounds the request that started the
// build, not this field, and the reader's own bounds stop a *statement* from
// growing, not a message assembled out of one.
//
// Sixteen kibibytes is far more than PostgreSQL's own longest verdict
// (message, detail and hint together are hundreds of bytes) and more than any
// refusal this codebase writes, while staying a size a person can read on a
// screen — which is the whole purpose of keeping the text at all.
const MaxBuildErrorBytes = 16 << 10

// boundBuildError is what every build outcome passes through before it is
// stored, served or recorded.
//
// Two different failures, both fatal in the same slow way. A refusal past
// MaxBuildErrorBytes is the rule above. A refusal PostgreSQL cannot store at
// all is worse: build_error is a `text` column and the audit payload is
// `jsonb`, both of which refuse an invalid byte sequence with SQLSTATE 22021,
// and that refusal comes from FinishBuild — so the row is never moved out of
// 'building', the stale-build sweep claims it again, and the game spends
// every staleBuildAfter interval doing a DROP DATABASE and a CREATE DATABASE
// on the cluster an olympiad is running on, for ever, without ever becoming
// ready. A dump is raw bytes; the reader quotes them; this is the boundary
// where they become text.
//
// "Cannot store" is two conditions and not one, which is the trap this
// function was written into. Invalid UTF-8 is the obvious half. The other is
// U+0000: it is *valid* UTF-8 — Go encodes it as the single byte \x00 and
// utf8.ValidString says yes — and PostgreSQL still refuses it in `text` and
// `jsonb` with the same 22021, because a NUL cannot exist in either. It is
// also the likeliest byte to arrive here: an organiser who exports with
// `pg_dump -Fc` uploads a binary file, and the reader quotes those bytes back
// at them in its refusal. So both are replaced with U+FFFD.
//
// Replaced rather than dropped, and cut on a rune boundary rather than at a
// byte count, so what an organiser reads is still their own message with a
// visible mark where it stopped. The rewrite is a single bounded pass rather
// than a whole-string ReplaceAll before the cut: the input is an organiser's
// own file quoted back, up to GAME_UPLOAD_MAX_FILE_BYTES of it, and each
// substituted byte grows to three — a sanitising pass over the whole of it
// would allocate three times a file this service already refuses to hold in
// memory (CLAUDE.md rule 12).
func boundBuildError(text string) string {
	if len(text) <= MaxBuildErrorBytes && utf8.ValidString(text) && !strings.ContainsRune(text, 0) {
		return text // the ordinary case: our own sentence, or PostgreSQL's
	}

	const ellipsis = "\n[…]"
	const replacement = '�'
	limit := MaxBuildErrorBytes - len(ellipsis)

	var out strings.Builder
	out.Grow(MaxBuildErrorBytes)
	// Ranging a string decodes it: an invalid byte comes back as
	// utf8.RuneError with a width of one, which is exactly the substitution
	// strings.ToValidUTF8 made, and U+0000 comes back as itself.
	for _, r := range text {
		if r == utf8.RuneError || r == 0 {
			r = replacement
		}
		if out.Len()+utf8.RuneLen(r) > limit {
			return out.String() + ellipsis
		}
		out.WriteRune(r)
	}
	return out.String()
}

// MaxScriptBytes bounds the SQL one game may carry (CLAUDE.md rule 2).
//
// The column is an unbounded `text`, and the request body limit bounds the
// request rather than this field. Half a mebibyte is far more SQL than an
// olympiad's game is: the design's own example is seven tables and a few
// hundred rows. A game that genuinely needs more rows than this writes them
// with `INSERT ... SELECT generate_series(...)`, which is both shorter and
// what somebody reviewing the script would rather read.
const MaxScriptBytes = 512 << 10

// TemplateStatus is where one contest's game has got to.
type TemplateStatus string

const (
	// TemplatePending is a script stored and not yet built. The state every
	// game passes through, including a rebuild.
	TemplatePending TemplateStatus = "pending"
	// TemplateBuilding is a build claimed by one worker.
	TemplateBuilding TemplateStatus = "building"
	// TemplateReady is a template database that exists and can be copied.
	TemplateReady TemplateStatus = "ready"
	// TemplateFailed is a build that ran and did not finish. BuildError says
	// why, in PostgreSQL's own words, because they are the useful ones to
	// whoever wrote the script.
	TemplateFailed TemplateStatus = "failed"
	// TemplateDropped is a template the reclaim sweep has removed (§2.4).
	TemplateDropped TemplateStatus = "dropped"
)

// TemplateSource says which of the three ways an organiser built this game:
// wrote (or pasted) it in the editor, uploaded a finished dump (migration
// 24), or described it structurally with the table builder (migration 26).
// The three share every other column — version, status, the build queue —
// because replacing a game is one event whichever path produced it (see
// Games.replaceGame).
type TemplateSource string

const (
	// SourceEditor is a script an organiser wrote or pasted directly. The
	// default for every row that predates migration 24.
	SourceEditor TemplateSource = "editor"
	// SourceFile is a script that arrived as a completed upload. Script is
	// empty for these rows — migration 24's own doc explains why the column
	// stays NOT NULL rather than growing a second branch — and UploadID names
	// which row of game_uploads it came from.
	SourceFile TemplateSource = "file"
	// SourceBuilder is a game described structurally — tables, columns, a
	// primary key — rather than written as SQL. Script is empty for these
	// rows too, the same reason it is empty for SourceFile: inventing one
	// here would claim a script the organiser never wrote. Definition
	// carries what they actually saved, and the SQL it is built from is
	// generated from it by a later task, not stored on this row.
	SourceBuilder TemplateSource = "builder"
)

// Template is one contest's game, as an organiser sees it.
type Template struct {
	ContestID uuid.UUID
	Database  string
	Version   int
	Status    TemplateStatus
	Source    TemplateSource
	// UploadID names the game_uploads row a file-sourced game came from. Nil
	// for SourceEditor and SourceBuilder — the pairing migration 24's own
	// CHECK enforces.
	UploadID *uuid.UUID
	// Definition is the structural description a builder-sourced game was
	// saved from — tables, columns, a primary key. The zero value (no
	// tables) for SourceEditor and SourceFile, the same way UploadID is nil
	// for anything that is not SourceFile: migration 26's own CHECK pairs
	// Definition's presence with SourceBuilder specifically, symmetrically
	// with how migration 24's pairs UploadID's with SourceFile.
	Definition Definition
	// Script is the SQL an author wrote in the editor. Staff-trusted input:
	// it runs as gamedb.RoleAuthor, never as the provisioning role
	// (gamedb.Provisioner.BuildTemplate's own doc), which is why writing it
	// sits behind PermissionContestEdit and is written to the audit trail.
	// Empty for SourceFile — a file-sourced game's SQL is the uploaded
	// bytes themselves (internal/gamefile.Store.Open, via UploadID) — and
	// for SourceBuilder, whose SQL does not exist yet at all (Definition's
	// own doc). Inventing a Script for either would be lying about where the
	// game came from.
	Script string
	// ScriptBytes is how long Script is, and is filled in whether or not
	// Script itself was read. That is the whole of its reason to exist: a
	// console watching a build polls twice a second for as long as the build
	// runs, and the only thing it wants from the script is its length. Reading
	// the column to measure it meant a twenty-minute build with two organisers
	// watching pulled about six hundred megabytes of the same script out of the
	// core database, decoded it into Go strings and threw it away — see
	// TemplateRepository.TemplateStatus, which is the read that fills this in
	// without Script.
	ScriptBytes int
	BuildError  string
	// DataChangedAt is when this game's table data last changed, and nil when
	// nothing has changed since the build.
	//
	// The table builder's data arrives after the game is built and cannot
	// arrive before it — the build runs seconds after the definition is
	// saved, and AppendTableRow refuses a table that is not in the saved
	// definition. Without this mark the rows were stored and never loaded,
	// and every participant copied an empty database.
	DataChangedAt *time.Time
	UpdatedAt     time.Time
}

// Building reports whether a build is under way, which is what an interface
// polls on.
func (t Template) Building() bool { return t.Status == TemplateBuilding || t.Status == TemplatePending }

// NeedsBuild reports a game whose built database no longer holds the data an
// organiser has since put into it — the one state RequestBuild exists for.
//
// Only a game that is `ready`: a build already waiting or running will pick
// the data up on its own, and a failed build's own error is the thing to
// show rather than an invitation to press a button that would replace it.
func (t Template) NeedsBuild() bool {
	return t.Status == TemplateReady && t.DataChangedAt != nil
}

// TemplateRepository is the storage the game's own lifecycle needs, apart from
// the pool's (Repository).
type TemplateRepository interface {
	// SaveScript stores script as contest's game and puts it back to
	// pending, bumping the version when one was already there. The version it
	// returns is the one a build will be recorded against.
	SaveScript(ctx context.Context, contestID uuid.UUID, database, script string) (Template, error)
	// SaveDefinition stores definition as contest's game and puts it back to
	// pending, the same way SaveScript does for an editor's script — the
	// table builder (migration 26) runs through the identical upsert as the
	// other two sources, which is what keeps a rebuild, a version bump and a
	// cleared schema cache one mechanism rather than three that could drift.
	SaveDefinition(ctx context.Context, contestID uuid.UUID, database string, definition Definition) (Template, error)
	// Template reads one contest's game, or ErrNoGame.
	Template(ctx context.Context, contestID uuid.UUID) (Template, error)
	// TemplateStatus reads everything about one contest's game except the two
	// columns that carry its content: the script comes back as its length in
	// ScriptBytes rather than its bytes, and the structural definition is not
	// read at all. Or ErrNoGame, exactly as Template.
	//
	// A second read rather than a parameter on the first, because the two
	// answer different questions and only one of them is polled. An organiser
	// watching a build asks every two seconds for as long as it runs, and wants
	// a status, a version and a length; Template's own callers want the script
	// itself. A script may be megabytes (MaxScriptBytes), so twenty minutes of
	// two watchers is hundreds of megabytes read, decoded and discarded — plus
	// a json.Unmarshal of the definition on every one of them.
	TemplateStatus(ctx context.Context, contestID uuid.UUID) (Template, error)
	// ClaimBuild moves one game from pending to building and returns it.
	//
	// The conditional update is the race arbiter, the same way ClaimSpare's
	// is: two workers ticking at once, or two managers saving a script in the
	// same second, must not both run CREATE DATABASE against one name. A game
	// stuck in building for longer than stale is claimed too — an API that
	// died mid-build would otherwise leave it there for ever.
	ClaimBuild(ctx context.Context, stale time.Duration) (Template, error)
	// FinishBuild records the outcome against the version that was built. The
	// version is what keeps a slow build from marking a newer script ready:
	// a script saved while the build ran has already bumped it.
	//
	// claimedAt is what ClaimBuild wrote as the row's updated_at, and it
	// arbitrates the data mark: the build may only clear a change it
	// actually saw, so a row an organiser added while this build ran keeps
	// its mark and is picked up by the next one.
	FinishBuild(ctx context.Context, contestID uuid.UUID, version int, buildError string, claimedAt time.Time) error
	// MarkTableDataChanged records that this contest's table data no longer
	// matches the database its game was built into. Called by the three
	// writes that change a table's rows, inside their own transaction, so a
	// change that rolled back leaves no request to build behind it and a
	// change that landed never loses one.
	MarkTableDataChanged(ctx context.Context, contestID uuid.UUID) error
	// RequestBuild puts a ready or failed game back to pending and raises its
	// version, the same upsert SaveScript's own does. Or ErrBuildInProgress
	// when the row was not in a state to be asked — a build already waiting
	// or running, which the caller's own earlier check may have missed to a
	// second organiser pressing the same button in the same second.
	RequestBuild(ctx context.Context, contestID uuid.UUID) (Template, error)
	// Policy is what the contest lets participants do, which is what the
	// build grants inside the template.
	//
	// Read apart from Repository.Game, which answers only for a template that
	// is already 'ready' — the state a build is by definition not in. Reading
	// the policy through Game would therefore have meant every *first* build
	// silently using the defaults instead of what the organiser configured
	// (CLAUDE.md rule 11: the value that drives a check crosses every
	// boundary it has to).
	Policy(ctx context.Context, contestID uuid.UUID) (sqlpolicy.Policy, error)

	// The upload half of the same table group (migration 24), declared here
	// rather than as a second interface: Games is the one consumer of both,
	// and completing an upload replaces the game through the exact statement
	// group SaveScript uses (CompleteUpload's own doc) — splitting the two
	// would only make that sharing harder to see, not narrower to depend on.

	// BeginUpload records a new upload in 'receiving'. id is generated by the
	// caller (Games.BeginUpload), never by the database, so it can name the
	// same id on disk (internal/gamefile) before this row exists. The
	// "one upload in progress per contest" rule is the database's own —
	// game_uploads_one_receiving_idx — and a violation of it comes back as
	// ErrUploadInProgress rather than a bare constraint error.
	BeginUpload(ctx context.Context, id, contestID uuid.UUID, filename string, declaredBytes int64) (Upload, error)
	// Upload reads one upload by id, or ErrUploadNotFound.
	Upload(ctx context.Context, id uuid.UUID) (Upload, error)
	// CurrentUpload reads a contest's one 'receiving' upload, or
	// ErrUploadNotFound.
	CurrentUpload(ctx context.Context, contestID uuid.UUID) (Upload, error)
	// UpdateReceived records how many bytes Store.Append actually wrote, so a
	// resumed browser can be told where to continue from.
	UpdateReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error
	// CompleteUpload marks id 'complete' with what Store.Complete measured,
	// retires previous (nil unless a different upload is being displaced —
	// Games.CompleteUpload's own doc), and replaces the contest's game in the
	// same statement group SaveScript uses: same version bump, same pending
	// status, same cleared schema cache.
	CompleteUpload(ctx context.Context, contestID, id uuid.UUID, database string, summary UploadSummary, previous *uuid.UUID) (Template, error)
	// AbortUpload marks one upload 'aborted'. It does not touch
	// game_templates — an aborted upload never became anybody's game.
	AbortUpload(ctx context.Context, id uuid.UUID) error
	// AbandonedUploads lists up to limit uploads still 'receiving' whose
	// updated_at is older than cutoff — the janitor's own candidates
	// (internal/app/background.go).
	AbandonedUploads(ctx context.Context, cutoff time.Time, limit int) ([]Upload, error)
	// UploadInUse reports whether anything still needs id's bytes on the
	// volume: an upload still 'receiving' chunks, or one a contest's game is
	// actually built from.
	//
	// The question the janitor's other sweep has to ask, and not the same as
	// "does a row exist" — which is what it used to ask, and which answers
	// "keep it" for every upload a later game displaced. A completed upload
	// stops being needed the moment the game stops naming it, whether that was
	// a second upload or a script written in the editor, and its row stays
	// behind as history either way (the same convention MarkDropped keeps for
	// an instance). See Games.sweepOrphanFiles.
	UploadInUse(ctx context.Context, id uuid.UUID) (bool, error)

	// The table builder's own per-table CSV data (migration 27), declared
	// here for the same reason the upload half above is: Games is the one
	// consumer, and every one of these mirrors an upload method one row above
	// it — see tabledata.go for what each does with them.

	// BeginTableData records a new table-data upload in 'receiving'.
	BeginTableData(ctx context.Context, id, contestID uuid.UUID, table string, declaredBytes int64) (TableData, error)
	// TableDataByID reads one table-data row by id, or ErrTableDataNotFound.
	TableDataByID(ctx context.Context, id uuid.UUID) (TableData, error)
	// CurrentTableData reads a table's one 'receiving' upload, or
	// ErrTableDataNotFound.
	CurrentTableData(ctx context.Context, contestID uuid.UUID, table string) (TableData, error)
	// ReadyTableData reads a table's one 'complete' file — the one a window
	// read, a row append, a delete or a build actually acts on — or
	// ErrTableDataNotFound when the table has none yet.
	ReadyTableData(ctx context.Context, contestID uuid.UUID, table string) (TableData, error)
	// UpdateTableDataReceived records how many bytes Store.Append actually
	// wrote, the same job UpdateReceived does for a dump.
	UpdateTableDataReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error
	// CompleteTableData marks id 'complete' with what the validation pass
	// measured, and retires previous (nil unless a different upload for the
	// same table is being displaced).
	CompleteTableData(ctx context.Context, contestID uuid.UUID, table string, id uuid.UUID, receivedBytes, lines int64, previous *uuid.UUID) (TableData, error)
	// CreateReadyTableData records a table's very first row: a file that
	// starts life already 'complete' rather than passing through
	// 'receiving' (AppendTableRow's own bootstrap doc explains why one
	// validated row needs no separate completion step).
	CreateReadyTableData(ctx context.Context, id, contestID uuid.UUID, table string, bytes, lines int64) (TableData, error)
	// AppendTableDataRow records one more row appended to an already-'complete'
	// file: its new byte length and its new row count together, so the two
	// can never read as having disagreed even for an instant.
	//
	// Both are floors, not assignments: neither figure may go backwards, so a
	// caller working from an older snapshot than another's cannot write its
	// own smaller pair over the newer one. Two forms adding a row to the same
	// table at once is exactly that situation, and the order they reach
	// storage in is not the order they read in (Games.AppendTableRow's own
	// doc). ErrTableDataChanged when there is no longer a 'complete' row to
	// record against — the game was replaced under the call, say.
	AppendTableDataRow(ctx context.Context, id uuid.UUID, receivedBytes, lines int64) (TableData, error)
	// AbortTableData marks one table-data upload 'aborted'.
	AbortTableData(ctx context.Context, id uuid.UUID) error
	// DiscardTableData retires every table-data row a contest still has —
	// the file a table's rows live in and any upload still receiving one —
	// and returns the ids whose bytes are now nobody's, so the caller can
	// remove them once its own transaction has committed.
	//
	// What it exists for is the moment a game stops being built by the table
	// builder (Games.replaceGame): the rows are addressed by table name
	// against the contest's *current* definition, and a game that is a
	// script or a dump names no table any of them could belong to. Left
	// behind, they are data nothing checks and nothing deletes — and the
	// next builder definition that happens to name the same table would
	// load them, against columns they were never validated for.
	DiscardTableData(ctx context.Context, contestID uuid.UUID) ([]uuid.UUID, error)
	// DeleteTableDataRow tombstones one row of a 'complete' file — a single
	// atomic array_append, refusing (ErrTooManyDeletedRows) past migration
	// 27's own bound rather than growing the array without limit.
	DeleteTableDataRow(ctx context.Context, id uuid.UUID, row int64) error
	// AbandonedTableData lists up to limit table-data uploads still
	// 'receiving' whose updated_at is older than cutoff — the janitor's own
	// candidates, mirroring AbandonedUploads.
	AbandonedTableData(ctx context.Context, cutoff time.Time, limit int) ([]TableData, error)
	// TableDataInUse reports whether anything still needs id's bytes on the
	// volume, mirroring UploadInUse for a table's own file.
	TableDataInUse(ctx context.Context, id uuid.UUID) (bool, error)
}

// Authoring answers whether a contest's game may still be replaced.
//
// The rule is the contest's own — contests.Contest.ContentEditable, a draft
// or a published contest and nothing later — and this package asks for the
// answer rather than the status so that the rule stays in one place. See
// ErrGameNotEditable for what replacing a running contest's game would do.
type Authoring interface {
	GameEditable(ctx context.Context, contestID uuid.UUID) (bool, error)
}

// requireEditable is Authoring's answer as the refusal every write to a
// contest's game gives: ErrGameNotEditable once the contest has started, and
// the underlying failure wrapped when the answer could not be had. Where each
// write asks is argued at the write; what it does with the answer is the same
// everywhere, so it is written once.
func (g *Games) requireEditable(ctx context.Context, contestID uuid.UUID) error {
	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return ErrGameNotEditable
	}
	return nil
}

// Games is the half of Service that owns a contest's game: the script an
// organiser writes, and the template database built from it.
//
// Apart from Service, which owns the pool of copies made from that template.
// The two share a table and nothing else: one runs when a contest is being
// written, the other while it is being played.
type Games struct {
	repo    TemplateRepository
	cluster TemplateCluster
	author  Authoring
	audit   *audit.Recorder
	uow     unitOfWork
	now     func() time.Time
	// files and limits are set by WithUploads. files is nil on an
	// installation with no upload volume configured — GAME_UPLOAD_DIR empty,
	// the same convention QueryRunnerAddr uses to turn the console off — and
	// every upload method refuses with ErrUploadsDisabled rather than
	// dereferencing it. The janitor's orphan-file sweep (sweepOrphanFiles)
	// asks files.UploadIDs for what the volume holds rather than this
	// package keeping its own directory path to read with os.ReadDir —
	// gamefile owns its own on-disk layout, this package does not.
	files  *gamefile.Store
	limits gamefile.Limits
	// tableFiles and tableLimits are the table builder's own per-table CSV
	// storage (tabledata.go), set by WithTableData — a second, independent
	// gamefile.Store from files above, never the same one (WithTableData's
	// own doc explains why). nil exactly when WithTableData was never
	// called, the same convention files follows for uploads.
	tableFiles  *gamefile.Store
	tableLimits gamefile.Limits
	// rowMarks is where in a table's own CSV file each five-hundredth row
	// begins, learned as pages are read — what keeps paging through a table
	// from costing a scan of the file per page. See tableRowIndex.
	rowMarks tableRowIndex
}

// TemplateCluster is the one thing building a game asks of the cluster.
//
// script is an io.Reader rather than a string: gamedb.Provisioner.
// BuildTemplate streams it statement by statement instead of holding it all
// in memory, which is what makes an uploaded dump's own gigabytes buildable
// at all. Build below wraps an editor-sourced game's script in
// strings.NewReader; finishUploadBuild hands the uploaded file straight
// through — the same interface either way, so both sources run the identical
// path on the cluster.
type TemplateCluster interface {
	BuildTemplate(ctx context.Context, name string, script io.Reader, policy sqlpolicy.Policy) error
	// LoadTableData copies data into one table of a database BuildTemplate
	// already built — the table builder's own seam (finishDefinitionBuild's
	// own doc), run through the same COPY ... FROM STDIN protocol path an
	// uploaded dump's rows already use. columns is the column list, in the
	// definition's own order, that the COPY statement targets; data is the
	// CSV rows themselves, with no header line — the caller (Games.
	// loadTableData) has already read and checked that line, and it is not
	// data to load.
	LoadTableData(ctx context.Context, database, table string, columns []string, data io.Reader) error
	// Drop removes a database outright — what a data load that fails after
	// the schema already built must do to the half-loaded template, the same
	// teardown BuildTemplate gives its own failures.
	Drop(ctx context.Context, name string) error
}

// unitOfWork is the transaction boundary a save and its audit entry share.
type unitOfWork interface {
	Do(ctx context.Context, fn func(context.Context) error) error
}

// NewGames assembles the game half.
func NewGames(repo TemplateRepository, cluster TemplateCluster, author Authoring) *Games {
	return &Games{repo: repo, cluster: cluster, author: author, now: time.Now}
}

// WithAudit records who replaced a game and when. Without it nothing is
// recorded — which is what the tests that are not about the trail use.
func (g *Games) WithAudit(recorder *audit.Recorder, uow unitOfWork) *Games {
	g.audit, g.uow = recorder, uow
	return g
}

// atomically runs fn inside the unit of work WithAudit supplied, so a write
// and the audit entry recording it land together or not at all — and simply
// runs it when there is none, which is the arrangement the tests that are not
// about the trail use.
func (g *Games) atomically(ctx context.Context, fn func(context.Context) error) error {
	if g.uow == nil {
		return fn(ctx)
	}
	return g.uow.Do(ctx, fn)
}

// record writes one audit entry, or nothing when the service was assembled
// without a trail.
func (g *Games) record(ctx context.Context, entry audit.Entry) error {
	if g.audit == nil {
		return nil
	}
	return g.audit.Record(ctx, entry)
}

// WithUploads turns on the file-upload half of a contest's game. files is
// the disk store an organiser's chunks land in; limits is the same Limits
// files was constructed with, kept here too because Games has to refuse an
// oversized upload before it ever reaches Store.Begin (CLAUDE.md rule 12 —
// bounded where the bytes arrive, not after a reservation was already made).
//
// Left uncalled, every upload method answers ErrUploadsDisabled — the state
// of an installation with no GAME_UPLOAD_DIR configured, the same convention
// QueryRunnerAddr uses to turn the SQL console off.
func (g *Games) WithUploads(files *gamefile.Store, limits gamefile.Limits) *Games {
	g.files, g.limits = files, limits
	return g
}

// UploadLimits reports the ceilings this installation applies to a chunked
// upload — the very gamefile.Limits WithUploads was given, not a second copy
// of it. internal/api's GameHandler asks for this rather than reading
// GAME_UPLOAD_CHUNK_BYTES / GAME_UPLOAD_MAX_FILE_BYTES from configuration a
// second time and publishing that instead: the value that decides whether a
// chunk is accepted (AppendChunk, wrapping gamefile.ErrChunkTooLarge) and the
// value a client is told to expect must be the one this package actually
// enforces, or a deployment where the two drifted would refuse every chunk a
// browser sends while telling that same browser its chunks are the right
// size (CLAUDE.md rule 11).
//
// Enabled is false, and Limits is the zero value, exactly when WithUploads
// was never called — no GAME_UPLOAD_DIR configured, the same state every
// upload method already answers with ErrUploadsDisabled. A caller must check
// Enabled before reading either number: an installation with uploads off is
// not the same fact as one whose operator configured a limit of zero, and
// this is what lets the two be told apart.
func (g *Games) UploadLimits() (gamefile.Limits, bool) {
	return g.limits, g.files != nil
}

// Of reads one contest's game, or ErrNoGame when it has none yet.
func (g *Games) Of(ctx context.Context, contestID uuid.UUID) (Template, error) {
	return g.repo.Template(ctx, contestID)
}

// StatusOf is Of without the game's own content: the script's length instead of
// the script, and no structural definition at all. ErrNoGame when the contest
// has no game yet, the same as Of.
//
// What a console polling a running build should ask for — see
// TemplateRepository.TemplateStatus for what the difference costs when it does
// not.
func (g *Games) StatusOf(ctx context.Context, contestID uuid.UUID) (Template, error) {
	return g.repo.TemplateStatus(ctx, contestID)
}

// Script returns the SQL one contest's game is built from, whether the
// contest has a game at all, and whether that game's SQL is not something a
// package can carry.
//
// It satisfies contests.GameSource — the one method the contest package's
// export asks of a game, declared over there by the consumer (Go layout rule
// 3) so that the contest package never imports this one. Of returns the whole
// Template, statuses, versions and build errors included, and none of that is
// part of a contest package: what an organizer re-authors is the script.
//
// A contest with no game is an absent game, not an error — exporting a draft
// whose game has not been written yet is ordinary. Anything else is reported,
// because "no game" makes the export succeed with a package that carries
// none, and a storage failure quietly wearing that answer would ship an
// incomplete package as a complete one.
//
// The third value is the one a SourceFile or SourceBuilder game needs.
// Template.Script is empty for those by construction — a file-sourced
// game's SQL is the uploaded bytes, on the API host's own volume, up to
// GAME_UPLOAD_MAX_FILE_BYTES of them, and a builder-sourced game's SQL does
// not exist at all yet (Definition's own doc) — and returning that empty
// string as "the game" told the export a contest had a game and then gave
// it nothing, which re-imports as ErrScriptEmpty. This package is the only
// one that knows the difference, so this is where it has to be said
// (CLAUDE.md rule 11).
func (g *Games) Script(ctx context.Context, contestID uuid.UUID) (script string, ok, omitted bool, err error) {
	template, err := g.repo.Template(ctx, contestID)
	switch {
	case errors.Is(err, ErrNoGame):
		return "", false, false, nil
	case err != nil:
		return "", false, false, fmt.Errorf("read the contest's game: %w", err)
	}
	if template.Source != SourceEditor {
		return "", true, true, nil
	}
	return template.Script, true, false, nil
}

// SetScript stores the SQL one contest's game is built from.
//
// It does not build. Storing puts the game back to pending and a worker picks
// it up (Build), because building creates a database and runs an author's
// whole script inside it — seconds at best, and not something to hold an HTTP
// request open for. The status is what an organiser watches instead.
func (g *Games) SetScript(ctx context.Context, actorID, contestID uuid.UUID, script string) (Template, error) {
	switch {
	case len(script) == 0:
		return Template{}, ErrScriptEmpty
	case len(script) > MaxScriptBytes:
		return Template{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrScriptTooLong, len(script), MaxScriptBytes)
	}

	// Storing a script over a file-sourced game displaces that game's upload
	// exactly the way completing a second upload does — SaveScript clears
	// upload_id, and nothing else in this service is ever told about the file
	// again. Read before the save, because after it the row no longer says
	// which upload it was.
	displaced, err := g.displacedUpload(ctx, contestID, nil)
	if err != nil {
		return Template{}, err
	}

	return g.replaceGame(ctx, contestID, displaced,
		func(ctx context.Context) (Template, error) {
			return g.repo.SaveScript(ctx, contestID, templateName(contestID), script)
		},
		func(saved Template) audit.Entry {
			// The script itself is not in the payload. It is up to half a
			// mebibyte of SQL, and the trail is a list of who did what — the
			// version is what identifies which script this was, and the
			// script of that version is still on the row.
			return audit.Entry{
				ActorID: &actorID, Action: audit.ActionGameScriptSet,
				Entity: "contest", EntityID: contestID.String(),
				Payload: map[string]any{"version": saved.Version, "script_bytes": len(script)},
			}
		},
	)
}

// SetDefinition stores the structural description one contest's game is
// built from — the table builder's own way in, alongside SetScript (the
// editor) and CompleteUpload (a finished dump).
//
// Validated the moment an organiser saves it, not when a background build
// eventually turns it into SQL (a later task's own work): Definition.
// Validate's own doc gives the same reasoning SetScript's empty/oversized
// checks above do — a mistake belongs to whoever made it at the moment they
// made it.
//
// It does not build, for the same reason SetScript does not: storing puts
// the game back to pending and a worker picks it up (Build), which for a
// builder-sourced game means finishDefinitionBuild — Definition.SQL turned
// into the CREATE TABLE statements, run through the identical BuildTemplate
// an editor's script goes through, and each table's own completed CSV loaded
// afterwards (loadTableData, tabledata.go).
//
// checkTableDataCompatibility runs immediately after Validate, for the same
// reason Validate itself runs before anything is asked of storage: a table
// that already holds data locks its own name, columns and primary key
// (ErrDefinitionTableLocked's own doc explains why), and that is as much a
// mistake in what was just submitted as an invalid identifier is.
func (g *Games) SetDefinition(ctx context.Context, actorID, contestID uuid.UUID, definition Definition) (Template, error) {
	if err := definition.Validate(); err != nil {
		return Template{}, err
	}
	if err := g.checkTableDataCompatibility(ctx, contestID, definition); err != nil {
		return Template{}, err
	}

	// Storing a definition over a file-sourced game displaces that game's
	// upload exactly the way SetScript's own displacedUpload call does —
	// SaveDefinition clears upload_id, and nothing else in this service is
	// ever told about the file again. Read before the save, because
	// afterwards the row no longer says which upload it was.
	displaced, err := g.displacedUpload(ctx, contestID, nil)
	if err != nil {
		return Template{}, err
	}

	return g.replaceGame(ctx, contestID, displaced,
		func(ctx context.Context) (Template, error) {
			return g.repo.SaveDefinition(ctx, contestID, templateName(contestID), definition)
		},
		func(saved Template) audit.Entry {
			// Table and column names only, in the count — not the whole
			// definition. It is small enough to fit in the payload, but the
			// trail is a list of who did what and not a second copy of the
			// row's own content, the same choice SetScript's own entry makes
			// for the script itself.
			return audit.Entry{
				ActorID: &actorID, Action: audit.ActionGameDefinitionSet,
				Entity: "contest", EntityID: contestID.String(),
				Payload: map[string]any{"version": saved.Version, "tables": len(definition.Tables)},
			}
		},
	)
}

// RequestBuild asks for a contest's game to be built again from what is
// already stored — the table builder's own missing half.
//
// The data an organiser fills a builder game with arrives after the build
// that would have loaded it, and cannot arrive before it: a row may only be
// typed into a table the saved definition already names, and saving the
// definition is what starts the build. Without this call the rows are stored
// and never loaded, and every participant copies an empty database.
//
// Refused once the contest is running, by the same gate that refuses a
// replacement: the version rises, every copy becomes stale, and a stale copy
// is dropped and made again. Carrying an edit into databases participants are
// already working in is a different mechanism and not this one.
func (g *Games) RequestBuild(ctx context.Context, actorID, contestID uuid.UUID) (Template, error) {
	current, err := g.repo.TemplateStatus(ctx, contestID)
	if err != nil {
		return Template{}, err // ErrNoGame travels as itself
	}
	if current.Building() {
		// Told apart from the repository's own refusal below so that "a build
		// is already under way" does not read as "somebody beat you to the
		// button" — they are the same sentence to the organiser, and this one
		// costs no write.
		return Template{}, ErrBuildInProgress
	}

	if err := g.requireEditable(ctx, contestID); err != nil {
		return Template{}, err
	}

	var asked Template
	run := func(ctx context.Context) error {
		var err error
		asked, err = g.repo.RequestBuild(ctx, contestID)
		if err != nil {
			return err
		}
		return g.record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameBuildRequested,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"version": asked.Version},
		})
	}
	err = g.atomically(ctx, run)
	if err != nil {
		return Template{}, err
	}
	return asked, nil
}

// replaceGame is the one path a contest's game is replaced through, whichever
// produced the new one: an organiser's own script (SetScript), a completed
// upload (CompleteUpload) or a saved table-builder definition (SetDefinition).
// Extracted so the three cannot become parallel paths — same GameEditable
// gate, same transaction shape, same audit write — which is exactly what
// CompleteUpload's own doc asks for.
//
// save does the storage write and returns the row that resulted; entry turns
// that row into the trail's own record of it. Both run inside one
// transaction when the service was built WithAudit, the same arrangement
// markDropped and markReclaimed use elsewhere in this package, so a write
// that lands with no trail of it — or a trail entry for a write that was
// rolled back — cannot happen.
//
// displaced, when not nil, is the upload the game being written leaves behind
// (displacedUpload names it). Its file is removed after the transaction has
// committed, never before — see the removal below for why this one place is
// the exception to Instances.DropInstance's "the real object goes first".
func (g *Games) replaceGame(
	ctx context.Context, contestID uuid.UUID, displaced *uuid.UUID,
	save func(context.Context) (Template, error),
	entry func(Template) audit.Entry,
) (Template, error) {
	if err := g.requireEditable(ctx, contestID); err != nil {
		return Template{}, err
	}

	var saved Template
	var discardedTableFiles []uuid.UUID
	run := func(ctx context.Context) error {
		var err error
		saved, err = save(ctx)
		if err != nil {
			return fmt.Errorf("store the game: %w", err)
		}
		// A game that is not the table builder's names no table the builder's
		// own per-table data could belong to, so that data goes with the game
		// it described — in this same transaction, so a replacement that is
		// rolled back does not take it. Deciding this from the row that was
		// just written, rather than at each of the three call sites, is what
		// keeps a fourth source from quietly inheriting the leak: the rows
		// used to survive every switch, which is how a table's structure lock
		// could be walked round through the editor and back (see
		// checkTableDataCompatibility, tabledata.go).
		if saved.Source != SourceBuilder {
			discardedTableFiles, err = g.repo.DiscardTableData(ctx, contestID)
			if err != nil {
				return fmt.Errorf("discard the table builder's own data: %w", err)
			}
		}
		return g.record(ctx, entry(saved))
	}

	if err := g.atomically(ctx, run); err != nil {
		return Template{}, err
	}

	// Only now, with the replacement committed, do the displaced upload's
	// bytes go.
	//
	// DropInstance's ordering — the real object first, the row after — is
	// right there because the removal *is* the operation being recorded.
	// Here it is conditional on a transaction that has not run yet: this
	// method checks GameEditable a second time inside it precisely because
	// the contest may have started while the upload was being hashed and
	// indexed, and an unconditional os.Remove before that check deletes the
	// file of the game that is about to be kept. That leaves a game still
	// naming an upload whose bytes are gone, and every rebuild of it stops
	// at BuildFailedInternally for good.
	//
	// Failing here is deliberately not the caller's failure. The game *has*
	// been replaced; reporting an error would tell an organiser their upload
	// did not go through when it did, and have them do it again. What is
	// left behind is a file no game names, which is exactly what the
	// janitor's orphan sweep now looks for (sweepOrphanFiles, and
	// TemplateRepository.UploadInUse for the question it asks) — the same
	// backstop that covers a crash between the commit above and this line.
	if displaced != nil {
		_ = g.retireUploadFile(*displaced)
	}
	// The same reasoning, and the same backstop, for the table builder's own
	// files: their rows are already retired, so sweepOrphanTableFiles collects
	// whatever a failure here (or a crash) leaves on the volume.
	if g.tableFiles != nil {
		for _, id := range discardedTableFiles {
			_ = g.retireTableDataFile(id)
		}
	}
	return saved, nil
}

// Build takes one game waiting to be built and builds it.
//
// One per call, on purpose: a build is minutes of cluster work in the worst
// case, and a tick that took every pending game at once would be a queue with
// no back pressure in front of the one cluster everything else also needs.
// The caller ticks; ErrNothingToBuild says there was nothing waiting.
func (g *Games) Build(ctx context.Context, stale time.Duration) (Template, error) {
	claimed, err := g.repo.ClaimBuild(ctx, stale)
	if err != nil {
		return Template{}, err
	}

	if claimed.Source == SourceFile {
		return g.finishUploadBuild(ctx, claimed)
	}
	if claimed.Source == SourceBuilder {
		return g.finishDefinitionBuild(ctx, claimed)
	}

	// Two variables and not one, because a failed build has two audiences that
	// must not be given the same string. buildErr is what the organiser reads
	// and what the trail keeps for good; cause is the whole truth, returned to
	// the caller for the service log (internal/app.buildGames) and written
	// nowhere a manager can read it.
	var (
		buildErr string
		cause    error
	)

	// The privileges the build grants inside the template are the contest's
	// own SQL policy, read now rather than carried on the claim: it is a
	// different table with a different editor, and the build has to grant
	// what it says at the moment it runs.
	policy, err := g.repo.Policy(ctx, claimed.ContestID)
	if err != nil {
		// A failure of the *core* database, not of the game cluster and not of
		// the script. Its text is a connection string of ours either way.
		cause = fmt.Errorf("read the contest's SQL policy: %w", err)
		buildErr = BuildFailedInternally
	}

	if buildErr == "" {
		if err := g.cluster.BuildTemplate(ctx, claimed.Database, strings.NewReader(claimed.Script), policy); err != nil {
			var refused ScriptFailure
			if errors.As(err, &refused) {
				// PostgreSQL's own words about their SQL, kept: whoever wrote
				// the script is the person who has to fix it, and "the build
				// failed" tells them nothing they can act on.
				buildErr = refused.ScriptRejection()
			} else {
				cause = fmt.Errorf("build the game template: %w", err)
				buildErr = BuildFailedInternally
			}
		}
	}

	return g.recordBuildOutcome(ctx, claimed, buildErr, cause)
}

// finishUploadBuild is what a claimed build does for a file-sourced game:
// it opens the upload's own bytes off disk (internal/gamefile.Store.Open)
// and runs them through the exact same gamedb.Provisioner.BuildTemplate an
// editor-sourced script uses — the whole point of that method taking an
// io.Reader now rather than a string (its own doc explains why).
//
// Every way that can fail before the script itself runs — no upload volume
// configured, a row with no upload id, the core database's own policy read,
// the file failing to open — is an installation- or row-level fault rather
// than anything an organiser did, and is reported the same way any other
// cause not the script's own is: BuildFailedInternally on the organiser's
// screen, the real reason kept for recordBuildOutcome's caller to log.
func (g *Games) finishUploadBuild(ctx context.Context, claimed Template) (Template, error) {
	var (
		buildErr string
		cause    error
	)

	switch {
	case g.files == nil:
		// GAME_UPLOAD_DIR was set when this game was built (or last
		// replaced) and is not set on this run of the process — a redeploy
		// that dropped the upload volume out from under a game still
		// waiting to build. Nothing an organiser can fix from their own
		// screen.
		cause = errors.New("a file-sourced game waited to build, but this installation has no upload volume configured")
		buildErr = BuildFailedInternally
	case claimed.UploadID == nil:
		// Migration 24's own CHECK ties SourceFile to a non-nil UploadID;
		// reaching this means that constraint was bypassed or the row is
		// otherwise corrupt, never anything the organiser's upload did.
		cause = fmt.Errorf("a file-sourced game has no upload id (contest %s)", claimed.ContestID)
		buildErr = BuildFailedInternally
	}

	// The privileges the build grants inside the template are the contest's
	// own SQL policy, read the same way Build reads it above: at the moment
	// the build actually runs, never carried on the claim.
	var policy sqlpolicy.Policy
	if buildErr == "" {
		var err error
		if policy, err = g.repo.Policy(ctx, claimed.ContestID); err != nil {
			cause = fmt.Errorf("read the contest's SQL policy: %w", err)
			buildErr = BuildFailedInternally
		}
	}

	if buildErr == "" {
		file, err := g.files.Open(claimed.UploadID.String())
		if err != nil {
			cause = fmt.Errorf("open the uploaded file: %w", err)
			buildErr = BuildFailedInternally
		} else {
			defer file.Close()
			if err := g.cluster.BuildTemplate(ctx, claimed.Database, file, policy); err != nil {
				var refused ScriptFailure
				if errors.As(err, &refused) {
					buildErr = refused.ScriptRejection()
				} else {
					cause = fmt.Errorf("build the game template from the uploaded file: %w", err)
					buildErr = BuildFailedInternally
				}
			}
		}
	}

	return g.recordBuildOutcome(ctx, claimed, buildErr, cause)
}

// finishDefinitionBuild is what a claimed build does for a builder-sourced
// game: turn the saved Definition into the CREATE TABLE statements that
// describe it (Definition.SQL) and run them through the exact path Build and
// finishUploadBuild already use — the same separate connection authenticated
// as game_author, the same contest policy grants, the same teardown of a
// half-built template on failure (gamedb.Provisioner.BuildTemplate's own
// doc). There is no third path here on purpose (see Definition.SQL's own
// doc): BuildTemplate cannot tell an organiser's own script from SQL this
// package generated, and it must not be asked to — generating it is this
// method's whole job, executing it is BuildTemplate's, exactly as for the
// other two sources.
//
// Definition.SQL can itself refuse — ErrDefinitionEmpty, for a row that
// reached here with no tables even though Validate refuses that before a
// save takes hold. That refusal is the organiser's own definition, worded
// plainly, never BuildFailedInternally: cause stays nil, the same way it
// does below for a script PostgreSQL itself refused, because in both cases
// the log has nothing to add that build_error does not already say.
//
// # The organiser's own rows
//
// Definition.SQL only ever emits CREATE TABLE, so every table BuildTemplate
// just made is empty. g.loadTableData fills them, called right here: after
// BuildTemplate has returned with no error and before recordBuildOutcome
// marks the game 'ready', because that is the one moment the database is
// known to exist, hold the tables just created, and not yet be promised to
// anybody as complete. BuildTemplate has already closed the connection it
// opened as game_author by the time it returns (runScript's own doc: a
// template with a connection on it cannot be copied, and that connection's
// own grants have been withdrawn besides), so loadTableData opens a
// connection of its own — as the provisioning role, not game_author
// (gamedb.Provisioner.LoadTableData's own doc says why) — rather than reuse
// one that is already gone. A data-load failure gets the identical teardown
// a schema-build failure does: the half-loaded template is dropped, because
// a database with some tables filled and one refused midway is worse than
// none.
func (g *Games) finishDefinitionBuild(ctx context.Context, claimed Template) (Template, error) {
	var (
		buildErr string
		cause    error
	)

	script, err := claimed.Definition.SQL()
	if err != nil {
		// Never BuildFailedInternally: an empty definition is a mistake in
		// what the organiser saved (or, in the ordinary path, could not
		// have saved at all — Validate's own doc), not a fault of this
		// installation's cluster.
		buildErr = err.Error()
	}

	// The privileges the build grants inside the template are the contest's
	// own SQL policy, read the same way Build and finishUploadBuild read it
	// above: at the moment the build actually runs, never carried on the
	// claim.
	var policy sqlpolicy.Policy
	if buildErr == "" {
		if policy, err = g.repo.Policy(ctx, claimed.ContestID); err != nil {
			cause = fmt.Errorf("read the contest's SQL policy: %w", err)
			buildErr = BuildFailedInternally
		}
	}

	if buildErr == "" {
		if err := g.cluster.BuildTemplate(ctx, claimed.Database, strings.NewReader(script), policy); err != nil {
			var refused ScriptFailure
			if errors.As(err, &refused) {
				// Generated SQL PostgreSQL still refused — a type this
				// package's own switch mapped correctly but a value the
				// server itself would not accept, or a name collision
				// Validate's per-table folding did not catch. PostgreSQL's
				// own words, the same as for an organiser's own script.
				buildErr = refused.ScriptRejection()
			} else {
				cause = fmt.Errorf("build the game template from its table-builder definition: %w", err)
				buildErr = BuildFailedInternally
			}
		}
	}

	// The seam this task's own brief names: every table BuildTemplate just
	// created is empty, and this is the one moment the database is known to
	// exist, hold them, and not yet be promised to anybody as ready. A table
	// with no completed CSV is left empty rather than refused — an organiser
	// may have described it and not yet filled it (Games.loadTableData's own
	// doc).
	if buildErr == "" {
		if err := g.loadTableData(ctx, claimed.ContestID, claimed.Database, claimed.Definition); err != nil {
			var refused ScriptFailure
			if errors.As(err, &refused) {
				buildErr = refused.ScriptRejection()
			} else {
				cause = fmt.Errorf("load the table-builder definition's own data: %w", err)
				buildErr = BuildFailedInternally
			}
			// A half-loaded template — some tables full, one refused midway
			// — is worse than none, the identical reasoning BuildTemplate's
			// own doc gives for tearing down a schema that failed to build.
			_ = g.cluster.Drop(context.WithoutCancel(ctx), claimed.Database)
		}
	}

	return g.recordBuildOutcome(ctx, claimed, buildErr, cause)
}

// recordBuildOutcome finishes a claimed build's row and audit trail, the
// last step of Build, finishUploadBuild and finishDefinitionBuild's own
// paths: whichever ran the script (or refused to, for one of the other two's
// own reasons), what happens to the claim afterward is identical.
func (g *Games) recordBuildOutcome(ctx context.Context, claimed Template, buildErr string, cause error) (Template, error) {
	// Once, here, because this is the one funnel every outcome passes
	// through — the row, the audit payload and what is handed back to the
	// caller all take their text from this variable, so bounding it anywhere
	// else would be bounding one of the three.
	buildErr = boundBuildError(buildErr)

	if err := g.repo.FinishBuild(ctx, claimed.ContestID, claimed.Version, buildErr, claimed.UpdatedAt); err != nil {
		return claimed, errors.Join(cause, fmt.Errorf("record the build's outcome: %w", err))
	}

	// A system event: nobody is at the keyboard when a build finishes, which
	// is exactly why the trail has to carry it. Recorded outside any
	// transaction and best effort — a trail write that failed must not make a
	// built game report as unbuilt, which would have the tick build it again.
	if g.audit != nil {
		payload := map[string]any{"version": claimed.Version, "database": claimed.Database, "ok": buildErr == ""}
		if buildErr != "" {
			// The organiser's text and not the cause. audit_log is append-only
			// and read by every manager of the contest, so a connect string
			// written here is a connect string kept for as long as the
			// installation exists — see BuildFailedInternally.
			payload["error"] = buildErr
		}
		if err := g.audit.Record(ctx, audit.Entry{
			Action: audit.ActionGameBuilt, Entity: "contest",
			EntityID: claimed.ContestID.String(), Payload: payload,
		}); err != nil {
			return claimed, errors.Join(cause, fmt.Errorf("record the build in the audit trail: %w", err))
		}
	}

	claimed.Status = TemplateReady
	claimed.BuildError = buildErr
	if buildErr != "" {
		claimed.Status = TemplateFailed
	}
	// cause is nil for a build that worked and for one the script itself broke
	// — the second is an answer, not a fault of this service, and a tick that
	// reported it as a job failure would page an operator about somebody's
	// typo. It is non-nil only where buildErr is BuildFailedInternally, which
	// is the case where the log is the only place the detail survives.
	return claimed, cause
}

// templateName is the database every participant's copy of one contest is
// made from — the `game_tpl_c{short}` shape migration 3 names, and the same
// halved identifier instanceName and spareName use.
func templateName(contest uuid.UUID) string {
	return "game_tpl_c" + short(contest)
}

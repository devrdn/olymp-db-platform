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
	// ErrScriptEmpty is a game with no SQL in it. An empty template would give
	// a contest whose every question answers "no such table".
	ErrScriptEmpty = errors.New("the game script is empty")
	// ErrScriptTooLong is a script past MaxScriptBytes.
	ErrScriptTooLong = errors.New("the game script is too long")
	// ErrGameNotEditable is a contest whose game may no longer be replaced.
	// Replacing a game bumps its version, and every stale participant copy is
	// dropped and remade, so in a running contest everyone would lose their
	// database at once. The gate is the contest's own ContentEditable.
	ErrGameNotEditable = errors.New("the contest's game can no longer be replaced")
	// ErrBuildInProgress is a build somebody else already claimed.
	ErrBuildInProgress = errors.New("the game is already being built")
)

// ScriptFailure is the one build failure whose text may be shown to the
// organiser: PostgreSQL's verdict on a statement in their own script.
// gamedb.ScriptError implements it.
//
// It is an interface the producer opts into, not something inferred from the
// error: pgx nests a *pgconn.PgError inside the *pgconn.ConnectError of a
// refused login, so "contains a database error" is also true for a connection
// that never opened, and that text names the role, host and port.
type ScriptFailure interface {
	error
	// ScriptRejection is the text that may be served to whoever wrote the
	// script and kept in the audit trail.
	ScriptRejection() string
}

// BuildFailedInternally is what a failed build tells an organiser when the
// failure was not their script.
//
// It is a fixed sentence because the real error names the provisioning role,
// the cluster's host and port, or the internal database, and this text is
// served to anyone with contest.view and written to the append-only audit
// trail. The real error goes to the service log instead.
const BuildFailedInternally = "The game could not be built. The failure was not in the script — ask an administrator to check the service log."

// MaxBuildErrorBytes bounds the stored text of a failed build (CLAUDE.md
// rule 2). A script refusal quotes the organiser's file back, and both sinks
// (game_templates.build_error and the append-only audit payload) keep it.
// 16 KiB is far above any PostgreSQL verdict and still readable on a screen.
const MaxBuildErrorBytes = 16 << 10

// boundBuildError makes a build outcome storable: at most MaxBuildErrorBytes,
// valid UTF-8, and no U+0000.
//
// PostgreSQL refuses invalid UTF-8 and NUL in both `text` and `jsonb`
// (SQLSTATE 22021). That refusal would come from FinishBuild, leaving the row
// in 'building' for the stale-build sweep to reclaim and rebuild for ever. NUL
// is valid UTF-8 to Go, so it needs its own check; it is also the likeliest
// byte here, since a `pg_dump -Fc` upload is binary.
//
// Bad bytes become U+FFFD and the cut falls on a rune boundary, in one bounded
// pass: the input can be as large as an uploaded file, and a whole-string
// replace would allocate up to three times that (CLAUDE.md rule 12).
func boundBuildError(text string) string {
	if len(text) <= MaxBuildErrorBytes && utf8.ValidString(text) && !strings.ContainsRune(text, 0) {
		return text // the ordinary case: our own sentence, or PostgreSQL's
	}

	const ellipsis = "\n[…]"
	const replacement = '�'
	limit := MaxBuildErrorBytes - len(ellipsis)

	var out strings.Builder
	out.Grow(MaxBuildErrorBytes)
	// Ranging a string yields utf8.RuneError for each invalid byte.
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

// MaxScriptBytes bounds the SQL one game may carry (CLAUDE.md rule 2). A game
// that needs more rows than this can generate them with
// `INSERT ... SELECT generate_series(...)`.
const MaxScriptBytes = 512 << 10

// TemplateStatus is where one contest's game has got to.
type TemplateStatus string

const (
	// TemplatePending is a script stored and not yet built, including a rebuild.
	TemplatePending TemplateStatus = "pending"
	// TemplateBuilding is a build claimed by one worker.
	TemplateBuilding TemplateStatus = "building"
	// TemplateReady is a template database that exists and can be copied.
	TemplateReady TemplateStatus = "ready"
	// TemplateFailed is a build that ran and did not finish; BuildError says why.
	TemplateFailed TemplateStatus = "failed"
	// TemplateDropped is a template the reclaim sweep has removed (§2.4).
	TemplateDropped TemplateStatus = "dropped"
)

// TemplateSource says how an organiser built this game. All sources share the
// version, status and build queue, because replacing a game is one event
// whichever path produced it (Games.replaceGame).
type TemplateSource string

const (
	// SourceEditor is a script an organiser wrote or pasted directly.
	SourceEditor TemplateSource = "editor"
	// SourceFile is an uploaded dump. Script is empty; UploadID names the
	// game_uploads row.
	SourceFile TemplateSource = "file"
	// SourceBuilder is a game described as tables and columns. Script is
	// empty; Definition holds what was saved, and the SQL is generated from it
	// at build time.
	SourceBuilder TemplateSource = "builder"
)

// Template is one contest's game, as an organiser sees it.
type Template struct {
	ContestID uuid.UUID
	Database  string
	Version   int
	Status    TemplateStatus
	Source    TemplateSource
	// UploadID is set only for SourceFile (enforced by a CHECK).
	UploadID *uuid.UUID
	// Definition is set only for SourceBuilder (enforced by a CHECK).
	Definition Definition
	// Script is the editor's SQL, empty for the other sources. It is
	// staff-trusted and runs as gamedb.RoleAuthor, never as the provisioning
	// role.
	Script string
	// ScriptBytes is len(Script), filled in even when Script was not read, so
	// a console polling a build can show the length without fetching the
	// script (TemplateRepository.TemplateStatus).
	ScriptBytes int
	BuildError  string
	// DataChangedAt is when the table builder's data last changed, nil when
	// nothing changed since the build. Rows can only be added after the build
	// that creates their table, so without this mark they would never be
	// loaded.
	DataChangedAt *time.Time
	UpdatedAt     time.Time
}

// Building reports whether a build is under way, which is what an interface
// polls on.
func (t Template) Building() bool { return t.Status == TemplateBuilding || t.Status == TemplatePending }

// NeedsBuild reports a ready game whose database no longer holds the data
// since put into it. A pending or running build will pick the data up anyway,
// and a failed build's error is what should be shown.
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
	// SaveDefinition is SaveScript for a table-builder definition, through the
	// same upsert, so version bump and schema cache reset stay one mechanism.
	SaveDefinition(ctx context.Context, contestID uuid.UUID, database string, definition Definition) (Template, error)
	// Template reads one contest's game, or ErrNoGame.
	Template(ctx context.Context, contestID uuid.UUID) (Template, error)
	// TemplateStatus is Template without the content: the script comes back
	// only as ScriptBytes and the definition is not read. Or ErrNoGame.
	// A console watching a build polls this every two seconds, and reading a
	// script of up to MaxScriptBytes each time would be hundreds of megabytes
	// over one build.
	TemplateStatus(ctx context.Context, contestID uuid.UUID) (Template, error)
	// ClaimBuild moves one game from pending to building and returns it.
	//
	// The conditional update is the race arbiter: two workers must not both
	// run CREATE DATABASE against one name. A game stuck in building for
	// longer than stale is claimed too, so a build whose API died is retried.
	ClaimBuild(ctx context.Context, stale time.Duration) (Template, error)
	// FinishBuild records the outcome against the version that was built, so a
	// slow build cannot mark a newer script ready.
	//
	// claimedAt is the updated_at ClaimBuild wrote. Only a data change made
	// before it is cleared, so a row added during the build keeps its mark for
	// the next one.
	FinishBuild(ctx context.Context, contestID uuid.UUID, version int, buildError string, claimedAt time.Time) error
	// MarkTableDataChanged records that this contest's table data no longer
	// matches its built database. Called inside the transaction of each write
	// that changes a table's rows, so the mark commits or rolls back with it.
	MarkTableDataChanged(ctx context.Context, contestID uuid.UUID) error
	// RequestBuild puts a ready or failed game back to pending and raises its
	// version. ErrBuildInProgress when the row is already pending or building,
	// which a concurrent request may have caused after the caller checked.
	RequestBuild(ctx context.Context, contestID uuid.UUID) (Template, error)
	// Policy is the contest's SQL policy, which the build grants inside the
	// template. Repository.Game cannot supply it, because it answers only for
	// a template that is already ready, and a first build would silently use
	// the defaults (CLAUDE.md rule 11).
	Policy(ctx context.Context, contestID uuid.UUID) (sqlpolicy.Policy, error)

	// Uploads. Declared here rather than as a second interface because Games
	// is the only consumer and CompleteUpload replaces the game through the
	// same statements SaveScript uses.

	// BeginUpload records a new upload in 'receiving'. The caller generates id
	// so the file on disk can carry it before this row exists. A second
	// upload in progress for the contest is refused by a unique index and
	// returned as ErrUploadInProgress.
	BeginUpload(ctx context.Context, id, contestID uuid.UUID, filename string, declaredBytes int64) (Upload, error)
	// Upload reads one upload by id, or ErrUploadNotFound.
	Upload(ctx context.Context, id uuid.UUID) (Upload, error)
	// CurrentUpload reads a contest's one 'receiving' upload, or
	// ErrUploadNotFound.
	CurrentUpload(ctx context.Context, contestID uuid.UUID) (Upload, error)
	// UpdateReceived records how many bytes Store.Append wrote, so a resumed
	// browser knows where to continue.
	UpdateReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error
	// CompleteUpload marks id 'complete', retires previous (nil unless another
	// upload is displaced), and replaces the contest's game the same way
	// SaveScript does.
	CompleteUpload(ctx context.Context, contestID, id uuid.UUID, database string, summary UploadSummary, previous *uuid.UUID) (Template, error)
	// AbortUpload marks one upload 'aborted' and leaves game_templates alone.
	AbortUpload(ctx context.Context, id uuid.UUID) error
	// AbandonedUploads lists up to limit uploads still 'receiving' whose
	// updated_at is older than cutoff.
	AbandonedUploads(ctx context.Context, cutoff time.Time, limit int) ([]Upload, error)
	// UploadInUse reports whether anything still needs id's bytes: an upload
	// still receiving, or one a contest's game is built from. A row existing
	// is not enough, since a displaced upload's row stays as history.
	UploadInUse(ctx context.Context, id uuid.UUID) (bool, error)

	// Table-builder CSV data, one file per table; each mirrors an upload
	// method above (see tabledata.go).

	// BeginTableData records a new table-data upload in 'receiving'.
	BeginTableData(ctx context.Context, id, contestID uuid.UUID, table string, declaredBytes int64) (TableData, error)
	// TableDataByID reads one table-data row by id, or ErrTableDataNotFound.
	TableDataByID(ctx context.Context, id uuid.UUID) (TableData, error)
	// CurrentTableData reads a table's one 'receiving' upload, or
	// ErrTableDataNotFound.
	CurrentTableData(ctx context.Context, contestID uuid.UUID, table string) (TableData, error)
	// ReadyTableData reads a table's one 'complete' file, or
	// ErrTableDataNotFound when the table has none yet.
	ReadyTableData(ctx context.Context, contestID uuid.UUID, table string) (TableData, error)
	// UpdateTableDataReceived is UpdateReceived for a table's file.
	UpdateTableDataReceived(ctx context.Context, id uuid.UUID, receivedBytes int64) error
	// CompleteTableData marks id 'complete' with what validation measured, and
	// retires previous (nil unless another upload for the table is displaced).
	CompleteTableData(ctx context.Context, contestID uuid.UUID, table string, id uuid.UUID, receivedBytes, lines int64, previous *uuid.UUID) (TableData, error)
	// CreateReadyTableData records a table's first row as a file that starts
	// 'complete'.
	CreateReadyTableData(ctx context.Context, id, contestID uuid.UUID, table string, bytes, lines int64) (TableData, error)
	// AppendTableDataRow records the new byte length and row count of a
	// 'complete' file after one row was appended, together.
	//
	// Both are floors, never lowered: two concurrent appends may reach storage
	// in a different order than they read, and the older one must not
	// overwrite the newer pair. ErrTableDataChanged when there is no longer a
	// 'complete' row, for example because the game was replaced.
	AppendTableDataRow(ctx context.Context, id uuid.UUID, receivedBytes, lines int64) (TableData, error)
	// AbortTableData marks one table-data upload 'aborted'.
	AbortTableData(ctx context.Context, id uuid.UUID) error
	// DiscardTableData retires every table-data row of a contest and returns
	// the ids whose files the caller removes after its transaction commits.
	//
	// Used when a game stops being builder-sourced. The rows are keyed by
	// table name, so left behind they would be loaded by a later definition
	// that reuses a name, against columns they were never validated for.
	DiscardTableData(ctx context.Context, contestID uuid.UUID) ([]uuid.UUID, error)
	// DeleteTableDataRow tombstones one row of a 'complete' file in one atomic
	// update, refusing with ErrTooManyDeletedRows past the stored bound.
	DeleteTableDataRow(ctx context.Context, id uuid.UUID, row int64) error
	// AbandonedTableData is AbandonedUploads for table data.
	AbandonedTableData(ctx context.Context, cutoff time.Time, limit int) ([]TableData, error)
	// TableDataInUse is UploadInUse for a table's file.
	TableDataInUse(ctx context.Context, id uuid.UUID) (bool, error)
}

// Authoring answers whether a contest's game may still be replaced. The rule
// lives in contests.Contest.ContentEditable; this package asks for the answer
// so the rule stays in one place.
type Authoring interface {
	GameEditable(ctx context.Context, contestID uuid.UUID) (bool, error)
}

// requireEditable returns ErrGameNotEditable once the contest has started.
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

// Games owns a contest's game: the script an organiser writes and the
// template database built from it. Service owns the copies made from that
// template.
type Games struct {
	repo    TemplateRepository
	cluster TemplateCluster
	author  Authoring
	audit   *audit.Recorder
	uow     unitOfWork
	now     func() time.Time
	// files and limits are set by WithUploads. files is nil when no upload
	// volume is configured, and every upload method then returns
	// ErrUploadsDisabled.
	files  *gamefile.Store
	limits gamefile.Limits
	// tableFiles and tableLimits are the table builder's CSV storage, set by
	// WithTableData; a separate store from files. nil when not configured.
	tableFiles  *gamefile.Store
	tableLimits gamefile.Limits
	// rowMarks caches where every five-hundredth row of a table's file
	// begins, so paging does not rescan the file (tableRowIndex).
	rowMarks tableRowIndex
}

// TemplateCluster is the one thing building a game asks of the cluster.
//
// script is a reader so an uploaded dump can be streamed statement by
// statement rather than held in memory.
type TemplateCluster interface {
	BuildTemplate(ctx context.Context, name string, script io.Reader, policy sqlpolicy.Policy) error
	// LoadTableData copies CSV rows into one table of a database BuildTemplate
	// already built, using COPY ... FROM STDIN. columns is the COPY column
	// list in definition order; data has no header line.
	LoadTableData(ctx context.Context, database, table string, columns []string, data io.Reader) error
	// Drop removes a database, used to tear down a template whose data load
	// failed.
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
// recorded.
func (g *Games) WithAudit(recorder *audit.Recorder, uow unitOfWork) *Games {
	g.audit, g.uow = recorder, uow
	return g
}

// atomically runs fn inside the unit of work WithAudit supplied, so a write
// and its audit entry commit together, or runs it directly when there is none.
func (g *Games) atomically(ctx context.Context, fn func(context.Context) error) error {
	if g.uow == nil {
		return fn(ctx)
	}
	return g.uow.Do(ctx, fn)
}

// record writes one audit entry, or nothing when the service has no trail.
func (g *Games) record(ctx context.Context, entry audit.Entry) error {
	if g.audit == nil {
		return nil
	}
	return g.audit.Record(ctx, entry)
}

// WithUploads turns on file uploads. limits must be the Limits files was
// built with: Games refuses an oversized upload before Store.Begin reserves
// anything (CLAUDE.md rule 12). Without it, upload methods return
// ErrUploadsDisabled.
func (g *Games) WithUploads(files *gamefile.Store, limits gamefile.Limits) *Games {
	g.files, g.limits = files, limits
	return g
}

// UploadLimits reports the limits this package enforces on a chunked upload,
// so the API publishes the same values it checks (CLAUDE.md rule 11). The
// bool is false, with zero Limits, when uploads are disabled; callers check
// it first, since disabled is not the same as a limit of zero.
func (g *Games) UploadLimits() (gamefile.Limits, bool) {
	return g.limits, g.files != nil
}

// Of reads one contest's game, or ErrNoGame when it has none yet.
func (g *Games) Of(ctx context.Context, contestID uuid.UUID) (Template, error) {
	return g.repo.Template(ctx, contestID)
}

// StatusOf is Of without the script and definition; it is what a console
// polling a build should call (TemplateRepository.TemplateStatus).
func (g *Games) StatusOf(ctx context.Context, contestID uuid.UUID) (Template, error) {
	return g.repo.TemplateStatus(ctx, contestID)
}

// Script returns the SQL a contest's game is built from, for the contest
// export (it satisfies contests.GameSource).
//
// ok is false for a contest with no game, which is not an error; any other
// failure is returned so an export does not ship an incomplete package as
// complete. omitted is true for file- and builder-sourced games, which have
// no script to carry; without it the export would package an empty script
// that re-imports as ErrScriptEmpty (CLAUDE.md rule 11).
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
// It does not build: the game goes back to pending and a worker builds it
// (Build), because a build can take far longer than an HTTP request should.
func (g *Games) SetScript(ctx context.Context, actorID, contestID uuid.UUID, script string) (Template, error) {
	switch {
	case len(script) == 0:
		return Template{}, ErrScriptEmpty
	case len(script) > MaxScriptBytes:
		return Template{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrScriptTooLong, len(script), MaxScriptBytes)
	}

	// Read before the save, which clears upload_id.
	displaced, err := g.displacedUpload(ctx, contestID, nil)
	if err != nil {
		return Template{}, err
	}

	return g.replaceGame(ctx, contestID, displaced,
		func(ctx context.Context) (Template, error) {
			return g.repo.SaveScript(ctx, contestID, templateName(contestID), script)
		},
		func(saved Template) audit.Entry {
			// The script stays on the row; the version identifies it.
			return audit.Entry{
				ActorID: &actorID, Action: audit.ActionGameScriptSet,
				Entity: "contest", EntityID: contestID.String(),
				Payload: map[string]any{"version": saved.Version, "script_bytes": len(script)},
			}
		},
	)
}

// SetDefinition stores the table-builder definition one contest's game is
// built from.
//
// It is validated on save, not at build time, so the organiser sees the
// mistake when they make it; that includes changes to a table that already
// holds data (checkTableDataCompatibility). Like SetScript, it does not build.
func (g *Games) SetDefinition(ctx context.Context, actorID, contestID uuid.UUID, definition Definition) (Template, error) {
	if err := definition.Validate(); err != nil {
		return Template{}, err
	}
	if err := g.checkTableDataCompatibility(ctx, contestID, definition); err != nil {
		return Template{}, err
	}

	// Read before the save, which clears upload_id.
	displaced, err := g.displacedUpload(ctx, contestID, nil)
	if err != nil {
		return Template{}, err
	}

	return g.replaceGame(ctx, contestID, displaced,
		func(ctx context.Context) (Template, error) {
			return g.repo.SaveDefinition(ctx, contestID, templateName(contestID), definition)
		},
		func(saved Template) audit.Entry {
			return audit.Entry{
				ActorID: &actorID, Action: audit.ActionGameDefinitionSet,
				Entity: "contest", EntityID: contestID.String(),
				Payload: map[string]any{"version": saved.Version, "tables": len(definition.Tables)},
			}
		},
	)
}

// RequestBuild rebuilds a contest's game from what is already stored, so
// table-builder rows added after the last build are loaded.
//
// Refused once the contest is running, like a replacement: the version rises
// and every participant copy is remade.
func (g *Games) RequestBuild(ctx context.Context, actorID, contestID uuid.UUID) (Template, error) {
	current, err := g.repo.TemplateStatus(ctx, contestID)
	if err != nil {
		return Template{}, err // ErrNoGame travels as itself
	}
	if current.Building() {
		// Same answer the repository gives on a race below, without a write.
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

// replaceGame is the one path a contest's game is replaced through (SetScript,
// CompleteUpload, SetDefinition): one editability gate, one transaction for
// the save and its audit entry.
//
// displaced, when not nil, is the upload the new game leaves behind; its file
// is removed only after the transaction commits.
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
		// A non-builder game has no tables for the builder's data to belong
		// to, so the data goes in the same transaction. Deciding it here from
		// the saved row covers every source, and closes a way round a table's
		// structure lock via the editor (checkTableDataCompatibility).
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

	// Files go only after the commit. Unlike DropInstance, where removing the
	// object is the operation, here the transaction can still refuse (the
	// contest may have started meanwhile), and removing first would leave a
	// kept game naming a file that is gone.
	//
	// A failure here is not the caller's: the game has been replaced. The
	// orphan sweeps (sweepOrphanFiles, sweepOrphanTableFiles) collect what is
	// left, including after a crash at this point.
	if displaced != nil {
		_ = g.retireUploadFile(*displaced)
	}
	if g.tableFiles != nil {
		for _, id := range discardedTableFiles {
			_ = g.retireTableDataFile(id)
		}
	}
	return saved, nil
}

// Build takes one game waiting to be built and builds it. ErrNothingToBuild
// means nothing was waiting.
//
// One per call: a build can be minutes of cluster work, and taking every
// pending game at once would put an unbounded queue in front of the cluster.
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

	// buildErr is what the organiser and the audit trail see; cause is the
	// full error, returned for the service log only.
	var (
		buildErr string
		cause    error
	)

	// Read at build time, not carried on the claim: the policy is edited
	// separately and the build must grant what it says now.
	policy, err := g.repo.Policy(ctx, claimed.ContestID)
	if err != nil {
		cause = fmt.Errorf("read the contest's SQL policy: %w", err)
		buildErr = BuildFailedInternally
	}

	if buildErr == "" {
		if err := g.cluster.BuildTemplate(ctx, claimed.Database, strings.NewReader(claimed.Script), policy); err != nil {
			var refused ScriptFailure
			if errors.As(err, &refused) {
				// PostgreSQL's words about their SQL are what the author
				// needs to fix it.
				buildErr = refused.ScriptRejection()
			} else {
				cause = fmt.Errorf("build the game template: %w", err)
				buildErr = BuildFailedInternally
			}
		}
	}

	return g.recordBuildOutcome(ctx, claimed, buildErr, cause)
}

// finishUploadBuild builds a file-sourced game by streaming the uploaded file
// through the same BuildTemplate an editor script uses. Any failure before the
// script runs is an installation or row fault, reported as
// BuildFailedInternally.
func (g *Games) finishUploadBuild(ctx context.Context, claimed Template) (Template, error) {
	var (
		buildErr string
		cause    error
	)

	switch {
	case g.files == nil:
		// The upload volume was removed from the configuration while a
		// file-sourced game was waiting to build.
		cause = errors.New("a file-sourced game waited to build, but this installation has no upload volume configured")
		buildErr = BuildFailedInternally
	case claimed.UploadID == nil:
		// A CHECK forbids this; the row is corrupt.
		cause = fmt.Errorf("a file-sourced game has no upload id (contest %s)", claimed.ContestID)
		buildErr = BuildFailedInternally
	}

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

// finishDefinitionBuild builds a builder-sourced game: Definition.SQL
// generates the CREATE TABLE statements, which run through the same
// BuildTemplate as any script, and then each table's CSV data is loaded.
//
// Data is loaded after BuildTemplate succeeds and before the game is marked
// ready. BuildTemplate has closed its game_author connection by then, so
// loadTableData opens its own. If the load fails the template is dropped,
// since a partly filled database is worse than none.
func (g *Games) finishDefinitionBuild(ctx context.Context, claimed Template) (Template, error) {
	var (
		buildErr string
		cause    error
	)

	script, err := claimed.Definition.SQL()
	if err != nil {
		// The organiser's definition is at fault, not the installation, so
		// the message is theirs and cause stays nil.
		buildErr = err.Error()
	}

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
				// Generated SQL PostgreSQL still refused, such as a value the
				// server rejects; its words go to the organiser.
				buildErr = refused.ScriptRejection()
			} else {
				cause = fmt.Errorf("build the game template from its table-builder definition: %w", err)
				buildErr = BuildFailedInternally
			}
		}
	}

	// A table with no completed CSV stays empty rather than failing the build.
	if buildErr == "" {
		if err := g.loadTableData(ctx, claimed.ContestID, claimed.Database, claimed.Definition); err != nil {
			var refused ScriptFailure
			if errors.As(err, &refused) {
				buildErr = refused.ScriptRejection()
			} else {
				cause = fmt.Errorf("load the table-builder definition's own data: %w", err)
				buildErr = BuildFailedInternally
			}
			_ = g.cluster.Drop(context.WithoutCancel(ctx), claimed.Database)
		}
	}

	return g.recordBuildOutcome(ctx, claimed, buildErr, cause)
}

// recordBuildOutcome finishes a claimed build's row and audit entry; every
// build path ends here.
func (g *Games) recordBuildOutcome(ctx context.Context, claimed Template, buildErr string, cause error) (Template, error) {
	// Bounded here, the one place the row, the audit payload and the return
	// value all take their text from.
	buildErr = boundBuildError(buildErr)

	if err := g.repo.FinishBuild(ctx, claimed.ContestID, claimed.Version, buildErr, claimed.UpdatedAt); err != nil {
		return claimed, errors.Join(cause, fmt.Errorf("record the build's outcome: %w", err))
	}

	// A system event, recorded outside any transaction: a failed trail write
	// must not make a built game look unbuilt and get built again.
	if g.audit != nil {
		payload := map[string]any{"version": claimed.Version, "database": claimed.Database, "ok": buildErr == ""}
		if buildErr != "" {
			// Never cause: the trail is append-only and readable by managers
			// (BuildFailedInternally).
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
	// cause is nil when the build worked or the script itself failed, so an
	// author's typo does not page an operator. It is set only with
	// BuildFailedInternally, where the log is the only place the detail goes.
	return claimed, cause
}

// templateName is the database every participant's copy of one contest is
// made from, `game_tpl_c{short}`.
func templateName(contest uuid.UUID) string {
	return "game_tpl_c" + short(contest)
}

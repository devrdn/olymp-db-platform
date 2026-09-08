package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// TemplateSource says which of the two ways an organiser built this game:
// wrote (or pasted) it in the editor, or uploaded a finished dump
// (migration 24). The two share every other column — version, status, the
// build queue — because replacing a game is one event whichever path
// produced it (see Games.replaceGame).
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
)

// UploadBuildUnavailable is what a file-sourced game's build_error reads
// until streaming an uploaded dump into the game cluster exists. Not a
// failure of the organiser's file or of this installation's cluster — Build
// says so honestly rather than running an empty init_script, which is what a
// 'file' row carries in that column (see finishUploadBuild).
const UploadBuildUnavailable = "This game was uploaded as a file. Building it is not available on this installation yet."

// Template is one contest's game, as an organiser sees it.
type Template struct {
	ContestID uuid.UUID
	Database  string
	Version   int
	Status    TemplateStatus
	Source    TemplateSource
	// UploadID names the game_uploads row a file-sourced game came from. Nil
	// for SourceEditor — the pairing migration 24's own CHECK enforces.
	UploadID *uuid.UUID
	// Script is the SQL an author uploaded. Staff-trusted input: it runs with
	// the provisioning role's privileges (gamedb.Provisioner.BuildTemplate's
	// own doc), which is why writing it sits behind PermissionContestEdit and
	// is written to the audit trail. Empty for SourceFile — nothing here
	// executes a file-sourced game's SQL yet, and inventing a Script for one
	// would be lying about where it came from.
	Script     string
	BuildError string
	UpdatedAt  time.Time
}

// Building reports whether a build is under way, which is what an interface
// polls on.
func (t Template) Building() bool { return t.Status == TemplateBuilding || t.Status == TemplatePending }

// TemplateRepository is the storage the game's own lifecycle needs, apart from
// the pool's (Repository).
type TemplateRepository interface {
	// SaveScript stores script as contest's game and puts it back to
	// pending, bumping the version when one was already there. The version it
	// returns is the one a build will be recorded against.
	SaveScript(ctx context.Context, contestID uuid.UUID, database, script string) (Template, error)
	// Template reads one contest's game, or ErrNoGame.
	Template(ctx context.Context, contestID uuid.UUID) (Template, error)
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
	FinishBuild(ctx context.Context, contestID uuid.UUID, version int, buildError string) error
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
	// UploadExists reports whether id names any upload row at all, whatever
	// its status. The janitor's other sweep uses this to tell a file that
	// belongs to a row it has not yet been told to remove apart from one no
	// row has ever named — see Games.sweepOrphanFiles.
	UploadExists(ctx context.Context, id uuid.UUID) (bool, error)
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
}

// TemplateCluster is the one thing building a game asks of the cluster.
type TemplateCluster interface {
	BuildTemplate(ctx context.Context, name, script string, policy sqlpolicy.Policy) error
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

// WithUploads turns on the file-upload half of a contest's game. files is
// the disk store an organiser's chunks land in; limits is the same Limits
// files was constructed with, kept here too because Games has to refuse an
// oversized upload before it ever reaches Store.Begin (CLAUDE.md rule 12 —
// bounded where the bytes arrive, not after a reservation was already made).
//
// dir is accepted, not stored: this package used to keep its own copy to
// os.ReadDir for the orphan-file sweep, but that read the directory's own
// layout by guesswork (see sweepOrphanFiles's doc). Now that the sweep asks
// files.UploadIDs instead, nothing here needs a path — the parameter stays
// so callers (main's own composition root) do not have to change for an
// implementation detail on this side.
//
// Left uncalled, every upload method answers ErrUploadsDisabled — the state
// of an installation with no GAME_UPLOAD_DIR configured, the same convention
// QueryRunnerAddr uses to turn the SQL console off.
func (g *Games) WithUploads(files *gamefile.Store, dir string, limits gamefile.Limits) *Games {
	g.files, g.limits = files, limits
	return g
}

// Of reads one contest's game, or ErrNoGame when it has none yet.
func (g *Games) Of(ctx context.Context, contestID uuid.UUID) (Template, error) {
	return g.repo.Template(ctx, contestID)
}

// Script returns the SQL one contest's game is built from, and whether the
// contest has a game at all.
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
func (g *Games) Script(ctx context.Context, contestID uuid.UUID) (string, bool, error) {
	template, err := g.repo.Template(ctx, contestID)
	switch {
	case errors.Is(err, ErrNoGame):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read the contest's game: %w", err)
	}
	return template.Script, true, nil
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

	return g.replaceGame(ctx, contestID,
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

// replaceGame is the one path a contest's game is replaced through, whichever
// produced the new one: an organiser's own script (SetScript) or a completed
// upload (CompleteUpload). Extracted so the two cannot become a second
// parallel path — same GameEditable gate, same transaction shape, same audit
// write — which is exactly what CompleteUpload's own doc asks for.
//
// save does the storage write and returns the row that resulted; entry turns
// that row into the trail's own record of it. Both run inside one
// transaction when the service was built WithAudit, the same arrangement
// markDropped and markReclaimed use elsewhere in this package, so a write
// that lands with no trail of it — or a trail entry for a write that was
// rolled back — cannot happen.
func (g *Games) replaceGame(
	ctx context.Context, contestID uuid.UUID,
	save func(context.Context) (Template, error),
	entry func(Template) audit.Entry,
) (Template, error) {
	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return Template{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return Template{}, ErrGameNotEditable
	}

	var saved Template
	run := func(ctx context.Context) error {
		var err error
		saved, err = save(ctx)
		if err != nil {
			return fmt.Errorf("store the game: %w", err)
		}
		if g.audit == nil {
			return nil
		}
		return g.audit.Record(ctx, entry(saved))
	}

	if g.uow != nil {
		err = g.uow.Do(ctx, run)
	} else {
		err = run(ctx)
	}
	if err != nil {
		return Template{}, err
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
		if err := g.cluster.BuildTemplate(ctx, claimed.Database, claimed.Script, policy); err != nil {
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

	if err := g.repo.FinishBuild(ctx, claimed.ContestID, claimed.Version, buildErr); err != nil {
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

// finishUploadBuild is what a claimed build does for a file-sourced game:
// nothing on the cluster, honestly. Streaming an uploaded dump into a
// database is a later task's own work — this task only gives the upload a
// place in the schema, the domain and the deployment (its own brief says so
// in as many words) — so running claimed.Script (empty for SourceFile, per
// Template's own doc) would build nothing, silently, and mark it 'ready'
// over a database with none of the organiser's tables in it. Refusing with a
// named reason instead is CLAUDE.md rule 1 applied to a gap in functionality
// rather than to an error: the organiser reads UploadBuildUnavailable on
// their own screen, exactly where a script's own SQLSTATE would otherwise
// appear, and nothing about it is mistaken for their fault or for a broken
// cluster.
func (g *Games) finishUploadBuild(ctx context.Context, claimed Template) (Template, error) {
	if err := g.repo.FinishBuild(ctx, claimed.ContestID, claimed.Version, UploadBuildUnavailable); err != nil {
		return claimed, fmt.Errorf("record the build's outcome: %w", err)
	}
	if g.audit != nil {
		if err := g.audit.Record(ctx, audit.Entry{
			Action: audit.ActionGameBuilt, Entity: "contest",
			EntityID: claimed.ContestID.String(),
			Payload: map[string]any{
				"version": claimed.Version, "database": claimed.Database,
				"ok": false, "error": UploadBuildUnavailable,
			},
		}); err != nil {
			return claimed, fmt.Errorf("record the build in the audit trail: %w", err)
		}
	}
	claimed.Status = TemplateFailed
	claimed.BuildError = UploadBuildUnavailable
	// Not returned as the tick's own error: this is not a fault the log has
	// to keep a cause for, the same reasoning Build's own doc gives for a
	// script PostgreSQL refused. An operator reads UploadBuildUnavailable off
	// the same trail row a real build failure would have left.
	return claimed, nil
}

// templateName is the database every participant's copy of one contest is
// made from — the `game_tpl_c{short}` shape migration 3 names, and the same
// halved identifier instanceName and spareName use.
func templateName(contest uuid.UUID) string {
	return "game_tpl_c" + short(contest)
}

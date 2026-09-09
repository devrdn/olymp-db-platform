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

// DefinitionBuildUnavailable is what a builder-sourced game's build_error
// reads until a later task turns a saved Definition into the SQL that
// actually builds it. Not a failure of the organiser's definition or of this
// installation's cluster — Build says so honestly rather than running an
// empty init_script, which is what a 'builder' row carries in that column
// (see finishDefinitionBuild) — the identical shape SourceFile's own build
// once had before streaming execution existed for it (see that task's own
// git history on finishUploadBuild).
const DefinitionBuildUnavailable = "This game was described with the table builder. Building it is not available on this installation yet."

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
// MaxBuildErrorBytes is the rule above. A refusal that is not valid UTF-8 is
// worse: build_error is a `text` column, PostgreSQL refuses an invalid byte
// sequence with SQLSTATE 22021, and that refusal comes from FinishBuild — so
// the row is never moved out of 'building', the stale-build sweep claims it
// again, and the game spends every staleBuildAfter interval doing a DROP
// DATABASE and a CREATE DATABASE on the cluster an olympiad is running on,
// for ever, without ever becoming ready. A dump is raw bytes; the reader
// quotes them; this is the boundary where they become text.
//
// Replaced rather than dropped, and cut on a rune boundary rather than at a
// byte count, so what an organiser reads is still their own message with a
// visible mark where it stopped.
func boundBuildError(text string) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= MaxBuildErrorBytes {
		return text
	}

	const ellipsis = "\n[…]"
	cut := MaxBuildErrorBytes - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + ellipsis
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
	// SaveDefinition stores definition as contest's game and puts it back to
	// pending, the same way SaveScript does for an editor's script — the
	// table builder (migration 26) runs through the identical upsert as the
	// other two sources, which is what keeps a rebuild, a version bump and a
	// cleared schema cache one mechanism rather than three that could drift.
	SaveDefinition(ctx context.Context, contestID uuid.UUID, database string, definition Definition) (Template, error)
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
// builder-sourced game currently means finishDefinitionBuild's own honest
// refusal (DefinitionBuildUnavailable) until the SQL-generation task exists.
func (g *Games) SetDefinition(ctx context.Context, actorID, contestID uuid.UUID, definition Definition) (Template, error) {
	if err := definition.Validate(); err != nil {
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
// game: nothing on the cluster, honestly. Turning a saved Definition into
// SQL and running it is a later task's own work — this one only gives the
// definition a place in the schema and the domain — so running
// claimed.Script (empty for SourceBuilder, per Template's own doc) would
// build nothing, silently, and mark the game 'ready' over a database with
// none of the organiser's tables in it.
//
// Refusing with a named reason instead is CLAUDE.md rule 1 applied to a gap
// in functionality rather than to an error, the same shape finishUploadBuild
// itself had before streaming execution existed for a file-sourced game.
// Unlike that earlier version, this one reuses recordBuildOutcome rather
// than duplicating its row update and audit write, because that helper did
// not exist yet at the point in this codebase's history finishUploadBuild
// was written this way — nothing about the reasoning changed, only what
// there already was to reuse. cause is nil: this is not a fault for the
// tick's own caller to log at every pass over a definition waiting on a
// feature that has not shipped yet, the same choice finishUploadBuild's
// predecessor made for the identical reason.
func (g *Games) finishDefinitionBuild(ctx context.Context, claimed Template) (Template, error) {
	return g.recordBuildOutcome(ctx, claimed, DefinitionBuildUnavailable, nil)
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

// templateName is the database every participant's copy of one contest is
// made from — the `game_tpl_c{short}` shape migration 3 names, and the same
// halved identifier instanceName and spareName use.
func templateName(contest uuid.UUID) string {
	return "game_tpl_c" + short(contest)
}

package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
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

// Template is one contest's game, as an organiser sees it.
type Template struct {
	ContestID uuid.UUID
	Database  string
	Version   int
	Status    TemplateStatus
	// Script is the SQL an author uploaded. Staff-trusted input: it runs with
	// the provisioning role's privileges (gamedb.Provisioner.BuildTemplate's
	// own doc), which is why writing it sits behind PermissionContestEdit and
	// is written to the audit trail.
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

	editable, err := g.author.GameEditable(ctx, contestID)
	if err != nil {
		return Template{}, fmt.Errorf("check whether the game may be replaced: %w", err)
	}
	if !editable {
		return Template{}, ErrGameNotEditable
	}

	var saved Template
	save := func(ctx context.Context) error {
		saved, err = g.repo.SaveScript(ctx, contestID, templateName(contestID), script)
		if err != nil {
			return fmt.Errorf("store the game script: %w", err)
		}
		if g.audit == nil {
			return nil
		}
		// The script itself is not in the payload. It is up to half a
		// mebibyte of SQL, and the trail is a list of who did what — the
		// version is what identifies which script this was, and the script of
		// that version is still on the row.
		return g.audit.Record(ctx, audit.Entry{
			ActorID: &actorID, Action: audit.ActionGameScriptSet,
			Entity: "contest", EntityID: contestID.String(),
			Payload: map[string]any{"version": saved.Version, "script_bytes": len(script)},
		})
	}

	if g.uow != nil {
		err = g.uow.Do(ctx, save)
	} else {
		err = save(ctx)
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

	// The privileges the build grants inside the template are the contest's
	// own SQL policy, read now rather than carried on the claim: it is a
	// different table with a different editor, and the build has to grant
	// what it says at the moment it runs.
	buildErr := ""
	policy, err := g.repo.Policy(ctx, claimed.ContestID)
	if err != nil {
		buildErr = fmt.Sprintf("read the contest's SQL policy: %v", err)
	}

	if buildErr == "" {
		if err := g.cluster.BuildTemplate(ctx, claimed.Database, claimed.Script, policy); err != nil {
			// PostgreSQL's own words, kept: whoever wrote the script is the
			// person who has to fix it, and "the build failed" tells them
			// nothing they can act on.
			buildErr = err.Error()
		}
	}

	if err := g.repo.FinishBuild(ctx, claimed.ContestID, claimed.Version, buildErr); err != nil {
		return claimed, fmt.Errorf("record the build's outcome: %w", err)
	}

	// A system event: nobody is at the keyboard when a build finishes, which
	// is exactly why the trail has to carry it. Recorded outside any
	// transaction and best effort — a trail write that failed must not make a
	// built game report as unbuilt, which would have the tick build it again.
	if g.audit != nil {
		payload := map[string]any{"version": claimed.Version, "database": claimed.Database, "ok": buildErr == ""}
		if buildErr != "" {
			payload["error"] = buildErr
		}
		if err := g.audit.Record(ctx, audit.Entry{
			Action: audit.ActionGameBuilt, Entity: "contest",
			EntityID: claimed.ContestID.String(), Payload: payload,
		}); err != nil {
			return claimed, fmt.Errorf("record the build in the audit trail: %w", err)
		}
	}

	claimed.Status = TemplateReady
	claimed.BuildError = buildErr
	if buildErr != "" {
		claimed.Status = TemplateFailed
	}
	return claimed, nil
}

// templateName is the database every participant's copy of one contest is
// made from — the `game_tpl_c{short}` shape migration 3 names, and the same
// halved identifier instanceName and spareName use.
func templateName(contest uuid.UUID) string {
	return "game_tpl_c" + short(contest)
}

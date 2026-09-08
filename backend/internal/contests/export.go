package contests

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// ErrPackageTooLarge reports a contest carrying more questions than one
// package may. See MaxPackageQuestions for why the answer is a refusal.
var ErrPackageTooLarge = errors.New("contest carries more questions than one package holds")

// MaxPackageQuestions bounds the one list in a package that authoring itself
// leaves unbounded (CLAUDE.md rule 2: an export is a list reaching a
// response, and it carries an explicit bound).
//
// Every other part of a package is already bounded where it was written: the
// game script by provisioning.MaxScriptBytes, a question's options by
// maxChoices, a reference answer by maxAnswerRunes, the writable tables by
// maxWritableTables, the languages by the installation's own catalogue.
// Questions are the exception — they are appended one request at a time, so
// no single request ever carried a list to bound — and this is where that
// bound is stated.
//
// Five hundred is two orders of magnitude past any real olympiad (the
// design's own example asks a handful) while staying a number a person can
// check. The refusal is deliberate: a package is only useful whole, and a
// truncated one is not a smaller contest but a contest whose answer key no
// longer matches its questions — with nothing in the file to say so.
const MaxPackageQuestions = 500

// Package is one contest as something that can be authored again: everything
// an organizer would otherwise retype, and nothing that belongs to the run it
// came from.
//
// Deliberately absent, and this is the decision rather than an omission:
// identifiers (a new installation mints its own), the lifecycle status, the
// schedule, and everything the contest accumulated while it ran — the roster,
// the registrations, the submissions, the query log. Last year's window and
// last year's participants are not part of "the contest"; they are part of
// last year.
//
// Present and sensitive: the reference answers. That is what makes this a
// full answer key and why the endpoint serving it sits behind
// rbac.PermissionContestEdit rather than contest.view (docs/ARCHITECTURE.md
// §15, item 12).
type Package struct {
	// Contest is the contest itself — its declared languages and default, its
	// titles per language, and its configuration. Its own identifier, status
	// and schedule are carried here because this is the domain object; the
	// HTTP layer decides what of it reaches the wire (api.ContestPackage).
	Contest Contest
	// Story is the crime, per language. A contest still being written may not
	// have one, and its Bodies are then empty rather than the export failing.
	Story Story
	// Questions are in display order, with their authored text, their options
	// and their reference answers.
	Questions []Question
	// Policy is how much SQL power the contest hands out. Never absent: an
	// unconfigured contest reports DefaultSQLPolicy, which is what
	// "unconfigured" means everywhere else.
	Policy SQLPolicy
	// Game is the SQL the contest's game database is built from, and HasGame
	// says whether there is one at all. Without it the package describes a
	// contest whose every question answers "no such table", so it belongs in
	// the package even though it lives in another package's storage.
	Game    string
	HasGame bool
	// GameOmitted says the contest has a game this package could not carry:
	// one built from an uploaded dump rather than from a script, whose SQL is
	// up to gigabytes on the API host's own volume and is never in this
	// service's storage at all (provisioning.SourceFile).
	//
	// It exists because "no game" and "a game that did not travel" are
	// different facts and this used to report them as the same one — Game was
	// the empty string either way, HasGame said true, and an organizer
	// exporting last year's olympiad got a package announcing a game and
	// carrying none. Re-importing that answers ErrScriptEmpty; the audit
	// entry meanwhile recorded "game: true". Set exactly when HasGame is
	// true and Game is empty by this reason rather than by absence.
	GameOmitted bool
}

// AnswerCount is how many reference answers the package carries, across every
// question — the number the trail records instead of the answers themselves
// (§9.2).
func (p Package) AnswerCount() int {
	total := 0
	for _, q := range p.Questions {
		total += len(q.Answers)
	}
	return total
}

// GameSource is the slice of a contest's game this package needs: the SQL an
// organizer wrote, nothing about the template built from it.
//
// Declared here, by the consumer, and kept to one method (Go layout rule 3):
// the game's own lifecycle lives in internal/provisioning, which this package
// must not import, and the export has no business with build status, versions
// or the cluster.
type GameSource interface {
	// Script returns the SQL one contest's game is built from, whether the
	// contest has a game at all, and whether that game's SQL is somewhere a
	// package cannot reach — an uploaded dump on the API host's disk, for
	// which script is empty and omitted is true.
	//
	// A contest without a game is not an error: exporting a draft whose game
	// has not been written yet is ordinary. The third value is here because
	// the empty script is not: the two used to be indistinguishable at this
	// boundary, so a game that could not travel arrived as a game that did
	// (Package.GameOmitted).
	Script(ctx context.Context, contestID uuid.UUID) (script string, ok, omitted bool, err error)
}

// ExportPackage assembles the contest as a package (docs/ARCHITECTURE.md §15,
// item 12: export before import, because an organizer wants last year's
// olympiad to edit rather than a blank form).
//
// Assembled whole rather than streamed. The reasoning is the same one that
// makes MaxPackageQuestions a refusal: a package that arrives in pieces can
// arrive incomplete after the status line already said 200, and an answer key
// missing its last three answers is worse than no file. Bounded, then, and
// small enough to be — the bound above is what makes that claim rather than
// an assumption about how many questions organizers write.
//
// The read is not transactional and does not need to be. A contest being
// edited while it is exported can produce a package one question behind, and
// that is the same staleness any GET of the constructor already has; what
// would matter — a half-written question — cannot happen, because a question
// is written whole in one transaction (Service.SaveQuestion).
func (s *Service) ExportPackage(ctx context.Context, actorID, contestID uuid.UUID) (Package, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Package{}, err
	}

	pkg := Package{Contest: c}

	// A contest still being written may have no story. That is a state of the
	// contest, not a failure of the export.
	story, err := s.stories.ByContest(ctx, contestID)
	switch {
	case err == nil:
		pkg.Story = story
	case errors.Is(err, ErrStoryNotFound):
	default:
		return Package{}, fmt.Errorf("read the story: %w", err)
	}

	questions, err := s.questions.List(ctx, contestID)
	if err != nil {
		return Package{}, fmt.Errorf("read the questions: %w", err)
	}
	if len(questions) > MaxPackageQuestions {
		return Package{}, fmt.Errorf("%w: %d questions, the limit is %d",
			ErrPackageTooLarge, len(questions), MaxPackageQuestions)
	}
	pkg.Questions = questions

	if pkg.Policy, err = s.policies.ByContest(ctx, contestID); err != nil {
		return Package{}, fmt.Errorf("read the sql policy: %w", err)
	}

	// Nil where the deployment has no game cluster at all (internal/app wires
	// the game half only then). The package then says so by carrying no game,
	// rather than the export failing over a circuit this contest may never
	// have had.
	if s.game != nil {
		script, ok, omitted, err := s.game.Script(ctx, contestID)
		if err != nil {
			return Package{}, fmt.Errorf("read the game script: %w", err)
		}
		pkg.Game, pkg.HasGame, pkg.GameOmitted = script, ok, omitted
	}

	// Recorded before the caller is handed the package, and a failure to
	// record fails the export: this is a full answer key leaving the
	// installation, which is precisely the event the trail exists for.
	//
	// Inside a unit of work like every other privileged action, even though
	// nothing else is being written — one statement in one transaction costs
	// a round trip and keeps this call site looking like the twenty others,
	// which is what stops the next one from inventing a third arrangement.
	//
	// Counts, never content (§9.2): the trail is read by organizers and must
	// not become the second place a reference answer can be looked up.
	payload := map[string]any{
		"questions": len(pkg.Questions),
		"answers":   pkg.AnswerCount(),
		"languages": c.LanguageCodes(),
		"game":      pkg.HasGame,
		// Recorded beside it rather than folded into "game": what an
		// organizer checks the trail for afterwards is whether the file they
		// downloaded is the whole contest, and "game: true" on its own
		// answered yes for a package the game never entered.
		"game_omitted": pkg.GameOmitted,
	}
	if err := s.uow.Do(ctx, func(ctx context.Context) error {
		return s.record(ctx, actorID, audit.ActionContestPackageExport, contestID, payload)
	}); err != nil {
		return Package{}, err
	}
	return pkg, nil
}

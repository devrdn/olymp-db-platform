package contests

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// ErrPackageTooLarge reports a contest with more than MaxPackageQuestions.
var ErrPackageTooLarge = errors.New("contest carries more questions than one package holds")

// MaxPackageQuestions bounds the one list in a package that authoring leaves
// unbounded, since questions are added one request at a time (CLAUDE.md
// rule 2). Exceeding it is a refusal, not truncation: a truncated package is
// an answer key that no longer matches its contest, with nothing to say so.
const MaxPackageQuestions = 500

// Package is one contest as something that can be authored again. It leaves
// out identifiers, status, schedule and everything the run accumulated
// (roster, submissions, query log).
//
// It carries the reference answers, so it is a full answer key and its
// endpoint requires contest.edit (docs/ARCHITECTURE.md §15, item 12).
type Package struct {
	// Contest is the domain object; the HTTP layer decides which fields
	// reach the wire.
	Contest Contest
	// Story has empty Bodies when the contest has no story yet.
	Story Story
	// Questions are in display order, with texts, options and answers.
	Questions []Question
	// Policy is DefaultSQLPolicy for an unconfigured contest.
	Policy SQLPolicy
	// Game is the SQL the game database is built from; HasGame says whether
	// there is a game at all.
	Game    string
	HasGame bool
	// GameOmitted is set when the contest has a game built from an uploaded
	// dump, which never lives in this service's storage and so cannot travel.
	// Then HasGame is true and Game is empty.
	GameOmitted bool
}

// AnswerCount is how many reference answers the package carries, the number
// the trail records instead of the answers (§9.2).
func (p Package) AnswerCount() int {
	total := 0
	for _, q := range p.Questions {
		total += len(q.Answers)
	}
	return total
}

// GameSource is the part of a contest's game the export needs (CLAUDE.md Go
// layout rule 3); internal/provisioning implements it.
type GameSource interface {
	// Script returns the SQL a contest's game is built from and whether the
	// contest has a game. omitted is true, with an empty script, for a game
	// built from an uploaded dump. No game is not an error.
	Script(ctx context.Context, contestID uuid.UUID) (script string, ok, omitted bool, err error)
}

// ExportPackage assembles the contest as a package (docs/ARCHITECTURE.md §15,
// item 12).
//
// Assembled whole rather than streamed, so a failure cannot leave a partial
// answer key behind a 200. The reads are not transactional: a package can be
// one question behind a concurrent edit, but never hold a half-written one,
// since questions are written whole.
func (s *Service) ExportPackage(ctx context.Context, actorID, contestID uuid.UUID) (Package, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Package{}, err
	}

	pkg := Package{Contest: c}

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

	// Nil without a game cluster; the package then carries no game.
	if s.game != nil {
		script, ok, omitted, err := s.game.Script(ctx, contestID)
		if err != nil {
			return Package{}, fmt.Errorf("read the game script: %w", err)
		}
		pkg.Game, pkg.HasGame, pkg.GameOmitted = script, ok, omitted
	}

	// An answer key leaving the installation: a failure to record fails the
	// export. Counts only, never content (§9.2).
	payload := map[string]any{
		"questions":    len(pkg.Questions),
		"answers":      pkg.AnswerCount(),
		"languages":    c.LanguageCodes(),
		"game":         pkg.HasGame,
		"game_omitted": pkg.GameOmitted,
	}
	if err := s.uow.Do(ctx, func(ctx context.Context) error {
		return s.record(ctx, actorID, audit.ActionContestPackageExport, contestID, payload)
	}); err != nil {
		return Package{}, err
	}
	return pkg, nil
}

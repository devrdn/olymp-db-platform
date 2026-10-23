package contests

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// maxSchemaNotesRunes bounds one language's table descriptions (CLAUDE.md
// rule 2). Half the story's room: three languages of Cyrillic at this bound
// still fit the 1 MiB request body.
const maxSchemaNotesRunes = 100_000

// SchemaNotes are the table descriptions: a markdown document per language the
// organiser writes about the game's tables. They are contest content, not the
// catalogue, so a contest that hides its catalogue still shows them.
type SchemaNotes struct {
	// Bodies hold the markdown per language code; never nil, and a language
	// is present only with a non-blank text.
	Bodies map[string]string
	// UpdatedAt is the latest save, zero when none was ever written.
	UpdatedAt time.Time
}

// SchemaNotesRepository stores a contest's table descriptions.
type SchemaNotesRepository interface {
	// ByContest returns every language's text; a contest with none has empty
	// Bodies, which is not an error.
	ByContest(ctx context.Context, contestID uuid.UUID) (SchemaNotes, error)
	// Save replaces the whole language set with exactly these texts, and
	// reports ErrNotFound for a contest that is not there.
	Save(ctx context.Context, contestID uuid.UUID, bodies map[string]string) (SchemaNotes, error)
}

// SchemaNotesText is the participant's read of the table descriptions.
type SchemaNotesText interface {
	// FirstIn returns the text in the first of langs that has one, or "" when
	// none does.
	FirstIn(ctx context.Context, contestID uuid.UUID, langs []string) (string, error)
}

// SchemaNotes returns the contest's table descriptions in every language.
func (s *Service) SchemaNotes(ctx context.Context, contestID uuid.UUID) (SchemaNotes, error) {
	if _, err := s.contests.ByID(ctx, contestID); err != nil {
		return SchemaNotes{}, err
	}
	return s.schemaNotes.ByContest(ctx, contestID)
}

// SetSchemaNotes replaces the table descriptions' whole language set. A blank
// text is dropped rather than stored, so a language is either written or
// absent and the participant's tab never opens on nothing.
func (s *Service) SetSchemaNotes(ctx context.Context, actorID, contestID uuid.UUID, bodies map[string]string) (SchemaNotes, error) {
	if _, err := s.editableContest(ctx, contestID); err != nil {
		return SchemaNotes{}, err
	}
	written := make(map[string]string, len(bodies))
	for lang, body := range bodies {
		if utf8.RuneCountInString(body) > maxSchemaNotesRunes {
			return SchemaNotes{}, fmt.Errorf("%w: table descriptions are at most %d characters", ErrInvalidContest, maxSchemaNotesRunes)
		}
		if strings.TrimSpace(body) != "" {
			written[lang] = body
		}
	}
	if err := s.checkLanguageCodes(ctx, langCodes(bodies)); err != nil {
		return SchemaNotes{}, err
	}
	langs := langCodes(written)

	var saved SchemaNotes
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if saved, err = s.schemaNotes.Save(ctx, contestID, written); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionContestSchemaNotesChange, contestID, map[string]any{
			"languages": langs,
		})
	})
	if err != nil {
		return SchemaNotes{}, err
	}
	return saved, nil
}

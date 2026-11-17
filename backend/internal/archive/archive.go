// Package archive answers which contests are in the archive and what of each
// may be shown: the open contests that have ended, and, when the organiser
// allows it, their story, table descriptions and visible questions. It does
// not decide results (the public leaderboard does), who may read it or how
// often (the handlers do), and it never reads reference answers or hidden
// questions.
package archive

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/i18n"
)

const (
	DefaultPageSize = 20
	MaxPageSize     = 50
	// MaxQueryRunes bounds the search text (CLAUDE.md rule 2).
	MaxQueryRunes = 100
)

// ErrNotFound means the contest is not in the archive: it does not exist, is
// closed, or has not ended.
var ErrNotFound = errors.New("contest is not in the archive")

// ErrDisabled means an administrator has switched the archive off.
var ErrDisabled = errors.New("the archive is switched off")

// ErrQueryTooLong means the search text is over MaxQueryRunes.
var ErrQueryTooLong = errors.New("archive search text is too long")

// Filter selects a page of the archive.
type Filter struct {
	// Query matches a title in any translation, case-insensitively.
	Query         string
	Limit, Offset int
}

// Normalize trims the query and brings the page into bounds.
func (f Filter) Normalize() Filter {
	f.Query = strings.TrimSpace(f.Query)
	switch {
	case f.Limit <= 0:
		f.Limit = DefaultPageSize
	case f.Limit > MaxPageSize:
		f.Limit = MaxPageSize
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}

// Contest is one archive entry.
type Contest struct {
	ID         uuid.UUID
	Status     string
	Visibility string
	StartsAt   *time.Time
	EndsAt     *time.Time
	// Participants counts registrations, the disqualified excluded.
	Participants int
	// Titles and Descriptions are keyed by language code.
	Titles, Descriptions map[string]string
	Languages            []string
	DefaultLanguage      string
	ArchiveTasks         bool
}

// TitleIn is the title in the visitor's language, falling back to the
// contest's default and then to any title it has.
func (c Contest) TitleIn(lang string) string { return pick(c.Titles, lang, c.DefaultLanguage) }

// DescriptionIn is the description by the same fallback as TitleIn.
func (c Contest) DescriptionIn(lang string) string {
	return pick(c.Descriptions, lang, c.DefaultLanguage)
}

// pick sorts the candidates so the answer does not depend on map order.
func pick(byLang map[string]string, lang, fallback string) string {
	codes := make([]string, 0, len(byLang))
	for code := range byLang {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	return byLang[i18n.Match([]string{lang}, codes, fallback)]
}

// Page is one page of the archive and the total matching the filter.
type Page struct {
	Items []Contest
	Total int
}

// Repository is the storage this package needs.
type Repository interface {
	// List leaves each contest's Descriptions empty: a page shows none.
	List(ctx context.Context, f Filter) (Page, error)
	// ByID returns ErrNotFound for a contest outside the archive.
	ByID(ctx context.Context, id uuid.UUID) (Contest, error)
}

// Switch is the administrator's on/off control of the archive.
type Switch interface {
	ArchiveEnabled(ctx context.Context) (bool, error)
}

// Config wires a Service. Every field is required.
type Config struct {
	Repository Repository
	Switch     Switch
	Stories    contests.StoryText
	Notes      contests.SchemaNotesText
	// Questions must be the participant-facing read: nothing the service
	// shows is filtered afterwards.
	Questions contests.VisibleQuestionRepository
}

// Service reads the archive.
type Service struct{ cfg Config }

// NewService returns the archive service.
func NewService(cfg Config) *Service { return &Service{cfg: cfg} }

// Enabled reports whether the archive is switched on.
func (s *Service) Enabled(ctx context.Context) (bool, error) {
	return s.cfg.Switch.ArchiveEnabled(ctx)
}

func (s *Service) require(ctx context.Context) error {
	on, err := s.Enabled(ctx)
	if err != nil {
		return err
	}
	if !on {
		return ErrDisabled
	}
	return nil
}

// List returns a page of the archive. The switch is checked first, then the
// search bound, because the repository neither normalizes nor bounds.
func (s *Service) List(ctx context.Context, f Filter) (Page, error) {
	if err := s.require(ctx); err != nil {
		return Page{}, err
	}
	f = f.Normalize()
	if utf8.RuneCountInString(f.Query) > MaxQueryRunes {
		return Page{}, ErrQueryTooLong
	}
	return s.cfg.Repository.List(ctx, f)
}

// Contest returns one archived contest. A contest that is closed or has not
// ended is refused even if the repository returned it.
func (s *Service) Contest(ctx context.Context, id uuid.UUID) (Contest, error) {
	if err := s.require(ctx); err != nil {
		return Contest{}, err
	}
	c, err := s.cfg.Repository.ByID(ctx, id)
	if err != nil {
		return Contest{}, err
	}
	ended := c.Status == contests.StatusFinished || c.Status == contests.StatusArchived
	if c.Visibility != contests.VisibilityOpen || !ended {
		return Contest{}, ErrNotFound
	}
	return c, nil
}

// Question is one visible question as a stranger sees it.
//
// Position is the 1-based place among the visible questions, never the
// stored order, whose gaps would reveal the hidden ones.
type Question struct {
	Position  int
	Kind      string
	Points    int
	IsBonus   bool
	Title     string
	BodyMD    string
	ChoiceIDs []string
	Choices   map[string]string
}

// Tasks is a contest's story, table descriptions and questions in one
// language.
type Tasks struct {
	Lang, Story, SchemaNotes string
	Questions                []Question
}

// Tasks returns the contest's tasks, or nil when the organiser keeps them
// private. The caller has already got c from Contest.
func (s *Service) Tasks(ctx context.Context, c Contest, lang string) (*Tasks, error) {
	if !c.ArchiveTasks {
		return nil, nil
	}
	story, err := s.cfg.Stories.BodyIn(ctx, c.ID, lang)
	if errors.Is(err, contests.ErrStoryNotFound) {
		story, err = "", nil
	}
	if err != nil {
		return nil, err
	}
	langs := []string{lang}
	if c.DefaultLanguage != "" && c.DefaultLanguage != lang {
		langs = append(langs, c.DefaultLanguage)
	}
	notes, err := s.cfg.Notes.FirstIn(ctx, c.ID, langs)
	if err != nil {
		return nil, err
	}
	visible, err := s.cfg.Questions.ForContest(ctx, c.ID, lang)
	if err != nil {
		return nil, err
	}
	questions := make([]Question, 0, len(visible))
	for i, q := range visible {
		questions = append(questions, Question{
			Position: i + 1, Kind: q.Kind, Points: q.Points, IsBonus: q.IsBonus,
			Title: q.Title, BodyMD: q.BodyMD, ChoiceIDs: q.ChoiceIDs, Choices: q.Choices,
		})
	}
	return &Tasks{Lang: lang, Story: story, SchemaNotes: notes, Questions: questions}, nil
}

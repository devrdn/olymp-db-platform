// Package archive answers what the contest archive lists: the open contests
// that have ended, with their titles in every language. It does not decide who
// may read it, how often, or what a contest's questions show; those belong to
// the service and handlers built on it.
package archive

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

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

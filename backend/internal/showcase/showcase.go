// Package showcase answers the landing page's two public reads: the
// installation's four totals and the few contests a visitor may see. Both are
// cached, and neither reveals more than the public leaderboard.
//
// It never shows a draft: the selection is applied in the SQL and again in
// Recent, so a change to the query cannot leak one. It knows no caller or
// session; the per-address budget is internal/api's.
package showcase

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/flight"
	"github.com/devrdn/db-contest/backend/internal/platform/i18n"
)

const (
	// MaxRecent bounds the list the page carries: one row of cards
	// (CLAUDE.md rule 2).
	MaxRecent = 3
	// CacheTTL is how long one read serves every visitor. It keeps an
	// anonymous page from being a lever on the database.
	CacheTTL = time.Minute
	// MaxStale is how long past CacheTTL a cached answer may still be served
	// while the refresh keeps failing: long enough to ride out a database
	// restart or failover, short enough that the page does not show a
	// finished contest as running. Past it the read fails.
	MaxStale = 15 * time.Minute
	// computeTimeout bounds one shared read (see flight.Do). The pool's
	// statement timeout caps each query, so this guards a connection acquire
	// that never returns.
	computeTimeout = 15 * time.Second
)

// Numbers are installation-wide totals: contests held, registrations, queries
// run and questions solved.
type Numbers struct {
	Contests     int64
	Participants int64
	Queries      int64
	Solved       int64
}

// Contest is one row of the page's list.
//
// A renderer reads Title only; Service.Recent picks it per caller from Titles
// and DefaultLanguage, so one cached list serves every language.
type Contest struct {
	ID     uuid.UUID
	Title  string
	Status string
	// StartsAt and EndsAt are absent for an unscheduled contest.
	StartsAt *time.Time
	EndsAt   *time.Time
	// TableOpen says whether the public leaderboard may be linked to: the
	// contest is over, or its frozen result was revealed.
	TableOpen bool

	// CoverHash names the uploaded cover; empty means the client draws one
	// from the contest's identifier. It is a hash, not a URL: the address is
	// the HTTP layer's business.
	CoverHash string
	// CoverAttribution credits the uploaded picture's author; publishing
	// requires one whenever CoverHash is set.
	CoverAttribution string

	Titles          map[string]string
	DefaultLanguage string
}

// publicStatuses are the contest statuses a visitor may be shown. A draft is
// never among them.
var publicStatuses = []string{
	contests.StatusPublished, contests.StatusRunning,
	contests.StatusFinished, contests.StatusArchived,
}

// Public reports whether a contest of this status may appear on a public page.
func Public(status string) bool { return slices.Contains(publicStatuses, status) }

// titleIn is the contest's title in the visitor's language, falling back to
// the contest's default and then to any title it has. Candidates are sorted so
// the answer does not depend on map order.
func (c Contest) titleIn(lang string) string {
	codes := make([]string, 0, len(c.Titles))
	for code := range c.Titles {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	return c.Titles[i18n.Match([]string{lang}, codes, c.DefaultLanguage)]
}

// Repository is the storage this package needs.
type Repository interface {
	Numbers(ctx context.Context) (Numbers, error)
	// Recent returns at most limit contests a visitor may see, newest first,
	// with every title each has.
	Recent(ctx context.Context, limit int) ([]Contest, error)
}

type Config struct {
	Repository Repository
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// CacheTTL defaults to the constant.
	CacheTTL time.Duration
}

type Service struct {
	repo Repository
	now  func() time.Time
	ttl  time.Duration

	mu       sync.Mutex
	numbers  cachedNumbers
	contests cachedContests

	flight singleflight.Group
}

// stamp records whether a cached entry exists and when it stops being fresh.
//
// Expiry counts from when the read was asked for, not when it returned. That
// orders overlapping reads, so a slow read that started first cannot
// overwrite a newer cached answer and stamp it with a fresh span.
type stamp struct {
	present bool
	expires time.Time
}

func (s stamp) fresh(now time.Time) bool { return s.present && now.Before(s.expires) }

// servable reports whether the entry may still be served while the refresh
// is failing.
func (s stamp) servable(now time.Time) bool { return s.present && now.Before(s.expires.Add(MaxStale)) }

// newerThan reports whether this entry's read was asked for later than the
// cached one's; a write must pass it.
func (s stamp) newerThan(cached stamp) bool {
	return !cached.present || s.expires.After(cached.expires)
}

type cachedNumbers struct {
	stamp
	value Numbers
}

type cachedContests struct {
	stamp
	value []Contest
}

func NewService(cfg Config) *Service {
	s := &Service{repo: cfg.Repository, now: cfg.Now, ttl: cfg.CacheTTL}
	if s.now == nil {
		s.now = time.Now
	}
	if s.ttl <= 0 {
		s.ttl = CacheTTL
	}
	return s
}

// Numbers returns the installation's four numbers, from the shared cache
// while it is fresh.
//
// A failed refresh serves the previous answer for up to MaxStale, since the
// page drops the whole row on failure. A caller whose own context is done gets
// ctx.Err() instead, so a closed tab is not logged as an outage.
func (s *Service) Numbers(ctx context.Context) (Numbers, error) {
	now := s.now()
	s.mu.Lock()
	entry := s.numbers
	s.mu.Unlock()
	if entry.fresh(now) {
		return entry.value, nil
	}

	result, err := flight.Do(ctx, &s.flight, "numbers", computeTimeout, func(ctx context.Context) (any, error) {
		return s.repo.Numbers(ctx)
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Numbers{}, ctxErr
		}
		if entry.servable(now) {
			return entry.value, nil
		}
		return Numbers{}, fmt.Errorf("read the showcase numbers: %w", err)
	}
	numbers := result.(Numbers)

	fresh := cachedNumbers{stamp: stamp{present: true, expires: now.Add(s.ttl)}, value: numbers}
	s.mu.Lock()
	if fresh.newerThan(s.numbers.stamp) {
		s.numbers = fresh
	}
	s.mu.Unlock()
	return numbers, nil
}

// Recent returns the contests a visitor may see, newest first, titled in
// lang. The result is the caller's own copy. The status filter and MaxRecent
// are applied again over what storage returned, as a second guard against a
// draft. Stale answers and a done context behave as in Numbers.
func (s *Service) Recent(ctx context.Context, lang string) ([]Contest, error) {
	now := s.now()
	s.mu.Lock()
	entry := s.contests
	s.mu.Unlock()
	if entry.fresh(now) {
		return title(entry.value, lang), nil
	}

	result, err := flight.Do(ctx, &s.flight, "recent", computeTimeout, func(ctx context.Context) (any, error) {
		return s.repo.Recent(ctx, MaxRecent)
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if entry.servable(now) {
			return title(entry.value, lang), nil
		}
		return nil, fmt.Errorf("read the recent contests: %w", err)
	}
	list := result.([]Contest)

	fresh := cachedContests{stamp: stamp{present: true, expires: now.Add(s.ttl)}, value: list}
	s.mu.Lock()
	if fresh.newerThan(s.contests.stamp) {
		s.contests = fresh
	}
	s.mu.Unlock()
	return title(list, lang), nil
}

// title copies the cached list into one caller's answer: titles in their
// language, public statuses only, at most MaxRecent rows. Titles is cloned so
// the caller's rows share no map with the cache.
func title(list []Contest, lang string) []Contest {
	out := make([]Contest, 0, min(len(list), MaxRecent))
	for _, c := range list {
		if !Public(c.Status) {
			continue
		}
		c.Title = c.titleIn(lang)
		c.Titles = maps.Clone(c.Titles)
		out = append(out, c)
		if len(out) == MaxRecent {
			break
		}
	}
	return out
}

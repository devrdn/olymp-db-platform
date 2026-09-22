// Package showcase answers the two reads the landing page makes: the four
// numbers an installation has to show for itself, and the handful of contests
// a visitor may be shown. Both are public — nobody is signed in on the page
// they serve — so both are cached, and neither opens anything the public
// leaderboard has not already opened.
//
// What it deliberately does not answer: anything about a draft. A draft's
// title, its window and even its existence belong to its authors (the same
// rule internal/profile states for the participant's catalogue), so the
// selection is stated twice — once in the SQL that reads it and once here, in
// Recent — because this is the list a change to that query could leak one
// into. It also tells nobody apart: there is no caller, no session and no
// identifier in any of it, and the numbers are totals over the installation
// rather than anybody's own.
//
// It contains no SQL and no HTTP. The storage it needs is the Repository
// declared here and implemented by internal/postgres.Showcase; who may call
// it — the per-address budget — is internal/api's.
//
// The design is docs/superpowers/specs/2026-09-22-landing-page-design.md
// (§1 and §4).
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
	"github.com/devrdn/db-contest/backend/internal/platform/i18n"
)

const (
	// MaxRecent bounds the list the page carries (CLAUDE.md rule 2). The
	// design asks for at most six rows, and a bound the domain states is one
	// the page cannot be talked out of by a repository that returns more.
	MaxRecent = 6
	// CacheTTL is how long one read serves every visitor. A minute, because
	// nothing on this page changes faster than that matters: a contest's
	// status moves on the scheduler's own tick and the numbers are a running
	// total. It is what keeps a page anybody may load without signing in from
	// being a lever on the database.
	CacheTTL = time.Minute
	// MaxStale is how long past its minute a cached answer may still be
	// served while the refresh keeps failing.
	//
	// Serving the last answer through a hiccup is what keeps a database
	// restart from blanking the page. Serving it through an afternoon is a
	// different thing: the numbers stop being the installation's, and a
	// contest the page says is running finished hours ago, with nothing on
	// the page saying so. A quarter of an hour is long enough to cover a
	// restart or a failover — both of which the landing page should ride out
	// without a visitor noticing — and short enough that what a visitor is
	// shown is still today's. Past it the read fails, the page drops the row
	// (design §2.3), and saying nothing is the honest answer.
	MaxStale = 15 * time.Minute
	// computeTimeout bounds one shared read once it no longer belongs to any
	// single caller (see computeOnce). Well above what two aggregate reads
	// take — the core pool's own statement timeout already caps each query —
	// so what it actually guards is a connection acquire that never returns.
	computeTimeout = 15 * time.Second
)

// Numbers is what the installation has to show for itself: contests held,
// registrations taken, queries run and questions solved. Totals over
// everything, never anybody's own.
type Numbers struct {
	Contests     int64
	Participants int64
	Queries      int64
	Solved       int64
}

// Contest is one row of the page's list.
//
// Title is the one the visitor was given, chosen by Service.Recent from
// Titles; a caller rendering the page reads Title and nothing else. Titles
// and DefaultLanguage are the raw material it was chosen from, carried
// because the choice belongs to the visitor's request and the read is shared
// by every visitor at once — one cached list, one title picked per caller,
// rather than a cache entry per language.
type Contest struct {
	ID     uuid.UUID
	Title  string
	Status string
	// StartsAt and EndsAt are the contest's window; either may be absent,
	// which is what an unscheduled contest looks like.
	StartsAt *time.Time
	EndsAt   *time.Time
	// TableOpen says whether the public leaderboard may be linked to: the
	// contest is over, or its frozen result was revealed.
	TableOpen bool

	Titles          map[string]string
	DefaultLanguage string
}

// publicStatuses are the contest statuses a visitor may be shown, and the
// same four internal/profile lists for a participant's own catalogue. A draft
// is not among them and never becomes one.
var publicStatuses = []string{
	contests.StatusPublished, contests.StatusRunning,
	contests.StatusFinished, contests.StatusArchived,
}

// Public reports whether a contest of this status may appear on a page nobody
// had to sign in to read.
func Public(status string) bool { return slices.Contains(publicStatuses, status) }

// titleIn is the contest's title in the visitor's language, falling back to
// the contest's own default and then to whatever it has: a row with no title
// in any language the visitor reads is still a row that happened.
//
// The candidates are sorted, so a contest with several translations and no
// usable preference answers the same title twice running rather than
// whichever one the map handed over first.
func (c Contest) titleIn(lang string) string {
	codes := make([]string, 0, len(c.Titles))
	for code := range c.Titles {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	return c.Titles[i18n.Match([]string{lang}, codes, c.DefaultLanguage)]
}

// Repository is the storage this package needs, implemented by
// internal/postgres.Showcase.
type Repository interface {
	// Numbers counts the installation's four numbers in one statement.
	Numbers(ctx context.Context) (Numbers, error)
	// Recent returns at most limit contests a visitor may see, newest first,
	// with every title each of them has.
	Recent(ctx context.Context, limit int) ([]Contest, error)
}

// Config assembles a Service.
type Config struct {
	Repository Repository
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// CacheTTL defaults to the constant of the same name.
	CacheTTL time.Duration
}

// Service answers the landing page's two reads from a shared, short-lived
// cache.
type Service struct {
	repo Repository
	now  func() time.Time
	ttl  time.Duration

	mu       sync.Mutex
	numbers  cachedNumbers
	contests cachedContests

	// flight collapses concurrent misses of the same read into one call to
	// the repository: a hundred visitors arriving together cost one.
	flight singleflight.Group
}

// stamp is what both cached entries carry: whether anything is here at all,
// and the moment the value stops being fresh.
//
// The moment is the cache's span past the point the read was *asked for*,
// never past the point it came back. A read that took a while is then never
// presented as fresher than it is, and — the reason it is written this way —
// the entries of two overlapping reads can be ordered by it. Without that
// order a waiter from an in-flight read that started first can come back
// after a later read has already cached a newer answer, overwrite it with the
// older value, and stamp that with a whole fresh span.
type stamp struct {
	present bool
	expires time.Time
}

// fresh reports whether the entry is here and still inside its span.
func (s stamp) fresh(now time.Time) bool { return s.present && now.Before(s.expires) }

// servable reports whether the entry may still be handed to a visitor while
// the refresh behind it is failing: inside its span, or inside the grace
// MaxStale allows past it.
func (s stamp) servable(now time.Time) bool { return s.present && now.Before(s.expires.Add(MaxStale)) }

// newerThan reports whether this entry's read was asked for later than the
// one already cached, which is the test a write has to pass.
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

// NewService returns the service.
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
// while it is still fresh.
//
// A failed refresh with a previous answer to hand serves that answer rather
// than nothing: the page drops the whole row when this read fails (design
// §2.3), and a momentary database hiccup is not a reason to tell a visitor
// the installation has run nothing. That lasts MaxStale past the answer's own
// minute and no longer; after it the failure is reported, because an answer
// nothing has been able to confirm for a quarter of an hour is no longer the
// installation's numbers.
//
// A caller whose own context is done is the one thing that is not a failed
// refresh, and it comes back as ctx.Err() — context.Canceled or
// context.DeadlineExceeded, the sentinels every caller already knows. Nothing
// here broke and there is nobody left to serve, so neither branch above
// applies: a stale answer would be written to a connection that has gone, and
// an error would be logged as an outage because somebody closed a tab.
func (s *Service) Numbers(ctx context.Context) (Numbers, error) {
	now := s.now()
	s.mu.Lock()
	entry := s.numbers
	s.mu.Unlock()
	if entry.fresh(now) {
		return entry.value, nil
	}

	result, err := s.computeOnce(ctx, "numbers", func(ctx context.Context) (any, error) {
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
// lang.
//
// The cached list is every title each contest has, so one read serves every
// language: the copy returned here is this caller's own, and nothing in it
// aliases what the next caller will be handed.
//
// The selection is applied again over what storage returned, and the list cut
// to MaxRecent again: the query already does both, and this is the second
// place a draft would have to get past to reach a page nobody signed in to
// read.
//
// A caller whose own context is done gets ctx.Err(), for Numbers' own reason.
func (s *Service) Recent(ctx context.Context, lang string) ([]Contest, error) {
	now := s.now()
	s.mu.Lock()
	entry := s.contests
	s.mu.Unlock()
	if entry.fresh(now) {
		return title(entry.value, lang), nil
	}

	result, err := s.computeOnce(ctx, "recent", func(ctx context.Context) (any, error) {
		return s.repo.Recent(ctx, MaxRecent)
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// Stale rather than empty, for Numbers' own reason, and only as long
		// as Numbers allows.
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

// title copies the cached list into the answer one caller gets: their
// language's title on every row, nothing a draft could ride in on, and at
// most MaxRecent rows.
//
// Titles is cloned rather than carried over, because copying a Contest copies
// the map header and not the map: without this the row a visitor is handed
// shares its translations with the entry every later visitor will be handed,
// and one caller writing into it would rewrite the cache. Nothing does today,
// which is exactly the kind of thing that stops being true later.
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

// computeOnce collapses every concurrent miss of one key into a single call
// to fn, the way internal/leaderboard's own does and for the same reason: the
// read belongs to the key rather than to whichever visitor happened to start
// it.
//
// fn runs detached from any one caller's context (context.WithoutCancel),
// bounded by computeTimeout instead, so a visitor who closes the tab does not
// cut short a read the visitors behind them are waiting for; each caller
// still honours its own context and stops waiting when that is done. A panic
// inside fn becomes an ordinary error rather than a crash on a goroutine
// nobody recovers.
func (s *Service) computeOnce(ctx context.Context, key string, fn func(ctx context.Context) (any, error)) (any, error) {
	ch := s.flight.DoChan(key, func() (result any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("compute %s: %v", key, r)
			}
		}()
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), computeTimeout)
		defer cancel()
		return fn(flightCtx)
	})

	select {
	case res := <-ch:
		return res.Val, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

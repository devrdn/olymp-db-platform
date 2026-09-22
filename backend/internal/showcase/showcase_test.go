package showcase_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/showcase"
)

// countingRepo answers from what a test put in it and counts how often it was
// asked: what the cache exists to keep small.
type countingRepo struct {
	numbers  showcase.Numbers
	contests []showcase.Contest
	// fail, when set, is what every read answers instead.
	fail error
	// during, when set, runs inside every read: what a test does to the world
	// while the repository is busy, such as moving the clock on.
	during func()

	numberReads  atomic.Int64
	contestReads atomic.Int64
	// limit is the limit the last Recent was asked for.
	limit atomic.Int64
}

func (r *countingRepo) Numbers(context.Context) (showcase.Numbers, error) {
	r.numberReads.Add(1)
	if r.during != nil {
		r.during()
	}
	if r.fail != nil {
		return showcase.Numbers{}, r.fail
	}
	return r.numbers, nil
}

func (r *countingRepo) Recent(_ context.Context, limit int) ([]showcase.Contest, error) {
	r.contestReads.Add(1)
	r.limit.Store(int64(limit))
	if r.during != nil {
		r.during()
	}
	if r.fail != nil {
		return nil, r.fail
	}
	return r.contests, nil
}

// newService returns a service over repo whose clock a test can move.
func newService(repo *countingRepo, now *time.Time) *showcase.Service {
	return showcase.NewService(showcase.Config{
		Repository: repo,
		Now:        func() time.Time { return *now },
	})
}

func TestManyCallersShareTheReadsBetweenThem(t *testing.T) {
	repo := &countingRepo{numbers: showcase.Numbers{Contests: 3, Participants: 40, Queries: 900, Solved: 120}}
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	service := newService(repo, &now)

	var wg sync.WaitGroup
	got := make([]showcase.Numbers, 10)
	errs := make([]error, 10)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = service.Numbers(t.Context())
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: Numbers() = %v", i, err)
		}
		if got[i] != repo.numbers {
			t.Errorf("caller %d read %+v, want %+v", i, got[i], repo.numbers)
		}
	}
	// Fewer reads than callers, not exactly one: what collapsing buys is that
	// a hall of visitors opening the page together does not become a hall's
	// worth of queries. Exactly one is a stronger promise than singleflight
	// makes — a caller arriving in the gap between the first flight finishing
	// and its answer reaching the cache starts a second flight, honestly and
	// rarely — and a test that asserts it fails a few times in twenty under
	// `-race`, which teaches the next reader to rerun tests rather than to
	// believe them.
	if reads := repo.numberReads.Load(); reads >= int64(len(got)) {
		t.Errorf("the repository was read %d times for %d callers: the reads are not being shared at all",
			reads, len(got))
	}
}

// The cache is a minute, not forever: the numbers move as an olympiad runs.
func TestAReadPastTheCacheAsksAgain(t *testing.T) {
	repo := &countingRepo{numbers: showcase.Numbers{Queries: 1}}
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	service := newService(repo, &now)

	if _, err := service.Numbers(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(showcase.CacheTTL - time.Second)
	if _, err := service.Numbers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reads := repo.numberReads.Load(); reads != 1 {
		t.Fatalf("the repository was read %d times inside the cache window, want once", reads)
	}

	now = now.Add(2 * time.Second)
	repo.numbers = showcase.Numbers{Queries: 2}
	fresh, err := service.Numbers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Queries != 2 {
		t.Errorf("queries = %d, want the fresh 2 once the minute is up", fresh.Queries)
	}
}

// An entry expires a minute after the read was decided on, not a minute after
// it came back.
//
// Two things follow, and the second is why it matters. A read that took a
// while is never presented as fresher than it is; and the entries of two
// overlapping reads can be ordered by when each was asked for, which is what
// keeps a waiter whose read started first from overwriting the newer answer
// another caller has already cached and stamping it with a whole fresh
// minute.
func TestACachedAnswerExpiresFromWhenItWasAskedFor(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	repo := &countingRepo{numbers: showcase.Numbers{Queries: 1}}
	// The read itself takes half of the cache's own minute.
	repo.during = func() { now = now.Add(showcase.CacheTTL / 2) }
	service := newService(repo, &now)

	if _, err := service.Numbers(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A minute and a second after the read was asked for, however long it
	// took to answer.
	now = now.Add(showcase.CacheTTL/2 + time.Second)
	if _, err := service.Numbers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reads := repo.numberReads.Load(); reads != 2 {
		t.Errorf("the repository was read %d times, want a second read once the entry's own minute is up", reads)
	}
}

// The page drops its whole row of numbers when this read fails, so a database
// hiccup must not cost a visitor the numbers the last read already got.
func TestAFailedRefreshServesTheAnswerAlreadyRead(t *testing.T) {
	repo := &countingRepo{numbers: showcase.Numbers{Queries: 900}}
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	service := newService(repo, &now)

	if _, err := service.Numbers(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * showcase.CacheTTL)
	repo.fail = errors.New("the database is unreachable")

	got, err := service.Numbers(t.Context())
	if err != nil {
		t.Fatalf("Numbers() = %v, want the previous answer", err)
	}
	if got.Queries != 900 {
		t.Errorf("queries = %d, want the 900 of the last successful read", got.Queries)
	}
}

// Stale has a ceiling. Serving the last answer through a hiccup is one thing;
// serving it through an afternoon of outage is telling visitors an olympiad
// is running when it finished hours ago, with nothing on the page saying the
// figures are old.
func TestStaleIsServedOnlyUntilItIsTooOld(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	repo := &countingRepo{
		numbers:  showcase.Numbers{Queries: 900},
		contests: []showcase.Contest{{ID: uuid.New(), Status: contests.StatusFinished}},
	}
	service := newService(repo, &now)

	if _, err := service.Numbers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Recent(t.Context(), "en"); err != nil {
		t.Fatal(err)
	}
	broken := errors.New("the database is unreachable")
	repo.fail = broken

	// The last moment the answer is still worth serving.
	now = now.Add(showcase.CacheTTL + showcase.MaxStale - time.Second)
	if got, err := service.Numbers(t.Context()); err != nil || got.Queries != 900 {
		t.Errorf("Numbers() = %+v, %v; want the previous answer inside the ceiling", got, err)
	}
	if got, err := service.Recent(t.Context(), "en"); err != nil || len(got) != 1 {
		t.Errorf("Recent() = %v, %v; want the previous answer inside the ceiling", got, err)
	}

	now = now.Add(2 * time.Second)
	if _, err := service.Numbers(t.Context()); !errors.Is(err, broken) {
		t.Errorf("Numbers() = %v, want the read's own error past the ceiling", err)
	}
	if _, err := service.Recent(t.Context(), "en"); !errors.Is(err, broken) {
		t.Errorf("Recent() = %v, want the read's own error past the ceiling", err)
	}
}

// A visitor who closed the tab is not a failed read. There is nobody left to
// serve a stale answer to, and nothing here is broken, so the caller's own
// cancellation comes back as itself: the handler can then tell it apart from
// an outage instead of logging one and writing a page to a connection that
// has gone.
func TestACallerWhoGoesAwayGetsTheirOwnCancellation(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	repo := &countingRepo{numbers: showcase.Numbers{Queries: 900}}
	service := newService(repo, &now)

	// A previous answer to hand, so that the stale branch is the one that
	// would otherwise be taken.
	if _, err := service.Numbers(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * showcase.CacheTTL)

	// The read goes away while the repository is still busy, and the
	// repository stays busy until the test is over: the caller must give up on
	// its own context rather than on anything the repository did.
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(t.Context())
	repo.during = func() {
		cancel()
		<-release
	}

	if _, err := service.Numbers(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Numbers() = %v, want the caller's own cancellation", err)
	}
	if _, err := service.Recent(ctx, "en"); !errors.Is(err, context.Canceled) {
		t.Errorf("Recent() = %v, want the caller's own cancellation", err)
	}
}

// With nothing read yet there is nothing to serve, and the caller is told so
// rather than handed four zeros: a showcase with zeros is a broken system.
func TestTheFirstFailureIsRefused(t *testing.T) {
	broken := errors.New("the database is unreachable")
	repo := &countingRepo{fail: broken}
	now := time.Now()

	if _, err := newService(repo, &now).Numbers(t.Context()); !errors.Is(err, broken) {
		t.Errorf("Numbers() = %v, want the read's own error", err)
	}
}

func TestRecentContestsAreTitledInTheVisitorsLanguage(t *testing.T) {
	repo := &countingRepo{contests: []showcase.Contest{{
		ID: uuid.New(), Status: contests.StatusRunning, DefaultLanguage: "ro",
		Titles: map[string]string{"ro": "Olimpiada", "en": "The Olympiad"},
	}}}
	now := time.Now()
	service := newService(repo, &now)

	for _, tc := range []struct{ lang, want string }{
		{"en", "The Olympiad"},
		{"ro", "Olimpiada"},
		// A language the contest does not speak: its own default, never
		// nothing.
		{"de", "Olimpiada"},
	} {
		got, err := service.Recent(t.Context(), tc.lang)
		if err != nil {
			t.Fatalf("Recent(%q) = %v", tc.lang, err)
		}
		if len(got) != 1 || got[0].Title != tc.want {
			t.Errorf("Recent(%q) titled the contest %q, want %q", tc.lang, got[0].Title, tc.want)
		}
	}
	if reads := repo.contestReads.Load(); reads != 1 {
		t.Errorf("the repository was read %d times for three languages, want once", reads)
	}
}

// The selection is stated twice on purpose: the query's clause and this one.
func TestADraftIsRefusedEvenWhenStorageHandsOneOver(t *testing.T) {
	draft := uuid.New()
	repo := &countingRepo{contests: []showcase.Contest{
		{ID: draft, Status: contests.StatusDraft},
		{ID: uuid.New(), Status: contests.StatusFinished},
	}}
	now := time.Now()

	got, err := newService(repo, &now).Recent(t.Context(), "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if c.ID == draft {
			t.Error("a draft contest reached the public page")
		}
	}
	if len(got) != 1 {
		t.Errorf("Recent() returned %d contests, want only the finished one", len(got))
	}
}

func TestTheListIsAskedForAndCutToMaxRecent(t *testing.T) {
	repo := &countingRepo{}
	for range showcase.MaxRecent + 3 {
		repo.contests = append(repo.contests, showcase.Contest{ID: uuid.New(), Status: contests.StatusFinished})
	}
	now := time.Now()

	got, err := newService(repo, &now).Recent(t.Context(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != showcase.MaxRecent {
		t.Errorf("Recent() returned %d contests, want at most %d", len(got), showcase.MaxRecent)
	}
	if limit := repo.limit.Load(); limit != showcase.MaxRecent {
		t.Errorf("the repository was asked for %d contests, want %d", limit, showcase.MaxRecent)
	}
}

// One cached list serves every visitor, so what one of them is handed must
// not be the same memory the next one reads.
func TestOneCallersListIsNotAnothers(t *testing.T) {
	repo := &countingRepo{contests: []showcase.Contest{{
		ID: uuid.New(), Status: contests.StatusFinished, DefaultLanguage: "en",
		Titles: map[string]string{"en": "The Olympiad"},
	}}}
	now := time.Now()
	service := newService(repo, &now)

	first, err := service.Recent(t.Context(), "en")
	if err != nil {
		t.Fatal(err)
	}
	first[0].Title = "rewritten by the first caller"

	second, err := service.Recent(t.Context(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Title != "The Olympiad" {
		t.Errorf("the second caller read %q, want the cached title", second[0].Title)
	}
}

// The slice is one caller's own, and so is every map inside it: a row of the
// answer must share nothing with the entry the next visitor will be handed.
func TestOneCallersTitlesAreNotAnothers(t *testing.T) {
	repo := &countingRepo{contests: []showcase.Contest{{
		ID: uuid.New(), Status: contests.StatusFinished, DefaultLanguage: "en",
		Titles: map[string]string{"en": "The Olympiad"},
	}}}
	now := time.Now()
	service := newService(repo, &now)

	first, err := service.Recent(t.Context(), "en")
	if err != nil {
		t.Fatal(err)
	}
	first[0].Titles["en"] = "rewritten by the first caller"

	second, err := service.Recent(t.Context(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Titles["en"] != "The Olympiad" {
		t.Errorf("the second caller read %q, want the cached title", second[0].Titles["en"])
	}
	if second[0].Title != "The Olympiad" {
		t.Errorf("the second caller was titled %q, want the cached title", second[0].Title)
	}
}

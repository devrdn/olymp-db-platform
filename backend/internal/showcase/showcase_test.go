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

	numberReads  atomic.Int64
	contestReads atomic.Int64
	// limit is the limit the last Recent was asked for.
	limit atomic.Int64
}

func (r *countingRepo) Numbers(context.Context) (showcase.Numbers, error) {
	r.numberReads.Add(1)
	if r.fail != nil {
		return showcase.Numbers{}, r.fail
	}
	return r.numbers, nil
}

func (r *countingRepo) Recent(_ context.Context, limit int) ([]showcase.Contest, error) {
	r.contestReads.Add(1)
	r.limit.Store(int64(limit))
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

func TestNumbersAreReadOnceForManyCallers(t *testing.T) {
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
	if reads := repo.numberReads.Load(); reads != 1 {
		t.Errorf("the repository was read %d times, want once for all ten callers", reads)
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

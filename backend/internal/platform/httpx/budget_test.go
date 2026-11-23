package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/httpx"
)

var codeTestTooOften = httpx.NewCode("test_budget_too_often", "A budget declared by the httpx tests was spent.")

// memoryCounter counts as auth.Limiter does, on the in-process cache.
type memoryCounter struct{ cache *cache.Memory }

func (m memoryCounter) Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error) {
	count, err := m.cache.Incr(ctx, subject, window)
	if err != nil {
		return false, err
	}
	return count <= int64(limit), nil
}

// count is how many attempts subject had made in its window. It reads the
// count by making one more attempt and not counting it, so read each subject
// once, at the end.
func (m memoryCounter) count(t *testing.T, subject string) int {
	t.Helper()
	count, err := m.cache.Incr(t.Context(), subject, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return int(count) - 1
}

func newBudgets(t *testing.T) (httpx.Budgets, memoryCounter) {
	t.Helper()
	counter := memoryCounter{cache: cache.NewMemory(1000)}
	return httpx.NewBudgets(counter), counter
}

func from(remoteAddr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr
	return r
}

func TestAKeyBudgetAdmitsItsLimitPerKeyAndRefusesPastIt(t *testing.T) {
	budgets, _ := newBudgets(t)
	reads := httpx.PerKey("reads:user", 3, time.Minute, codeTestTooOften, "Too many reads")

	for i := range 3 {
		if err := budgets.Charge(t.Context(), reads.Of("ana")); err != nil {
			t.Fatalf("charge %d: %v", i+1, err)
		}
	}
	if err := budgets.Charge(t.Context(), reads.Of("ana")); !errors.Is(err, httpx.ErrBudgetSpent) {
		t.Fatalf("past the limit = %v, want ErrBudgetSpent", err)
	}
	if err := budgets.Charge(t.Context(), reads.Of("bob")); err != nil {
		t.Errorf("another key = %v, want its own budget", err)
	}
}

func TestARefusalSaysWhichCodeToAnswerAndToWaitOutTheWindow(t *testing.T) {
	budgets, _ := newBudgets(t)
	uploads := httpx.PerKey("uploads:contest", 1, 10*time.Minute, codeTestTooOften, "Too many uploads for this contest")
	_ = budgets.Charge(t.Context(), uploads.Of("c"))

	err := budgets.Charge(t.Context(), uploads.Of("c"))
	var refusal *httpx.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Charge() = %v, want a *Refusal", err)
	}
	if refusal.Code() != codeTestTooOften || refusal.Message() != "Too many uploads for this contest" ||
		refusal.RetryAfter() != 10*time.Minute {
		t.Errorf("refusal = %s %q after %s, want its declaration's code and message after its window",
			refusal.Code(), refusal.Message(), refusal.RetryAfter())
	}
}

func TestAnAddressBudgetCountsAnIPv6NetworkAsOneCaller(t *testing.T) {
	budgets, _ := newBudgets(t)
	reads := httpx.PerAddress("reads:ip", 2, time.Minute, codeTestTooOften, "Too many reads")

	_ = budgets.Charge(t.Context(), reads.Of(from("[2001:db8:1:2::1]:5000")))
	_ = budgets.Charge(t.Context(), reads.Of(from("[2001:db8:1:2::2]:5000")))
	if err := budgets.Charge(t.Context(), reads.Of(from("[2001:db8:1:2::3]:5000"))); !errors.Is(err, httpx.ErrBudgetSpent) {
		t.Errorf("a third host of the same /64 = %v, want ErrBudgetSpent", err)
	}
	if err := budgets.Charge(t.Context(), reads.Of(from("203.0.113.7:5000"))); err != nil {
		t.Errorf("another address = %v, want its own budget", err)
	}
}

// CLAUDE.md rule 5, whatever order the handler names them in: the address,
// then what the service resolved, then what the caller typed. A refusal stops
// the call, so a refused caller makes no counter for a key it invented.
func TestBudgetsAreChargedBoundedFirstAndStopAtTheFirstRefusal(t *testing.T) {
	budgets, counter := newBudgets(t)
	byAddress := httpx.PerAddress("form:ip", 1, time.Minute, codeTestTooOften, "Too many from this address")
	byContest := httpx.PerKey("form:contest", 5, time.Minute, codeTestTooOften, "Too many for this contest")
	byEmail := httpx.PerTypedKey("form:email", 5, time.Minute, codeTestTooOften, "Too many for this email")
	r := from("203.0.113.7:5000")

	if err := budgets.Charge(t.Context(), byEmail.Of("ana@example.edu"), byContest.Of("c"), byAddress.Of(r)); err != nil {
		t.Fatalf("first charge: %v", err)
	}
	err := budgets.Charge(t.Context(), byEmail.Of("bob@example.edu"), byContest.Of("c"), byAddress.Of(r))

	var refusal *httpx.Refusal
	if !errors.As(err, &refusal) || refusal.Message() != "Too many from this address" {
		t.Fatalf("Charge() = %v, want the address budget's refusal", err)
	}
	if got := counter.count(t, "form:contest:c"); got != 1 {
		t.Errorf("the contest was charged %d times, want once: the address refused first", got)
	}
	if got := counter.count(t, "form:email:bob@example.edu"); got != 0 {
		t.Errorf("the refused caller's email has %d attempts, want no counter at all", got)
	}
}

type failingCounter struct{}

var errCacheDown = errors.New("cache down")

func (failingCounter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, errCacheDown
}

// Without the counter, refusing is safe; but it is ours, not the caller's
// budget, so it is not ErrBudgetSpent.
func TestACounterThatCannotBeKeptRefusesAsUnavailable(t *testing.T) {
	budgets := httpx.NewBudgets(failingCounter{})
	reads := httpx.PerKey("reads:user", 3, time.Minute, codeTestTooOften, "Too many reads")

	err := budgets.Charge(t.Context(), reads.Of("ana"))
	if !errors.Is(err, httpx.ErrBudgetUnavailable) || !errors.Is(err, errCacheDown) {
		t.Fatalf("Charge() = %v, want ErrBudgetUnavailable wrapping the cache's error", err)
	}
	if errors.Is(err, httpx.ErrBudgetSpent) {
		t.Error("a counter failure was reported as a spent budget")
	}
}

func TestABudgetThatCouldNeverAdmitAnythingPanics(t *testing.T) {
	for name, declare := range map[string]func(){
		"no limit":  func() { httpx.PerKey("x", 0, time.Minute, codeTestTooOften, "x") },
		"no window": func() { httpx.PerAddress("x", 1, 0, codeTestTooOften, "x") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("declared without a panic")
				}
			}()
			declare()
		})
	}
}

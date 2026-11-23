package mailing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/mailing"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	retryafter "github.com/devrdn/olymp-db-platform/backend/internal/platform/retry"
)

func newBudget(t *testing.T, name string, limits mailing.Limits, c *cache.Memory, logs *bytes.Buffer) *mailing.Budget {
	t.Helper()
	return mailing.NewBudget(name, limits, auth.NewLimiter(c), slog.New(slog.NewTextHandler(logs, nil)))
}

func memoryCache(t *testing.T) *cache.Memory {
	t.Helper()
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestABudgetAdmitsItsMinuteThenRefuses(t *testing.T) {
	var logs bytes.Buffer
	b := newBudget(t, "signup", mailing.Limits{PerMinute: 2, PerDay: 100}, memoryCache(t), &logs)

	for i := range 2 {
		if err := b.Spend(t.Context()); err != nil {
			t.Fatalf("spend %d = %v, want admitted", i, err)
		}
	}
	err := b.Spend(t.Context())
	if !errors.Is(err, mailing.ErrBudgetSpent) {
		t.Fatalf("past the minute = %v, want ErrBudgetSpent", err)
	}
	// The minute's window, not the day's.
	if wait, ok := retryafter.Delay(err); !ok || wait != time.Minute {
		t.Errorf("the refusal says to wait %s (%v), want the minute", wait, ok)
	}
}

func TestABudgetHasADailyCeiling(t *testing.T) {
	var logs bytes.Buffer
	b := newBudget(t, "signup", mailing.Limits{PerMinute: 100, PerDay: 1}, memoryCache(t), &logs)

	if err := b.Spend(t.Context()); err != nil {
		t.Fatalf("the first message was refused: %v", err)
	}
	err := b.Spend(t.Context())
	if !errors.Is(err, mailing.ErrBudgetSpent) {
		t.Fatalf("past the day = %v, want ErrBudgetSpent", err)
	}
	if wait, ok := retryafter.Delay(err); !ok || wait != 24*time.Hour {
		t.Errorf("the refusal says to wait %s (%v), want the day", wait, ok)
	}
}

// An operator hears that a budget ran out, once a window: a flood past it
// must not flood the log too.
func TestABudgetLogsTheFirstRefusalOfAWindowOnly(t *testing.T) {
	var logs bytes.Buffer
	b := newBudget(t, "recovery", mailing.Limits{PerMinute: 1, PerDay: 100}, memoryCache(t), &logs)

	for range 5 {
		_ = b.Spend(t.Context())
	}

	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("%d warnings, want one for the window:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "budget=recovery") || !strings.Contains(logs.String(), "window=1m0s") {
		t.Errorf("the warning does not name the budget and its window:\n%s", logs.String())
	}
}

// Two kinds of mail never share a counter, so a flood of one leaves the
// other its whole budget.
func TestBudgetsOfTwoKindsCountApart(t *testing.T) {
	var logs bytes.Buffer
	c := memoryCache(t)
	signups := newBudget(t, "signup", mailing.Limits{PerMinute: 1, PerDay: 100}, c, &logs)
	resets := newBudget(t, "recovery", mailing.Limits{PerMinute: 1, PerDay: 100}, c, &logs)

	for range 3 {
		_ = signups.Spend(t.Context())
	}

	if err := resets.Spend(t.Context()); err != nil {
		t.Errorf("recovery after a sign-up flood = %v, want admitted", err)
	}
}

type brokenLimiter struct{}

func (brokenLimiter) Spend(context.Context, string, int, time.Duration) (bool, bool, error) {
	return false, false, errors.New("cache down")
}

func TestABudgetThatCannotCountRefuses(t *testing.T) {
	var logs bytes.Buffer
	b := mailing.NewBudget("signup", mailing.Limits{PerMinute: 1, PerDay: 1}, brokenLimiter{}, slog.New(slog.NewTextHandler(&logs, nil)))

	if err := b.Spend(t.Context()); err == nil || errors.Is(err, mailing.ErrBudgetSpent) {
		t.Errorf("without a counter = %v, want the counter's error, not a spent budget", err)
	}
}

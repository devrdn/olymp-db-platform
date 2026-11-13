package mailing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
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
		if ok, err := b.Spend(t.Context()); !ok || err != nil {
			t.Fatalf("spend %d = %v, %v, want admitted", i, ok, err)
		}
	}
	if ok, err := b.Spend(t.Context()); ok || err != nil {
		t.Errorf("past the minute = %v, %v, want refused", ok, err)
	}
}

func TestABudgetHasADailyCeiling(t *testing.T) {
	var logs bytes.Buffer
	b := newBudget(t, "signup", mailing.Limits{PerMinute: 100, PerDay: 1}, memoryCache(t), &logs)

	if ok, _ := b.Spend(t.Context()); !ok {
		t.Fatal("the first message was refused")
	}
	if ok, err := b.Spend(t.Context()); ok || err != nil {
		t.Errorf("past the day = %v, %v, want refused", ok, err)
	}
}

// An operator hears that a budget ran out, once a window: a flood past it
// must not flood the log too.
func TestABudgetLogsTheFirstRefusalOfAWindowOnly(t *testing.T) {
	var logs bytes.Buffer
	b := newBudget(t, "recovery", mailing.Limits{PerMinute: 1, PerDay: 100}, memoryCache(t), &logs)

	for range 5 {
		_, _ = b.Spend(t.Context())
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
		_, _ = signups.Spend(t.Context())
	}

	if ok, err := resets.Spend(t.Context()); !ok || err != nil {
		t.Errorf("recovery after a sign-up flood = %v, %v, want admitted", ok, err)
	}
}

type brokenLimiter struct{}

func (brokenLimiter) Spend(context.Context, string, int, time.Duration) (bool, bool, error) {
	return false, false, errors.New("cache down")
}

func TestABudgetThatCannotCountRefuses(t *testing.T) {
	var logs bytes.Buffer
	b := mailing.NewBudget("signup", mailing.Limits{PerMinute: 1, PerDay: 1}, brokenLimiter{}, slog.New(slog.NewTextHandler(&logs, nil)))

	if ok, err := b.Spend(t.Context()); ok || err == nil {
		t.Errorf("without a counter = %v, %v, want refused with the error", ok, err)
	}
}

package authtest_test

import (
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/auth/authtest"
)

func TestTheLimiterCountsUntilItsWindowPasses(t *testing.T) {
	var l authtest.Limiter
	for i := range 2 {
		if ok, _, _ := l.Spend(t.Context(), "s", 2, time.Minute); !ok {
			t.Fatalf("attempt %d refused", i)
		}
	}
	if ok, first, _ := l.Spend(t.Context(), "s", 2, time.Minute); ok || !first {
		t.Errorf("third = %v first %v, want the window's first refusal", ok, first)
	}
	_, _, _ = l.Spend(t.Context(), "day", 1, 24*time.Hour)

	l.Pass(time.Minute)

	if ok, _, _ := l.Spend(t.Context(), "s", 2, time.Minute); !ok {
		t.Error("the minute's window outlived Pass")
	}
	if ok, _, _ := l.Spend(t.Context(), "day", 1, 24*time.Hour); ok {
		t.Error("Pass(minute) ended a day's window")
	}
}

package contests_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

func TestContestFilterClampsThePageSize(t *testing.T) {
	got := contests.Filter{Limit: 100000, Offset: -1}.Normalize()

	if got.Limit > 200 {
		t.Errorf("Normalize().Limit = %d, want it clamped", got.Limit)
	}
	if got.Offset != 0 {
		t.Errorf("Normalize().Offset = %d, want 0", got.Offset)
	}
}

func TestContestFilterFillsInADefaultPageSize(t *testing.T) {
	if got := (contests.Filter{}).Normalize(); got.Limit <= 0 {
		t.Errorf("Normalize().Limit = %d, want a positive default", got.Limit)
	}
}

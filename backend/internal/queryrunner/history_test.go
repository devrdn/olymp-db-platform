package queryrunner_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

func TestNormalizeHistoryPageDefaultsAndClamps(t *testing.T) {
	for _, tc := range []struct {
		name           string
		limit, offset  int
		wantL, wantOff int
	}{
		{"nothing asked", 0, 0, queryrunner.DefaultHistoryLimit, 0},
		{"negative limit falls back to the default", -1, 0, queryrunner.DefaultHistoryLimit, 0},
		{"too large a limit is clamped", 100000, 0, queryrunner.MaxHistoryLimit, 0},
		{"a limit within bounds is kept exactly", 10, 0, 10, 0},
		{"a negative offset is clamped to zero", 10, -5, 10, 0},
		{"a positive offset is kept exactly", 10, 40, 10, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotL, gotOff := queryrunner.NormalizeHistoryPage(tc.limit, tc.offset)
			if gotL != tc.wantL || gotOff != tc.wantOff {
				t.Fatalf("NormalizeHistoryPage(%d, %d) = (%d, %d), want (%d, %d)",
					tc.limit, tc.offset, gotL, gotOff, tc.wantL, tc.wantOff)
			}
		})
	}
}

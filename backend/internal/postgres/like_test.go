package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

func TestEscapeLikeNeutralisesPatternMetacharacters(t *testing.T) {
	// A search string is literal text to the person typing it: '%' must not
	// match everyone and a trailing '\' must not make Postgres reject the
	// pattern with a 500.
	cases := map[string]string{
		`plain`:      `plain`,
		`50%`:        `50\%`,
		`under_line`: `under\_line`,
		`trailing\`:  `trailing\\`,
		`\%_`:        `\\\%\_`,
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeLikeDropsWhatNoStoredTextCanContain(t *testing.T) {
	// A NUL byte or bytes that are not UTF-8 cannot be in any stored row, and
	// PostgreSQL refuses them by failing the statement: a search box answered
	// a 500 for one pasted control character.
	cases := map[string]string{
		"ab\x00c":  `abc`,
		"ab\xffc":  `abc`,
		"50%\x00":  `50\%`,
		"\x00\xfe": ``,
		"Лидделл_": `Лидделл\_`,
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestASearchWithAControlCharacterFindsNothingRatherThanFailing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		found, _, err := NewContests(testPool).List(ctx, contests.Filter{Query: "zq-no-such-title\x00", Limit: 10})
		if err != nil || len(found) != 0 {
			t.Errorf("List() = (%d contests, %v), want none and no error", len(found), err)
		}
	})
}

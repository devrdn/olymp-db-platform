package postgres

import "testing"

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

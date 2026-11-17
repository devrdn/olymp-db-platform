package archive

import "testing"

func TestNormalizeBoundsThePage(t *testing.T) {
	cases := []struct {
		in   Filter
		want Filter
	}{
		{Filter{}, Filter{Limit: DefaultPageSize}},
		{Filter{Limit: 500, Offset: -3, Query: "  sql "}, Filter{Limit: MaxPageSize, Query: "sql"}},
		{Filter{Limit: 7, Offset: 14}, Filter{Limit: 7, Offset: 14}},
	}
	for _, c := range cases {
		if got := c.in.Normalize(); got != c.want {
			t.Errorf("Normalize(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestTitleFallsBackToTheDefaultThenToAny(t *testing.T) {
	c := Contest{Titles: map[string]string{"ro": "Olimpiada", "en": "Olympiad"}, DefaultLanguage: "ro",
		Descriptions: map[string]string{"ro": "Descriere"}}
	if got := c.TitleIn("en"); got != "Olympiad" {
		t.Errorf("TitleIn(en) = %q", got)
	}
	if got := c.TitleIn("ru"); got != "Olimpiada" {
		t.Errorf("TitleIn(ru) = %q, want the default's", got)
	}
	if got := c.DescriptionIn("en"); got != "Descriere" {
		t.Errorf("DescriptionIn(en) = %q, want the default's", got)
	}
}

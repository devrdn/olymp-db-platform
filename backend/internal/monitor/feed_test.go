package monitor

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAFeedCursorSurvivesTheRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 123456000, time.UTC)
	for _, c := range []Cursor{
		{At: at, Source: SourceQuery, ID: "42"},
		{At: at, Source: SourceAnswer, ID: uuid.NewString()},
		{At: at, Source: SourceStart, ID: uuid.NewString()},
	} {
		got, err := ParseCursor(c.Encode())
		if err != nil || got.Compare(c) != 0 || !got.At.Equal(c.At) {
			t.Errorf("ParseCursor(Encode(%+v)) = %+v, %v", c, got, err)
		}
	}
}

func TestAForgedCursorIsRefused(t *testing.T) {
	enc := func(s string) string { return Cursor{}.encodeRaw(s) }
	for _, text := range []string{
		"", "!!!", enc("1.q"), enc("x.q.1"), enc("1.z.1"), enc("1.q.nope"), enc("1.n.1"),
		enc("1.n." + "6F9619FF-8B86-D011-B42D-00C04FC964FF"), string(make([]byte, 200)),
	} {
		if _, err := ParseCursor(text); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("ParseCursor(%q) = %v, want ErrInvalidCursor", text, err)
		}
	}
}

func TestCursorsOrderByTimeThenSourceThenID(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	ordered := []Cursor{
		{At: at, Source: SourceAudit, ID: "900"},
		{At: at, Source: SourceEvent, ID: "9"},
		{At: at, Source: SourceEvent, ID: "10"},
		{At: at, Source: SourceQuery, ID: "1"},
		{At: at, Source: SourceAnswer, ID: "00000000-0000-0000-0000-00000000000a"},
		{At: at, Source: SourceAnswer, ID: "f0000000-0000-0000-0000-000000000000"},
		{At: at.Add(time.Microsecond), Source: SourceAudit, ID: "1"},
	}
	for i := range ordered {
		for j := range ordered {
			want := compareInts(int64(i), int64(j))
			if got := ordered[i].Compare(ordered[j]); got != want {
				t.Errorf("Compare(%d, %d) = %d, want %d", i, j, got, want)
			}
		}
	}
}

func TestAFeedQueryRefusesWhatItCannotServe(t *testing.T) {
	c := &Cursor{Source: SourceQuery, ID: "1"}
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	for name, q := range map[string]FeedQuery{
		"both directions": {After: c, Before: c},
		"unknown kind":    {Kinds: []string{"coffee"}},
		"too many kinds":  {Kinds: make([]string, MaxFeedKinds+1)},
		"reversed range":  {From: at, Until: at},
	} {
		if _, err := q.Normalize(); !errors.Is(err, ErrInvalidFeedFilter) {
			t.Errorf("%s: %v, want ErrInvalidFeedFilter", name, err)
		}
	}
	q, err := FeedQuery{Limit: 10_000}.Normalize()
	if err != nil || q.Limit != MaxFeedPage {
		t.Errorf("limit = %d, %v, want %d", q.Limit, err, MaxFeedPage)
	}
	q, _ = FeedQuery{}.Normalize()
	if q.Limit != DefaultFeedPage {
		t.Errorf("default limit = %d", q.Limit)
	}
}

func TestMergeFeedTakesTheFirstItemsPastTheCursorInOrder(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	item := func(sec int, source Source, id int) FeedItem {
		return FeedItem{At: at.Add(time.Duration(sec) * time.Second), Source: source, ID: strconv.Itoa(id)}
	}
	items := []FeedItem{item(3, SourceQuery, 3), item(1, SourceQuery, 1), item(2, SourceEvent, 7),
		item(2, SourceAudit, 8), item(4, SourceEvent, 9)}

	after := item(1, SourceQuery, 1).Cursor()
	page := MergeFeed(FeedQuery{After: &after, Limit: 2}, items)
	if len(page.Items) != 2 || page.Items[0].ID != "8" || page.Items[1].ID != "7" || !page.More {
		t.Errorf("after: %+v", page)
	}

	before := item(4, SourceEvent, 9).Cursor()
	page = MergeFeed(FeedQuery{Before: &before, Limit: 3}, items)
	if len(page.Items) != 3 || page.Items[0].ID != "8" || page.Items[2].ID != "3" || !page.More {
		t.Errorf("before: %+v, want the three newest before it, oldest first", page)
	}

	page = MergeFeed(FeedQuery{Limit: 10}, items)
	if len(page.Items) != 5 || page.More || page.Items[0].ID != "1" || page.Items[4].ID != "9" {
		t.Errorf("newest: %+v", page)
	}
}

package monitor

import (
	"cmp"
	"context"
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
			want := cmp.Compare(i, j)
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

func TestACursorOutsideTheClockIsRefused(t *testing.T) {
	for _, at := range []time.Time{EarliestCursorTime.Add(-time.Microsecond), LatestCursorTime.Add(time.Microsecond)} {
		if _, err := ParseCursor(Cursor{At: at, Source: SourceQuery, ID: "1"}.Encode()); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("cursor at %v: %v, want ErrInvalidCursor", at, err)
		}
	}
	for _, at := range []time.Time{EarliestCursorTime, LatestCursorTime} {
		if _, err := ParseCursor(Cursor{At: at, Source: SourceQuery, ID: "1"}.Encode()); err != nil {
			t.Errorf("cursor at the bound %v: %v", at, err)
		}
	}
}

type pagedSources struct {
	items         map[Source][]FeedItem
	registrations []uuid.UUID
	reads         int
	fail          int // fail the read with this number, when set
	queries       []FeedQuery
	sources       []Source
}

func (p *pagedSources) FeedRegistrations(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return p.registrations, nil
}

func (p *pagedSources) FeedSource(_ context.Context, q FeedQuery, source Source) ([]FeedItem, error) {
	p.reads++
	p.queries = append(p.queries, q)
	p.sources = append(p.sources, source)
	if p.fail != 0 && p.reads == p.fail {
		return nil, errors.New("the database went away")
	}
	var out []FeedItem
	for _, item := range p.items[source] {
		if q.Registration != uuid.Nil && item.Registration != q.Registration {
			continue
		}
		if !q.Until.IsZero() && !item.At.Before(q.Until) {
			continue
		}
		if item.Cursor().Compare(*q.After) > 0 && len(out) <= q.Limit {
			out = append(out, item)
		}
	}
	return out, nil
}

func TestStreamFeedMergesEverySourceOnceInOrder(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	reg := uuid.New()
	sources := &pagedSources{items: map[Source][]FeedItem{}}
	total := 0
	for i := range 1000 {
		source := []Source{SourceQuery, SourceEvent, SourceAnswer}[i%3]
		id := strconv.Itoa(i)
		if source == SourceAnswer {
			id = uuid.NewString()
		}
		// Every third item shares its second with the one before.
		sources.items[source] = append(sources.items[source],
			FeedItem{Source: source, ID: id, At: at.Add(time.Duration(i/3*2+i%2) * time.Second), Registration: reg})
		total++
	}
	var got []FeedItem
	if err := StreamFeed(t.Context(), sources, FeedQuery{Registration: reg}, time.Now(), func(item FeedItem) error {
		got = append(got, item)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Fatalf("streamed %d items, want %d", len(got), total)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Cursor().Compare(got[i].Cursor()) >= 0 {
			t.Fatalf("items %d and %d out of order", i-1, i)
		}
	}
	// Each source's pages once plus one empty read: linear.
	if limit := 3*(1000/3/MaxFeedPage+2) + 3; sources.reads > limit {
		t.Errorf("reads = %d, want at most %d", sources.reads, limit)
	}
}

func TestStreamFeedStopsOnAFailedRead(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	sources := &pagedSources{items: map[Source][]FeedItem{SourceQuery: {{Source: SourceQuery, ID: "1", At: at}}}, fail: 2}
	err := StreamFeed(t.Context(), sources, FeedQuery{}, time.Now(), func(FeedItem) error { return nil })
	if err == nil {
		t.Error("a failed read ended the stream as if it were complete")
	}
}

func TestAContestStreamReadsEachRegistrationsQueriesOnItsOwn(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	a, b := uuid.New(), uuid.New()
	sources := &pagedSources{registrations: []uuid.UUID{a, b}, items: map[Source][]FeedItem{}}
	for i := range 300 {
		reg := []uuid.UUID{a, b}[i%2]
		sources.items[SourceQuery] = append(sources.items[SourceQuery],
			FeedItem{Source: SourceQuery, ID: strconv.Itoa(i + 1), At: at.Add(time.Duration(i) * time.Second), Registration: reg})
	}
	streamed := 0
	if err := StreamFeed(t.Context(), sources, FeedQuery{Contest: uuid.New()}, at.Add(time.Hour), func(FeedItem) error {
		streamed++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if streamed != 300 {
		t.Fatalf("streamed %d, want 300", streamed)
	}
	for i, q := range sources.queries {
		if perRegistration[sources.sources[i]] && (q.Registration == uuid.Nil || q.Limit != streamPage) {
			t.Errorf("source %d was read for the whole contest or by a large page: %+v", sources.sources[i], q)
		}
	}
}

func TestAStreamEndsAtOneInstantForEverySource(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	reg := uuid.New()
	sources := &pagedSources{items: map[Source][]FeedItem{
		SourceQuery: {{Source: SourceQuery, ID: "1", At: now.Add(-FeedSettle - time.Second), Registration: reg},
			{Source: SourceQuery, ID: "2", At: now.Add(-FeedSettle / 2), Registration: reg}},
		SourceEvent: {{Source: SourceEvent, ID: "3", At: now.Add(-time.Millisecond), Registration: reg}},
	}}
	var got []string
	if err := StreamFeed(t.Context(), sources, FeedQuery{Registration: reg}, now, func(item FeedItem) error {
		got = append(got, item.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "1" {
		t.Errorf("streamed %v, want only what is older than the settle window", got)
	}
	for _, q := range sources.queries {
		if !q.Until.Equal(now.Add(-FeedSettle)) {
			t.Errorf("a source was read up to %v, want %v", q.Until, now.Add(-FeedSettle))
		}
	}
}

func TestAShortPageEndsAStreamWithoutAnotherRead(t *testing.T) {
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	reg := uuid.New()
	sources := &pagedSources{items: map[Source][]FeedItem{}}
	for i := range MaxFeedPage + 5 { // one full page and a short one
		sources.items[SourceEvent] = append(sources.items[SourceEvent],
			FeedItem{Source: SourceEvent, ID: strconv.Itoa(i + 1), At: at.Add(time.Duration(i) * time.Second), Registration: reg})
	}
	if err := StreamFeed(t.Context(), sources, FeedQuery{Registration: reg}, at.Add(time.Hour),
		func(FeedItem) error { return nil }); err != nil {
		t.Fatal(err)
	}
	reads := map[Source]int{}
	for _, source := range sources.sources {
		reads[source]++
	}
	if reads[SourceEvent] != 2 {
		t.Errorf("event source read %d times, want 2 (a full page, then a short one)", reads[SourceEvent])
	}
	for source, n := range reads {
		if source != SourceEvent && n != 1 {
			t.Errorf("empty source %d read %d times, want once", source, n)
		}
	}
}

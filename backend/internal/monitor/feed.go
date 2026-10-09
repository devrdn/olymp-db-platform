package monitor

import (
	"cmp"
	"container/heap"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The feed is everything a contest's participants did, in time order. Each
// journal is read as its own index range past a cursor and merged here;
// nothing is copied into a feed table.

// Source is the journal a feed item was read from. It breaks ties after the
// time and before the id, so a cursor names exactly one position.
type Source int

const (
	// SourceAudit: sign-ins, sign-outs, failed sign-ins and
	// disqualifications from audit_log (bigint id).
	SourceAudit Source = iota
	// SourceStart: registrations.started_at (registration id).
	SourceStart
	// SourceEvent: participant_events (bigint id).
	SourceEvent
	// SourceQuery: query_log (bigint id).
	SourceQuery
	// SourceAnswer: submissions (uuid).
	SourceAnswer
	// SourceFinish: registrations.finished_at (registration id).
	SourceFinish
	sourceCount
)

var sourceCodes = [sourceCount]string{"a", "s", "e", "q", "n", "f"}

func (s Source) numericID() bool {
	return s == SourceAudit || s == SourceEvent || s == SourceQuery
}

// The kinds of feed item beyond the participant events' own (Kind).
const (
	FeedSignIn       = "sign_in"
	FeedSignOut      = "sign_out"
	FeedSignInFailed = "sign_in_failed"
	FeedDisqualified = "disqualified"
	FeedStarted      = "started"
	FeedFinished     = "finished"
	FeedKindQuery    = "query"
	FeedKindAnswer   = "answer"
)

var feedKinds = map[string]Source{
	FeedSignIn: SourceAudit, FeedSignOut: SourceAudit, FeedSignInFailed: SourceAudit, FeedDisqualified: SourceAudit,
	FeedStarted: SourceStart, FeedFinished: SourceFinish,
	FeedKindQuery: SourceQuery, FeedKindAnswer: SourceAnswer,
	string(KindPageLeft): SourceEvent, string(KindPaste): SourceEvent,
	string(KindIPChanged): SourceEvent, string(KindParallelSession): SourceEvent,
	string(KindTabCreated): SourceEvent, string(KindTabRenamed): SourceEvent, string(KindTabDeleted): SourceEvent,
}

// The bounds of a feed page (CLAUDE.md rule 2).
const (
	MaxFeedPage     = 200
	DefaultFeedPage = 100
	MaxFeedKinds    = 16
	maxCursorLength = 128
)

// FeedSettle is how much of the newest time a forward read leaves for the
// next one. Rows are stamped with their transaction's start time but commit
// out of order, so a cursor moved past a row's time could miss it forever.
// Two seconds is far past how long a journalling transaction stays open.
const FeedSettle = 2 * time.Second

// SignInGrace is how far around a contest a participant's sign-ins still
// belong to its feed: from this long before the contest's start (or the
// participant's own, without one), never before their registration, until
// this long after the contest's end or their own earlier finish.
const SignInGrace = time.Hour

var (
	ErrInvalidCursor = errors.New("the feed cursor is not valid")
	// ErrExportTooWide: more registrations than a contest-wide stream reads
	// (MaxRosterRows).
	ErrExportTooWide = fmt.Errorf("a contest of more than %d registrations is too large to export whole", MaxRosterRows)
	// ErrInvalidFeedFilter: an unknown kind, too many kinds, a time range
	// that ends before it starts, or both directions at once.
	ErrInvalidFeedFilter = errors.New("the feed filter is not valid")
)

// The range of times a cursor may name, so a client-sent time PostgreSQL
// cannot hold is refused rather than a 500.
var (
	EarliestCursorTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	LatestCursorTime   = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

type Cursor struct {
	At     time.Time
	Source Source
	ID     string
}

func (c Cursor) Encode() string {
	raw := strconv.FormatInt(c.At.UnixMicro(), 10) + "." + sourceCodes[c.Source] + "." + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func ParseCursor(text string) (Cursor, error) {
	if text == "" || len(text) > maxCursorLength {
		return Cursor{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), ".", 3)
	if len(parts) != 3 {
		return Cursor{}, ErrInvalidCursor
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || micros < EarliestCursorTime.UnixMicro() || micros > LatestCursorTime.UnixMicro() {
		return Cursor{}, ErrInvalidCursor
	}
	source := slices.Index(sourceCodes[:], parts[1])
	if source < 0 {
		return Cursor{}, ErrInvalidCursor
	}
	c := Cursor{At: time.UnixMicro(micros).UTC(), Source: Source(source), ID: parts[2]}
	if c.Source.numericID() {
		if _, err := strconv.ParseInt(c.ID, 10, 64); err != nil {
			return Cursor{}, ErrInvalidCursor
		}
	} else if id, err := uuid.Parse(c.ID); err != nil || id.String() != c.ID {
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}

// Compare orders two cursors as the feed does: by time to the microsecond
// (what the database keeps), then source, then id. Uuids compare by their
// canonical text, which orders as PostgreSQL compares uuid.
func (c Cursor) Compare(other Cursor) int {
	if a, b := c.At.UnixMicro(), other.At.UnixMicro(); a != b {
		return cmp.Compare(a, b)
	}
	if c.Source != other.Source {
		return cmp.Compare(c.Source, other.Source)
	}
	if c.Source.numericID() {
		a, _ := strconv.ParseInt(c.ID, 10, 64)
		b, _ := strconv.ParseInt(other.ID, 10, 64)
		return cmp.Compare(a, b)
	}
	return strings.Compare(c.ID, other.ID)
}

type FeedQuery struct {
	Contest uuid.UUID
	// Registration uuid.Nil is the whole contest.
	Registration uuid.UUID
	// Kinds empty is every kind.
	Kinds []string
	// From and Until bound the time, [From, Until); zero is unbounded.
	From  time.Time
	Until time.Time
	// After reads forward from a position, oldest first (live polling);
	// Before reads back, newest first. Neither is the newest page.
	After  *Cursor
	Before *Cursor
	// Limit is 1..MaxFeedPage; zero is DefaultFeedPage.
	Limit int
}

func (q FeedQuery) Ascending() bool { return q.After != nil }

func (q FeedQuery) Normalize() (FeedQuery, error) {
	if q.After != nil && q.Before != nil {
		return q, fmt.Errorf("%w: after and before together", ErrInvalidFeedFilter)
	}
	if len(q.Kinds) > MaxFeedKinds {
		return q, fmt.Errorf("%w: at most %d kinds", ErrInvalidFeedFilter, MaxFeedKinds)
	}
	for _, kind := range q.Kinds {
		if _, ok := feedKinds[kind]; !ok {
			return q, fmt.Errorf("%w: unknown kind %q", ErrInvalidFeedFilter, kind)
		}
	}
	if !q.From.IsZero() && !q.Until.IsZero() && !q.From.Before(q.Until) {
		return q, fmt.Errorf("%w: the range ends before it starts", ErrInvalidFeedFilter)
	}
	switch {
	case q.Limit <= 0:
		q.Limit = DefaultFeedPage
	case q.Limit > MaxFeedPage:
		q.Limit = MaxFeedPage
	}
	return q, nil
}

func (q FeedQuery) Reads(source Source) bool {
	if len(q.Kinds) == 0 {
		return true
	}
	for _, kind := range q.Kinds {
		if feedKinds[kind] == source {
			return true
		}
	}
	return false
}

// KindsOf lists the filter's kinds read from source; nil means every kind.
func (q FeedQuery) KindsOf(source Source) []string {
	var kinds []string
	for _, kind := range q.Kinds {
		if feedKinds[kind] == source {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

type FeedItem struct {
	Source       Source
	ID           string
	At           time.Time
	Kind         string
	Registration uuid.UUID
	Login        string
	FullName     string
	// Data is what the kind carries: QueryData, AnswerData, AuditData, the
	// event's stored payload (json.RawMessage), or nil.
	Data any
}

func (i FeedItem) Cursor() Cursor { return Cursor{At: i.At, Source: i.Source, ID: i.ID} }

type QueryData struct {
	ID           int64  `json:"id"`
	SQL          string `json:"sql"`
	SQLTruncated bool   `json:"sql_truncated"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	DurationMs   *int   `json:"duration_ms"`
	RowCount     *int   `json:"row_count"`
	IP           string `json:"ip,omitempty"`
}

type AnswerData struct {
	ID            uuid.UUID `json:"id"`
	QuestionID    uuid.UUID `json:"question_id"`
	QuestionOrd   int       `json:"question_ord"`
	AttemptNo     int       `json:"attempt_no"`
	Value         string    `json:"value"`
	Correct       bool      `json:"correct"`
	PointsAwarded int       `json:"points_awarded"`
}

type AuditData struct {
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// FeedPage is one page of the feed, oldest first whichever way it was read.
type FeedPage struct {
	Items []FeedItem
	// More says there are more items in the direction the page was read.
	More bool
}

// MergeFeed merges what each source returned for q into one page. Each
// source read at most q.Limit+1 rows past the cursor, so the first q.Limit
// merged items are exactly the feed's and one more means there are more.
// Items at or behind the cursor are dropped, so a boundary never repeats or
// skips an item.
func MergeFeed(q FeedQuery, items []FeedItem) FeedPage {
	kept := items[:0:0]
	for _, item := range items {
		switch {
		case q.After != nil && item.Cursor().Compare(*q.After) <= 0:
		case q.Before != nil && item.Cursor().Compare(*q.Before) >= 0:
		default:
			kept = append(kept, item)
		}
	}
	ascending := q.Ascending()
	slices.SortFunc(kept, func(a, b FeedItem) int {
		if ascending {
			return a.Cursor().Compare(b.Cursor())
		}
		return b.Cursor().Compare(a.Cursor())
	})
	page := FeedPage{Items: kept}
	if len(kept) > q.Limit {
		page.Items, page.More = kept[:q.Limit], true
	}
	if !ascending {
		slices.Reverse(page.Items)
	}
	return page
}

// encodeRaw encodes arbitrary cursor text, for tests.
func (Cursor) encodeRaw(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// FeedSourceReader reads one source forwards (after q.After, oldest first, at
// most q.Limit+1 items) and lists a contest's registrations for a
// contest-wide stream.
type FeedSourceReader interface {
	FeedSource(ctx context.Context, q FeedQuery, source Source) ([]FeedItem, error)
	FeedRegistrations(ctx context.Context, contest uuid.UUID) ([]uuid.UUID, error)
}

// streamPage is small because a contest-wide stream holds one page per
// registration source at once.
const streamPage = 50

// perRegistration are the sources a contest-wide stream reads one
// registration at a time, since they are indexed per registration.
var perRegistration = map[Source]bool{SourceAudit: true, SourceQuery: true, SourceAnswer: true}

// StreamFeed hands every item after q.After (or from the start) to yield,
// oldest first, until the feed ends or yield refuses.
//
// It is a k-way merge over streams read a page at a time, so every row is
// read once. Memory is one page per stream: at most registrations ×
// streamPage × 3 items. The stream ends at now − FeedSettle, fixed at the
// start, so nothing still settling is exported.
func StreamFeed(ctx context.Context, r FeedSourceReader, q FeedQuery, now time.Time, yield func(FeedItem) error) error {
	q, err := q.Normalize()
	if err != nil {
		return err
	}
	start := Cursor{At: EarliestCursorTime, Source: SourceAudit, ID: "0"}
	if q.After != nil {
		start = *q.After
	}
	if end := now.Add(-FeedSettle); q.Until.IsZero() || end.Before(q.Until) {
		q.Until = end
	}
	q.Before, q.Limit = nil, MaxFeedPage

	var registrations []uuid.UUID
	if q.Registration == uuid.Nil {
		found, err := r.FeedRegistrations(ctx, q.Contest)
		if err != nil {
			return err
		}
		registrations = found
	}
	var streams []*stream
	for source := range sourceCount {
		if !q.Reads(source) {
			continue
		}
		if q.Registration != uuid.Nil || !perRegistration[source] {
			streams = append(streams, &stream{source: source, query: q, cursor: start})
			continue
		}
		for _, registration := range registrations {
			one := q
			one.Registration, one.Limit = registration, streamPage
			streams = append(streams, &stream{source: source, query: one, cursor: start})
		}
	}
	fill := func(s *stream) error {
		if len(s.buf) > 0 || s.done {
			return nil
		}
		page := s.query
		page.After = &s.cursor
		items, err := r.FeedSource(ctx, page, s.source)
		if err != nil {
			return err
		}
		// A short page is the source's last, since q.Until is fixed.
		last := len(items) <= page.Limit
		kept := items[:0]
		for _, item := range items {
			if item.Cursor().Compare(s.cursor) > 0 {
				kept = append(kept, item)
			}
		}
		slices.SortFunc(kept, func(a, b FeedItem) int { return a.Cursor().Compare(b.Cursor()) })
		s.buf, s.done = kept, last || len(kept) == 0
		return nil
	}
	ready := &streamHeap{}
	for _, s := range streams {
		if err := fill(s); err != nil {
			return err
		}
		if len(s.buf) > 0 {
			ready.items = append(ready.items, s)
		}
	}
	ready.head = func(s *stream) Cursor { return s.buf[0].Cursor() }
	heap.Init(ready)
	for ready.Len() > 0 {
		next := ready.items[0]
		item := next.buf[0]
		next.buf, next.cursor = next.buf[1:], item.Cursor()
		if err := yield(item); err != nil {
			return err
		}
		if err := fill(next); err != nil {
			return err
		}
		if len(next.buf) > 0 {
			heap.Fix(ready, 0)
		} else {
			heap.Pop(ready)
		}
	}
	return nil
}

type stream struct {
	source Source
	query  FeedQuery
	cursor Cursor
	buf    []FeedItem
	done   bool
}

// streamHeap orders streams by their head item, so picking the next among
// thousands is logarithmic.
type streamHeap struct {
	items []*stream
	head  func(*stream) Cursor
}

func (h *streamHeap) Len() int           { return len(h.items) }
func (h *streamHeap) Less(i, j int) bool { return h.head(h.items[i]).Compare(h.head(h.items[j])) < 0 }
func (h *streamHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *streamHeap) Push(x any)         { h.items = append(h.items, x.(*stream)) }
func (h *streamHeap) Pop() any {
	last := h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	return last
}

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

// The feed (design §4): everything a contest's participants did, merged from
// every journal that records it, in time order. Nothing is copied into a
// feed table; each source is read as its own index range past a cursor and
// the pieces are merged here.

// Source is the journal a feed item was read from. Its order is the tiebreak
// between items of the same instant, after the time and before the id, so a
// cursor names one position in the merged order and never two.
type Source int

const (
	// SourceAudit: sign-ins, sign-outs, failed sign-ins and
	// disqualifications, from audit_log. Id: the trail's bigint id.
	SourceAudit Source = iota
	// SourceStart: a participant's clock starting (registrations.started_at).
	// Id: the registration.
	SourceStart
	// SourceEvent: participant_events. Id: the event's bigint id.
	SourceEvent
	// SourceQuery: query_log. Id: the row's bigint id.
	SourceQuery
	// SourceAnswer: submissions. Id: the submission's uuid.
	SourceAnswer
	// SourceFinish: a participant finishing (registrations.finished_at). Id:
	// the registration.
	SourceFinish
	sourceCount
)

// sourceCodes spell the sources inside a cursor.
var sourceCodes = [sourceCount]string{"a", "s", "e", "q", "n", "f"}

// numericID reports whether the source's ids are bigints rather than uuids.
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

// feedKinds maps every kind a filter may name to the source it is read from.
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
	// MaxFeedPage is the most items one page carries (design §4).
	MaxFeedPage = 200
	// DefaultFeedPage is the page size when the caller names none.
	DefaultFeedPage = 100
	// MaxFeedKinds bounds the kind filter: there are no more kinds than this.
	MaxFeedKinds = 16
	// maxCursorLength bounds what ParseCursor will even try to decode.
	maxCursorLength = 128
)

// FeedSettle is how much of the newest time a forward read (FeedQuery.After)
// leaves for the next one. Every journal stamps its rows with the time their
// transaction started, and transactions do not commit in that order: a query
// journalled at 10:00:00.9 can become visible after an event stamped
// 10:00:01.0 was already delivered, and a poll that had moved its cursor past
// 10:00:01.0 would never see it. Two seconds is far past how long a
// journalling transaction stays open, and costs the live screen two seconds
// of delay.
const FeedSettle = 2 * time.Second

// SignInGrace is how long past the contest's end — or past the participant's
// own finish, when they finished first — their sign-ins are still the
// contest's business: signing back in to read a result belongs to it, the
// next week's lecture does not. It bounds them from below the same way:
// from this long before the contest's start (or, when the contest has none,
// the participant's own), and never before their registration — signing in to
// check the machine works belongs to the contest, the weeks between an
// early enrolment and the day do not.
const SignInGrace = time.Hour

// The feed's refusals (CLAUDE.md rule 1).
var (
	// ErrInvalidCursor: a cursor this service did not issue.
	ErrInvalidCursor = errors.New("the feed cursor is not valid")
	// ErrExportTooWide: a contest with more registrations than a
	// contest-wide stream reads one at a time (MaxRosterRows).
	ErrExportTooWide = fmt.Errorf("a contest of more than %d registrations is too large to export whole", MaxRosterRows)
	// ErrInvalidFeedFilter: an unknown kind, too many kinds, a time range
	// that ends before it starts, or both directions at once.
	ErrInvalidFeedFilter = errors.New("the feed filter is not valid")
)

// The range of times a cursor may name. A cursor is the client's to send
// back, and a time PostgreSQL cannot hold (or one pgx wraps on the way) would
// otherwise reach the database as a 500 rather than a refusal. Nothing the
// feed shows is older than the first bound or newer than the second.
var (
	EarliestCursorTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	LatestCursorTime   = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Cursor is one position in the merged feed: the time, the source and the
// id of an item.
type Cursor struct {
	At     time.Time
	Source Source
	ID     string
}

// Encode spells the cursor opaquely, for a client to hand back.
func (c Cursor) Encode() string {
	raw := strconv.FormatInt(c.At.UnixMicro(), 10) + "." + sourceCodes[c.Source] + "." + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseCursor reads a cursor Encode produced, or refuses it with
// ErrInvalidCursor.
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
// (what the database keeps), then source, then id — numerically for bigint
// ids, and for uuids by their canonical text, which orders as their bytes do
// and as PostgreSQL compares uuid.
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

// FeedQuery asks for one page of the feed.
type FeedQuery struct {
	Contest uuid.UUID
	// Registration narrows the feed to one participant; uuid.Nil is the
	// whole contest.
	Registration uuid.UUID
	// Kinds narrows the feed to these kinds; empty is every kind.
	Kinds []string
	// From and Until bound the time, [From, Until); zero is unbounded.
	From  time.Time
	Until time.Time
	// After asks for the items after this position, oldest first — the live
	// feed polling for what is new. Before asks for the items before it,
	// newest first — scrolling back. Neither is the newest page.
	After  *Cursor
	Before *Cursor
	// Limit is the page size, 1..MaxFeedPage; zero is DefaultFeedPage.
	Limit int
}

// Ascending reports whether the page is read forwards in time.
func (q FeedQuery) Ascending() bool { return q.After != nil }

// Normalize checks the query and fills its defaults.
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

// Reads reports whether the page needs the source at all.
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

// KindsOf lists the kinds of the filter that are read from source; nil when
// the filter names none, which is every kind of it.
func (q FeedQuery) KindsOf(source Source) []string {
	var kinds []string
	for _, kind := range q.Kinds {
		if feedKinds[kind] == source {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// FeedItem is one thing a participant did.
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

// Cursor is the item's position in the feed.
func (i FeedItem) Cursor() Cursor { return Cursor{At: i.At, Source: i.Source, ID: i.ID} }

// QueryData is a query in the feed or the queries tab.
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

// AnswerData is an attempt in the feed.
type AnswerData struct {
	ID            uuid.UUID `json:"id"`
	QuestionID    uuid.UUID `json:"question_id"`
	QuestionOrd   int       `json:"question_ord"`
	AttemptNo     int       `json:"attempt_no"`
	Value         string    `json:"value"`
	Correct       bool      `json:"correct"`
	PointsAwarded int       `json:"points_awarded"`
}

// AuditData is a sign-in, a sign-out, a failed sign-in or a
// disqualification.
type AuditData struct {
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	// Reason is why a sign-in failed.
	Reason string `json:"reason,omitempty"`
}

// FeedPage is one page of the feed, oldest item first whichever way it was
// read.
type FeedPage struct {
	Items []FeedItem
	// More says there are more items past the page in the direction it was
	// read: older ones for a Before or newest page, newer ones for After.
	More bool
}

// MergeFeed merges what each source returned for q into one page.
//
// Each source was read as its own range past the cursor, in the page's
// direction, with at most q.Limit+1 rows; the first q.Limit of the merged
// order are therefore exactly the first q.Limit of the whole feed, and one
// more item anywhere says there are more. Items at or behind the cursor are
// dropped rather than trusted not to be there, so a boundary can neither
// repeat an item nor, since every source is read strictly past the same
// position, skip one.
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

// encodeRaw encodes an arbitrary cursor text, for tests of ParseCursor.
func (Cursor) encodeRaw(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// FeedSourceReader reads one source of the feed forwards: the items of that
// source after q.After, oldest first, at most q.Limit+1 of them, named. And
// the registrations of a contest, which a contest-wide stream reads its
// per-registration sources by. Implemented by internal/postgres.Watch.
type FeedSourceReader interface {
	FeedSource(ctx context.Context, q FeedQuery, source Source) ([]FeedItem, error)
	FeedRegistrations(ctx context.Context, contest uuid.UUID) ([]uuid.UUID, error)
}

// streamPage is the page one stream of StreamFeed reads at a time from a
// source of one registration. Small, because a contest-wide stream holds one
// page of each of its registrations' sources at once.
const streamPage = 50

// perRegistration are the sources a contest-wide stream reads one
// registration at a time. They are the ones stored per registration and
// read as a range per registration (query_log, submissions, and the
// participant's own trail in audit_log); read for the whole contest, each
// page would re-read every registration's range up to the page's limit.
var perRegistration = map[Source]bool{SourceAudit: true, SourceQuery: true, SourceAnswer: true}

// StreamFeed hands every item of the feed after q.After (or from the
// beginning) to yield, oldest first, until the feed ends or yield refuses.
//
// A k-way merge: every stream is read forwards from its own position, a page
// at a time in its own index order, and the oldest head among them is handed
// over next, so every row is read once and the cost grows with what is
// exported, never with its square. For the whole contest, the sources stored
// per registration are one stream per registration (perRegistration), each
// its own index range; the others are one stream for the contest. What is
// held in memory is one page per stream: at most registrations ×
// streamPage × 3 items for a contest.
//
// The stream ends at now − FeedSettle, fixed once when it starts, so every
// source ends at the same instant and nothing still settling is exported.
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
		// A page shorter than it could have been is the source's last: the
		// stream's end (q.Until) is fixed, so nothing can join it later.
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
	// The streams with an item in hand, ordered by that item: the next item
	// of the feed is always the top one's head.
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

// stream is one source (of one registration, or of the contest) that
// StreamFeed reads forwards a page at a time.
type stream struct {
	source Source
	query  FeedQuery
	cursor Cursor
	buf    []FeedItem
	done   bool
}

// streamHeap orders streams by the item each has in hand (container/heap):
// picking the next item of a contest-wide export among thousands of streams
// is a logarithm, not a scan of them all.
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

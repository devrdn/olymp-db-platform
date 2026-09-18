package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
)

// The organiser's view of what a contest's participants did (design §4),
// under /contests/{id}/monitor/…: the participants table with its flags, the
// live feed, and per participant the timeline, the queries, the answers with
// the queries that led to them, the workspace with its history, and CSV
// exports.
//
// Every route is Authenticate, then contest.monitor on the contest in the
// URL, then the organiser's own read budget (monitorReadsPerMinute per
// account, refusals counted), in that order: the budget's key is the
// account, one per person, and nobody without the permission creates one. A
// registration named in the URL must be this contest's; another contest's
// and one that does not exist are the same 404, so the routes are not a way
// to learn what exists elsewhere.

// monitorReadsPerMinute is one organiser's budget across every route here.
// A monitoring screen polls the feed and the table every five seconds — 24 a
// minute — and an organiser may keep a few open beside a participant's page;
// 240 leaves room for that and not for a script walking the journals.
const monitorReadsPerMinute = 240

// monitorWindow is the budget's fixed window.
const monitorWindow = time.Minute

// monitorTimeLayout keeps milliseconds: the feed orders events inside one
// second, and a time that says otherwise would make the order look wrong.
const monitorTimeLayout = "2006-01-02T15:04:05.000Z"

// registrationIDParam and revisionIDParam name the URL's parameters.
const (
	registrationIDParam = "registrationID"
	revisionIDParam     = "revisionID"
)

// MonitorReader is the slice of monitor.WatchService these routes need.
type MonitorReader interface {
	Roster(ctx context.Context, contest uuid.UUID) (monitor.Roster, error)
	Participant(ctx context.Context, contest, registration uuid.UUID) (monitor.Participant, error)
	Feed(ctx context.Context, q monitor.FeedQuery) (monitor.FeedPage, error)
	StreamFeed(ctx context.Context, q monitor.FeedQuery, yield func(monitor.FeedItem) error) error
	Queries(ctx context.Context, q monitor.QueriesQuery) (monitor.QueriesPage, error)
	Answers(ctx context.Context, contest, registration uuid.UUID) (monitor.Answers, error)
	Workspace(ctx context.Context, contest, registration uuid.UUID) (monitor.Workspace, error)
	Revision(ctx context.Context, contest, registration uuid.UUID, id int64) (monitor.RevisionBody, error)
	// RecordView and RecordExport write the audit trail of watching
	// (design §7); registration is uuid.Nil for the contest-wide views.
	RecordView(ctx context.Context, viewer, contest, registration uuid.UUID) error
	RecordExport(ctx context.Context, viewer, contest, registration uuid.UUID) error
}

// MonitorLimiter is the slice of auth.Limiter the read budget needs.
type MonitorLimiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// MonitorHandler serves the organiser's monitoring routes.
type MonitorHandler struct {
	watch   MonitorReader
	limiter MonitorLimiter
	mw      *auth.Middleware
	log     *slog.Logger
	// exports keeps one account to one CSV download at a time, for the
	// reason the participant's own export does (inFlightExports).
	exports inFlightExports
	// exportRows and exportBytes bound one CSV download (monitor_export.go).
	exportRows  int
	exportBytes int
}

// NewMonitorHandler returns the handler.
func NewMonitorHandler(watch MonitorReader, limiter MonitorLimiter, mw *auth.Middleware, log *slog.Logger) *MonitorHandler {
	return &MonitorHandler{watch: watch, limiter: limiter, mw: mw, log: log,
		exportRows: maxMonitorExportRows, exportBytes: maxMonitorExportBytes}
}

// WithExportLimits replaces the bounds of one CSV download, in rows and in
// bytes; for tests, which cannot write two hundred thousand rows to see one.
func (h *MonitorHandler) WithExportLimits(rows, bytes int) *MonitorHandler {
	h.exportRows, h.exportBytes = rows, bytes
	return h
}

// Mount registers the routes.
func (h *MonitorHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequireContestPermission(rbac.PermissionContestMonitor), h.budget)
		base := "/contests/{" + contestIDParam + "}/monitor"
		one := base + "/participants/{" + registrationIDParam + "}"
		r.Get(base+"/participants", h.participants)
		r.Get(base+"/feed", h.feed)
		r.Get(one, h.participant)
		r.Get(one+"/timeline", h.timeline)
		r.Get(one+"/queries", h.queries)
		r.Get(one+"/answers", h.answers)
		r.Get(one+"/workspace", h.workspace)
		r.Get(one+"/workspace/revisions/{"+revisionIDParam+"}", h.revision)
		r.Get(base+"/export.csv", h.contestCSV)
		r.Get(one+"/export.csv", h.participantCSV)
	})
}

// budget spends one read of the organiser's budget before the route does
// anything, a refused read included (CLAUDE.md rule 13).
func (h *MonitorHandler) budget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := auth.IdentityFrom(r.Context())
		allowed, err := h.limiter.Allow(r.Context(), "monitor:user:"+identity.UserID.String(), monitorReadsPerMinute, monitorWindow)
		if err != nil {
			h.log.ErrorContext(r.Context(), "could not check the monitoring read budget", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(monitorWindow/time.Second)))
			httpx.Error(w, r, http.StatusTooManyRequests, codeMonitorTooOften,
				"Too many monitoring reads this minute; wait before asking again")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// monitorContest is the contest in the URL; RequireContestPermission already
// refused one that does not parse.
func monitorContest(r *http.Request) uuid.UUID {
	id, _ := uuid.Parse(chi.URLParam(r, contestIDParam))
	return id
}

// registration reads the registration in the URL. One that does not parse is
// the same answer as one that is not this contest's.
func (h *MonitorHandler) registration(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, registrationIDParam))
	if err != nil {
		h.fail(w, r, monitor.ErrParticipantNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// viewed records the organiser's view before the answer is sent (design
// §7), and answers the refusal itself when the trail cannot take it: a view
// the trail does not show is the one thing this must not allow.
func (h *MonitorHandler) viewed(w http.ResponseWriter, r *http.Request, registration uuid.UUID) bool {
	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.watch.RecordView(r.Context(), identity.UserID, monitorContest(r), registration); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

func monitorTime(t time.Time) string { return t.UTC().Format(monitorTimeLayout) }

func monitorOptionalTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := monitorTime(*t)
	return &s
}

type monitorRosterRow struct {
	RegistrationID   uuid.UUID     `json:"registration_id"`
	Login            string        `json:"login"`
	FullName         string        `json:"full_name"`
	Status           string        `json:"status"`
	StartedAt        *string       `json:"started_at"`
	FinishedAt       *string       `json:"finished_at"`
	Queries          int           `json:"queries"`
	QueryErrors      int           `json:"query_errors"`
	QueryRejected    int           `json:"query_rejected"`
	Addresses        int           `json:"addresses"`
	Correct          int           `json:"correct"`
	Wrong            int           `json:"wrong"`
	PageLeft         int           `json:"page_left"`
	AwayMs           int64         `json:"away_ms"`
	Pastes           int           `json:"pastes"`
	IPChanges        int           `json:"ip_changes"`
	ParallelSessions int           `json:"parallel_sessions"`
	LastActivity     *string       `json:"last_activity"`
	Flags            monitor.Flags `json:"flags"`
}

type monitorRosterResponse struct {
	GeneratedAt string             `json:"generated_at"`
	Truncated   bool               `json:"truncated"`
	Rows        []monitorRosterRow `json:"rows"`
}

// participants is the table of every participant with their counters and
// flags, from the three-second cache.
func (h *MonitorHandler) participants(w http.ResponseWriter, r *http.Request) {
	roster, err := h.watch.Roster(r.Context(), monitorContest(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := monitorRosterResponse{GeneratedAt: monitorTime(roster.GeneratedAt), Truncated: roster.Truncated,
		Rows: make([]monitorRosterRow, 0, len(roster.Rows))}
	for _, row := range roster.Rows {
		out.Rows = append(out.Rows, monitorRosterRow{
			RegistrationID: row.Registration, Login: row.Login, FullName: row.FullName, Status: row.Status,
			StartedAt: monitorOptionalTime(row.StartedAt), FinishedAt: monitorOptionalTime(row.FinishedAt),
			Queries: row.Queries, QueryErrors: row.QueryErrors, QueryRejected: row.QueryRejected,
			Addresses: row.Addresses, Correct: row.Correct, Wrong: row.Wrong,
			PageLeft: row.PageLeft, AwayMs: row.AwayMs, Pastes: row.Pastes,
			IPChanges: row.IPChanges, ParallelSessions: row.ParallelSessions,
			LastActivity: monitorOptionalTime(row.LastActivity), Flags: row.Flags(),
		})
	}
	if !h.viewed(w, r, uuid.Nil) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type monitorParticipantResponse struct {
	RegistrationID uuid.UUID `json:"registration_id"`
	Login          string    `json:"login"`
	FullName       string    `json:"full_name"`
	Status         string    `json:"status"`
	StartedAt      *string   `json:"started_at"`
	FinishedAt     *string   `json:"finished_at"`
}

// participant is who one participant is, for the page's heading.
func (h *MonitorHandler) participant(w http.ResponseWriter, r *http.Request) {
	registration, ok := h.registration(w, r)
	if !ok {
		return
	}
	p, err := h.watch.Participant(r.Context(), monitorContest(r), registration)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.viewed(w, r, registration) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, monitorParticipantResponse{
		RegistrationID: p.Registration, Login: p.Login, FullName: p.FullName, Status: p.Status,
		StartedAt: monitorOptionalTime(p.StartedAt), FinishedAt: monitorOptionalTime(p.FinishedAt),
	})
}

type monitorFeedItem struct {
	Cursor         string    `json:"cursor"`
	At             string    `json:"at"`
	Kind           string    `json:"kind"`
	RegistrationID uuid.UUID `json:"registration_id"`
	Login          string    `json:"login"`
	FullName       string    `json:"full_name"`
	Data           any       `json:"data"`
}

type monitorFeedResponse struct {
	Items []monitorFeedItem `json:"items"`
	// More says there are more items past the page in the direction it was
	// read.
	More bool `json:"more"`
	// Newest and Oldest are the cursors to ask after= and before= with next;
	// absent on an empty page.
	Newest string `json:"newest,omitempty"`
	Oldest string `json:"oldest,omitempty"`
}

// feed is the whole contest's feed; ?participant= narrows it to one.
func (h *MonitorHandler) feed(w http.ResponseWriter, r *http.Request) {
	q, ok := h.feedQuery(w, r)
	if !ok {
		return
	}
	if raw := r.URL.Query().Get("participant"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			h.fail(w, r, monitor.ErrParticipantNotFound)
			return
		}
		q.Registration = id
	}
	h.serveFeed(w, r, q)
}

// timeline is one participant's feed.
func (h *MonitorHandler) timeline(w http.ResponseWriter, r *http.Request) {
	registration, ok := h.registration(w, r)
	if !ok {
		return
	}
	q, ok := h.feedQuery(w, r)
	if !ok {
		return
	}
	q.Registration = registration
	h.serveFeed(w, r, q)
}

// feedQuery reads the feed's parameters: after or before (a cursor), kinds
// (comma-separated), from and until (RFC 3339), limit.
func (h *MonitorHandler) feedQuery(w http.ResponseWriter, r *http.Request) (monitor.FeedQuery, bool) {
	params := r.URL.Query()
	q := monitor.FeedQuery{Contest: monitorContest(r), Limit: intParam(r, "limit")}
	for name, target := range map[string]**monitor.Cursor{"after": &q.After, "before": &q.Before} {
		if raw := params.Get(name); raw != "" {
			c, err := monitor.ParseCursor(raw)
			if err != nil {
				h.fail(w, r, err)
				return q, false
			}
			*target = &c
		}
	}
	if raw := params.Get("kinds"); raw != "" {
		// Bounded before it is split, so a long list costs nothing.
		if len(raw) > 512 {
			h.fail(w, r, monitor.ErrInvalidFeedFilter)
			return q, false
		}
		q.Kinds = strings.Split(raw, ",")
	}
	for name, target := range map[string]*time.Time{"from": &q.From, "until": &q.Until} {
		if raw := params.Get(name); raw != "" {
			t, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				h.fail(w, r, monitor.ErrInvalidFeedFilter)
				return q, false
			}
			*target = t
		}
	}
	return q, true
}

func (h *MonitorHandler) serveFeed(w http.ResponseWriter, r *http.Request, q monitor.FeedQuery) {
	page, err := h.watch.Feed(r.Context(), q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := monitorFeedResponse{Items: make([]monitorFeedItem, 0, len(page.Items)), More: page.More}
	for _, item := range page.Items {
		out.Items = append(out.Items, monitorFeedItem{
			Cursor: item.Cursor().Encode(), At: monitorTime(item.At), Kind: item.Kind,
			RegistrationID: item.Registration, Login: item.Login, FullName: item.FullName, Data: feedData(item.Data),
		})
	}
	if n := len(page.Items); n > 0 {
		out.Oldest, out.Newest = out.Items[0].Cursor, out.Items[n-1].Cursor
	}
	if !h.viewed(w, r, q.Registration) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

// feedData is an item's data as the response carries it: an event's stored
// payload as it is, everything else as its own shape, and an empty object
// rather than null.
func feedData(data any) any {
	if data == nil {
		return struct{}{}
	}
	return data
}

type monitorQuery struct {
	Cursor     string `json:"cursor"`
	ExecutedAt string `json:"executed_at"`
	monitor.QueryData
}

type monitorQueriesResponse struct {
	Items []monitorQuery `json:"items"`
	More  bool           `json:"more"`
}

func toMonitorQueries(items []monitor.LoggedQuery) []monitorQuery {
	out := make([]monitorQuery, 0, len(items))
	for _, q := range items {
		out = append(out, monitorQuery{Cursor: q.Cursor().Encode(), ExecutedAt: monitorTime(q.At), QueryData: q.QueryData})
	}
	return out
}

// queries is one participant's queries, whole, newest first: ?status=,
// ?q= (a substring, without case), ?cursor= (the last item's), ?limit=.
func (h *MonitorHandler) queries(w http.ResponseWriter, r *http.Request) {
	registration, ok := h.registration(w, r)
	if !ok {
		return
	}
	params := r.URL.Query()
	q := monitor.QueriesQuery{Contest: monitorContest(r), Registration: registration,
		Status: params.Get("status"), Search: params.Get("q"), Limit: intParam(r, "limit")}
	if raw := params.Get("cursor"); raw != "" {
		c, err := monitor.ParseCursor(raw)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		q.Before = &c
	}
	page, err := h.watch.Queries(r.Context(), q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.viewed(w, r, registration) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, monitorQueriesResponse{Items: toMonitorQueries(page.Items), More: page.More})
}

type monitorAttempt struct {
	monitor.AnswerData
	SubmittedAt string         `json:"submitted_at"`
	Queries     []monitorQuery `json:"queries"`
	MoreQueries int            `json:"more_queries"`
}

type monitorQuestionAttempts struct {
	QuestionID  uuid.UUID        `json:"question_id"`
	QuestionOrd int              `json:"question_ord"`
	Attempts    []monitorAttempt `json:"attempts"`
}

type monitorAnswersResponse struct {
	Questions []monitorQuestionAttempts `json:"questions"`
	Truncated bool                      `json:"truncated"`
}

// answers is every attempt, by question, with the queries that led to each.
func (h *MonitorHandler) answers(w http.ResponseWriter, r *http.Request) {
	registration, ok := h.registration(w, r)
	if !ok {
		return
	}
	answers, err := h.watch.Answers(r.Context(), monitorContest(r), registration)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := monitorAnswersResponse{Truncated: answers.Truncated, Questions: make([]monitorQuestionAttempts, 0, len(answers.Questions))}
	for _, question := range answers.Questions {
		group := monitorQuestionAttempts{QuestionID: question.QuestionID, QuestionOrd: question.QuestionOrd,
			Attempts: make([]monitorAttempt, 0, len(question.Attempts))}
		for _, a := range question.Attempts {
			group.Attempts = append(group.Attempts, monitorAttempt{AnswerData: a.AnswerData,
				SubmittedAt: monitorTime(a.At), Queries: toMonitorQueries(a.Queries), MoreQueries: a.MoreQueries})
		}
		out.Questions = append(out.Questions, group)
	}
	if !h.viewed(w, r, registration) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type monitorRevision struct {
	ID        int64  `json:"id"`
	Document  string `json:"document"`
	Title     string `json:"title"`
	StartedAt string `json:"started_at"`
	UpdatedAt string `json:"updated_at"`
	Size      int    `json:"size"`
}

func toMonitorRevision(r monitor.RevisionInfo) monitorRevision {
	return monitorRevision{ID: r.ID, Document: r.Document, Title: r.Title,
		StartedAt: monitorTime(r.StartedAt), UpdatedAt: monitorTime(r.UpdatedAt), Size: r.Size}
}

type monitorTab struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Position  int       `json:"position"`
	Body      string    `json:"body"`
	UpdatedAt string    `json:"updated_at"`
}

type monitorWorkspaceResponse struct {
	Notes struct {
		Body      string  `json:"body"`
		UpdatedAt *string `json:"updated_at"`
	} `json:"notes"`
	Tabs      []monitorTab      `json:"tabs"`
	Revisions []monitorRevision `json:"revisions"`
	Truncated bool              `json:"truncated"`
}

// workspace is the notes and tabs now and the list of their revisions.
func (h *MonitorHandler) workspace(w http.ResponseWriter, r *http.Request) {
	registration, ok := h.registration(w, r)
	if !ok {
		return
	}
	ws, err := h.watch.Workspace(r.Context(), monitorContest(r), registration)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var out monitorWorkspaceResponse
	out.Notes.Body, out.Notes.UpdatedAt = ws.Notes.Body, monitorOptionalTime(ws.Notes.UpdatedAt)
	out.Truncated = ws.Truncated
	out.Tabs = make([]monitorTab, 0, len(ws.Tabs))
	for _, tab := range ws.Tabs {
		out.Tabs = append(out.Tabs, monitorTab{ID: tab.ID, Title: tab.Title, Position: tab.Position,
			Body: tab.Body, UpdatedAt: monitorTime(tab.UpdatedAt)})
	}
	out.Revisions = make([]monitorRevision, 0, len(ws.Revisions))
	for _, rev := range ws.Revisions {
		out.Revisions = append(out.Revisions, toMonitorRevision(rev))
	}
	if !h.viewed(w, r, registration) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type monitorRevisionBody struct {
	monitorRevision
	Body string `json:"body"`
}

// revision is one revision, whole.
func (h *MonitorHandler) revision(w http.ResponseWriter, r *http.Request) {
	registration, ok := h.registration(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, revisionIDParam), 10, 64)
	if err != nil || id <= 0 {
		h.fail(w, r, monitor.ErrRevisionNotFound)
		return
	}
	rev, err := h.watch.Revision(r.Context(), monitorContest(r), registration, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.viewed(w, r, registration) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, monitorRevisionBody{monitorRevision: toMonitorRevision(rev.RevisionInfo), Body: rev.Body})
}

// fail maps a monitoring refusal to a response (CLAUDE.md rule 1).
func (h *MonitorHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, monitor.ErrParticipantNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeMonitorParticipantNotFound, "No such participant in this contest")
	case errors.Is(err, monitor.ErrRevisionNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeMonitorRevisionNotFound, "No such revision of this participant")
	case errors.Is(err, monitor.ErrInvalidCursor):
		httpx.Error(w, r, http.StatusBadRequest, codeMonitorInvalidCursor, err.Error())
	case errors.Is(err, monitor.ErrInvalidFeedFilter), errors.Is(err, monitor.ErrInvalidQueryFilter):
		httpx.Error(w, r, http.StatusBadRequest, codeMonitorInvalidFilter, err.Error())
	default:
		h.log.ErrorContext(r.Context(), "the monitoring read failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

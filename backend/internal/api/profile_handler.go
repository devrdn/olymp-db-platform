package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/profile"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

// A participant's own account under /me/...:
// the summary, their contests, and for each contest that has ended for them the
// report, queries, answers, notes and log file.
//
// Authentication only: "what did I do" is not a permission anyone grants. The
// registration always comes from the session; no URL names anyone else's.
//
// Every route under {contestId} spends the read budget first (CLAUDE.md rule
// 13), then admits through profile.Service.Open. A missing contest, someone
// else's, and one still running for the caller are the same 404, so these
// routes reveal nothing about what exists.
//
// Nothing here is audited: the trail records access to other people's data,
// and reading one's own would bury it.

// ProfileReadsPerMinute is one account's budget across every route here:
// generous for a person reading pages, tight for a script walking the journals.
const ProfileReadsPerMinute = 120

const profileWindow = time.Minute

// ProfileWatch is the slice of monitor.WatchService these routes need: the same
// reads staff make of a registration, with a different admission and a narrower
// response.
type ProfileWatch interface {
	Queries(ctx context.Context, q monitor.QueriesQuery) (monitor.QueriesPage, error)
	Answers(ctx context.Context, contest, registration uuid.UUID) (monitor.Answers, error)
	Workspace(ctx context.Context, contest, registration uuid.UUID) (monitor.Workspace, error)
}

// ProfileLimiter is the slice of auth.Limiter the read budget needs.
type ProfileLimiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// ProfileHandler serves a participant's own profile.
type ProfileHandler struct {
	profile *profile.Service
	watch   ProfileWatch
	history QueryHistory
	limiter ProfileLimiter
	mw      *auth.Middleware
	log     *slog.Logger
	// defaultLocale answers when neither the request nor the contest decides
	// the language (§6.2).
	defaultLocale string
	// exports keeps one registration to one CSV download at a time, shared with
	// the play screen's route.
	exports *ExportGate
	// exportSlots caps concurrent downloads service-wide, shared with every
	// export route.
	exportSlots *ExportSlots
}

// NewProfileHandler returns the handler.
func NewProfileHandler(service *profile.Service, watch ProfileWatch, history QueryHistory,
	limiter ProfileLimiter, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ProfileHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ProfileHandler{profile: service, watch: watch, history: history, limiter: limiter,
		mw: mw, log: log, defaultLocale: defaultLocale,
		exports: NewExportGate(), exportSlots: NewExportSlots(0)}
}

// WithExports gives this handler the per-registration download gate, the same
// one ParticipantHandler gets, so one gate covers both routes.
func (h *ProfileHandler) WithExports(gate *ExportGate) *ProfileHandler {
	if gate != nil {
		h.exports = gate
	}
	return h
}

// WithExportSlots gives this handler the service-wide download count shared by
// every export route. nil keeps the handler's own.
func (h *ProfileHandler) WithExportSlots(slots *ExportSlots) *ProfileHandler {
	if slots != nil {
		h.exportSlots = slots
	}
	return h
}

// Mount registers the routes.
func (h *ProfileHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.budget)
		r.Get("/me/summary", h.summary)
		r.Get("/me/contests", h.contests)
		one := "/me/contests/{" + contestIDParam + "}"
		r.Get(one+"/report", h.report)
		r.Get(one+"/queries", h.queries)
		r.Get(one+"/answers", h.answers)
		r.Get(one+"/workspace", h.workspace)
		// A separate path rather than ?format=, as for the play screen's
		// download.
		r.Get(one+"/log.csv", h.logCSV)
	})
}

// budget spends one read of the caller's budget before anything else, refusals
// included (CLAUDE.md rule 13). Keyed by account only, so the key space is
// bounded (rule 5).
func (h *ProfileHandler) budget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := auth.IdentityFrom(r.Context())
		allowed, err := h.limiter.Allow(r.Context(), "profile:user:"+identity.UserID.String(),
			ProfileReadsPerMinute, profileWindow)
		if err != nil {
			h.log.ErrorContext(r.Context(), "could not check the profile read budget", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(profileWindow/time.Second)))
			httpx.Error(w, r, http.StatusTooManyRequests, codeProfileTooOften,
				"Too many profile reads this minute; wait before asking again")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// admit resolves the caller's own registration in the contest named in the URL,
// or answers the refusal. An unparseable identifier gets the same 404 as one
// that names nothing.
func (h *ProfileHandler) admit(w http.ResponseWriter, r *http.Request) (profile.Access, bool) {
	identity, _ := auth.IdentityFrom(r.Context())
	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		h.fail(w, r, profile.ErrNotFound)
		return profile.Access{}, false
	}
	access, err := h.profile.Open(r.Context(), contestID, identity.UserID)
	if err != nil {
		h.fail(w, r, err)
		return profile.Access{}, false
	}
	return access, true
}

type profileSummaryResponse struct {
	Contests int `json:"contests"`
	Finished int `json:"finished"`
	Queries  int `json:"queries"`
	Solved   int `json:"solved"`
}

func (h *ProfileHandler) summary(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	summary, err := h.profile.Summary(r.Context(), identity.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, profileSummaryResponse{Contests: summary.Contests,
		Finished: summary.Finished, Queries: summary.Queries, Solved: summary.Solved})
}

// profileOwnNumbers is what the participant scored, in the shape the contest's
// scoring mode gives.
type profileOwnNumbers struct {
	Scoring string `json:"scoring"`
	Points  int    `json:"points"`
	Solved  int    `json:"solved"`
	// Penalty is ICPC's, and absent in every other mode.
	Penalty *int `json:"penalty,omitempty"`
	// State is the table's state (live, frozen, final), so the interface can
	// say why there is no place; PlaceOpen says whether there is a place to
	// read.
	State     string `json:"state"`
	PlaceOpen bool   `json:"place_open"`
}

func toProfileOwnNumbers(result profile.Result) profileOwnNumbers {
	out := profileOwnNumbers{Scoring: result.Scoring, Points: result.Points,
		Solved: result.Solved, State: result.State, PlaceOpen: result.PlaceOpen}
	if result.Scoring == contests.ScoringICPC {
		penalty := result.Penalty
		out.Penalty = &penalty
	}
	return out
}

// profileListResult is a list row's result. It has no place: that would mean
// computing a standings table per contest for an overview.
type profileListResult struct {
	profileOwnNumbers
}

// profileReportResult is the report's result, which carries the place from the
// same cached standings the contest page uses.
//
// Place and Participants are null, not zero, when there is no place: the table
// is not open, or gives this row none (in winner mode, everyone but the
// winner).
type profileReportResult struct {
	profileOwnNumbers
	Place        *int `json:"place"`
	Participants *int `json:"participants"`
	// Winner marks the registration that won a winner-mode contest.
	Winner bool `json:"winner,omitempty"`
	// Truncated says the table was cut at the leaderboard's row bound, so
	// participants counts its rows, not everyone.
	Truncated bool `json:"truncated,omitempty"`
}

func toProfileListResult(result profile.Result) *profileListResult {
	return &profileListResult{profileOwnNumbers: toProfileOwnNumbers(result)}
}

// toProfileReportResult is nil when the table has no row for the registration,
// sending result: null. A zeroed object would show a score of nought under no
// mode. The table is bounded (leaderboard.DefaultMaxRows), so this is every
// participant below the cut in a large contest.
func toProfileReportResult(result *profile.Result) *profileReportResult {
	if result == nil {
		return nil
	}
	out := &profileReportResult{profileOwnNumbers: toProfileOwnNumbers(*result),
		Winner: result.Winner}
	// Only a row the table placed; everyone else's place stays null.
	if result.PlaceOpen && result.Place > 0 {
		place, participants := result.Place, result.Participants
		out.Place, out.Participants, out.Truncated = &place, &participants, result.Truncated
	}
	return out
}

type profileContestResponse struct {
	ContestID          uuid.UUID `json:"contest_id"`
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	StartsAt           string    `json:"starts_at,omitempty"`
	EndsAt             string    `json:"ends_at,omitempty"`
	RegistrationStatus string    `json:"registration_status"`
	// Over says the contest has ended for this caller, so its report can be
	// opened.
	Over bool `json:"over"`
	// Result is absent while the contest is not over for the caller: the
	// profile shows nothing from inside a running contest.
	Result *profileListResult `json:"result,omitempty"`
}

type profileContestsResponse struct {
	Items []profileContestResponse `json:"items"`
	// Truncated says the account has more registrations than one profile
	// carries (profile.MaxContests).
	Truncated bool `json:"truncated"`
}

func (h *ProfileHandler) contests(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	rows, truncated, err := h.profile.Contests(r.Context(), identity.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := profileContestsResponse{Truncated: truncated, Items: make([]profileContestResponse, 0, len(rows))}
	for _, row := range rows {
		c := row.Contest
		lang := negotiateLang(r, c.LanguageCodes(), c.DefaultLanguage(), h.defaultLocale)
		item := profileContestResponse{ContestID: c.ID, Title: c.Translations[lang].Title, Status: c.Status,
			StartsAt: formatTime(c.StartsAt), EndsAt: formatTime(c.EndsAt),
			RegistrationStatus: row.Participant.Status, Over: row.Over}
		if row.Over {
			item.Result = toProfileListResult(row.Result)
		}
		out.Items = append(out.Items, item)
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

type profileQuestionResponse struct {
	QuestionID uuid.UUID `json:"question_id"`
	Ord        int       `json:"ord"`
	Attempts   int       `json:"attempts"`
	Solved     bool      `json:"solved"`
	SolvedAt   string    `json:"solved_at,omitempty"`
	Points     int       `json:"points"`
	// Penalty is the minutes this question cost an ICPC row, zero in other
	// modes. Always sent beside points; result.scoring decides which one to
	// show, since both can be zero.
	Penalty int `json:"penalty"`
}

type profileReportResponse struct {
	ContestID uuid.UUID `json:"contest_id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	StartsAt  string    `json:"starts_at,omitempty"`
	EndsAt    string    `json:"ends_at,omitempty"`
	// Result is null when the table has no row for this registration; the rest
	// of the report is still sent.
	Result            *profileReportResult `json:"result"`
	StartedAt         string               `json:"started_at,omitempty"`
	Queries           int                  `json:"queries"`
	SuccessfulQueries int                  `json:"successful_queries"`
	// WorkedMs is from the clock starting to the last answer, absent if either
	// is missing.
	WorkedMs *int64 `json:"worked_ms,omitempty"`
	// Disqualified says the registration was excluded; their own work is still
	// theirs to read.
	Disqualified bool                      `json:"disqualified,omitempty"`
	Questions    []profileQuestionResponse `json:"questions"`
	// Truncated says there were more attempts than one read carries, so
	// questions describe the first of them.
	Truncated bool `json:"truncated,omitempty"`
}

func (h *ProfileHandler) report(w http.ResponseWriter, r *http.Request) {
	access, ok := h.admit(w, r)
	if !ok {
		return
	}
	report, err := h.profile.Report(r.Context(), access)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	c := report.Contest
	lang := negotiateLang(r, c.LanguageCodes(), c.DefaultLanguage(), h.defaultLocale)
	out := profileReportResponse{ContestID: c.ID, Title: c.Translations[lang].Title, Status: c.Status,
		StartsAt: formatTime(c.StartsAt), EndsAt: formatTime(c.EndsAt),
		Result: toProfileReportResult(report.Result), StartedAt: formatTime(report.Participant.StartedAt),
		Queries: report.Activity.Queries, SuccessfulQueries: report.Activity.Successful,
		Disqualified: report.Participant.Status == contests.RegistrationDisqualified,
		Truncated:    report.Truncated,
		Questions:    make([]profileQuestionResponse, 0, len(report.Questions)),
	}
	if worked, ok := report.Worked(); ok {
		ms := worked.Milliseconds()
		out.WorkedMs = &ms
	}
	for _, question := range report.Questions {
		out.Questions = append(out.Questions, profileQuestionResponse{
			QuestionID: question.QuestionID, Ord: question.Ord, Attempts: question.Attempts,
			Solved: question.Solved, SolvedAt: formatTime(question.SolvedAt), Points: question.Points,
			Penalty: question.Penalty,
		})
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

// profileQuery is one of the caller's own queries: monitor.QueryData without
// the address. Spelled out rather than embedded, so it cannot gain a field
// when the organiser's response does.
type profileQuery struct {
	Cursor       string `json:"cursor"`
	ExecutedAt   string `json:"executed_at"`
	ID           int64  `json:"id"`
	SQL          string `json:"sql"`
	SQLTruncated bool   `json:"sql_truncated,omitempty"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	DurationMs   *int   `json:"duration_ms"`
	RowCount     *int   `json:"row_count"`
}

// toProfileQueries reduces the organiser's page to what the participant may
// read: no address, and error text filtered by participantSafeError on top of
// the staff redaction.
func toProfileQueries(items []monitor.LoggedQuery) []profileQuery {
	out := make([]profileQuery, 0, len(items))
	for _, q := range items {
		out = append(out, profileQuery{
			Cursor: q.Cursor().Encode(), ExecutedAt: monitorTime(q.At), ID: q.ID, SQL: q.SQL,
			SQLTruncated: q.SQLTruncated, Status: q.Status,
			Error:      participantSafeError(queryrunner.Status(q.Status), q.Error),
			DurationMs: q.DurationMs, RowCount: q.RowCount,
		})
	}
	return out
}

type profileQueriesResponse struct {
	Items []profileQuery `json:"items"`
	More  bool           `json:"more"`
}

// queries is the caller's own queries, newest first: ?status=, ?q=
// (case-insensitive substring), ?cursor= (the last item's), ?limit=.
func (h *ProfileHandler) queries(w http.ResponseWriter, r *http.Request) {
	access, ok := h.admit(w, r)
	if !ok {
		return
	}
	params := r.URL.Query()
	q := monitor.QueriesQuery{Contest: access.Contest.ID, Registration: access.Participant.ID,
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
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, profileQueriesResponse{Items: toProfileQueries(page.Items), More: page.More})
}

type profileAttempt struct {
	monitor.AnswerData
	SubmittedAt string         `json:"submitted_at"`
	Queries     []profileQuery `json:"queries"`
	MoreQueries int            `json:"more_queries"`
}

type profileQuestionAttempts struct {
	QuestionID  uuid.UUID        `json:"question_id"`
	QuestionOrd int              `json:"question_ord"`
	Attempts    []profileAttempt `json:"attempts"`
}

type profileAnswersResponse struct {
	Questions []profileQuestionAttempts `json:"questions"`
	Truncated bool                      `json:"truncated"`
}

// answers is every attempt by question with the queries that led to it, the
// queries reduced as in the participant's own log.
func (h *ProfileHandler) answers(w http.ResponseWriter, r *http.Request) {
	access, ok := h.admit(w, r)
	if !ok {
		return
	}
	answers, err := h.watch.Answers(r.Context(), access.Contest.ID, access.Participant.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := profileAnswersResponse{Truncated: answers.Truncated,
		Questions: make([]profileQuestionAttempts, 0, len(answers.Questions))}
	for _, question := range answers.Questions {
		group := profileQuestionAttempts{QuestionID: question.QuestionID, QuestionOrd: question.QuestionOrd,
			Attempts: make([]profileAttempt, 0, len(question.Attempts))}
		for _, a := range question.Attempts {
			group.Attempts = append(group.Attempts, profileAttempt{AnswerData: a.AnswerData,
				SubmittedAt: monitorTime(a.At), Queries: toProfileQueries(a.Queries), MoreQueries: a.MoreQueries})
		}
		out.Questions = append(out.Questions, group)
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

type profileTab struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Position  int       `json:"position"`
	Body      string    `json:"body"`
	UpdatedAt string    `json:"updated_at"`
}

// profileWorkspaceResponse is the notes and tabs as the contest left them. It
// has no revisions: edit history is the organiser's monitoring tool, not
// something offered back to the participant.
type profileWorkspaceResponse struct {
	Notes struct {
		Body      string  `json:"body"`
		UpdatedAt *string `json:"updated_at"`
	} `json:"notes"`
	Tabs []profileTab `json:"tabs"`
}

func (h *ProfileHandler) workspace(w http.ResponseWriter, r *http.Request) {
	access, ok := h.admit(w, r)
	if !ok {
		return
	}
	ws, err := h.watch.Workspace(r.Context(), access.Contest.ID, access.Participant.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var out profileWorkspaceResponse
	out.Notes.Body, out.Notes.UpdatedAt = ws.Notes.Body, monitorOptionalTime(ws.Notes.UpdatedAt)
	out.Tabs = make([]profileTab, 0, len(ws.Tabs))
	for _, tab := range ws.Tabs {
		out.Tabs = append(out.Tabs, profileTab{ID: tab.ID, Title: tab.Title, Position: tab.Position,
			Body: tab.Body, UpdatedAt: monitorTime(tab.UpdatedAt)})
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

// logCSV is the caller's query log as a file after the contest: the same file
// and bounds as during it (queryLogCSVExport).
func (h *ProfileHandler) logCSV(w http.ResponseWriter, r *http.Request) {
	access, ok := h.admit(w, r)
	if !ok {
		return
	}
	queryLogCSVExport{history: h.history, exports: h.exports, slots: h.exportSlots, fail: h.fail, log: h.log}.
		serve(w, r, access.Participant.ID, access.Contest.ID, func() {
			// A second download while the first is still writing is refused
			// like the read budget.
			w.Header().Set("Retry-After", strconv.Itoa(int(profileWindow/time.Second)))
			httpx.Error(w, r, http.StatusTooManyRequests, codeProfileTooOften,
				"A download of this log is already running; wait for it to finish")
		})
}

// profileContestNotFound is the one answer for every contest that is not the
// caller's finished one, so the answer never reveals what exists or who is on
// it.
const profileContestNotFound = "No finished contest of yours with that identifier"

// profileMonitorErrors is monitorErrors with the three answers the profile
// gives under its own codes; clients read both sets, so neither can change.
// These are the only monitor refusals the profile's calls can produce.
var profileMonitorErrors = monitorErrors.with(
	errorRow{err: monitor.ErrParticipantNotFound, status: http.StatusNotFound, code: codeProfileContestNotFound,
		message: profileContestNotFound},
	errorRow{err: monitor.ErrInvalidCursor, status: http.StatusBadRequest, code: codeProfileInvalidCursor},
	errorRow{err: monitor.ErrInvalidQueryFilter, status: http.StatusBadRequest, code: codeProfileInvalidFilter},
)

// fail maps a refusal to a response (CLAUDE.md rule 1).
func (h *ProfileHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	// No error matches both these rows and the sentinels below, and the two
	// "not found" answers are the same, so the order does not matter.
	if profileMonitorErrors.answer(w, r, h.log, err) {
		return
	}
	switch {
	case errors.Is(err, profile.ErrNotFound), errors.Is(err, leaderboard.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeProfileContestNotFound, profileContestNotFound)
	case errors.Is(err, ErrExportsBusy):
		exportsBusy(w, r)
	default:
		h.log.ErrorContext(r.Context(), "the profile read failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

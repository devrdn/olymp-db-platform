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

// What a participant is shown of their own account (the participant profile
// design, §3), under /me/…: the profile's four numbers, the contests they are
// on, and for each one that has ended for them the report, their queries,
// their answers, their notes and their log as a file.
//
// Authentication and nothing else. Not contest.monitor, not contest.view:
// taking part in a contest is a registration, and what these routes answer is
// "what did I do", which no administrator grants. The registration is
// resolved from the session on every route, so no request can name anybody
// else's — there is no identifier for one in any of these URLs.
//
// Every route under {contestId} spends the caller's read budget first
// (CLAUDE.md rule 13, refusals counted), then admits through
// profile.Service.Open: the caller's own registration, and a contest that has
// ended for them. A contest that does not exist, one somebody else is on, and
// one still running for the caller are the same 404 with the same code, so
// these routes are not a way to learn what exists.
//
// Nothing here is audited. The trail records access to other people's data
// (design §7); reading one's own is not that, and a profile that wrote a line
// per tab would bury the trail that matters.

// ProfileReadsPerMinute is one account's budget across every route here. A
// profile is a page somebody opens, reads and leaves — several tabs of it,
// with the report's four tabs beside them, are nowhere near this; a script
// walking the journals is.
const ProfileReadsPerMinute = 120

// profileWindow is the budget's fixed window.
const profileWindow = time.Minute

// ProfileWatch is the slice of monitor.WatchService these routes need. The
// same three reads a contest's staff make of the same registration: the
// profile has no second implementation of them, only a different admission
// and a narrower response (design §1).
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
	// defaultLocale answers when a request expresses no usable preference and
	// the contest narrows nothing down (§6.2).
	defaultLocale string
	// exports keeps one registration to one CSV download at a time, shared
	// with the play screen's copy of the same route (WithExports), for the
	// reason ExportGate gives.
	exports *ExportGate
	// exportSlots keeps the whole service to as many downloads at once as the
	// core pool can spare, shared with every other export route
	// (WithExportSlots). See ExportSlots.
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

// WithExports gives this handler the gate that decides how many CSV downloads
// of one registration may be open at once — the same one the play screen's
// handler is given (ParticipantHandler.WithExports), so the bound is one gate
// over both routes rather than two gates that happen never to meet.
func (h *ProfileHandler) WithExports(gate *ExportGate) *ProfileHandler {
	if gate != nil {
		h.exports = gate
	}
	return h
}

// WithExportSlots gives this handler the service-wide count of downloads
// holding a database connection — the same one every other export route is
// given, for the reason ExportSlots gives. nil leaves the handler's own in
// place.
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
		// A distinct last segment rather than a ?format= on a route above,
		// for the reason the play screen's own download has one: a content
		// type is not something a client should have to ask for in a query
		// string it might forget.
		r.Get(one+"/log.csv", h.logCSV)
	})
}

// budget spends one read of the caller's budget before the route does
// anything, a refused read included (CLAUDE.md rule 13). Keyed by the
// account, which is one counter per person and bounded by the accounts that
// exist (rule 5) — nothing a request carries reaches the key.
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

// admit resolves the caller's own registration in the contest named in the
// URL, and answers the refusal itself.
//
// An identifier that does not parse is the same answer as one that names
// nothing: a caller who cannot be told whether a contest exists cannot be
// told whether their typo was a contest either.
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

// summary is the profile's four numbers.
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

// profileOwnNumbers is what the participant scored, in whichever of the two
// shapes the contest's mode makes the result.
type profileOwnNumbers struct {
	Scoring string `json:"scoring"`
	Points  int    `json:"points"`
	Solved  int    `json:"solved"`
	// Penalty is ICPC's, and absent in every other mode.
	Penalty *int `json:"penalty,omitempty"`
	// State is the table's own state (live, frozen, final), so the interface
	// can say why there is no place rather than leaving a gap, and PlaceOpen
	// whether there is a place to go and read at all.
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

// profileListResult is a list row's result. It deliberately has no place:
// naming one means computing a whole standings table per contest to
// decorate an overview (design §2.1). PlaceOpen says whether the report has
// one to show.
type profileListResult struct {
	profileOwnNumbers
}

// profileReportResult is the report's, which does carry the place — one
// contest, one table, the same cached computation the contest's own page is
// served from.
//
// Place and Participants are pointers so that "no place" is null rather than
// zero, the way the contest's own table already reports an unplaced row: a
// place of nought reads as a place. There are two ways to have none — the
// table is not open yet, or it is open and gives this row no place, which in
// winner mode is everybody but the winner.
type profileReportResult struct {
	profileOwnNumbers
	Place        *int `json:"place"`
	Participants *int `json:"participants"`
	// Winner marks the one registration that won a winner-mode contest, and
	// is absent for every other row and every other mode.
	Winner bool `json:"winner,omitempty"`
	// Truncated says the table was cut at the leaderboard's row bound, so
	// participants counts its rows rather than everybody on the contest.
	Truncated bool `json:"truncated,omitempty"`
}

func toProfileListResult(result profile.Result) *profileListResult {
	return &profileListResult{profileOwnNumbers: toProfileOwnNumbers(result)}
}

// toProfileReportResult is nil for a registration the table carries no row
// for, and the response sends result: null.
//
// A zeroed object would be worse than nothing: scoring and state would be
// empty strings, which name no mode and no table, and a reader shown them is
// told a result of nought in a contest with no rules. The table is bounded
// (leaderboard.DefaultMaxRows), so this is every participant of a large
// contest below the cut, not a rarity — what they get is the rest of their
// report and a line saying their row is outside the published table.
func toProfileReportResult(result *profile.Result) *profileReportResult {
	if result == nil {
		return nil
	}
	out := &profileReportResult{profileOwnNumbers: toProfileOwnNumbers(*result),
		Winner: result.Winner}
	// Only a row the table actually placed. An open table that places nobody
	// but its winner leaves everybody else's place null, together with the
	// count they are not placed among.
	if result.PlaceOpen && result.Place > 0 {
		place, participants := result.Place, result.Participants
		out.Place, out.Participants, out.Truncated = &place, &participants, result.Truncated
	}
	return out
}

type profileContestResponse struct {
	ContestID uuid.UUID `json:"contest_id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	StartsAt  string    `json:"starts_at,omitempty"`
	EndsAt    string    `json:"ends_at,omitempty"`
	// RegistrationStatus is the caller's own standing on the roster:
	// registered, active, finished or disqualified.
	RegistrationStatus string `json:"registration_status"`
	// Over says the contest has ended for this caller, and so that its report
	// can be opened. A contest that has not is a line and a way back into it.
	Over bool `json:"over"`
	// Result is absent for a contest that is not over for the caller: during
	// one, the profile shows nothing of what is happening inside it. It
	// carries no place — the report does.
	Result *profileListResult `json:"result,omitempty"`
}

type profileContestsResponse struct {
	Items []profileContestResponse `json:"items"`
	// Truncated says the account has more registrations than one profile
	// carries (profile.MaxContests).
	Truncated bool `json:"truncated"`
}

// contests is the caller's own contests, newest first.
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
	// Penalty is the minutes this question cost an ICPC row, and nought in
	// every other mode, where nothing charges minutes. Sent always and beside
	// points rather than instead of it: which of the two a reader is shown is
	// decided by result.scoring, the same field that decides it for the
	// result above, and a client that had to guess from an absence would get
	// it wrong for a contest where both are nought.
	Penalty int `json:"penalty"`
}

type profileReportResponse struct {
	ContestID uuid.UUID `json:"contest_id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	StartsAt  string    `json:"starts_at,omitempty"`
	EndsAt    string    `json:"ends_at,omitempty"`
	// Result is null when the table carries no row for this registration; see
	// toProfileReportResult. Everything else on the report is still the
	// participant's own work and is still sent.
	Result *profileReportResult `json:"result"`
	// StartedAt is when the caller's own clock started, and Queries and
	// SuccessfulQueries what their session cost.
	StartedAt         string `json:"started_at,omitempty"`
	Queries           int    `json:"queries"`
	SuccessfulQueries int    `json:"successful_queries"`
	// WorkedMs is from the clock starting to the last answer, absent when
	// either end is missing: a participant who never started, or never
	// answered, worked for no stretch this can name.
	WorkedMs *int64 `json:"worked_ms,omitempty"`
	// Disqualified says the registration was excluded. Their own work is
	// still theirs to read (design §1).
	Disqualified bool                      `json:"disqualified,omitempty"`
	Questions    []profileQuestionResponse `json:"questions"`
	// Truncated says the participant made more attempts than one read of the
	// answers carries, so the questions describe the first of them.
	Truncated bool `json:"truncated,omitempty"`
}

// report is the contest's result tab.
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

// profileQuery is one of the caller's own queries.
//
// monitor.QueryData's own shape minus the address: it is the participant's
// own address, it explains nothing to them, and it is in the way on the
// screen (design §2.2). Spelled out here rather than embedded, because
// leaving a field out of an embedded struct is not something Go's encoder
// offers — and because this response is a contract of its own, which should
// not gain a field the day the organiser's does.
type profileQuery struct {
	Cursor     string `json:"cursor"`
	ExecutedAt string `json:"executed_at"`
	ID         int64  `json:"id"`
	SQL        string `json:"sql"`
	// SQLTruncated says sql is the beginning of the statement, not all of it.
	SQLTruncated bool   `json:"sql_truncated,omitempty"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	DurationMs   *int   `json:"duration_ms"`
	RowCount     *int   `json:"row_count"`
}

// toProfileQueries is the organiser's page as the participant may read it:
// no address, and the error text reduced by participantSafeError — the same
// guard the play screen's own log applies, not the staff redaction the
// monitoring reads leave in place. Applied on top of that one, so this can
// only ever be narrower.
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

// queries is the caller's own queries, newest first: ?status=, ?q= (a
// substring, without case), ?cursor= (the last item's), ?limit=.
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

// answers is every attempt of the caller's, by question, with the queries
// that led to each — the same read the contest's staff make, with the
// queries under it reduced to what the participant's own log shows.
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

// profileWorkspaceResponse is the notes and tabs as the contest left them.
// There is deliberately no list of revisions: the history of an edit is a
// monitoring fact about how somebody worked, and it is the organiser's tool,
// not a record the participant is offered back (design §2.2).
type profileWorkspaceResponse struct {
	Notes struct {
		Body      string  `json:"body"`
		UpdatedAt *string `json:"updated_at"`
	} `json:"notes"`
	Tabs []profileTab `json:"tabs"`
}

// workspace is the caller's own notes and SQL tabs as they stood at the end.
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

// logCSV is the caller's own query log as a file, after the contest — the
// same file they could take during it (design §2.2), by the same writer and
// the same bounds (queryLogCSVExport).
func (h *ProfileHandler) logCSV(w http.ResponseWriter, r *http.Request) {
	access, ok := h.admit(w, r)
	if !ok {
		return
	}
	queryLogCSVExport{history: h.history, exports: h.exports, slots: h.exportSlots, fail: h.fail, log: h.log}.
		serve(w, r, access.Participant.ID, access.Contest.ID, func() {
			// A second download of a file the first one is still writing is
			// asking faster than this installation allows, which is the
			// budget's own refusal.
			w.Header().Set("Retry-After", strconv.Itoa(int(profileWindow/time.Second)))
			httpx.Error(w, r, http.StatusTooManyRequests, codeProfileTooOften,
				"A download of this log is already running; wait for it to finish")
		})
}

// profileContestNotFound is the one answer for every contest that is not the
// caller's finished one: one that does not exist, one somebody else is on, one
// still running for this caller, and a registration the monitoring reads do
// not recognise. Telling them apart would say what exists and who is on it.
const profileContestNotFound = "No finished contest of yours with that identifier"

// profileMonitorErrors is monitorErrors with the three answers the profile
// gives under codes of its own, on routes the participant reads about their
// own finished contest rather than an organiser about someone else's. Clients
// read both sets of codes, so neither can change. These routes read through
// ParseCursor and ProfileWatch's Queries, Answers and Workspace, which between
// them refuse with exactly these three; the table's other rows (a feed's
// filter, a revision, the signals') belong to calls the profile does not make.
var profileMonitorErrors = monitorErrors.with(
	errorRow{err: monitor.ErrParticipantNotFound, status: http.StatusNotFound, code: codeProfileContestNotFound,
		message: profileContestNotFound},
	errorRow{err: monitor.ErrInvalidCursor, status: http.StatusBadRequest, code: codeProfileInvalidCursor},
	errorRow{err: monitor.ErrInvalidQueryFilter, status: http.StatusBadRequest, code: codeProfileInvalidFilter},
)

// fail maps a refusal to a response (CLAUDE.md rule 1).
func (h *ProfileHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	// The profile wraps the monitoring's errors (a failed read of the
	// answers, for one) but no error matches both one of these rows and a
	// profile or leaderboard sentinel below, and the two "not found" answers
	// are the same anyway, so the order decides nothing.
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

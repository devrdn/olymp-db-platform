package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The endpoints a participant of a running contest uses to work on it: three
// read-only ones — the story, the visible questions, and this participant's
// own query log — and one that writes, answering a question. Read-only as far
// as the contest goes: under individual timing, a successful read of the
// story or the questions starts the participant's own clock
// (queryproxy.Service.StartOnRead), because reading the contest is taking part
// in it.
//
// What may never reach a response here, under any parameter, in any
// language, in any error message: a reference answer, a hidden question
// (is_visible = false — it exists fully and is simply not shown, §6.1),
// anything about another participant, or anything about a contest the caller
// is not enrolled in — including whether it exists.
//
// Access is decided by one rule, asked through queryproxy.Service.Access: the
// same admission the SQL console requires before it will take a query
// (registered and not disqualified or finished, the contest running, the
// address allowed). This handler asks it and nothing else: no permission
// check, because taking part in a contest is a registration, not a
// permission an administrator grants — the façade looks the registration up,
// the same way ConsoleHandler does. answer builds on the very same admission,
// and hands Submit the caller's address so that Submit, the method that
// writes, asks the same participation gate again for itself
// (contests.SubmitCommand's own doc says why both ask).

// ParticipantAccess is the slice of queryproxy.Service this handler needs: is
// the caller allowed into this contest right now, and who and what did that
// resolve to — plus the same pre-lookup rate check Run itself pays before it
// will take a query (CLAUDE.md rule 13: a read that costs database round
// trips needs the same charge a query does, not a free pass because nothing
// here executes SQL of the participant's own).
type ParticipantAccess interface {
	Access(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, error)
	// AdmitRead applies the caller's own rate budget before Access runs its
	// lookups. See queryproxy.Service.AdmitRead for why the key (userID) is
	// bounded and why it is checked ahead of everything else.
	AdmitRead(userID uuid.UUID) error
	// StartOnRead starts an individual participant's clock on their first
	// read of the contest's content (queryproxy.Service.StartOnRead). The
	// story and question endpoints call it once their content has been read,
	// before it is sent; nothing else here does. addr is the caller's, as
	// Access was given it: starting is admitted by the same gate as the read.
	StartOnRead(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (contests.Participant, error)
	// Schema describes the contest's game, for the console's schema panel, to
	// a participant and contest Access has already admitted: it does not admit
	// them again. The one rule that is its own: a contest that closed its
	// catalogues does not show its shape here either
	// (queryproxy.ErrSchemaHidden). A successful read starts the clock the
	// same way the story and the questions do, from addr.
	Schema(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (provisioning.Schema, error)
}

// Submitter is the slice of contests.Service this handler needs to record an
// answer — declared here, narrow, rather than the handler holding the whole
// service (Go layout rule 3): everything this file does with it is one call.
type Submitter interface {
	Submit(ctx context.Context, cmd contests.SubmitCommand) (contests.SubmitOutcome, error)
}

// QueryHistory is the read side of the query log this handler needs: one
// registration's own rows, newest first, paged. Declared here rather than in
// queryrunner (Go layout rule 3) because this handler is the only consumer —
// postgres.QueryLog, which already implements Journal for the write side,
// implements this too.
type QueryHistory interface {
	History(ctx context.Context, registrationID uuid.UUID, limit, offset int) ([]queryrunner.HistoryEntry, int, error)
	// ExportHistory streams every one of that registration's rows, oldest
	// first, for the CSV download beside the paged read. Two methods on one
	// interface rather than two interfaces, because they are two reads of one
	// table by one handler — and one implementation, postgres.QueryLog, which
	// is also what writes it.
	//
	// A yield that fails stops the stream: the caller is writing to a socket,
	// and a client that hung up must not have the rest of the log read out of
	// the database on its behalf.
	//
	// truncated says the log was longer than one download may carry, so the
	// file can say where it stopped instead of merely stopping.
	ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) (truncated bool, err error)
}

// AnswerLimiter is the slice of auth.Limiter the answer endpoint needs
// (CLAUDE.md rule 3): one fixed-window counter per subject.
type AnswerLimiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// AnswerRate is the answer endpoint's own throttle: PerMinute answers per
// registration, counted by Limiter.
//
// A budget of its own, not the read budget AdmitRead spends. That one is sized
// for SQL queries and polling; an answer is a guess, and at thirty a minute a
// candidate list read out of the game database is tried in no time. Keyed by
// the registration admission resolved — one per enrolment, never anything the
// request names — so the key space is bounded by the roster (CLAUDE.md rule
// 5), and it follows AdmitRead, whose key is the account, so a caller who is
// not a participant never creates a counter here at all.
type AnswerRate struct {
	Limiter   AnswerLimiter
	PerMinute int
}

// answerWindow is the answer throttle's fixed window.
const answerWindow = time.Minute

// ParticipantHandler serves a participant's own view of, and actions on, a
// running contest.
type ParticipantHandler struct {
	access    ParticipantAccess
	reader    *contests.Reader
	history   QueryHistory
	submitter Submitter
	answers   AnswerRate
	mw        *auth.Middleware
	log       *slog.Logger
	// defaultLocale answers when a request expresses no usable preference and
	// the contest narrows nothing down (§6.2).
	defaultLocale string
	// exports keeps one registration to one CSV download at a time, shared
	// with the profile's copy of the same route (WithExports). See
	// queryLogCSVExport, and ExportGate for why a rate limit is not this.
	exports *ExportGate
	// exportSlots keeps the whole service to as many downloads at once as the
	// core pool can spare, shared with every other export route
	// (WithExportSlots). See ExportSlots.
	exportSlots *ExportSlots
	// workspaces serves the participant's notes and tabs
	// (participant_workspace.go); nil leaves those routes unmounted.
	workspaces Workspaces
	// watcher hears of every request admission lets through; nil watches
	// nothing. See WithWatcher.
	watcher Watcher
	// signals takes the browser's signal batches
	// (participant_signals.go); nil leaves that route unmounted.
	signals SignalRecorder
}

// Watcher hears of every admitted participant request, to detect a
// registration's address changing or a second session using it
// (monitor.Tracker). It never refuses and bounds its own time.
type Watcher interface {
	Observe(ctx context.Context, visit monitor.Visit)
}

// WithWatcher reports every request these endpoints admit to watcher — the
// same watcher the console's façade reports its queries to, so the play
// screen and the console are one trail per registration.
func (h *ParticipantHandler) WithWatcher(watcher Watcher) *ParticipantHandler {
	h.watcher = watcher
	return h
}

// observe reports an admitted request to the watcher.
func (h *ParticipantHandler) observe(r *http.Request, participant contests.Participant, contest contests.Contest) {
	if h.watcher == nil {
		return
	}
	h.watcher.Observe(r.Context(), monitor.Visit{
		Contest: contest.ID, Registration: participant.ID,
		Address: clientAddress(r), Session: sessionTag(r), UserAgent: r.UserAgent(),
	})
}

// NewParticipantHandler assembles the endpoints.
//
// Panics without an answer throttle: config.Load never produces a rate below
// one, so a missing one is a wiring bug, and answering unthrottled is not a
// state to fall back to quietly.
func NewParticipantHandler(access ParticipantAccess, reader *contests.Reader, history QueryHistory, submitter Submitter, answers AnswerRate, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ParticipantHandler {
	if answers.Limiter == nil || answers.PerMinute < 1 {
		panic(fmt.Sprintf("api: participant handler needs an answer limiter and a positive rate, got %d", answers.PerMinute))
	}
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ParticipantHandler{access: access, reader: reader, history: history, submitter: submitter,
		answers: answers, mw: mw, log: log, defaultLocale: defaultLocale,
		exports: NewExportGate(), exportSlots: NewExportSlots(0)}
}

// WithExports gives this handler the gate that decides how many CSV downloads
// of one registration may be open at once.
//
// internal/app hands the same gate to the profile's handler, which serves the
// same file after the contest: one registration, one download, whichever of
// the two routes it was asked from. Without this the handler keeps a gate of
// its own, so the bound holds inside the route either way — what the shared
// gate adds is that it holds across both of them, instead of resting on the
// two admission rules never letting the same registration through at once.
func (h *ParticipantHandler) WithExports(gate *ExportGate) *ParticipantHandler {
	if gate != nil {
		h.exports = gate
	}
	return h
}

// WithExportSlots gives this handler the service-wide count of downloads
// holding a database connection (ExportSlots).
//
// internal/app hands the same count to every handler that serves an export,
// the organiser's included: the connections they take come from one pool, so
// the bound on them is one number. A handler given none keeps a count of its
// own at DefaultExportConcurrency, which bounds this route but says nothing
// about the others — correct for a test that mounts one handler, and not what
// a deployment wants. nil leaves the handler's own in place.
func (h *ParticipantHandler) WithExportSlots(slots *ExportSlots) *ParticipantHandler {
	if slots != nil {
		h.exportSlots = slots
	}
	return h
}

// Mount registers the routes.
//
// Under /play, not at /contests/{id}/story and /contests/{id}/questions:
// those paths are already ContestsHandler's own, and they answer a different
// question in a different shape — the staff authoring view, every
// translation and every reference answer, gated by the contest.view
// permission. Registering this handler's routes at the same paths does not
// fail to build; chi's router silently lets the later Mount win, which was
// caught here by probing the assembled router rather than by reasoning about
// it: with both handlers mounted, GET /contests/{id}/story stopped answering
// as the staff endpoint at all. A distinct prefix is what keeps "the
// organizer's content" and "what a participant may see of it" from ever
// racing for the same URL — and it matches the participant screen's own
// namespace the plan already commits to (frontend/app/(participant)/contests/
// [contestId]/play/*).
// answer is mounted at /contests/{id}/questions/{questionId}/answer rather
// than under /play: it names a question directly, the way the staff endpoints
// already do (/contests/{id}/questions/{questionId}), and there is no risk of
// the Mount-order collision the doc above warns about — ContestsHandler never
// registers POST on that exact path, only GET/PATCH/PUT/DELETE without the
// /answer suffix.
func (h *ParticipantHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate)
		r.Get("/contests/{"+contestIDParam+"}/play/story", h.story)
		r.Get("/contests/{"+contestIDParam+"}/play/questions", h.questions)
		r.Get("/contests/{"+contestIDParam+"}/play/log", h.queryLog)
		// The same log as a file (§9.1). A distinct last segment rather than
		// a ?format= on the route above: what the two return differs in more
		// than encoding — one is a page and the other is the whole session —
		// and a content type is not something a client should have to ask for
		// in a query string it might forget.
		r.Get("/contests/{"+contestIDParam+"}/play/log.csv", h.queryLogCSV)
		r.Get("/contests/{"+contestIDParam+"}/play/schema", h.schema)
		r.Post("/contests/{"+contestIDParam+"}/questions/{"+questionIDParam+"}/answer", h.answer)
		h.mountWorkspace(r)
		h.mountSignals(r)
	})
}

// admit resolves the caller's own identity and address against the contest
// named in the URL, through the one façade both this handler and the SQL
// console ask (queryproxy.Service.Access). Every route below calls this
// first and only proceeds to Reader once it succeeds.
//
// AdmitRead runs before Access and before the URL is even parsed into
// anything Access could look up with: it is the same order Run itself uses
// (a rate check keyed by the account, ahead of any lookup at all), so a
// caller cannot spend Access's two database round trips — or, on
// /play/questions, Reader's own two more — for free by asking as fast as the
// network allows.
func (h *ParticipantHandler) admit(w http.ResponseWriter, r *http.Request) (contests.Participant, contests.Contest, bool) {
	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.access.AdmitRead(identity.UserID); err != nil {
		h.fail(w, r, err)
		return contests.Participant{}, contests.Contest{}, false
	}

	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return contests.Participant{}, contests.Contest{}, false
	}

	participant, contest, err := h.access.Access(r.Context(), contestID, identity.UserID, clientAddress(r))
	if err != nil {
		h.fail(w, r, err)
		return contests.Participant{}, contests.Contest{}, false
	}
	h.observe(r, participant, contest)
	return participant, contest, true
}

// startOnRead starts an individual participant's clock once a read of the
// contest's content has succeeded and before the content is sent, and answers
// the refusal itself when it cannot. After the read, so a read refused for any
// reason starts nothing; before the response, so content is never sent to a
// participant whose clock could not be started.
func (h *ParticipantHandler) startOnRead(w http.ResponseWriter, r *http.Request, contest contests.Contest, participant contests.Participant) bool {
	if _, err := h.access.StartOnRead(r.Context(), contest, participant, clientAddress(r)); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

// languageFor resolves which language to answer contest in, by the one
// resolution order every language-dependent endpoint uses (§6.2): the
// request's own preference, then the contest's default, then the
// installation's.
func (h *ParticipantHandler) languageFor(r *http.Request, contest contests.Contest) string {
	return negotiateLang(r, contest.LanguageCodes(), contest.DefaultLanguage(), h.defaultLocale)
}

// storyResponse is the crime story in the participant's own language.
type storyResponse struct {
	Lang   string `json:"lang"`
	BodyMD string `json:"body_md"`
}

func (h *ParticipantHandler) story(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}
	lang := h.languageFor(r, contest)

	body, err := h.reader.Story(r.Context(), contest.ID, lang)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.startOnRead(w, r, contest, participant) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, storyResponse{Lang: lang, BodyMD: body})
}

// participantQuestionResponse is one visible question as its participant
// sees it. There is deliberately no field for a reference answer or for
// max_attempts itself — attempts_remaining is derived once, here, so a
// client never has to (and never could) work out "closed" from a setting it
// was not given.
// There is deliberately no ordinal field here (see ParticipantQuestion's own
// doc): the items array already arrives in display order, and a number dense
// across hidden questions too would tell the caller exactly how many
// questions are hidden and where.
type participantQuestionResponse struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Points    int               `json:"points"`
	ChoiceIDs []string          `json:"choice_ids"`
	BodyMD    string            `json:"body_md"`
	Choices   map[string]string `json:"choices,omitempty"`
	// AttemptsRemaining is omitted for a question with no cap: absent, not
	// zero, because zero would read as "no attempts left".
	AttemptsRemaining *int `json:"attempts_remaining,omitempty"`
	Closed            bool `json:"closed"`
	// CanAnswer says whether the participant may submit to this question
	// right now — always true for an unclosed question, except in a
	// sequential contest where it is true for only one of them at a time
	// (§6.1.1, finding 3). Without it a sequential contest shows several
	// unclosed questions with nothing to say which one is actually open,
	// and the participant finds out by trying each and collecting refusals.
	CanAnswer bool `json:"can_answer"`
	// Correct and PointsAwarded (finding 5) are what let a reloaded screen
	// tell "closed because solved" from "closed because every attempt is
	// spent" — before this, both looked identical once Closed was true.
	// Always present, not omitted at zero/false: a question this
	// registration never got right must read as exactly that, the same way
	// answerResponse's own PointsAwarded is never omitted for a wrong
	// attempt.
	Correct       bool `json:"correct"`
	PointsAwarded int  `json:"points_awarded"`
}

func toParticipantQuestionResponse(q contests.ParticipantQuestion) participantQuestionResponse {
	out := participantQuestionResponse{
		ID:                q.ID.String(),
		Kind:              q.Kind,
		Points:            q.Points,
		ChoiceIDs:         q.ChoiceIDs,
		BodyMD:            q.BodyMD,
		Choices:           q.Choices,
		AttemptsRemaining: q.AttemptsRemaining,
		Closed:            q.Closed,
		CanAnswer:         q.CanAnswer,
		Correct:           q.Correct,
		PointsAwarded:     q.PointsAwarded,
	}
	out.ChoiceIDs = emptyIfNil(out.ChoiceIDs)
	return out
}

type participantQuestionListResponse struct {
	Lang  string                        `json:"lang"`
	Items []participantQuestionResponse `json:"items"`
}

func (h *ParticipantHandler) questions(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}
	lang := h.languageFor(r, contest)

	found, err := h.reader.Questions(r.Context(), contest.ID, participant.ID, lang, contest.SequentialActive())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.startOnRead(w, r, contest, participant) {
		return
	}

	items := make([]participantQuestionResponse, 0, len(found))
	for _, q := range found {
		items = append(items, toParticipantQuestionResponse(q))
	}
	httpx.JSON(w, r, http.StatusOK, participantQuestionListResponse{Lang: lang, Items: items})
}

// queryLogEntryResponse is one row of the participant's own query log —
// never another participant's, and nothing this endpoint could leak beyond
// what query_log already carries for exactly this: the statement, how it
// ended, and when.
type queryLogEntryResponse struct {
	SQL string `json:"sql"`
	// SQLTruncated says sql is the beginning of the statement and not the
	// whole of it — the page is bounded in bytes as well as in rows
	// (queryrunner.MaxHistorySQLChars), and a participant handed a shortened
	// copy of their own query has to be told that is what it is. Omitted when
	// there is nothing to report, like every other optional field here; the
	// whole statement is in the CSV download beside the panel.
	SQLTruncated bool   `json:"sql_truncated,omitempty"`
	Status       string `json:"status"`
	// Error is omitted for a query that did not fail.
	Error string `json:"error,omitempty"`
	// DurationMs and RowCount are omitted rather than zero for a row still
	// running — see queryrunner.HistoryEntry's own doc.
	DurationMs *int   `json:"duration_ms,omitempty"`
	RowCount   *int   `json:"row_count,omitempty"`
	ExecutedAt string `json:"executed_at"`
}

// queryLogResponse is one page of the log, newest first, with the total
// count so the interface can offer "load more" without guessing whether
// there is any.
type queryLogResponse struct {
	Items []queryLogEntryResponse `json:"items"`
	Total int                     `json:"total"`
}

// queryLog serves GET .../play/log: this participant's own query history,
// and only theirs. limit and offset come straight from the query string —
// h.history.History clamps them itself (queryrunner.NormalizeHistoryPage), so
// a caller asking for an unreasonable page gets the largest page this
// installation allows rather than a refusal (CLAUDE.md rule 2: the bound is
// the domain's, enforced where the data is read, not merely accepted here).
func (h *ParticipantHandler) queryLog(w http.ResponseWriter, r *http.Request) {
	participant, _, ok := h.admit(w, r)
	if !ok {
		return
	}

	found, total, err := h.history.History(r.Context(), participant.ID, intParam(r, "limit"), intParam(r, "offset"))
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the participant's query history", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	items := make([]queryLogEntryResponse, 0, len(found))
	for _, entry := range found {
		items = append(items, queryLogEntryResponse{
			SQL:          entry.SQL,
			SQLTruncated: entry.SQLTruncated,
			Status:       string(entry.Status),
			Error:        participantSafeError(entry.Status, entry.Error),
			DurationMs:   entry.DurationMs,
			RowCount:     entry.RowCount,
			ExecutedAt:   entry.ExecutedAt.UTC().Format(timeLayout),
		})
	}
	httpx.JSON(w, r, http.StatusOK, queryLogResponse{Items: items, Total: total})
}

// queryLogCSV serves GET .../play/log.csv: this participant's whole query
// log as a file, and only theirs.
//
// The file itself, and every bound on it, is queryLogCSVExport's
// (querylog_csv.go) — the participant's own profile serves the same download
// after the contest, and two copies of a streamed export are two places its
// bounds could drift.
//
// Whose rows: participant.ID, resolved by Access from the session and the
// contest in the URL. Nothing the request carries selects a registration.
//
// The rate budget comes first (admit, and CLAUDE.md rule 13): this is the
// most expensive read this handler offers, so it is the last one that should
// be free.
func (h *ParticipantHandler) queryLogCSV(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}
	// A second download of a file the first one is still writing is asking
	// faster than the installation allows, and is refused as the rate refusal
	// it really is.
	queryLogCSVExport{history: h.history, exports: h.exports, slots: h.exportSlots, fail: h.fail, log: h.log}.
		serve(w, r, participant.ID, contest.ID, func() { h.fail(w, r, queryrunner.ErrTooManyQueries) })
}

// answerRequest is the body of POST .../answer: one value, compared against
// the question's reference answers server-side (§6) — never anything that
// would let the request itself say which question it thinks is right.
type answerRequest struct {
	Value string `json:"value"`
}

// answerResponse is what a participant learns after answering: never a
// reference answer, only what SubmitOutcome already carries — the same two
// derived facts (attempts_remaining, closed) the questions list computes for
// every question, by the same two functions, so this response can never
// describe "closed" differently from what a follow-up GET .../play/questions
// would say.
type answerResponse struct {
	Correct           bool `json:"correct"`
	PointsAwarded     int  `json:"points_awarded"`
	AttemptsRemaining *int `json:"attempts_remaining,omitempty"`
	Closed            bool `json:"closed"`
}

func (h *ParticipantHandler) answer(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}
	// Before the question is parsed, the body decoded or anything graded or
	// written: a malformed or refused answer is an attempt too (CLAUDE.md rule
	// 13), and counting only the ones that reach grading would leave a way to
	// probe for free.
	if !h.admitAnswer(w, r, participant.ID) {
		return
	}

	questionID, err := uuid.Parse(chi.URLParam(r, questionIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidQuestionID, "Question identifier is not valid")
		return
	}

	var req answerRequest
	if !decodeBody(w, r, &req) {
		return
	}

	outcome, err := h.submitter.Submit(r.Context(), contests.SubmitCommand{
		Participant: participant,
		Contest:     contest,
		QuestionID:  questionID,
		Value:       req.Value,
		Address:     clientAddress(r),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	httpx.JSON(w, r, http.StatusOK, answerResponse{
		Correct:           outcome.Correct,
		PointsAwarded:     outcome.PointsAwarded,
		AttemptsRemaining: outcome.AttemptsRemaining,
		Closed:            outcome.Closed,
	})
}

// admitAnswer spends one answer of this registration's budget, and answers
// the refusal itself when there is none left.
//
// Retry-After is the whole window: the counter's window began at the first
// answer counted in it, which this handler does not know, so a minute is the
// honest upper bound. A counter that cannot be kept refuses (auth.Limiter's
// own rule): grading unthrottled is exactly what this exists to prevent.
func (h *ParticipantHandler) admitAnswer(w http.ResponseWriter, r *http.Request, registrationID uuid.UUID) bool {
	allowed, err := h.answers.Limiter.Allow(r.Context(), "answer:reg:"+registrationID.String(), h.answers.PerMinute, answerWindow)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not check the answer rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(answerWindow/time.Second)))
		httpx.Error(w, r, http.StatusTooManyRequests, codeAnswerTooOften,
			"Too many answers this minute; wait before answering again")
		return false
	}
	return true
}

// participantContestsErrors is contestsErrors as a participant hears it.
//
// One answer differs. A registration that vanished mid-request — an organiser
// removing the caller between admission and Submit starting their clock — is
// participant_not_found to an organiser, a code the play screen does not
// know. To the participant it is what a missing registration is everywhere
// else on their side (queryproxy's classifyParticipant): not taking part.
var participantContestsErrors = contestsErrors.with(
	errorRow{err: contests.ErrParticipantNotFound, status: http.StatusForbidden, code: codeNotAParticipant,
		message: "The caller is not taking part in this contest"},
)

// fail maps an error from admission (AdmitRead, Access, StartOnRead), Schema,
// the reader, contests.Service.Submit or the export slots to a response.
//
// CLAUDE.md rule 1: every one of these is a declared sentinel with a mapping
// in errortable.go (queryproxy's, the rate refusal, the reader's and
// Submit's), and a test that walks the package's list asserting the 4xx it
// produces. What is left here is this handler's own: the export slots.
func (h *ParticipantHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	// Admission and the rate limit: the same tables the console and the
	// events channel answer from (errortable.go).
	if queryproxyErrors.answer(w, r, h.log, err) || queryrunnerErrors.answer(w, r, h.log, err) {
		return
	}
	if participantContestsErrors.answer(w, r, h.log, err) {
		return
	}
	switch {
	case errors.Is(err, ErrExportsBusy):
		exportsBusy(w, r)
	default:
		h.log.ErrorContext(r.Context(), "participant content could not be read", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

// schemaResponse is the game's shape, as the console's schema panel draws it.
type schemaResponse struct {
	Tables []schemaTable `json:"tables"`
	// Truncated says the game has more than the panel is being shown. The
	// same flag a truncated query result carries, for the same reason: a
	// short answer presented as a complete one is a wrong answer.
	Truncated bool `json:"truncated"`
}

type schemaTable struct {
	Name    string         `json:"name"`
	Columns []schemaColumn `json:"columns"`
}

type schemaColumn struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	References string `json:"references"`
}

// schema answers what the game looks like.
//
// A read like the story and the questions, so it goes through the same admit
// — the rate budget first, then the one place that answers "may this student
// see this contest", then the watcher — and hands Schema the pair admission
// resolved. The refusal that is this endpoint's own,
// ErrSchemaHidden, is a 403 rather than a 404: the contest exists and the
// participant is in it; what they are being told is that this olympiad does
// not hand its schema over, which is a rule of the game rather than a
// missing thing.
func (h *ParticipantHandler) schema(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}

	schema, err := h.access.Schema(r.Context(), contest, participant, clientAddress(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Never nil in the body, the same rule the query result follows: a client
	// that has to tell `null` from `[]` before it can draw a tree is a client
	// with a bug waiting.
	answer := schemaResponse{Tables: make([]schemaTable, 0, len(schema.Tables)), Truncated: schema.Truncated}
	for _, table := range schema.Tables {
		columns := make([]schemaColumn, 0, len(table.Columns))
		for _, column := range table.Columns {
			columns = append(columns, schemaColumn{
				Name: column.Name, Type: column.Type,
				Nullable: column.Nullable, References: column.References,
			})
		}
		answer.Tables = append(answer.Tables, schemaTable{Name: table.Name, Columns: columns})
	}
	httpx.JSON(w, r, http.StatusOK, answer)
}

// participantSafeError is the journalled failure, reduced to what this
// participant may be told.
//
// `query_log.error_text` is written by queryrunner.Journalled straight from
// the error the run produced, *before* anything above it sanitises anything.
// Both of the console's own guards are therefore bypassed by reading the
// column back: ConsoleHandler.fail, which turns a failure of ours into a
// generic sentence, and queryproxy's ErrDatabaseDeclined, which withholds
// PostgreSQL's own words in a contest that hides its schema. Returned
// verbatim, this endpoint handed back the game cluster's host, port and role
// together with the participant's own internal database name — and let anyone
// reconstruct a hidden schema one guess at a time: ask the console, read the
// real "relation does not exist" here.
//
// So it is a whitelist by status, not a search for bad strings. A `rejected`
// row is the SQL validator refusing the participant's own query — "syntax
// error at or near" is a fact about text they typed, and the most useful
// thing they can be told. Every other status covers a failure that reached,
// or tried to reach, something that is not theirs, and the status alone says
// what happened.
//
// The cost is real and worth naming: in a contest whose catalogues are open,
// PostgreSQL's own words about a missing relation no longer appear in the
// log, though the console still shows them at the moment of the run.
// Recovering that needs the journal to record what *kind* of failure it was
// rather than only its text, which is a column this does not add.
func participantSafeError(status queryrunner.Status, text string) string {
	if status == queryrunner.StatusRejected {
		return text
	}
	return ""
}

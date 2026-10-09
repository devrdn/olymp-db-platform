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

// The endpoints a participant of a running contest uses: the story, the visible
// questions, the schema, their own query log, and answering a question. Under
// individual timing, a successful read of the story, questions or schema starts
// the participant's clock (queryproxy.Service.StartOnRead).
//
// Nothing here may ever reveal a reference answer, a hidden question, anything
// about another participant, or anything about a contest the caller is not
// enrolled in, including whether it exists.
//
// Access is one rule, queryproxy.Service.Access, the same admission the SQL
// console requires. There is no permission check: taking part is a
// registration, not a permission. answer passes the caller's address so Submit
// asks the participation gate again for itself.

// ParticipantAccess is the slice of queryproxy.Service this handler needs:
// admission, plus the same pre-lookup rate check Run pays (CLAUDE.md rule 13),
// because a read that costs database round trips must not be free.
type ParticipantAccess interface {
	Access(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, error)
	// AdmitRead charges the caller's rate budget before Access runs any lookup.
	AdmitRead(userID uuid.UUID) error
	// StartOnRead starts an individual participant's clock on their first read
	// of the content. The story and question endpoints call it after the read
	// and before sending; addr is admitted by the same gate as the read.
	StartOnRead(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (contests.Participant, error)
	// Schema describes the game for the console's schema panel, to a
	// participant Access has already admitted. A contest that hides its
	// catalogues refuses with queryproxy.ErrSchemaHidden. A successful read
	// starts the clock like the story does.
	Schema(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (provisioning.Schema, error)
}

// Submitter is the slice of contests.Service this handler needs to record an
// answer.
type Submitter interface {
	Submit(ctx context.Context, cmd contests.SubmitCommand) (contests.SubmitOutcome, error)
}

// QueryHistory is the read side of the query log: one registration's own rows,
// newest first, paged.
type QueryHistory interface {
	History(ctx context.Context, registrationID uuid.UUID, limit, offset int) ([]queryrunner.HistoryEntry, int, error)
	// ExportHistory streams every row of that registration, oldest first, for
	// the CSV download. A failing yield stops the stream, so a client that hung
	// up does not have the rest of the log read on its behalf. truncated says
	// the log was longer than one download may carry.
	ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) (truncated bool, err error)
}

// AnswerLimiter is the slice of auth.Limiter the answer endpoint needs.
type AnswerLimiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// AnswerRate is the answer endpoint's own throttle: PerMinute answers per
// registration.
//
// A separate budget from AdmitRead's, which is sized for queries and polling;
// at that rate a candidate list could be guessed quickly. It is keyed by the
// registration admission resolved, so the key space is bounded by the roster
// (CLAUDE.md rule 5), and a non-participant never creates a counter here.
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
	// defaultLocale answers when neither the request nor the contest decides
	// the language (§6.2).
	defaultLocale string
	// exports keeps one registration to one CSV download at a time, shared with
	// the profile's copy of the route.
	exports *ExportGate
	// exportSlots caps concurrent downloads service-wide, shared with every
	// export route.
	exportSlots *ExportSlots
	// workspaces serves notes and tabs; nil leaves those routes unmounted.
	workspaces Workspaces
	// watcher hears of every admitted request; nil watches nothing.
	watcher Watcher
	// signals takes the browser's signal batches; nil leaves that route
	// unmounted.
	signals SignalRecorder
}

// Watcher hears of every admitted participant request, to detect a
// registration's address changing or a second session using it
// (monitor.Tracker). It never refuses and bounds its own time.
type Watcher interface {
	Observe(ctx context.Context, visit monitor.Visit)
}

// WithWatcher reports every admitted request to watcher, the same one the
// console reports to, so the play screen and the console form one trail per
// registration.
func (h *ParticipantHandler) WithWatcher(watcher Watcher) *ParticipantHandler {
	h.watcher = watcher
	return h
}

func (h *ParticipantHandler) observe(r *http.Request, participant contests.Participant, contest contests.Contest) {
	if h.watcher == nil {
		return
	}
	h.watcher.Observe(r.Context(), monitor.Visit{
		Contest: contest.ID, Registration: participant.ID,
		Address: clientAddress(r), Session: sessionTag(r), UserAgent: r.UserAgent(),
	})
}

// NewParticipantHandler assembles the endpoints. It panics without an answer
// throttle: config never produces a rate below one, so a missing one is a
// wiring bug, not something to run without.
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

// WithExports gives this handler the gate limiting one registration to one CSV
// download at a time. internal/app shares it with the profile handler, which
// serves the same file after the contest, so the bound holds across both
// routes.
func (h *ParticipantHandler) WithExports(gate *ExportGate) *ParticipantHandler {
	if gate != nil {
		h.exports = gate
	}
	return h
}

// WithExportSlots gives this handler the service-wide count of downloads
// holding a database connection. internal/app shares one count across every
// export handler because they draw from one pool; without it the handler bounds
// only its own route. nil keeps the handler's own.
func (h *ParticipantHandler) WithExportSlots(slots *ExportSlots) *ParticipantHandler {
	if slots != nil {
		h.exportSlots = slots
	}
	return h
}

// Mount registers the routes.
//
// They live under /play because /contests/{id}/story and
// /contests/{id}/questions belong to ContestsHandler, the staff view with every
// translation and reference answer. chi lets a later Mount silently win on the
// same path, so sharing it would replace the staff endpoint.
//
// answer is at /contests/{id}/questions/{questionId}/answer, outside /play:
// ContestsHandler registers no POST on that path, so it does not collide.
func (h *ParticipantHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate)
		r.Get("/contests/{"+contestIDParam+"}/play/story", h.story)
		r.Get("/contests/{"+contestIDParam+"}/play/questions", h.questions)
		r.Get("/contests/{"+contestIDParam+"}/play/log", h.queryLog)
		// The same log as a file (§9.1). A separate path rather than ?format=,
		// because the file is the whole session, not a page.
		r.Get("/contests/{"+contestIDParam+"}/play/log.csv", h.queryLogCSV)
		r.Get("/contests/{"+contestIDParam+"}/play/schema", h.schema)
		r.Post("/contests/{"+contestIDParam+"}/questions/{"+questionIDParam+"}/answer", h.answer)
		h.mountWorkspace(r)
		h.mountSignals(r)
	})
}

// admit resolves the caller against the contest in the URL through
// queryproxy.Service.Access, the same admission the console uses. Every route
// calls it first.
//
// AdmitRead runs before the URL is even parsed, as in Run: a caller must not
// get Access's database round trip, or the Reader's on /play/questions, for
// free by asking as fast as the network allows.
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

// startOnRead starts an individual participant's clock after a successful read
// and before the content is sent: a refused read starts nothing, and content is
// never sent to a participant whose clock could not start.
func (h *ParticipantHandler) startOnRead(w http.ResponseWriter, r *http.Request, contest contests.Contest, participant contests.Participant) bool {
	if _, err := h.access.StartOnRead(r.Context(), contest, participant, clientAddress(r)); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

// languageFor picks the response language (§6.2): the request's preference,
// then the contest's default, then the installation's.
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

// participantQuestionResponse is one visible question as its participant sees
// it. It has no field for a reference answer or for max_attempts;
// attempts_remaining is derived here so the client never has to work out
// "closed".
//
// It has no ordinal either: items arrive in display order, and a number dense
// across hidden questions would reveal how many are hidden and where.
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
	// CanAnswer says whether the participant may submit to this question now.
	// It is true for every unclosed question except in a sequential contest,
	// where only one is open at a time (§6.1.1).
	CanAnswer bool `json:"can_answer"`
	// Correct and PointsAwarded let a reloaded screen tell "closed because
	// solved" from "closed because attempts are spent". Never omitted, so a
	// question never solved reads as exactly that.
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

// queryLogEntryResponse is one row of the participant's own query log.
type queryLogEntryResponse struct {
	SQL string `json:"sql"`
	// SQLTruncated says sql is only the beginning of the statement: the page is
	// bounded in bytes as well as rows (queryrunner.MaxHistorySQLChars). The
	// whole statement is in the CSV download.
	SQLTruncated bool   `json:"sql_truncated,omitempty"`
	Status       string `json:"status"`
	// Error is omitted for a query that did not fail.
	Error string `json:"error,omitempty"`
	// DurationMs and RowCount are omitted, not zero, for a query still running.
	DurationMs *int   `json:"duration_ms,omitempty"`
	RowCount   *int   `json:"row_count,omitempty"`
	ExecutedAt string `json:"executed_at"`
}

// queryLogResponse is one page of the log, newest first, with the total so the
// interface can offer "load more".
type queryLogResponse struct {
	Items []queryLogEntryResponse `json:"items"`
	Total int                     `json:"total"`
}

// queryLog serves this participant's own query history. limit and offset are
// passed through unchecked: History clamps them
// (queryrunner.NormalizeHistoryPage), so an oversized page gets the largest
// allowed (CLAUDE.md rule 2).
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

// queryLogCSV serves this participant's whole query log as a file. The file and
// its bounds belong to queryLogCSVExport, shared with the profile's
// after-contest download. The registration comes from Access, never from the
// request. The rate budget is charged first (CLAUDE.md rule 13): this is the
// most expensive read here.
func (h *ParticipantHandler) queryLogCSV(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}
	// A second download while the first is still writing is refused as a rate
	// refusal.
	queryLogCSVExport{history: h.history, exports: h.exports, slots: h.exportSlots, fail: h.fail, log: h.log}.
		serve(w, r, participant.ID, contest.ID, func() { h.fail(w, r, queryrunner.ErrTooManyQueries) })
}

// answerRequest is the body of POST .../answer: one value, compared server-side
// against the reference answers (§6).
type answerRequest struct {
	Value string `json:"value"`
}

// answerResponse is what a participant learns after answering, never a
// reference answer. attempts_remaining and closed are derived by the same
// functions as the questions list, so the two cannot disagree.
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
	// Charged before anything is parsed or graded: a malformed or refused
	// answer is an attempt too (CLAUDE.md rule 13).
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

// admitAnswer spends one answer of this registration's budget, or answers the
// refusal.
//
// Retry-After is the whole window, since the window's start is not known here.
// A counter that cannot be kept refuses: grading unthrottled is what this
// exists to prevent.
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

// participantContestsErrors is contestsErrors as a participant hears it. A
// registration removed between admission and Submit is participant_not_found to
// an organiser; to the participant it is not_a_participant, as everywhere else
// on their side.
var participantContestsErrors = contestsErrors.with(
	errorRow{err: contests.ErrParticipantNotFound, status: http.StatusForbidden, code: codeNotAParticipant,
		message: "The caller is not taking part in this contest"},
)

// fail maps errors from admission, Schema, the reader, Submit and the export
// slots (CLAUDE.md rule 1). All but the export slots are answered from
// errortable.go.
func (h *ParticipantHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
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
	// Truncated says the game has more than the panel shows, so a short answer
	// is not presented as complete.
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

// schema answers what the game looks like. It is admitted like the story and
// the questions. ErrSchemaHidden is 403, not 404: the contest exists and the
// participant is in it, but this contest does not hand its schema over.
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

	// Never nil, so the client draws [] rather than handling null.
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

// participantSafeError reduces the journalled failure to what this participant
// may be told.
//
// query_log.error_text is written from the raw error, before any sanitising.
// Returned verbatim it would bypass both console guards (ConsoleHandler.fail
// and queryproxy's ErrDatabaseDeclined): it would expose the game cluster's
// host, port and role, and let a participant reconstruct a hidden schema from
// "relation does not exist" messages.
//
// So it is a whitelist by status. A `rejected` row is the SQL validator
// refusing the participant's own text, which they should see; every other
// status touched something that is not theirs, and the status alone says what
// happened. The cost: in a contest with open catalogues, PostgreSQL's message
// about a missing relation no longer appears in the log, though the console
// shows it at run time.
func participantSafeError(status queryrunner.Status, text string) string {
	if status == queryrunner.StatusRejected {
		return text
	}
	return ""
}

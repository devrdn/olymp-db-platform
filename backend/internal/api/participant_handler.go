package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/httpx"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryproxy"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryrunner"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The endpoints a participant of a running contest uses: the story with the
// table descriptions, the visible questions, the schema, their own query log,
// answering a question, and resetting their own database
// (participant_reset.go). A successful read of the story, questions or schema
// records the participant's start, which under individual timing starts their
// clock (participation.Admission.Start).
//
// Nothing here may ever reveal a reference answer, a hidden question, anything
// about another participant, or anything about a contest the caller is not
// enrolled in, including whether it exists.
//
// Admission is one module, participation.Admitter, the same one the SQL
// console's queries go through. There is no permission check: taking part is
// a registration, not a permission. answer passes the caller's address so
// Submit asks the participation gate again for itself.

// ParticipantSchemas describes the game for the console's schema panel, to a
// participant already admitted (queryproxy.Service.Schema). A contest that
// hides its catalogues refuses with queryproxy.ErrSchemaHidden.
type ParticipantSchemas interface {
	Schema(ctx context.Context, contest contests.Contest, participant contests.Participant) (provisioning.Schema, error)
}

// Submitter is the slice of contests.Service this handler needs to record an
// answer.
type Submitter interface {
	Submit(ctx context.Context, cmd contests.SubmitCommand) (contests.SubmitOutcome, error)
}

// QueryHistory is the read side of the query log: one registration's own rows,
// newest first, paged, one of them whole, or all of them as a file.
type QueryHistory interface {
	// History pages from the newest row, or, with a positive before, from
	// the rows older than that one.
	History(ctx context.Context, registrationID uuid.UUID, limit, offset int, before int64) ([]queryrunner.HistoryEntry, int, error)
	// HistoryEntry reads row id with its statement whole. found is false when
	// the row is not that registration's, whether or not it exists.
	HistoryEntry(ctx context.Context, registrationID uuid.UUID, id int64) (entry queryrunner.HistoryEntry, found bool, err error)
	QueryLogExport
}

// QueryLogExport is the part of the query log the CSV download reads.
type QueryLogExport interface {
	// ExportHistory streams every row of that registration, oldest first, for
	// the CSV download. A failing yield stops the stream, so a client that hung
	// up does not have the rest of the log read on its behalf. truncated says
	// the log was longer than one download may carry.
	ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) (truncated bool, err error)
}

// AnswerRate is the answer endpoint's own throttle: PerMinute answers per
// registration.
//
// A separate budget from the read budget, which is sized for page loads; at that
// rate a candidate list could be guessed quickly. It is keyed by the
// registration admission resolved, so the key space is bounded by the roster
// (CLAUDE.md rule 5), and a non-participant never creates a counter here.
type AnswerRate struct {
	Limiter   httpx.Counter
	PerMinute int
}

// ParticipantHandler serves a participant's own view of, and actions on, a
// running contest.
type ParticipantHandler struct {
	participation Participation
	// schemas is nil until WithSchemas.
	schemas   ParticipantSchemas
	reader    *contests.Reader
	history   QueryHistory
	submitter Submitter
	// answers is charged to the registration admission resolved, before the
	// gate, through answerBudget.
	answers httpx.KeyBudget
	budgets budgets
	mw      *auth.Middleware
	log     *slog.Logger
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
	// signals takes the browser's signal batches; nil leaves that route
	// unmounted.
	signals SignalRecorder
	// resets remakes the participant's database; nil leaves that route
	// unmounted.
	resets DatabaseReset
}

// NewParticipantHandler assembles the endpoints. It panics without an answer
// throttle: config never produces a rate below one, so a missing one is a
// wiring bug, not something to run without.
func NewParticipantHandler(admission Participation, reader *contests.Reader, history QueryHistory, submitter Submitter, answers AnswerRate, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ParticipantHandler {
	if answers.Limiter == nil || answers.PerMinute < 1 {
		panic(fmt.Sprintf("api: participant handler needs an answer limiter and a positive rate, got %d", answers.PerMinute))
	}
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ParticipantHandler{participation: admission, reader: reader, history: history, submitter: submitter,
		answers: httpx.PerKey("answer:reg", answers.PerMinute, time.Minute, codeAnswerTooOften,
			"Too many answers this minute; wait before answering again"),
		budgets: newBudgets(answers.Limiter, log),
		mw:      mw, log: log, defaultLocale: defaultLocale,
		exports: NewExportGate(), exportSlots: NewExportSlots(0)}
}

// WithSchemas serves the schema panel from schemas. Without it, as when no
// Query Runner is deployed, the panel is admitted like every read and then
// refused with queryproxy.ErrSchemaHidden, the safe direction to fail.
func (h *ParticipantHandler) WithSchemas(schemas ParticipantSchemas) *ParticipantHandler {
	h.schemas = schemas
	return h
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
		r.Get("/contests/{"+contestIDParam+"}/play/log/{"+queryLogEntryParam+"}", h.queryLogEntry)
		// The same log as a file (§9.1). A separate path rather than ?format=,
		// because the file is the whole session, not a page.
		r.Get("/contests/{"+contestIDParam+"}/play/log.csv", h.queryLogCSV)
		r.Get("/contests/{"+contestIDParam+"}/play/schema", h.schema)
		r.Post("/contests/{"+contestIDParam+"}/questions/{"+questionIDParam+"}/answer", h.answer)
		h.mountWorkspace(r)
		h.mountSignals(r)
		h.mountReset(r)
	})
}

// languageFor picks the response language (§6.2): the request's preference,
// then the contest's default, then the installation's.
func (h *ParticipantHandler) languageFor(r *http.Request, contest contests.Contest) string {
	return negotiateLang(r, contest.LanguageCodes(), contest.DefaultLanguage(), h.defaultLocale)
}

// storyResponse is the crime story in the participant's own language, with the
// table descriptions beside it.
//
// The descriptions ride on this read rather than a route of their own: the
// play screen loads both at once, and a second route would charge the read
// budget and run admission twice for every page load. SchemaNotesMD is
// always present, empty when the contest has none in the participant's
// language or its default.
type storyResponse struct {
	Lang          string `json:"lang"`
	BodyMD        string `json:"body_md"`
	SchemaNotesMD string `json:"schema_notes_md"`
}

func (h *ParticipantHandler) story(w http.ResponseWriter, r *http.Request) {
	admission, ok := h.participation.admit(w, r, h.mw, h.participation.Reads, h.fail)
	if !ok {
		return
	}
	contest := admission.Contest
	lang := h.languageFor(r, contest)

	body, err := h.reader.Story(r.Context(), contest.ID, lang)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	notes, err := h.reader.SchemaNotes(r.Context(), contest, lang)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !start(w, r, admission, h.fail) {
		return
	}
	httpx.JSON(w, r, http.StatusOK, storyResponse{Lang: lang, BodyMD: body, SchemaNotesMD: notes})
}

// participantQuestionResponse is one visible question as its participant sees
// it. It has no field for a reference answer or for max_attempts;
// attempts_remaining is derived here so the client never has to work out
// "closed".
//
// It has no ordinal either: items arrive in display order, and a number dense
// across hidden questions would reveal how many are hidden and where.
type participantQuestionResponse struct {
	// Locked is always false here; it is what tells this shape from
	// lockedQuestionResponse in the same list.
	Locked bool   `json:"locked"`
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Points int    `json:"points"`
	// IsBonus is absent from lockedQuestionResponse on purpose.
	IsBonus   bool     `json:"is_bonus"`
	ChoiceIDs []string `json:"choice_ids"`
	// Title is empty when the question has none in this language; nothing is
	// derived from the body in its place.
	Title   string            `json:"title"`
	BodyMD  string            `json:"body_md"`
	Choices map[string]string `json:"choices,omitempty"`
	// AttemptsRemaining is omitted for a question with no cap: absent, not
	// zero, because zero would read as "no attempts left".
	AttemptsRemaining *int `json:"attempts_remaining,omitempty"`
	Closed            bool `json:"closed"`
	// CanAnswer says whether the participant may submit to this question now:
	// exactly when it is not closed. In a sequential contest the questions
	// not yet reached come as lockedQuestionResponse instead (§6.1.1).
	CanAnswer bool `json:"can_answer"`
	// Correct and PointsAwarded let a reloaded screen tell "closed because
	// solved" from "closed because attempts are spent". Never omitted, so a
	// question never solved reads as exactly that.
	Correct       bool `json:"correct"`
	PointsAwarded int  `json:"points_awarded"`
}

// lockedQuestionResponse is a question a sequential contest has not reached
// (§6.1.1): a type with no field for content, rather than a question with its
// content blanked, so a client cannot read a withheld title as none. Position
// counts only the listed questions.
type lockedQuestionResponse struct {
	Position int  `json:"position"`
	Locked   bool `json:"locked"`
}

// toQuestionItem is one entry of the list: a lockedQuestionResponse or a
// participantQuestionResponse.
func toQuestionItem(q contests.ParticipantQuestion) any {
	if q.Locked {
		return lockedQuestionResponse{Position: q.Position, Locked: true}
	}
	return toParticipantQuestionResponse(q)
}

func toParticipantQuestionResponse(q contests.ParticipantQuestion) participantQuestionResponse {
	out := participantQuestionResponse{
		ID:                q.ID.String(),
		Kind:              q.Kind,
		Points:            q.Points,
		IsBonus:           q.IsBonus,
		ChoiceIDs:         q.ChoiceIDs,
		Title:             q.Title,
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
	Lang  string `json:"lang"`
	Items []any  `json:"items"`
}

func (h *ParticipantHandler) questions(w http.ResponseWriter, r *http.Request) {
	admission, ok := h.participation.admit(w, r, h.mw, h.participation.Reads, h.fail)
	if !ok {
		return
	}
	contest := admission.Contest
	lang := h.languageFor(r, contest)

	found, err := h.reader.Questions(r.Context(), contest.ID, admission.Participant.ID, lang, contest.SequentialActive())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !start(w, r, admission, h.fail) {
		return
	}

	items := make([]any, 0, len(found))
	for _, q := range found {
		items = append(items, toQuestionItem(q))
	}
	httpx.JSON(w, r, http.StatusOK, participantQuestionListResponse{Lang: lang, Items: items})
}

// queryLogEntryResponse is one row of the participant's own query log.
type queryLogEntryResponse struct {
	// ID names the row for reading it whole (.../play/log/{entryId}).
	ID  int64  `json:"id"`
	SQL string `json:"sql"`
	// SQLTruncated says sql is only the beginning of the statement: the page is
	// bounded in bytes as well as rows (queryrunner.MaxHistorySQLChars). The
	// row read on its own, and the CSV download, carry it whole.
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
// allowed (CLAUDE.md rule 2). "Load older" sends ?before=, a row id.
func (h *ParticipantHandler) queryLog(w http.ResponseWriter, r *http.Request) {
	admission, ok := h.participation.admit(w, r, h.mw, h.participation.Reads, h.fail)
	if !ok {
		return
	}

	// Anything that is not a row id asks for the newest page.
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil || before < 0 {
		before = 0
	}
	found, total, err := h.history.History(r.Context(), admission.Participant.ID, intParam(r, "limit"), intParam(r, "offset"), before)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the participant's query history", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	items := make([]queryLogEntryResponse, 0, len(found))
	for _, entry := range found {
		items = append(items, toQueryLogEntryResponse(entry))
	}
	httpx.JSON(w, r, http.StatusOK, queryLogResponse{Items: items, Total: total})
}

func toQueryLogEntryResponse(entry queryrunner.HistoryEntry) queryLogEntryResponse {
	return queryLogEntryResponse{
		ID:           entry.ID,
		SQL:          entry.SQL,
		SQLTruncated: entry.SQLTruncated,
		Status:       string(entry.Status),
		Error:        participantSafeError(entry.Status, entry.Error),
		DurationMs:   entry.DurationMs,
		RowCount:     entry.RowCount,
		ExecutedAt:   entry.ExecutedAt.UTC().Format(timeLayout),
	}
}

// queryLogEntryParam names the row in .../play/log/{entryId}.
const queryLogEntryParam = "entryId"

// queryLogEntry serves one row of this participant's own log with its
// statement whole, which a page cuts. It is admitted and charged like the
// page. An id that is not one of the caller's rows, or cannot be an id at
// all, is not found: whether it is someone else's is not theirs to learn.
func (h *ParticipantHandler) queryLogEntry(w http.ResponseWriter, r *http.Request) {
	admission, ok := h.participation.admit(w, r, h.mw, h.participation.Reads, h.fail)
	if !ok {
		return
	}
	notFound := func() {
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "No such query in your log")
	}

	id, err := strconv.ParseInt(chi.URLParam(r, queryLogEntryParam), 10, 64)
	if err != nil || id <= 0 {
		notFound()
		return
	}
	entry, found, err := h.history.HistoryEntry(r.Context(), admission.Participant.ID, id)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read a row of the participant's query history", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}
	if !found {
		notFound()
		return
	}
	httpx.JSON(w, r, http.StatusOK, toQueryLogEntryResponse(entry))
}

// queryLogCSV serves this participant's whole query log as a file. The file and
// its bounds belong to queryLogCSVExport, shared with the profile's
// after-contest download. The registration comes from admission, never from
// the request. The read budget is charged first (CLAUDE.md rule 13): this is
// the most expensive read here.
func (h *ParticipantHandler) queryLogCSV(w http.ResponseWriter, r *http.Request) {
	admission, ok := h.participation.admit(w, r, h.mw, h.participation.Reads, h.fail)
	if !ok {
		return
	}
	// A second download while the first is still writing is refused as a rate
	// refusal.
	queryLogCSVExport{history: h.history, exports: h.exports, slots: h.exportSlots, fail: h.fail, log: h.log}.
		serve(w, r, admission.Participant.ID, admission.Contest.ID, func() { h.fail(w, r, queryrunner.ErrTooManyQueries) })
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
	admission, ok := h.participation.admit(w, r, h.mw, answerBudget{reads: h.participation.Reads, answers: h.answers, budgets: h.budgets}, h.fail)
	if !ok {
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
		Participant: admission.Participant,
		Contest:     admission.Contest,
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

// answerBudget is what an answer spends, both before anything is parsed or
// graded, since a malformed or refused answer is an attempt too (CLAUDE.md
// rule 13): a read of the play screen per account before the lookup, then one
// of the registration's answers before the gate. A counter that cannot be kept
// refuses: grading unthrottled is what this exists to prevent.
type answerBudget struct {
	reads   participation.Budget
	answers httpx.KeyBudget
	budgets budgets
}

func (b answerBudget) Charge(ctx context.Context, account uuid.UUID) error {
	return b.reads.Charge(ctx, account)
}

func (b answerBudget) ChargeRegistration(ctx context.Context, participant contests.Participant, _ contests.Contest) error {
	return b.budgets.Charge(ctx, b.answers.Of(participant.ID.String()))
}

// participantContestsErrors is contestsErrors as a participant hears it. A
// registration removed between admission and Submit is participant_not_found to
// an organiser; to the participant it is not_a_participant, as everywhere else
// on their side.
var participantContestsErrors = contestsErrors.with(
	errorRow{err: contests.ErrParticipantNotFound, status: http.StatusForbidden, code: codeNotAParticipant,
		message: "The caller is not taking part in this contest"},
)

// fail maps errors from admission, Schema, the reader, Submit, the answer rate
// and the export slots (CLAUDE.md rule 1). All but the last are answered from
// errortable.go.
func (h *ParticipantHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if h.answered(w, r, err) {
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

// answered answers err from the tables every participant route shares:
// admission's, the console's and the contests', as a participant hears them.
func (h *ParticipantHandler) answered(w http.ResponseWriter, r *http.Request, err error) bool {
	return queryproxyErrors.answer(w, r, h.log, err) || queryrunnerErrors.answer(w, r, h.log, err) ||
		participantContestsErrors.answer(w, r, h.log, err) || answerBudgetError(w, r, h.log, err)
}

// schemaResponse is the game's shape, as the console's schema panel and
// diagram draw it.
type schemaResponse struct {
	Tables []schemaTable `json:"tables"`
	// Truncated says the game has more than the panel shows, so a short answer
	// is not presented as complete.
	Truncated bool `json:"truncated"`
}

type schemaTable struct {
	Name        string             `json:"name"`
	Columns     []schemaColumn     `json:"columns"`
	PrimaryKey  []string           `json:"primary_key"`
	ForeignKeys []schemaForeignKey `json:"foreign_keys"`
}

type schemaForeignKey struct {
	Name              string   `json:"name"`
	Columns           []string `json:"columns"`
	ReferencedTable   string   `json:"referenced_table"`
	ReferencedColumns []string `json:"referenced_columns"`
}

type schemaColumn struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	References string `json:"references"`
}

// schema answers what the game looks like. It is admitted, and starts the
// participant, like the story and the questions. ErrSchemaHidden is 403, not
// 404: the contest exists and the participant is in it, but this contest does
// not hand its schema over.
func (h *ParticipantHandler) schema(w http.ResponseWriter, r *http.Request) {
	admission, ok := h.participation.admit(w, r, h.mw, h.participation.Reads, h.fail)
	if !ok {
		return
	}
	if h.schemas == nil {
		h.fail(w, r, queryproxy.ErrSchemaHidden)
		return
	}

	schema, err := h.schemas.Schema(r.Context(), admission.Contest, admission.Participant)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !start(w, r, admission, h.fail) {
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
		keys := make([]schemaForeignKey, 0, len(table.ForeignKeys))
		for _, key := range table.ForeignKeys {
			keys = append(keys, schemaForeignKey{
				Name: key.Name, Columns: key.Columns,
				ReferencedTable: key.ReferencedTable, ReferencedColumns: key.ReferencedColumns,
			})
		}
		primary := table.PrimaryKey
		if primary == nil {
			primary = []string{}
		}
		answer.Tables = append(answer.Tables, schemaTable{
			Name: table.Name, Columns: columns, PrimaryKey: primary, ForeignKeys: keys,
		})
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

package api

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The endpoints a participant of a running contest uses to work on it: three
// read-only ones — the story, the visible questions, and this participant's
// own query log — and one that writes, answering a question.
//
// What may never reach a response here, under any parameter, in any
// language, in any error message: a reference answer, a hidden question
// (is_visible = false — it exists fully and is simply not shown, §6.1),
// anything about another participant, or anything about a contest the caller
// is not enrolled in — including whether it exists.
//
// Access is decided exactly once, by queryproxy.Service.Access, which is the
// same admission the SQL console requires before it will take a query
// (registered and not disqualified or finished, the contest running, the
// address allowed). This handler asks it and nothing else: no permission
// check, because taking part in a contest is a registration, not a
// permission an administrator grants — the façade looks the registration up,
// the same way ConsoleHandler does. answer builds on the very same admission
// rather than a second one of its own (contests.SubmitCommand's own doc
// explains why contests.Service.Submit could not ask Access itself, and why
// that is this handler's job instead).

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
	// Schema describes the contest's game, for the console's schema panel. It
	// applies Access's own admission itself and then the one rule that is its
	// own: a contest that closed its catalogues does not show its shape here
	// either (queryproxy.ErrSchemaHidden).
	Schema(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (provisioning.Schema, error)
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
	ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) error
}

// ParticipantHandler serves a participant's own view of, and actions on, a
// running contest.
type ParticipantHandler struct {
	access    ParticipantAccess
	reader    *contests.Reader
	history   QueryHistory
	submitter Submitter
	mw        *auth.Middleware
	log       *slog.Logger
	// defaultLocale answers when a request expresses no usable preference and
	// the contest narrows nothing down (§6.2).
	defaultLocale string
}

// NewParticipantHandler assembles the endpoints.
func NewParticipantHandler(access ParticipantAccess, reader *contests.Reader, history QueryHistory, submitter Submitter, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ParticipantHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ParticipantHandler{access: access, reader: reader, history: history, submitter: submitter, mw: mw, log: log, defaultLocale: defaultLocale}
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

	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, auth.CodeInvalidContestID, "Contest identifier is not valid")
		return contests.Participant{}, contests.Contest{}, false
	}

	participant, contest, err := h.access.Access(r.Context(), contestID, identity.UserID, clientAddress(r))
	if err != nil {
		h.fail(w, r, err)
		return contests.Participant{}, contests.Contest{}, false
	}
	return participant, contest, true
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
	_, contest, ok := h.admit(w, r)
	if !ok {
		return
	}
	lang := h.languageFor(r, contest)

	body, err := h.reader.Story(r.Context(), contest.ID, lang)
	if err != nil {
		h.fail(w, r, err)
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
	if out.ChoiceIDs == nil {
		out.ChoiceIDs = []string{}
	}
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
	SQL    string `json:"sql"`
	Status string `json:"status"`
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
			SQL:        entry.SQL,
			Status:     string(entry.Status),
			Error:      participantSafeError(entry.Status, entry.Error),
			DurationMs: entry.DurationMs,
			RowCount:   entry.RowCount,
			ExecutedAt: entry.ExecutedAt.UTC().Format(timeLayout),
		})
	}
	httpx.JSON(w, r, http.StatusOK, queryLogResponse{Items: items, Total: total})
}

// queryLogCSVColumns is the file's header row, and the order of every row
// under it. The same facts the panel shows, plus the error text — a
// spreadsheet has room for it where a table column does not.
var queryLogCSVColumns = []string{"executed_at", "status", "duration_ms", "row_count", "error", "sql"}

// queryLogCSV serves GET .../play/log.csv: this participant's whole query
// log as a file, and only theirs.
//
// Streamed, not bounded — the opposite choice from the contest package next
// door, and for the opposite reason. A log has no natural size: a participant
// who never stops querying over a two-hour olympiad puts hundreds of rows in
// it, and a file quietly missing most of them is not a record of anything. So
// there is no page here (§9.1 is explicit that CSV streams row by row with no
// volume ceiling), and what keeps memory flat instead is that a row is
// written to the socket as it arrives: postgres.QueryLog.ExportHistory hands
// them over one at a time, csv.Writer's own bufio flushes when its buffer
// fills, and nothing accumulates a log's worth of anything. The package is
// bounded because a partial package is corrupt; the log is streamed because a
// partial log is the only alternative to no log at all.
//
// The rate budget comes first (admit, and CLAUDE.md rule 13): this is the
// most expensive read this handler offers, so it is the last one that should
// be free.
//
// Whose rows: participant.ID, resolved by Access from the session and the
// contest in the URL. Nothing the request carries selects a registration, and
// there is no staff route to this endpoint — the admin journal panel of §9.1,
// which is the thing that would name somebody else's registration, is not
// built yet and will not be built here.
func (h *ParticipantHandler) queryLogCSV(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}

	// Headers cannot be set once a byte is written, and whether the read even
	// starts is only known when the first row arrives (or the stream ends).
	// So the response is opened by this closure, called at most once: either
	// from the first row, or from the empty case below. A failure before it
	// runs still has a status line to spend, and spends it on saying so.
	writer := csv.NewWriter(w)
	opened := false
	open := func() error {
		opened = true
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		// The identifier, not the contest's title: a title is authored text
		// in any script and this header is ASCII.
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", "query-log-"+contest.ID.String()+".csv"))
		w.WriteHeader(http.StatusOK)
		return writer.Write(queryLogCSVColumns)
	}

	err := h.history.ExportHistory(r.Context(), participant.ID, func(entry queryrunner.HistoryEntry) error {
		if !opened {
			if err := open(); err != nil {
				return err
			}
		}
		return writer.Write([]string{
			entry.ExecutedAt.UTC().Format(timeLayout),
			string(entry.Status),
			// Empty rather than zero for a row still running: an empty cell
			// is how CSV says "not recorded", and a zero here would read as a
			// query that took no time and returned nothing.
			optionalNumber(entry.DurationMs),
			optionalNumber(entry.RowCount),
			// The same guard the paged read applies, not a second opinion
			// about it: this file reads the same unsanitised column, and a
			// download is the more convenient way round a guard than a page
			// is, because it arrives as a document somebody keeps.
			participantSafeError(entry.Status, entry.Error),
			entry.SQL,
		})
	})
	if err != nil {
		if !opened {
			h.log.ErrorContext(r.Context(), "could not read the participant's query log for export", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
			return
		}
		// The status line is already out and said 200. Logged rather than
		// swallowed: what the participant has is a file that stops early, and
		// the log is the only place that fact survives.
		h.log.ErrorContext(r.Context(), "the participant's query log export stopped early", "error", err)
	}

	// A participant who ran nothing still gets a file: a header row and no
	// rows under it. A zero-byte download is indistinguishable from a failed
	// one.
	if !opened {
		if err := open(); err != nil {
			h.log.ErrorContext(r.Context(), "could not write the query log export header", "error", err)
			return
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		h.log.ErrorContext(r.Context(), "could not finish the query log export", "error", err)
	}
}

// optionalNumber renders a count that may not have been recorded yet.
func optionalNumber(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
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

	questionID, err := uuid.Parse(chi.URLParam(r, questionIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidQuestionID, "Question identifier is not valid")
		return
	}

	var req answerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	outcome, err := h.submitter.Submit(r.Context(), contests.SubmitCommand{
		Participant: participant,
		Contest:     contest,
		QuestionID:  questionID,
		Value:       req.Value,
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

// fail maps a refusal from queryproxy.Service.AdmitRead, from
// queryproxy.Service.Access, from the reader, or from contests.Service.Submit,
// to a response.
//
// CLAUDE.md rule 1: every one of these is a declared sentinel with a mapping
// here and a handler test asserting the 4xx it produces.
func (h *ParticipantHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, queryrunner.ErrTooManyQueries):
		httpx.Error(w, r, http.StatusTooManyRequests, codeQueryTooOften,
			"This caller is asking faster than this installation allows")
	case errors.Is(err, queryproxy.ErrNotAParticipant):
		// The same answer whether the caller never registered, was
		// disqualified, or the contest named in the URL belongs to somebody
		// else entirely: telling those apart would say whether an account is
		// on a roster, or whether a contest exists at all.
		httpx.Error(w, r, http.StatusForbidden, codeNotAParticipant, "The caller is not taking part in this contest")
	case errors.Is(err, queryproxy.ErrContestNotRunning):
		httpx.Error(w, r, http.StatusConflict, codeContestNotRunning, "The contest is not running")
	case errors.Is(err, queryproxy.ErrFinished):
		httpx.Error(w, r, http.StatusConflict, codeContestFinished, "The participant has already finished")
	case errors.Is(err, queryproxy.ErrSchemaHidden):
		// A rule of the game, not an outage and not a missing resource: the
		// contest exists and the caller is in it. The interface reads this
		// code and simply does not offer the panel.
		httpx.Error(w, r, http.StatusForbidden, codeSchemaHidden, "This contest does not show the game's schema")
	case errors.Is(err, queryproxy.ErrNoGameYet):
		httpx.Error(w, r, http.StatusConflict, codeNoGameYet, "The contest has no game database yet")
	case errors.Is(err, queryproxy.ErrAddressNotAllowed):
		httpx.Error(w, r, http.StatusForbidden, codeAddressNotAllowed,
			"This contest is only available from the university network")
	case errors.Is(err, contests.ErrStoryNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeStoryNotFound, "This contest has no story yet")
	case errors.Is(err, contests.ErrQuestionNotFound):
		// Also the answer when the question named in the URL belongs to
		// another contest: that it exists elsewhere is not this caller's
		// business (contests.Service.Submit's own doc).
		httpx.Error(w, r, http.StatusNotFound, codeQuestionNotFound, "No such question in this contest")
	case errors.Is(err, contests.ErrAnswerTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeAnswerTooLong, err.Error())
	case errors.Is(err, contests.ErrQuestionClosed):
		httpx.Error(w, r, http.StatusConflict, codeQuestionClosed,
			"This question is already answered correctly, or every attempt has been used")
	case errors.Is(err, contests.ErrQuestionNotOpen):
		// §6.1.1: the server is what enforces sequential order, not the
		// interface — a direct request naming a question that has not opened
		// yet is refused here, the same 409 family as codeQuestionClosed
		// (another fact about this question's current state, not a
		// permission the caller lacks).
		httpx.Error(w, r, http.StatusConflict, codeQuestionNotOpen,
			"A question ordered before this one is not closed yet")
	case errors.Is(err, contests.ErrDeadlinePassed):
		httpx.Error(w, r, http.StatusConflict, codeDeadlinePassed, "The deadline for this contest has passed")
	case errors.Is(err, contests.ErrTooManyAttemptConflicts):
		// Finding 1: running out of retries is a fact about this exact
		// moment, not an outage — the same 409 family as codeQuestionClosed
		// and codeStatusChanged, and the same honest instruction: try again.
		httpx.Error(w, r, http.StatusConflict, codeAttemptConflict,
			"Too many submissions to this question arrived at once; try again")
	case errors.Is(err, queryproxy.ErrUnavailable):
		h.log.ErrorContext(r.Context(), "could not resolve participant access", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
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
// see this contest". The refusal that is this endpoint's own,
// ErrSchemaHidden, is a 403 rather than a 404: the contest exists and the
// participant is in it; what they are being told is that this olympiad does
// not hand its schema over, which is a rule of the game rather than a
// missing thing.
func (h *ParticipantHandler) schema(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The contest identifier is not a UUID")
		return
	}
	if err := h.access.AdmitRead(identity.UserID); err != nil {
		h.fail(w, r, err)
		return
	}

	schema, err := h.access.Schema(r.Context(), contestID, identity.UserID, clientAddress(r))
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

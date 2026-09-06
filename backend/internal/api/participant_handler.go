package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The endpoints a participant of a running contest uses to work on it: two
// read-only ones — the story, and the visible questions — and one that
// writes, answering a question.
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
}

// Submitter is the slice of contests.Service this handler needs to record an
// answer — declared here, narrow, rather than the handler holding the whole
// service (Go layout rule 3): everything this file does with it is one call.
type Submitter interface {
	Submit(ctx context.Context, cmd contests.SubmitCommand) (contests.SubmitOutcome, error)
}

// ParticipantHandler serves a participant's own view of, and actions on, a
// running contest.
type ParticipantHandler struct {
	access    ParticipantAccess
	reader    *contests.Reader
	submitter Submitter
	mw        *auth.Middleware
	log       *slog.Logger
	// defaultLocale answers when a request expresses no usable preference and
	// the contest narrows nothing down (§6.2).
	defaultLocale string
}

// NewParticipantHandler assembles the endpoints.
func NewParticipantHandler(access ParticipantAccess, reader *contests.Reader, submitter Submitter, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ParticipantHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ParticipantHandler{access: access, reader: reader, submitter: submitter, mw: mw, log: log, defaultLocale: defaultLocale}
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

	found, err := h.reader.Questions(r.Context(), contest.ID, participant.ID, lang)
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
	case errors.Is(err, contests.ErrDeadlinePassed):
		httpx.Error(w, r, http.StatusConflict, codeDeadlinePassed, "The deadline for this contest has passed")
	case errors.Is(err, queryproxy.ErrUnavailable):
		h.log.ErrorContext(r.Context(), "could not resolve participant access", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	default:
		h.log.ErrorContext(r.Context(), "participant content could not be read", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

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
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The two read-only endpoints a participant of a running contest uses to see
// what they are working on: the story, and the visible questions.
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
// the same way ConsoleHandler does.

// ParticipantAccess is the slice of queryproxy.Service this handler needs: is
// the caller allowed into this contest right now, and who and what did that
// resolve to.
type ParticipantAccess interface {
	Access(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, error)
}

// ParticipantHandler serves a participant's own view of a running contest.
type ParticipantHandler struct {
	access ParticipantAccess
	reader *contests.Reader
	mw     *auth.Middleware
	log    *slog.Logger
	// defaultLocale answers when a request expresses no usable preference and
	// the contest narrows nothing down (§6.2).
	defaultLocale string
}

// NewParticipantHandler assembles the endpoints.
func NewParticipantHandler(access ParticipantAccess, reader *contests.Reader, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ParticipantHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ParticipantHandler{access: access, reader: reader, mw: mw, log: log, defaultLocale: defaultLocale}
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
func (h *ParticipantHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate)
		r.Get("/contests/{"+contestIDParam+"}/play/story", h.story)
		r.Get("/contests/{"+contestIDParam+"}/play/questions", h.questions)
	})
}

// admit resolves the caller's own identity and address against the contest
// named in the URL, through the one façade both this handler and the SQL
// console ask (queryproxy.Service.Access). Every route below calls this
// first and only proceeds to Reader once it succeeds.
func (h *ParticipantHandler) admit(w http.ResponseWriter, r *http.Request) (contests.Participant, contests.Contest, bool) {
	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, auth.CodeInvalidContestID, "Contest identifier is not valid")
		return contests.Participant{}, contests.Contest{}, false
	}

	identity, _ := auth.IdentityFrom(r.Context())
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
type participantQuestionResponse struct {
	ID        string            `json:"id"`
	Ord       int               `json:"ord"`
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
		Ord:               q.Ord,
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

// fail maps a refusal from queryproxy.Service.Access, or from the reader, to
// a response.
//
// CLAUDE.md rule 1: every one of these is a declared sentinel with a mapping
// here and a handler test asserting the 4xx it produces.
func (h *ParticipantHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
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
	case errors.Is(err, queryproxy.ErrUnavailable):
		h.log.ErrorContext(r.Context(), "could not resolve participant access", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	default:
		h.log.ErrorContext(r.Context(), "participant content could not be read", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

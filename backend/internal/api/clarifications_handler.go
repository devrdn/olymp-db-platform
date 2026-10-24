package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Questions to the organisers, their answers and announcements
// (internal/clarifications), from both sides:
//
//   - a participant's, under /contests/{id}/play/clarifications: reading their
//     feed spends the read budget, then passes the participation gate; asking
//     spends the account's ask budget before the gate, so it closes with the
//     contest like every other /play route;
//   - staff's, under /contests/{id}/clarifications and /announcements, behind
//     contest.clarify.
//
// A participant is never shown who asked anything; staff see the asker's
// login and name. The routes that name a clarification by id are staff routes,
// and one of another contest is not found. After the contest ends for a
// participant the gate refuses their feed too, as it refuses all of /play.

// ClarificationAccess is the slice of queryproxy.Service the participant
// routes need.
type ClarificationAccess interface {
	AdmitRead(userID uuid.UUID) error
	Access(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, error)
	// StartOnRead records the participant's start, which under individual
	// timing starts their clock: the feed carries the organisers' corrections
	// of the tasks, and asking is an act in the contest.
	StartOnRead(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (contests.Participant, error)
}

// Clarifications is the slice of clarifications.Service these routes call.
type Clarifications interface {
	AdmitAsk(ctx context.Context, account uuid.UUID) error
	Ask(ctx context.Context, cmd clarifications.AskCommand) (clarifications.Clarification, error)
	Feed(ctx context.Context, q clarifications.FeedQuery) (clarifications.FeedPage, error)
	Queue(ctx context.Context, q clarifications.QueueQuery) (clarifications.QueuePage, error)
	Answer(ctx context.Context, cmd clarifications.AnswerCommand) (clarifications.Asked, error)
	EditAnswer(ctx context.Context, cmd clarifications.AnswerCommand) (clarifications.Asked, error)
	Announce(ctx context.Context, cmd clarifications.AnnounceCommand) (clarifications.Announcement, error)
	Announcements(ctx context.Context, contestID uuid.UUID, limit, offset int) ([]clarifications.Announcement, int, error)
}

// ClarificationsHandler serves both sides of the clarifications.
type ClarificationsHandler struct {
	service Clarifications
	access  ClarificationAccess
	mw      *auth.Middleware
	log     *slog.Logger
	// watcher hears of every admitted participant request; nil watches
	// nothing.
	watcher Watcher
}

func NewClarificationsHandler(service Clarifications, access ClarificationAccess, mw *auth.Middleware, log *slog.Logger) *ClarificationsHandler {
	return &ClarificationsHandler{service: service, access: access, mw: mw, log: log}
}

// WithWatcher reports every admitted participant request to watcher, the one
// the play screen reports to.
func (h *ClarificationsHandler) WithWatcher(watcher Watcher) *ClarificationsHandler {
	h.watcher = watcher
	return h
}

const clarificationIDParam = "clarificationID"

// Mount registers the routes.
func (h *ClarificationsHandler) Mount(r chi.Router) {
	contest := "/contests/{" + contestIDParam + "}"
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate)
		r.Get(contest+"/play/clarifications", h.feed)
		r.Post(contest+"/play/clarifications", h.ask)

		r.Group(func(r chi.Router) {
			r.Use(h.mw.RequireContestPermission(rbac.PermissionContestClarify))
			r.Get(contest+"/clarifications", h.queue)
			r.Post(contest+"/clarifications/{"+clarificationIDParam+"}/answer", h.answer)
			r.Put(contest+"/clarifications/{"+clarificationIDParam+"}/answer", h.editAnswer)
			r.Get(contest+"/announcements", h.announcements)
			r.Post(contest+"/announcements", h.announce)
		})
	})
}

// admit resolves the caller as a participant of the contest in the URL.
func (h *ClarificationsHandler) admit(w http.ResponseWriter, r *http.Request, userID uuid.UUID) (contests.Participant, contests.Contest, bool) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return contests.Participant{}, contests.Contest{}, false
	}
	participant, contest, err := h.access.Access(r.Context(), contestID, userID, clientAddress(r))
	if err != nil {
		h.fail(w, r, err)
		return contests.Participant{}, contests.Contest{}, false
	}
	if h.watcher != nil {
		h.watcher.Observe(r.Context(), monitor.Visit{
			Contest: contest.ID, Registration: participant.ID,
			Address: clientAddress(r), Session: sessionTag(r), UserAgent: r.UserAgent(),
		})
	}
	return participant, contest, true
}

// startOnRead records the participant's start after a successful read or ask
// and before anything is sent, so a refused request starts nothing and nothing
// is sent to a participant whose start could not be recorded.
func (h *ClarificationsHandler) startOnRead(w http.ResponseWriter, r *http.Request, contest contests.Contest, participant contests.Participant) bool {
	if _, err := h.access.StartOnRead(r.Context(), contest, participant, clientAddress(r)); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

// clarificationTimeLayout keeps the database's microseconds, always six
// digits: the play screen tells a new entry from one it has seen by comparing
// these times as text.
const clarificationTimeLayout = "2006-01-02T15:04:05.000000Z"

func formatClarificationTime(t time.Time) string { return t.UTC().Format(clarificationTimeLayout) }

func optionalTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := formatClarificationTime(*t)
	return &formatted
}

func optionalID(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	text := id.String()
	return &text
}

// seenAnswerResponse is an answer as a participant reads it: no field for who
// answered.
type seenAnswerResponse struct {
	Body       string  `json:"body"`
	Declined   bool    `json:"declined"`
	Visibility string  `json:"visibility"`
	AnsweredAt string  `json:"answered_at"`
	EditedAt   *string `json:"edited_at"`
}

// seenClarificationResponse is a clarification as a participant reads it. It
// has no field for who asked: Mine is all they learn.
type seenClarificationResponse struct {
	Kind       string              `json:"kind"`
	ID         string              `json:"id"`
	PostedAt   string              `json:"posted_at"`
	Mine       bool                `json:"mine"`
	QuestionID *string             `json:"question_id"`
	Body       string              `json:"body"`
	AskedAt    string              `json:"asked_at"`
	Answer     *seenAnswerResponse `json:"answer"`
}

type seenAnnouncementResponse struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	PostedAt string `json:"posted_at"`
	Body     string `json:"body"`
}

const (
	feedKindClarification = "clarification"
	feedKindAnnouncement  = "announcement"
)

func toSeenClarification(id uuid.UUID, postedAt time.Time, s clarifications.Seen) seenClarificationResponse {
	out := seenClarificationResponse{
		Kind: feedKindClarification, ID: id.String(), PostedAt: formatClarificationTime(postedAt),
		Mine: s.Mine, QuestionID: optionalID(s.QuestionID), Body: s.Body, AskedAt: formatClarificationTime(s.AskedAt),
	}
	if a := s.Answer; a != nil {
		out.Answer = &seenAnswerResponse{
			Body: a.Body, Declined: a.Declined, Visibility: string(a.Visibility),
			AnsweredAt: formatClarificationTime(a.At), EditedAt: optionalTime(a.EditedAt),
		}
	}
	return out
}

// feedResponse is one page of a participant's feed, newest first. Items are
// seenClarificationResponse or seenAnnouncementResponse, told apart by kind.
type feedResponse struct {
	Items []any `json:"items"`
	// Next is the cursor of the next page, null on the last.
	Next *string `json:"next"`
	// Open is how many of the caller's questions wait for an answer.
	Open int `json:"open"`
}

func (h *ClarificationsHandler) feed(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	// Before any lookup, as every /play read (CLAUDE.md rule 13).
	if err := h.access.AdmitRead(identity.UserID); err != nil {
		h.fail(w, r, err)
		return
	}
	participant, contest, ok := h.admit(w, r, identity.UserID)
	if !ok {
		return
	}
	var before *clarifications.Cursor
	if text := r.URL.Query().Get("before"); text != "" {
		cursor, err := clarifications.ParseCursor(text)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		before = &cursor
	}
	page, err := h.service.Feed(r.Context(), clarifications.FeedQuery{
		ContestID: contest.ID, RegistrationID: participant.ID,
		Before: before, Limit: intParam(r, "limit"), Sequential: contest.SequentialActive(),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.startOnRead(w, r, contest, participant) {
		return
	}

	out := feedResponse{Items: make([]any, 0, len(page.Items)), Open: page.Open}
	for _, item := range page.Items {
		if item.Announcement != nil {
			out.Items = append(out.Items, seenAnnouncementResponse{
				Kind: feedKindAnnouncement, ID: item.ID.String(),
				PostedAt: formatClarificationTime(item.PostedAt), Body: item.Announcement.Body,
			})
			continue
		}
		out.Items = append(out.Items, toSeenClarification(item.ID, item.PostedAt, *item.Clarification))
	}
	if page.Next != nil {
		next := page.Next.String()
		out.Next = &next
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

// askRequest is a participant's question; question_id is optional.
type askRequest struct {
	QuestionID *string `json:"question_id"`
	Body       string  `json:"body"`
}

func (h *ClarificationsHandler) ask(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	// Charged before the gate and the body: every attempt counts, and someone
	// on no roster cannot make lookups for free (CLAUDE.md rules 5 and 13).
	if err := h.service.AdmitAsk(r.Context(), identity.UserID); err != nil {
		h.fail(w, r, err)
		return
	}
	participant, contest, ok := h.admit(w, r, identity.UserID)
	if !ok {
		return
	}
	var req askRequest
	if !decodeBody(w, r, &req) {
		return
	}
	var questionID *uuid.UUID
	if req.QuestionID != nil {
		id, err := uuid.Parse(*req.QuestionID)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidQuestionID, "Question identifier is not valid")
			return
		}
		questionID = &id
	}
	asked, err := h.service.Ask(r.Context(), clarifications.AskCommand{
		ContestID: contest.ID, RegistrationID: participant.ID, QuestionID: questionID,
		Sequential: contest.SequentialActive(), Body: req.Body,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.startOnRead(w, r, contest, participant) {
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toSeenClarification(asked.ID, asked.PostedAt(), clarifications.Seen{
		Mine: true, QuestionID: asked.QuestionID, Body: asked.Body, AskedAt: asked.CreatedAt,
	}))
}

type askerResponse struct {
	Login    string `json:"login"`
	FullName string `json:"full_name"`
}

type staffAnswerResponse struct {
	Body       string `json:"body"`
	Declined   bool   `json:"declined"`
	Visibility string `json:"visibility"`
	AnsweredAt string `json:"answered_at"`
	// AnsweredBy is the login of who answered first; empty once that account
	// is gone.
	AnsweredBy string  `json:"answered_by"`
	EditedAt   *string `json:"edited_at"`
}

// staffClarificationResponse is a clarification as its contest's staff see
// it, with who asked.
type staffClarificationResponse struct {
	ID         string               `json:"id"`
	QuestionID *string              `json:"question_id"`
	Body       string               `json:"body"`
	CreatedAt  string               `json:"created_at"`
	Asker      askerResponse        `json:"asker"`
	Answer     *staffAnswerResponse `json:"answer"`
}

func toStaffClarification(a clarifications.Asked) staffClarificationResponse {
	out := staffClarificationResponse{
		ID: a.ID.String(), QuestionID: optionalID(a.QuestionID), Body: a.Body,
		CreatedAt: formatClarificationTime(a.CreatedAt),
		Asker:     askerResponse{Login: a.AskerLogin, FullName: a.AskerFullName},
	}
	if ans := a.Answer; ans != nil {
		out.Answer = &staffAnswerResponse{
			Body: ans.Body, Declined: ans.Declined, Visibility: string(ans.Visibility),
			AnsweredAt: formatClarificationTime(ans.At), AnsweredBy: a.AnsweredByLogin, EditedAt: optionalTime(ans.EditedAt),
		}
	}
	return out
}

type countsResponse struct {
	Open     int `json:"open"`
	Answered int `json:"answered"`
}

type queueResponse struct {
	Items  []staffClarificationResponse `json:"items"`
	Counts countsResponse               `json:"counts"`
}

// queue pages ?state=open (oldest first, the default) or ?state=answered
// (most recently answered first), with ?limit, and ?after naming the last
// clarification the desk holds.
func (h *ClarificationsHandler) queue(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	after := uuid.Nil
	if text := r.URL.Query().Get("after"); text != "" {
		parsed, err := uuid.Parse(text)
		if err != nil {
			h.fail(w, r, clarifications.ErrInvalidCursor)
			return
		}
		after = parsed
	}
	page, err := h.service.Queue(r.Context(), clarifications.QueueQuery{
		ContestID: contestID, State: clarifications.State(r.URL.Query().Get("state")),
		Limit: intParam(r, "limit"), After: after,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := queueResponse{
		Items:  make([]staffClarificationResponse, 0, len(page.Items)),
		Counts: countsResponse{Open: page.Counts.Open, Answered: page.Counts.Answered},
	}
	for _, item := range page.Items {
		out.Items = append(out.Items, toStaffClarification(item))
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

// answerBody is a staff answer, or its replacement.
type answerBody struct {
	Body       string `json:"body"`
	Declined   bool   `json:"declined"`
	Visibility string `json:"visibility"`
}

func (h *ClarificationsHandler) answer(w http.ResponseWriter, r *http.Request) {
	h.writeAnswer(w, r, h.service.Answer)
}

func (h *ClarificationsHandler) editAnswer(w http.ResponseWriter, r *http.Request) {
	h.writeAnswer(w, r, h.service.EditAnswer)
}

// writeAnswer reads an answer and hands it to write. An id that cannot be one
// is not found, like one of another contest.
func (h *ClarificationsHandler) writeAnswer(w http.ResponseWriter, r *http.Request,
	write func(context.Context, clarifications.AnswerCommand) (clarifications.Asked, error)) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, clarificationIDParam))
	if err != nil {
		h.fail(w, r, clarifications.ErrNotFound)
		return
	}
	var req answerBody
	if !decodeBody(w, r, &req) {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	written, err := write(r.Context(), clarifications.AnswerCommand{
		ContestID: contestID, ClarificationID: id, Actor: identity.UserID,
		Body: req.Body, Declined: req.Declined, Visibility: clarifications.Visibility(req.Visibility),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toStaffClarification(written))
}

type announcementResponse struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func toAnnouncementResponse(a clarifications.Announcement) announcementResponse {
	return announcementResponse{ID: a.ID.String(), Body: a.Body, CreatedAt: formatClarificationTime(a.CreatedAt)}
}

type announcementsResponse struct {
	Items []announcementResponse `json:"items"`
	Total int                    `json:"total"`
}

func (h *ClarificationsHandler) announcements(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	items, total, err := h.service.Announcements(r.Context(), contestID, intParam(r, "limit"), intParam(r, "offset"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := announcementsResponse{Items: make([]announcementResponse, 0, len(items)), Total: total}
	for _, a := range items {
		out.Items = append(out.Items, toAnnouncementResponse(a))
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type announceRequest struct {
	Body string `json:"body"`
}

func (h *ClarificationsHandler) announce(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	var req announceRequest
	if !decodeBody(w, r, &req) {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	posted, err := h.service.Announce(r.Context(), clarifications.AnnounceCommand{
		ContestID: contestID, Actor: identity.UserID, Body: req.Body,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toAnnouncementResponse(posted))
}

// fail answers the gate's and the read budget's refusals as every /play route
// does, and the package's own from clarificationsErrors (CLAUDE.md rule 1).
func (h *ClarificationsHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if queryproxyErrors.answer(w, r, h.log, err) || clarificationsErrors.answer(w, r, h.log, err) {
		return
	}
	h.log.ErrorContext(r.Context(), "a clarification request failed", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

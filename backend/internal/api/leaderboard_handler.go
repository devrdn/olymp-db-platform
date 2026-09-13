package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
)

// Request budgets for the two reads anybody outside the staff can make.
const (
	// LeaderboardPublicPerMinute is per address. Generous, because a
	// university network can put a hundred students behind one address and a
	// page polls every fifteen seconds; the shared computation in
	// leaderboard.Service is what bounds the database, this bounds the API.
	LeaderboardPublicPerMinute = 120
	// leaderboardParticipantPerMinute is per account, which one person with a
	// few tabs open does not approach.
	leaderboardParticipantPerMinute = 60
	leaderboardWindow               = time.Minute
)

// LeaderboardHandler serves the contest's table to its three audiences.
type LeaderboardHandler struct {
	service       *leaderboard.Service
	limiter       *auth.Limiter
	mw            *auth.Middleware
	log           *slog.Logger
	defaultLocale string
}

// NewLeaderboardHandler returns the handler.
func NewLeaderboardHandler(service *leaderboard.Service, limiter *auth.Limiter, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *LeaderboardHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &LeaderboardHandler{service: service, limiter: limiter, mw: mw, log: log, defaultLocale: defaultLocale}
}

// Mount registers the routes.
//
// The public table sits outside authentication on purpose: the page it
// serves is open to anybody with the link (the design's decision 5). The
// participant's copy is the same table plus which row is theirs, and the
// staff's is the live one.
func (h *LeaderboardHandler) Mount(r chi.Router) {
	r.Get("/contests/{"+contestIDParam+"}/leaderboard", h.public)

	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate)
		r.Get("/contests/{"+contestIDParam+"}/play/leaderboard", h.participant)
	})
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequireContestPermission(rbac.PermissionContestView))
		r.Get("/contests/{"+contestIDParam+"}/leaderboard/live", h.live)
	})
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequireContestPermission(rbac.PermissionContestEdit))
		r.Post("/contests/{"+contestIDParam+"}/leaderboard/reveal", h.reveal)
	})
}

// leaderboardRow is one row as anybody outside the staff sees it: no
// identifier of any kind, and one label, never both.
type leaderboardRow struct {
	// Place is null for an unplaced row (winner mode, not the winner).
	Place        *int   `json:"place"`
	Label        string `json:"label"`
	Deleted      bool   `json:"deleted,omitempty"`
	Points       int    `json:"points"`
	Solved       int    `json:"solved"`
	LastScoredAt string `json:"last_scored_at,omitempty"`
	Winner       bool   `json:"winner,omitempty"`
	IsYou        bool   `json:"is_you,omitempty"`
}

type leaderboardResponse struct {
	State   string `json:"state"`
	Scoring string `json:"scoring"`
	Title   string `json:"title"`
	// FrozenAt is set while the table is frozen.
	FrozenAt string `json:"frozen_at,omitempty"`
	// EndsAt lets a frozen table say whether the contest is still going. The
	// window is not a secret: the contest lists already show it.
	EndsAt string `json:"ends_at,omitempty"`
	// GeneratedAt is when the table was computed, never when anybody last
	// answered: during a freeze the second would say that something changed.
	GeneratedAt string           `json:"generated_at"`
	Truncated   bool             `json:"truncated"`
	Rows        []leaderboardRow `json:"rows"`
}

// public is the table anybody may read.
func (h *LeaderboardHandler) public(w http.ResponseWriter, r *http.Request) {
	// Before the identifier is even parsed: a refused caller still spends its
	// own budget, and nothing past this line costs a database read for free.
	if !h.admit(w, r, "leaderboard:ip:"+addressKey(r), LeaderboardPublicPerMinute) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		h.notFound(w, r)
		return
	}
	view, err := h.service.Public(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, h.toResponse(r, view, uuid.Nil))
}

// participant is the public table plus which row is the caller's.
func (h *LeaderboardHandler) participant(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	if !h.admit(w, r, "leaderboard:user:"+identity.UserID.String(), leaderboardParticipantPerMinute) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		h.notFound(w, r)
		return
	}
	view, own, err := h.service.ForParticipant(r.Context(), id, identity.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, h.toResponse(r, view, own))
}

type staffRow struct {
	Place        *int   `json:"place"`
	Login        string `json:"login"`
	FullName     string `json:"full_name"`
	Deleted      bool   `json:"deleted,omitempty"`
	Disqualified bool   `json:"disqualified,omitempty"`
	Points       int    `json:"points"`
	Solved       int    `json:"solved"`
	LastScoredAt string `json:"last_scored_at,omitempty"`
	Winner       bool   `json:"winner,omitempty"`
}

type staffLeaderboardResponse struct {
	// Shown is what everybody but the staff sees right now.
	Shown struct {
		State    string `json:"state"`
		FrozenAt string `json:"frozen_at,omitempty"`
	} `json:"shown"`
	Scoring     string     `json:"scoring"`
	FreezeMin   *int       `json:"freeze_min"`
	Names       string     `json:"names"`
	RevealedAt  string     `json:"revealed_at,omitempty"`
	GeneratedAt string     `json:"generated_at"`
	Truncated   bool       `json:"truncated"`
	Rows        []staffRow `json:"rows"`
}

// live is the staff table.
func (h *LeaderboardHandler) live(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		h.notFound(w, r)
		return
	}
	view, err := h.service.Live(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	out := staffLeaderboardResponse{
		Scoring: view.Contest.Scoring, FreezeMin: view.Contest.LeaderboardFreezeMin,
		Names: view.Contest.LeaderboardNames, RevealedAt: formatTime(view.Contest.LeaderboardRevealedAt),
		GeneratedAt: view.GeneratedAt.UTC().Format(timeLayout), Truncated: view.Truncated,
		Rows: make([]staffRow, 0, len(view.Rows)),
	}
	out.Shown.State = view.Shown.State
	out.Shown.FrozenAt = formatTime(view.Shown.FrozenAt)
	for _, row := range view.Rows {
		out.Rows = append(out.Rows, staffRow{
			Place: place(row), Login: row.Login, FullName: row.FullName, Deleted: row.AccountDeleted,
			Disqualified: row.Disqualified, Points: row.Points, Solved: row.Solved,
			LastScoredAt: formatTime(row.LastScoredAt), Winner: row.Winner,
		})
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type revealResponse struct {
	RevealedAt string `json:"revealed_at"`
}

// reveal opens a frozen table's result.
func (h *LeaderboardHandler) reveal(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		h.notFound(w, r)
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	at, err := h.service.Reveal(r.Context(), identity.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, revealResponse{RevealedAt: at.UTC().Format(timeLayout)})
}

func (h *LeaderboardHandler) toResponse(r *http.Request, view leaderboard.View, own uuid.UUID) leaderboardResponse {
	c := view.Contest
	lang := negotiateLang(r, c.LanguageCodes(), c.DefaultLanguage(), h.defaultLocale)
	out := leaderboardResponse{
		State: view.State, Scoring: c.Scoring, Title: c.Translations[lang].Title,
		FrozenAt: formatTime(view.FrozenAt), EndsAt: formatTime(c.EndsAt),
		GeneratedAt: view.GeneratedAt.UTC().Format(timeLayout),
		Truncated:   view.Truncated, Rows: make([]leaderboardRow, 0, len(view.Rows)),
	}
	// The rows are the shared cached computation: read, never written.
	for _, row := range view.Rows {
		out.Rows = append(out.Rows, leaderboardRow{
			Place: place(row), Label: row.Label(c.LeaderboardNames), Deleted: row.AccountDeleted,
			Points: row.Points, Solved: row.Solved, LastScoredAt: formatTime(row.LastScoredAt),
			Winner: row.Winner, IsYou: own != uuid.Nil && row.Registration == own,
		})
	}
	return out
}

func place(row leaderboard.Row) *int {
	if row.Place == 0 {
		return nil
	}
	p := row.Place
	return &p
}

// admit spends one request of subject's budget.
func (h *LeaderboardHandler) admit(w http.ResponseWriter, r *http.Request, subject string, limit int) bool {
	allowed, err := h.limiter.Allow(r.Context(), subject, limit, leaderboardWindow)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not check the leaderboard rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		httpx.Error(w, r, http.StatusTooManyRequests, codeLeaderboardTooOften,
			"The leaderboard is being asked for too often; wait before asking again")
		return false
	}
	return true
}

// addressKey is the caller's address, or one shared bucket when none can be
// read: an unreadable address must not become a way around the limit.
func addressKey(r *http.Request) string {
	if addr := httpx.ClientIP(r); addr != "" {
		return addr
	}
	return "unknown"
}

// noIndex keeps a table of names out of search engines.
func noIndex(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex")
}

func (h *LeaderboardHandler) notFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotFound, codeNotFound, "Resource not found")
}

func (h *LeaderboardHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, leaderboard.ErrNotFound):
		h.notFound(w, r)
	case errors.Is(err, leaderboard.ErrNotAParticipant):
		httpx.Error(w, r, http.StatusForbidden, codeNotAParticipant, "The caller is not taking part in this contest")
	case errors.Is(err, leaderboard.ErrNotRevealable):
		httpx.Error(w, r, http.StatusConflict, codeLeaderboardNotRevealable, err.Error())
	default:
		h.log.ErrorContext(r.Context(), "the leaderboard could not be served", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

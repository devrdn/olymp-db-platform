package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
)

// Request budgets for the two reads anybody outside the staff can make.
const (
	// LeaderboardPublicPerMinute is per address, generous because a university
	// network puts a hundred students behind one address and a page polls every
	// fifteen seconds. leaderboard.Service's shared computation protects the
	// database; this protects the API.
	LeaderboardPublicPerMinute = 120
	// leaderboardParticipantPerMinute is per account.
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

// Mount registers the routes. The public table needs no authentication: the
// page is open to anyone with the link. The participant's copy marks their own
// row; the staff's is the live one.
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
// identifier, and one label, never both.
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
	// Penalty and Cells are ICPC's, and absent in every other mode.
	Penalty *int              `json:"penalty,omitempty"`
	Cells   []leaderboardCell `json:"cells,omitzero"`
}

// leaderboardCell is one question on an ICPC row, carrying what its state
// means: a solve's attempt, minute and first-solver mark; a failure's wrong
// attempts; a pending cell's attempts since the freeze and any wrong ones
// before it; nothing for an untried question. Its position in the row names the
// question.
type leaderboardCell struct {
	State    string `json:"state"`
	Attempts *int   `json:"attempts,omitempty"`
	Minute   *int   `json:"minute,omitempty"`
	First    *bool  `json:"first,omitempty"`
	Pending  *int   `json:"pending,omitempty"`
}

type leaderboardResponse struct {
	State   string `json:"state"`
	Scoring string `json:"scoring"`
	Title   string `json:"title"`
	// FrozenAt is set while the table is frozen.
	FrozenAt string `json:"frozen_at,omitempty"`
	// EndsAt lets a frozen table say whether the contest is still going; the
	// window is already public.
	EndsAt string `json:"ends_at,omitempty"`
	// GeneratedAt is when the table was computed, never the last answer: during
	// a freeze that would reveal that something changed.
	GeneratedAt string `json:"generated_at"`
	Truncated   bool   `json:"truncated"`
	// Questions names the ICPC grid's columns by letter; absent in other modes.
	Questions []string         `json:"questions,omitzero"`
	Rows      []leaderboardRow `json:"rows"`
}

func (h *LeaderboardHandler) public(w http.ResponseWriter, r *http.Request) {
	// Before the identifier is parsed: a refused caller still spends its
	// budget, and nothing below reads the database for free.
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
	// The ICPC fields, as on leaderboardRow. The staff view is not frozen, so
	// its cells are never pending.
	Penalty *int              `json:"penalty,omitempty"`
	Cells   []leaderboardCell `json:"cells,omitzero"`
}

type staffLeaderboardResponse struct {
	// Shown is what everyone but the staff sees now.
	Shown struct {
		State    string `json:"state"`
		FrozenAt string `json:"frozen_at,omitempty"`
	} `json:"shown"`
	// Status is the contest's status, not the table's: Shown.State can stay
	// "frozen" past the contest's end, so this is what tells the interface the
	// contest finished and revealing may be possible.
	Status      string     `json:"status"`
	Scoring     string     `json:"scoring"`
	FreezeMin   *int       `json:"freeze_min"`
	Names       string     `json:"names"`
	RevealedAt  string     `json:"revealed_at,omitempty"`
	GeneratedAt string     `json:"generated_at"`
	Truncated   bool       `json:"truncated"`
	Questions   []string   `json:"questions,omitzero"`
	Rows        []staffRow `json:"rows"`
}

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
		Status:  view.Contest.Status,
		Scoring: view.Contest.Scoring, FreezeMin: view.Contest.LeaderboardFreezeMin,
		Names: view.Contest.LeaderboardNames, RevealedAt: formatTime(view.Contest.LeaderboardRevealedAt),
		GeneratedAt: view.GeneratedAt.UTC().Format(timeLayout), Truncated: view.Truncated,
		Questions: questionLetters(view.Contest.Scoring, view.Questions),
		Rows:      make([]staffRow, 0, len(view.Rows)),
	}
	out.Shown.State = view.Shown.State
	out.Shown.FrozenAt = formatTime(view.Shown.FrozenAt)
	for _, row := range view.Rows {
		penalty, cells := icpcRow(view.Contest.Scoring, row)
		out.Rows = append(out.Rows, staffRow{
			Place: place(row), Login: row.Login, FullName: row.FullName, Deleted: row.AccountDeleted,
			Disqualified: row.Disqualified, Points: row.Points, Solved: row.Solved,
			LastScoredAt: formatTime(row.LastScoredAt), Winner: row.Winner, Penalty: penalty, Cells: cells,
		})
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type revealResponse struct {
	RevealedAt string `json:"revealed_at"`
}

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
		Truncated:   view.Truncated, Questions: questionLetters(c.Scoring, view.Questions),
		Rows: make([]leaderboardRow, 0, len(view.Rows)),
	}
	for _, row := range view.Rows {
		penalty, cells := icpcRow(c.Scoring, row)
		out.Rows = append(out.Rows, leaderboardRow{
			Place: place(row), Label: row.Label(c.LeaderboardNames), Deleted: row.AccountDeleted,
			Points: row.Points, Solved: row.Solved, LastScoredAt: formatTime(row.LastScoredAt),
			Winner: row.Winner, IsYou: own != uuid.Nil && row.Registration == own,
			Penalty: penalty, Cells: cells,
		})
	}
	return out
}

// questionLetters names the ICPC grid's columns; nil, so absent, in other
// modes.
func questionLetters(scoring string, n int) []string {
	if scoring != contests.ScoringICPC {
		return nil
	}
	letters := make([]string, n)
	for i := range letters {
		letters[i] = leaderboard.QuestionLetter(i)
	}
	return letters
}

// icpcRow is a row's penalty and grid; nil for both, so absent, in other modes.
func icpcRow(scoring string, row leaderboard.Row) (*int, []leaderboardCell) {
	if scoring != contests.ScoringICPC {
		return nil, nil
	}
	penalty := row.Penalty
	cells := make([]leaderboardCell, len(row.Cells))
	for i, c := range row.Cells {
		cell := leaderboardCell{State: c.State()}
		switch cell.State {
		case leaderboard.CellSolved:
			attempt, minute, first := c.SolvedOnAttempt(), c.Minute, c.First
			cell.Attempts, cell.Minute, cell.First = &attempt, &minute, &first
		case leaderboard.CellFailed:
			wrong := c.Wrong
			cell.Attempts = &wrong
		case leaderboard.CellPending:
			pending := c.Pending
			cell.Pending = &pending
			// Wrong attempts before the freeze are sent only when there were
			// any.
			if c.Wrong > 0 {
				wrong := c.Wrong
				cell.Attempts = &wrong
			}
		}
		cells[i] = cell
	}
	return &penalty, cells
}

func place(row leaderboard.Row) *int {
	if row.Place == 0 {
		return nil
	}
	p := row.Place
	return &p
}

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

// addressKey is the caller's address as a rate-limit subject (an IPv6 /64 is
// one caller), or one shared bucket when unreadable, so an unreadable address
// is no way around the limit.
func addressKey(r *http.Request) string {
	if addr := httpx.ClientSubject(r); addr != "" {
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

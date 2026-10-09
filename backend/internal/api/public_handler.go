package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/i18n"
	"github.com/devrdn/db-contest/backend/internal/showcase"
)

// The budget of the landing page's two reads.
const (
	// PublicReadsPerMinute is per address, generous because a school puts a
	// hundred browsers behind one address. The database is protected by
	// showcase.Service's minute of cache; this protects the API.
	PublicReadsPerMinute = 120
	publicWindow         = time.Minute
	// maxLangLength bounds the stated language preference (CLAUDE.md rule 2):
	// it is matched against every translation on the list, on a page that needs
	// no sign-in. A well-formed BCP 47 tag with language, script, region and
	// variant stays under 35 characters.
	maxLangLength = 35
)

// PublicHandler serves what a visitor with no session may read: the
// installation's numbers and its recent contests.
type PublicHandler struct {
	service       *showcase.Service
	limiter       *auth.Limiter
	log           *slog.Logger
	defaultLocale string
}

// NewPublicHandler returns the handler.
func NewPublicHandler(service *showcase.Service, limiter *auth.Limiter, log *slog.Logger, defaultLocale string) *PublicHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &PublicHandler{service: service, limiter: limiter, log: log, defaultLocale: defaultLocale}
}

// Mount registers the two reads a visitor without a session may make, under
// /public/ because every other /contests route requires a session.
func (h *PublicHandler) Mount(r chi.Router) {
	r.Get("/public/stats", h.stats)
	r.Get("/public/contests", h.contests)
}

type publicNumbersResponse struct {
	Contests     int64 `json:"contests"`
	Participants int64 `json:"participants"`
	Queries      int64 `json:"queries"`
	Solved       int64 `json:"solved"`
}

func (h *PublicHandler) stats(w http.ResponseWriter, r *http.Request) {
	// Before any database work; a refused caller still spends its budget
	// (CLAUDE.md rule 13).
	if !h.admit(w, r) {
		return
	}
	numbers, err := h.service.Numbers(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, publicNumbersResponse{
		Contests: numbers.Contests, Participants: numbers.Participants,
		Queries: numbers.Queries, Solved: numbers.Solved,
	})
}

// publicContest is one row of the page's list: the identifier, for the link to
// the public table, and nothing else a contest holds.
type publicContest struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// StartsAt and EndsAt are absent for a contest with no window yet.
	StartsAt string `json:"starts_at,omitempty"`
	EndsAt   string `json:"ends_at,omitempty"`
	// TableOpen says whether the card may link to the public leaderboard.
	TableOpen bool `json:"table_open"`
	// CoverHash names the card's picture, absent when the page draws a cover
	// from the identifier. The client builds the address from the hash, so a
	// replaced cover gets a new address instead of a stale cached one.
	CoverHash string `json:"cover_hash,omitempty"`
	// CoverAttribution credits an uploaded picture's author; a drawn cover has
	// none.
	CoverAttribution string `json:"cover_attribution,omitempty"`
}

type publicContestsResponse struct {
	Items []publicContest `json:"items"`
}

func (h *PublicHandler) contests(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	list, err := h.service.Recent(r.Context(), visitorLang(r, h.defaultLocale))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := publicContestsResponse{Items: make([]publicContest, 0, len(list))}
	for _, c := range list {
		out.Items = append(out.Items, publicContest{
			ID: c.ID.String(), Title: c.Title, Status: c.Status,
			StartsAt: formatTime(c.StartsAt), EndsAt: formatTime(c.EndsAt),
			TableOpen: c.TableOpen,
			CoverHash: c.CoverHash, CoverAttribution: c.CoverAttribution,
		})
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

// visitorLang is the visitor's language preference as one code. Not
// negotiateLang: each contest declares its own set and the list is one shared
// read, so the preference travels down and showcase.Service.Recent chooses per
// contest.
//
// A value longer than maxLangLength is skipped, not refused, so the visitor
// gets the default language instead of an error; header tags are read one by
// one so an unusable tag does not hide a usable one.
func visitorLang(r *http.Request, fallback string) string {
	if explicit := strings.TrimSpace(r.URL.Query().Get("lang")); explicit != "" && len(explicit) <= maxLangLength {
		return explicit
	}
	for _, preferred := range i18n.ParseAcceptLanguage(r.Header.Get("Accept-Language")) {
		if len(preferred) <= maxLangLength {
			return preferred
		}
	}
	return fallback
}

func (h *PublicHandler) admit(w http.ResponseWriter, r *http.Request) bool {
	allowed, err := h.limiter.Allow(r.Context(), "public:ip:"+addressKey(r), PublicReadsPerMinute, publicWindow)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not check the public read rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		httpx.Error(w, r, http.StatusTooManyRequests, codePublicTooOften,
			"The landing page is being asked for too often; wait before asking again")
		return false
	}
	return true
}

// fail answers a failed read. showcase refuses nothing, so the only cases are
// storage failing (a 500 and a log line) and a visitor who closed the tab,
// which is ordinary on a landing page and noted only at debug.
func (h *PublicHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if ctxErr := r.Context().Err(); ctxErr != nil {
		h.log.DebugContext(r.Context(), "the visitor left before the landing page was served", "error", ctxErr)
		return
	}
	h.log.ErrorContext(r.Context(), "the landing page could not be served", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

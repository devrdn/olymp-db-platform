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
	// PublicReadsPerMinute is per address, and generous for the same reason
	// the public leaderboard's is: a school or a university puts a hundred
	// browsers behind one address, and the page is the first thing each of
	// them loads. What bounds the database is the minute of cache in
	// showcase.Service; this bounds the API.
	PublicReadsPerMinute = 120
	publicWindow         = time.Minute
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

// Mount registers the two reads a visitor without a session may make.
//
// Outside Authenticate, beside the public leaderboard, and under /public/
// because /contests is a path whose other routes require a session: two
// access rules on one path is the mistake nobody notices later.
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
	// Before any database work: a refused caller still spends its own budget
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

// publicContest is one row of the page's list. It carries the identifier
// because the row links to the contest's public table, and nothing else a
// contest holds: no author, no roster, no settings.
type publicContest struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// StartsAt and EndsAt are absent for a contest with no window yet.
	StartsAt string `json:"starts_at,omitempty"`
	EndsAt   string `json:"ends_at,omitempty"`
	// TableOpen says whether the row may link to the public leaderboard.
	TableOpen bool `json:"table_open"`
}

// publicContestsResponse names its list "items", as every other list this
// API serves does.
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
		})
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

// visitorLang is the language the visitor asked for, as the single code the
// domain then matches against each contest's own translations.
//
// Not negotiateLang, which picks from a set of languages known before the
// read: here every contest declares its own set, and the list is one shared
// read serving every visitor at once. So the request's preference travels
// down and the choice is made per contest (showcase.Service.Recent), with the
// installation's default standing in for a request that states none. The
// value is matched and never stored, so it needs no bound of its own.
func visitorLang(r *http.Request, fallback string) string {
	if explicit := strings.TrimSpace(r.URL.Query().Get("lang")); explicit != "" {
		return explicit
	}
	if preferred := i18n.ParseAcceptLanguage(r.Header.Get("Accept-Language")); len(preferred) > 0 {
		return preferred[0]
	}
	return fallback
}

// admit spends one request of the caller's address budget.
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

// fail answers a failed read.
//
// There is one branch here and it is not a refusal: showcase refuses nothing
// (CLAUDE.md rule 1 asks for a sentinel per refusal, and these two reads have
// none to make — they take no argument, name nobody and can only fail on
// storage). The branch is the visitor who is no longer there. A landing page
// is the one screen people open and close without waiting, so a closed tab is
// an ordinary event: it is not an outage, nobody paged about it wants to see
// it, and there is no longer a connection to write a body to. It is noted at
// debug and left at that. Anything else arriving here is the database, which
// is a 500 and a line in the log.
func (h *PublicHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if ctxErr := r.Context().Err(); ctxErr != nil {
		h.log.DebugContext(r.Context(), "the visitor left before the landing page was served", "error", ctxErr)
		return
	}
	h.log.ErrorContext(r.Context(), "the landing page could not be served", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

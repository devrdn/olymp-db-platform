package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/archive"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/httpx"
)

// ArchiveReadsPerMinute is one account's budget for both archive reads. The
// archive does not change from one minute to the next, so a person browsing
// it never needs more.
const (
	ArchiveReadsPerMinute = 60
	archiveWindow         = time.Minute
)

// ArchiveHandler serves the archive of finished contests to signed-in
// readers. Results are not here: they stay on the leaderboard route.
type ArchiveHandler struct {
	service       *archive.Service
	limiter       *auth.Limiter
	mw            *auth.Middleware
	log           *slog.Logger
	defaultLocale string
}

// NewArchiveHandler returns the handler.
func NewArchiveHandler(service *archive.Service, limiter *auth.Limiter, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ArchiveHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ArchiveHandler{service: service, limiter: limiter, mw: mw, log: log, defaultLocale: defaultLocale}
}

// Mount registers the routes. The gate runs before authentication and the
// limiter, so while the archive is off every request to it, signed in or
// not, whatever the method or the identifier, meets the router's own 404 and
// spends nothing.
func (h *ArchiveHandler) Mount(r chi.Router) {
	r.Route("/archive", func(r chi.Router) {
		r.Use(h.gate, h.mw.Authenticate, noStore)
		r.Get("/contests", h.list)
		r.Get("/contests/{"+contestIDParam+"}", h.contest)
	})
}

type archiveListItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	StartsAt     string `json:"starts_at,omitempty"`
	EndsAt       string `json:"ends_at,omitempty"`
	Participants int    `json:"participants"`
}

type archiveListResponse struct {
	Items []archiveListItem `json:"items"`
	Total int               `json:"total"`
}

type archiveContestResponse struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	StartsAt     string `json:"starts_at,omitempty"`
	EndsAt       string `json:"ends_at,omitempty"`
	Participants int    `json:"participants"`
	// Tasks is absent unless the organiser allowed showing them.
	Tasks *archiveTasksResponse `json:"tasks,omitempty"`
}

type archiveTasksResponse struct {
	Lang          string                    `json:"lang"`
	StoryMD       string                    `json:"story_md"`
	SchemaNotesMD string                    `json:"schema_notes_md"`
	Questions     []archiveQuestionResponse `json:"questions"`
}

// archiveQuestionResponse carries no identifier, attempt limit or answer.
type archiveQuestionResponse struct {
	Position  int               `json:"position"`
	Kind      string            `json:"kind"`
	Points    int               `json:"points"`
	IsBonus   bool              `json:"is_bonus"`
	Title     string            `json:"title"`
	BodyMD    string            `json:"body_md"`
	ChoiceIDs []string          `json:"choice_ids"`
	Choices   map[string]string `json:"choices,omitempty"`
}

func (h *ArchiveHandler) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on, err := h.service.Enabled(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if !on {
			h.fail(w, r, archive.ErrDisabled)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *ArchiveHandler) list(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	query := r.URL.Query()
	// A malformed number reads as 0, which the service turns into the default.
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	page, err := h.service.List(r.Context(), archive.Filter{Query: query.Get("q"), Limit: limit, Offset: offset})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	lang := visitorLang(r, h.defaultLocale)
	out := archiveListResponse{Items: make([]archiveListItem, 0, len(page.Items)), Total: page.Total}
	for _, c := range page.Items {
		out.Items = append(out.Items, archiveListItem{
			ID: c.ID.String(), Title: c.TitleIn(lang), Status: c.Status,
			StartsAt: formatTime(c.StartsAt), EndsAt: formatTime(c.EndsAt), Participants: c.Participants,
		})
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h *ArchiveHandler) contest(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		h.fail(w, r, archive.ErrNotFound)
		return
	}
	c, err := h.service.Contest(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// One language for title, description and tasks, chosen among the
	// contest's own: a raw visitor language the contest lacks would find no
	// questions.
	lang := negotiateLang(r, c.Languages, c.DefaultLanguage, h.defaultLocale)
	tasks, err := h.service.Tasks(r.Context(), c, lang)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := archiveContestResponse{
		ID: c.ID.String(), Title: c.TitleIn(lang), Description: c.DescriptionIn(lang), Status: c.Status,
		StartsAt: formatTime(c.StartsAt), EndsAt: formatTime(c.EndsAt), Participants: c.Participants,
	}
	if tasks != nil {
		out.Tasks = toArchiveTasks(tasks)
	}
	noIndex(w)
	httpx.JSON(w, r, http.StatusOK, out)
}

func toArchiveTasks(t *archive.Tasks) *archiveTasksResponse {
	out := &archiveTasksResponse{
		Lang: t.Lang, StoryMD: t.Story, SchemaNotesMD: t.SchemaNotes,
		Questions: make([]archiveQuestionResponse, 0, len(t.Questions)),
	}
	for _, q := range t.Questions {
		choiceIDs := q.ChoiceIDs
		if choiceIDs == nil {
			choiceIDs = []string{}
		}
		out.Questions = append(out.Questions, archiveQuestionResponse{
			Position: q.Position, Kind: q.Kind, Points: q.Points, IsBonus: q.IsBonus,
			Title: q.Title, BodyMD: q.BodyMD, ChoiceIDs: choiceIDs, Choices: q.Choices,
		})
	}
	return out
}

// admit charges the account before any parsing or storage read, and a
// refusal is charged like a success.
func (h *ArchiveHandler) admit(w http.ResponseWriter, r *http.Request) bool {
	identity, _ := auth.IdentityFrom(r.Context())
	allowed, err := h.limiter.Allow(r.Context(), "archive:user:"+identity.UserID.String(), ArchiveReadsPerMinute, archiveWindow)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not check the archive rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(archiveWindow/time.Second)))
		httpx.Error(w, r, http.StatusTooManyRequests, codeArchiveTooOften,
			"The archive is being asked for too often; wait before asking again")
		return false
	}
	return true
}

// fail answers a switched-off archive and a contest outside it with the
// router's own 404, so neither can be told from an address that does not
// exist.
func (h *ArchiveHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, archive.ErrDisabled), errors.Is(err, archive.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "Resource not found")
	case errors.Is(err, archive.ErrQueryTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
			"The search text is longer than "+strconv.Itoa(archive.MaxQueryRunes)+" characters")
	case r.Context().Err() != nil:
		h.log.DebugContext(r.Context(), "the archive request was abandoned", "error", err)
	default:
		h.log.ErrorContext(r.Context(), "the archive could not be served", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// URL parameters. contestIDParam is also what the authorisation middleware
// reads to scope a permission.
const (
	contestIDParam  = "contestID"
	questionIDParam = "questionID"
	memberIDParam   = "userID"
)

// timeLayout is the API's timestamp format; workspace documents use
// versionLayout instead.
const timeLayout = "2006-01-02T15:04:05Z"

// ContestsHandler serves the contest constructor: the contest, its content,
// staff and participants. Creating a contest is an installation-wide
// permission; everything about one contest is scoped to it (§7), so an
// organiser never reaches somebody else's.
type ContestsHandler struct {
	service *contests.Service
	mw      *auth.Middleware
	log     *slog.Logger
	// defaultLocale answers when neither the request nor the contest decides
	// the language.
	defaultLocale string
}

// NewContestsHandler assembles the contest endpoints.
func NewContestsHandler(service *contests.Service, mw *auth.Middleware, log *slog.Logger, defaultLocale string) *ContestsHandler {
	if defaultLocale == "" {
		defaultLocale = "en"
	}
	return &ContestsHandler{service: service, mw: mw, log: log, defaultLocale: defaultLocale}
}

// Mount registers the routes under /contests.
func (h *ContestsHandler) Mount(r chi.Router) {
	r.Route("/contests", func(r chi.Router) {
		r.Use(h.mw.Authenticate)

		// The listing is scoped inside the handler: students and staff see
		// different result sets, not different rights.
		r.Get("/", h.list)
		r.With(h.mw.RequirePermission(rbac.PermissionContestCreate)).Post("/", h.create)

		r.Route("/{"+contestIDParam+"}", func(r chi.Router) {
			// Self-signup is open to any authenticated account; the contest's
			// own rules decide.
			r.Post("/enroll", h.enroll)

			r.Group(func(r chi.Router) {
				r.Use(h.mw.RequireContestPermission(rbac.PermissionContestView))
				r.Get("/", h.byID)
				r.Get("/publish-check", h.publishCheck)
				r.Get("/sql-policy", h.policy)
				r.Get("/story", h.story)
				r.Get("/questions", h.listQuestions)
				r.Get("/questions/{"+questionIDParam+"}", h.question)
				r.Get("/managers", h.listManagers)
			})

			r.Group(func(r chi.Router) {
				r.Use(h.mw.RequireContestPermission(rbac.PermissionContestEdit))
				r.Patch("/", h.update)
				// Unlike the rest of this group, reachable on a finished or
				// archived contest: extending the grace period is the one
				// exception SettingsEditable allows (§2.4).
				r.Patch("/grace", h.extendGrace)
				r.Delete("/", h.delete)
				r.Put("/languages", h.setLanguages)
				r.Put("/translations", h.setTranslations)
				r.Put("/sql-policy", h.setPolicy)
				r.Put("/story", h.setStory)
				r.Post("/questions", h.addQuestion)
				r.Put("/questions/order", h.reorderQuestions)
				r.Patch("/questions/{"+questionIDParam+"}", h.updateQuestion)
				// The whole question in one transaction: fields, wording and
				// reference answers. PATCH edits a part; PUT replaces it.
				r.Put("/questions/{"+questionIDParam+"}", h.saveQuestion)
				r.Delete("/questions/{"+questionIDParam+"}", h.deleteQuestion)
				r.Put("/questions/{"+questionIDParam+"}/texts", h.setQuestionTexts)
				r.Put("/questions/{"+questionIDParam+"}/answers", h.setAnswers)

				// The contest as a file (contest_package.go). Under
				// contest.edit, not contest.view, because the package carries
				// the reference answers (docs/ARCHITECTURE.md §15, item 12).
				r.Get("/export", h.exportPackage)
			})

			// Publishing and starting need contest.publish, not contest.edit.
			r.With(h.mw.RequireContestPermission(rbac.PermissionContestPublish)).
				Post("/status", h.setStatus)

			// Appointing staff is the owner's alone.
			r.Group(func(r chi.Router) {
				r.Use(h.mw.RequireContestPermission(rbac.PermissionContestManage))
				r.Put("/managers/{"+memberIDParam+"}", h.grantManager)
				r.Delete("/managers/{"+memberIDParam+"}", h.revokeManager)
			})

			r.Group(func(r chi.Router) {
				r.Use(h.mw.RequireContestPermission(rbac.PermissionParticipantManage))
				r.Get("/participants", h.listParticipants)
				r.Post("/participants", h.addParticipants)
				r.Delete("/participants/{"+memberIDParam+"}", h.removeParticipant)
				r.Post("/participants/{"+memberIDParam+"}/disqualify", h.disqualifyParticipant)

				// Behind participant.manage: every role that may see the people
				// screen already holds it, so this opens nothing wider.
				r.Get("/people/directory", h.directorySearch)
			})
		})
	})
}

// ContestResponse is a contest as its staff see it. A dedicated type, so a
// field added to the domain object is not published automatically.
type ContestResponse struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Enrollment   string `json:"enrollment"`
	QuestionMode string `json:"question_mode"`
	// Progression decides the order questions may be answered in (§6.1.1).
	Progression string `json:"progression"`
	// Scoring decides how a result is derived from submissions (§6.1.1).
	Scoring     string `json:"scoring"`
	Timing      string `json:"timing"`
	DurationMin *int   `json:"duration_min,omitempty"`
	StartsAt    string `json:"starts_at,omitempty"`
	EndsAt      string `json:"ends_at,omitempty"`
	// ICPCPenaltyMin is ICPC's per-attempt penalty in minutes, always present
	// even in other modes (§6.1.1).
	ICPCPenaltyMin int                            `json:"icpc_penalty_min"`
	AllowedCIDRs   []string                       `json:"allowed_cidrs"`
	Settings       SettingsResponse               `json:"settings"`
	Leaderboard    LeaderboardSettingsResponse    `json:"leaderboard"`
	Languages      []LanguageResponse             `json:"languages"`
	Translations   map[string]TranslationResponse `json:"translations"`
	CreatedAt      string                         `json:"created_at"`
	UpdatedAt      string                         `json:"updated_at"`
	// MayMonitor says whether the caller holds contest.monitor here, so the
	// workspace can offer its monitoring tab without restating the rule. Sent
	// only by GET /contests/{id}.
	MayMonitor *bool `json:"may_monitor,omitempty"`
}

// LeaderboardSettingsResponse is how the contest's table is shown. FreezeMin is
// null, not omitted, when there is no freeze.
type LeaderboardSettingsResponse struct {
	FreezeMin  *int   `json:"freeze_min"`
	Names      string `json:"names"`
	RevealedAt string `json:"revealed_at,omitempty"`
}

// leaderboardRequest changes the table's settings; absent means leave them
// alone. freeze_min has three states a *int cannot hold: a number sets the
// freeze, null removes it, no key leaves it.
type leaderboardRequest struct {
	FreezeMin json.RawMessage `json:"freeze_min"`
	Names     string          `json:"names"`
}

func (req *leaderboardRequest) apply(cmd *contests.UpdateCommand) error {
	cmd.LeaderboardNames = req.Names
	switch raw := bytes.TrimSpace(req.FreezeMin); {
	case len(raw) == 0:
	case bytes.Equal(raw, []byte("null")):
		cmd.ClearLeaderboardFreeze = true
	default:
		var minutes int
		if err := json.Unmarshal(raw, &minutes); err != nil {
			return fmt.Errorf("leaderboard.freeze_min must be a whole number of minutes or null")
		}
		cmd.LeaderboardFreezeMin = &minutes
	}
	return nil
}

// SettingsResponse mirrors contests.Settings on the wire.
type SettingsResponse struct {
	EnrollmentDeadline   string `json:"enrollment_deadline,omitempty"`
	QueryRateLimitPerMin int    `json:"query_rate_limit_per_min"`
	GracePeriodMin       int    `json:"grace_period_min"`
}

// LanguageResponse is one language a contest is offered in.
type LanguageResponse struct {
	Code      string `json:"code"`
	IsDefault bool   `json:"is_default"`
}

// TranslationResponse is the authored text in one language.
type TranslationResponse struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// ContestSummary is a contest in a listing: one negotiated title and the
// language it was served in.
type ContestSummary struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Enrollment   string `json:"enrollment"`
	QuestionMode string `json:"question_mode"`
	// Scoring and ICPCPenaltyMin let the game screen render ICPC differently
	// before showing a question.
	Scoring        string `json:"scoring"`
	ICPCPenaltyMin int    `json:"icpc_penalty_min"`
	Lang           string `json:"lang"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	StartsAt       string `json:"starts_at,omitempty"`
	EndsAt         string `json:"ends_at,omitempty"`
	// Enrolled says whether the caller is registered for this contest. It comes
	// from the authenticated identity only, never from the request.
	Enrolled bool `json:"enrolled"`
	// CoverHash names this contest's picture, absent for a drawn cover. It
	// travels on the listing because the story screen already reads it
	// (docs/design/SPEC.md §10).
	CoverHash string `json:"cover_hash,omitempty"`
	// CoverAttribution credits the picture's author; absent for a drawn cover.
	CoverAttribution string `json:"cover_attribution,omitempty"`
}

func toContestResponse(c contests.Contest) ContestResponse {
	out := ContestResponse{
		ID:             c.ID.String(),
		Status:         c.Status,
		Enrollment:     c.Enrollment,
		QuestionMode:   c.QuestionMode,
		Progression:    c.Progression,
		Scoring:        c.Scoring,
		Timing:         c.Timing,
		DurationMin:    c.DurationMin,
		StartsAt:       formatTime(c.StartsAt),
		EndsAt:         formatTime(c.EndsAt),
		ICPCPenaltyMin: c.ICPCPenaltyMin,
		AllowedCIDRs:   make([]string, 0, len(c.AllowedCIDRs)),
		Settings: SettingsResponse{
			EnrollmentDeadline:   formatTime(c.Settings.EnrollmentDeadline),
			QueryRateLimitPerMin: c.Settings.QueryRateLimitPerMin,
			GracePeriodMin:       c.Settings.GracePeriodMin,
		},
		Leaderboard: LeaderboardSettingsResponse{
			FreezeMin:  c.LeaderboardFreezeMin,
			Names:      c.LeaderboardNames,
			RevealedAt: formatTime(c.LeaderboardRevealedAt),
		},
		Languages:    make([]LanguageResponse, 0, len(c.Languages)),
		Translations: make(map[string]TranslationResponse, len(c.Translations)),
		CreatedAt:    c.CreatedAt.UTC().Format(timeLayout),
		UpdatedAt:    c.UpdatedAt.UTC().Format(timeLayout),
	}
	for _, prefix := range c.AllowedCIDRs {
		out.AllowedCIDRs = append(out.AllowedCIDRs, prefix.String())
	}
	for _, l := range c.Languages {
		out.Languages = append(out.Languages, LanguageResponse{Code: l.Code, IsDefault: l.IsDefault})
	}
	for lang, t := range c.Translations {
		out.Translations[lang] = TranslationResponse{Title: t.Title, Description: t.Description}
	}
	return out
}

func (h *ContestsHandler) toSummary(r *http.Request, c contests.Contest) ContestSummary {
	lang := h.negotiate(r, c)
	translation := c.Translations[lang]
	return ContestSummary{
		ID:             c.ID.String(),
		Status:         c.Status,
		Enrollment:     c.Enrollment,
		QuestionMode:   c.QuestionMode,
		Scoring:        c.Scoring,
		ICPCPenaltyMin: c.ICPCPenaltyMin,
		Lang:           lang,
		Title:          translation.Title,
		Description:    translation.Description,
		StartsAt:       formatTime(c.StartsAt),
		EndsAt:         formatTime(c.EndsAt),

		CoverHash:        c.CoverHash,
		CoverAttribution: c.CoverAttribution,
	}
}

// negotiate picks the language to answer in (negotiateLang, §6.2).
func (h *ContestsHandler) negotiate(r *http.Request, c contests.Contest) string {
	available := c.LanguageCodes()
	if len(available) == 0 {
		// A contest with no languages chosen yet still has authored text;
		// answer from what exists.
		available = translationCodes(c)
	}
	return negotiateLang(r, available, c.DefaultLanguage(), h.defaultLocale)
}

func translationCodes(c contests.Contest) []string {
	codes := make([]string, 0, len(c.Translations))
	for lang := range c.Translations {
		codes = append(codes, lang)
	}
	return codes
}

type contestListResponse struct {
	Items []ContestSummary `json:"items"`
	Total int              `json:"total"`
}

// list returns the contests the caller may see: staff see those they run,
// everyone sees those they take part in and the open ones, and an installation
// administrator sees all.
func (h *ContestsHandler) list(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	enrolled, err := boolParam(r, "enrolled")
	if err != nil {
		// Refused rather than ignored, so the screen never silently shows the
		// wrong list.
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	filter := contests.Filter{
		Query:  r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Limit:  intParam(r, "limit"),
		Offset: intParam(r, "offset"),
	}
	switch {
	case r.URL.Query().Get("scope") == "participant":
		filter.VisibleTo = identity.UserID
	case identity.Has(rbac.PermissionContestAdminAll):
		// Everything, unscoped.
	case identity.Has(rbac.PermissionContestCreate):
		filter.ManagedBy = identity.UserID
	default:
		filter.VisibleTo = identity.UserID
	}

	// The participant's "mine" and "the rest open to me". It only narrows the
	// scope above. Outside a participant scope there is no "me", and it would
	// silently return nothing, so it is refused.
	if enrolled != nil {
		if filter.VisibleTo == uuid.Nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
				"enrolled applies to a participant listing only; add scope=participant")
			return
		}
		filter.Enrolled = enrolled
	}

	found, total, err := h.service.List(r.Context(), filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Which contests on this page the caller is on, from the session's identity
	// only.
	ids := make([]uuid.UUID, 0, len(found))
	for _, c := range found {
		ids = append(ids, c.ID)
	}
	on, err := h.service.EnrolledIn(r.Context(), identity.UserID, ids)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]ContestSummary, 0, len(found))
	for _, c := range found {
		summary := h.toSummary(r, c)
		summary.Enrolled = on[c.ID]
		items = append(items, summary)
	}
	httpx.JSON(w, r, http.StatusOK, contestListResponse{Items: items, Total: total})
}

// boolParam reads an optional true/false query parameter. Absent is nil, a
// third answer distinct from false.
func boolParam(r *http.Request, name string) (*bool, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be true or false, got %q", name, raw)
	}
	return &value, nil
}

type contestRequest struct {
	Enrollment   string `json:"enrollment"`
	QuestionMode string `json:"question_mode"`
	// Progression and Scoring: an empty string means "leave it" on update and
	// "the domain default" on create.
	Progression string  `json:"progression"`
	Scoring     string  `json:"scoring"`
	Timing      string  `json:"timing"`
	DurationMin *int    `json:"duration_min"`
	StartsAt    *string `json:"starts_at"`
	EndsAt      *string `json:"ends_at"`
	// ICPCPenaltyMin: nil means "leave it" on update and
	// contests.DefaultICPCPenaltyMin on create.
	ICPCPenaltyMin *int                           `json:"icpc_penalty_min"`
	AllowedCIDRs   []string                       `json:"allowed_cidrs"`
	Settings       *SettingsResponse              `json:"settings"`
	Leaderboard    *leaderboardRequest            `json:"leaderboard"`
	Languages      []LanguageResponse             `json:"languages"`
	Translations   map[string]TranslationResponse `json:"translations"`
}

func (h *ContestsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req contestRequest
	if !decodeBody(w, r, &req) {
		return
	}

	starts, ends, err := req.window()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	cidrs, err := parseCIDRs(req.AllowedCIDRs)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidCIDR, err.Error())
		return
	}
	settings, err := req.settings()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	// The same decoding as update, so the two agree on what a null freeze
	// means.
	var board contests.UpdateCommand
	if req.Leaderboard != nil {
		if err := req.Leaderboard.apply(&board); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
	}

	identity, _ := auth.IdentityFrom(r.Context())
	created, err := h.service.Create(r.Context(), contests.CreateCommand{
		LeaderboardFreezeMin: board.LeaderboardFreezeMin,
		LeaderboardNames:     board.LeaderboardNames,
		ActorID:              identity.UserID,
		Enrollment:           req.Enrollment,
		QuestionMode:         req.QuestionMode,
		Progression:          req.Progression,
		Scoring:              req.Scoring,
		Timing:               req.Timing,
		DurationMin:          req.DurationMin,
		StartsAt:             starts,
		EndsAt:               ends,
		ICPCPenaltyMin:       req.ICPCPenaltyMin,
		AllowedCIDRs:         cidrs,
		Settings:             settings,
		Languages:            toDomainLanguages(req.Languages),
		Translations:         toDomainTranslations(req.Translations),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toContestResponse(created))
}

func (h *ContestsHandler) byID(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	c, err := h.service.ByID(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A second authorisation, since the middleware keeps no decision to reuse.
	// It only decides whether a tab is offered, so a failure is logged and
	// treated as "no".
	may, err := h.mw.MayOnContest(r, rbac.PermissionContestMonitor, id)
	if err != nil {
		h.log.WarnContext(r.Context(), "could not decide whether the caller may monitor the contest", "error", err)
		may = false
	}
	// Every translation, not the negotiated one: staff are editing all of them.
	out := toContestResponse(c)
	out.MayMonitor = &may
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h *ContestsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	var req contestRequest
	if !decodeBody(w, r, &req) {
		return
	}
	starts, ends, err := req.window()
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	cidrs, err := parseCIDRs(req.AllowedCIDRs)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidCIDR, err.Error())
		return
	}

	cmd := contests.UpdateCommand{
		ContestID:      id,
		Enrollment:     req.Enrollment,
		QuestionMode:   req.QuestionMode,
		Progression:    req.Progression,
		Scoring:        req.Scoring,
		Timing:         req.Timing,
		DurationMin:    req.DurationMin,
		StartsAt:       starts,
		EndsAt:         ends,
		ICPCPenaltyMin: req.ICPCPenaltyMin,
		AllowedCIDRs:   cidrs,
	}
	if req.Settings != nil {
		settings, err := req.settings()
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		cmd.Settings = &settings
	}
	if req.Leaderboard != nil {
		if err := req.Leaderboard.apply(&cmd); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
	}
	identity, _ := auth.IdentityFrom(r.Context())
	cmd.ActorID = identity.UserID

	updated, err := h.service.Update(r.Context(), cmd)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toContestResponse(updated))
}

// graceRequest carries the one field extendGrace may change, so the endpoint
// does not look as if other settings were negotiable.
type graceRequest struct {
	GracePeriodMin int `json:"grace_period_min"`
}

// extendGrace lengthens a finished or archived contest's game-database grace
// period (§2.4), the one exception to its frozen settings.
func (h *ContestsHandler) extendGrace(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	var req graceRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	updated, err := h.service.ExtendGrace(r.Context(), identity.UserID, id, req.GracePeriodMin)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toContestResponse(updated))
}

func (h *ContestsHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.Delete(r.Context(), identity.UserID, id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

type statusRequest struct {
	Status string `json:"status"`
}

func (h *ContestsHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	var req statusRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.Transition(r.Context(), identity.UserID, id, req.Status); err != nil {
		h.fail(w, r, err)
		return
	}

	c, err := h.service.ByID(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toContestResponse(c))
}

type publishCheckResponse struct {
	Ready    bool             `json:"ready"`
	Problems []problemPayload `json:"problems"`
}

type problemPayload struct {
	Code       string `json:"code"`
	Lang       string `json:"lang,omitempty"`
	QuestionID string `json:"question_id,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func (h *ContestsHandler) publishCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	err := h.service.CheckPublish(r.Context(), id)
	switch {
	case err == nil:
		httpx.JSON(w, r, http.StatusOK, publishCheckResponse{Ready: true, Problems: []problemPayload{}})
	case errors.Is(err, contests.ErrNotPublishable):
		// 200, not an error: asking what is left is not a failure.
		httpx.JSON(w, r, http.StatusOK, publishCheckResponse{
			Ready: false, Problems: problemsOf(err),
		})
	default:
		h.fail(w, r, err)
	}
}

func problemsOf(err error) []problemPayload {
	var notReady *contests.NotPublishableError
	if !errors.As(err, &notReady) {
		return nil
	}
	out := make([]problemPayload, 0, len(notReady.Problems))
	for _, p := range notReady.Problems {
		payload := problemPayload{Code: p.Code, Lang: p.Lang, Detail: p.Detail}
		if p.QuestionID != uuid.Nil {
			payload.QuestionID = p.QuestionID.String()
		}
		out = append(out, payload)
	}
	return out
}

type languagesRequest struct {
	Languages []LanguageResponse `json:"languages"`
}

func (h *ContestsHandler) setLanguages(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	var req languagesRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.SetLanguages(r.Context(), identity.UserID, id, toDomainLanguages(req.Languages)); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

type translationsRequest struct {
	Translations map[string]TranslationResponse `json:"translations"`
}

func (h *ContestsHandler) setTranslations(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	var req translationsRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.SetTranslations(r.Context(), identity.UserID, id, toDomainTranslations(req.Translations)); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// PolicyResponse is a contest's SQL access policy.
type PolicyResponse struct {
	Mode            string   `json:"mode"`
	WritableTables  []string `json:"writable_tables"`
	AllowCreateView bool     `json:"allow_create_view"`
	AllowOwnTables  bool     `json:"allow_own_tables"`
	AllowTempTables bool     `json:"allow_temp_tables"`
	AllowCatalog    bool     `json:"allow_catalog"`
	DiskQuotaRatio  int      `json:"disk_quota_ratio"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

func (h *ContestsHandler) policy(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	p, err := h.service.Policy(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toPolicyResponse(p))
}

func (h *ContestsHandler) setPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	var req PolicyResponse
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	err := h.service.SetPolicy(r.Context(), identity.UserID, id, contests.SQLPolicy{
		Mode:            req.Mode,
		WritableTables:  req.WritableTables,
		AllowCreateView: req.AllowCreateView,
		AllowOwnTables:  req.AllowOwnTables,
		AllowTempTables: req.AllowTempTables,
		AllowCatalog:    req.AllowCatalog,
		DiskQuotaRatio:  req.DiskQuotaRatio,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

func toPolicyResponse(p contests.SQLPolicy) PolicyResponse {
	out := PolicyResponse{
		Mode:            p.Mode,
		WritableTables:  p.WritableTables,
		AllowCreateView: p.AllowCreateView,
		AllowOwnTables:  p.AllowOwnTables,
		AllowTempTables: p.AllowTempTables,
		AllowCatalog:    p.AllowCatalog,
		DiskQuotaRatio:  p.DiskQuotaRatio,
	}
	out.WritableTables = emptyIfNil(out.WritableTables)
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = p.UpdatedAt.UTC().Format(timeLayout)
	}
	return out
}

// contestIDFrom reads the contest in the URL, answering 400 invalid_contest_id
// itself. GameHandler keeps its own because its routes answer invalid_request.
func contestIDFrom(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, auth.CodeInvalidContestID, "Contest identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

func (h *ContestsHandler) memberID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, memberIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidUserID, "User identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

// contestUserErrors is usersErrors except that a missing account is
// user_not_found here, where the account screens say not_found. Clients read
// both codes, so neither can change.
var contestUserErrors = usersErrors.with(
	errorRow{err: users.ErrNotFound, status: http.StatusNotFound, code: codeUserNotFound,
		message: "User not found"},
)

// fail maps a domain error to a response. The mapping lives in contestsErrors
// (errortable.go); this adds the answer that carries details and the 500
// fallback.
func (h *ContestsHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	// Checked first: no contests error wraps a users one, so the order cannot
	// change an answer.
	if contestUserErrors.answer(w, r, h.log, err) {
		return
	}
	// The publish gate's refusal carries the list of what is missing. It is
	// never wrapped around another sentinel, so checking it first changes
	// nothing else.
	var notReady *contests.NotPublishableError
	if errors.As(err, &notReady) {
		httpx.ErrorWithDetails(w, r, http.StatusUnprocessableEntity,
			codeNotPublishable, notPublishableMessage,
			map[string]any{"problems": problemsOf(err)})
		return
	}
	if contestsErrors.answer(w, r, h.log, err) {
		return
	}
	h.log.ErrorContext(r.Context(), "contest operation failed", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

func (req contestRequest) window() (*time.Time, *time.Time, error) {
	starts, err := parseTime(req.StartsAt)
	if err != nil {
		return nil, nil, fmt.Errorf("starts_at: %w", err)
	}
	ends, err := parseTime(req.EndsAt)
	if err != nil {
		return nil, nil, fmt.Errorf("ends_at: %w", err)
	}
	return starts, ends, nil
}

func (req contestRequest) settings() (contests.Settings, error) {
	if req.Settings == nil {
		return contests.Settings{}, nil
	}
	deadline, err := parseTime(&req.Settings.EnrollmentDeadline)
	if err != nil {
		return contests.Settings{}, fmt.Errorf("enrollment_deadline: %w", err)
	}
	return contests.Settings{
		EnrollmentDeadline:   deadline,
		QueryRateLimitPerMin: req.Settings.QueryRateLimitPerMin,
		GracePeriodMin:       req.Settings.GracePeriodMin,
	}, nil
}

func parseTime(value *string) (*time.Time, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil, errors.New("must be an RFC 3339 timestamp")
	}
	utc := parsed.UTC()
	return &utc, nil
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

// parseCIDRs turns the request's networks into prefixes. nil means "leave the
// restriction alone" and empty means "clear it", so an empty list returns a
// non-nil slice.
func parseCIDRs(values []string) ([]netip.Prefix, error) {
	if values == nil {
		return nil, nil
	}
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid network in CIDR notation", value)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func toDomainLanguages(in []LanguageResponse) []contests.ContestLanguage {
	out := make([]contests.ContestLanguage, 0, len(in))
	for _, l := range in {
		out = append(out, contests.ContestLanguage{Code: l.Code, IsDefault: l.IsDefault})
	}
	return out
}

func toDomainTranslations(in map[string]TranslationResponse) []contests.Translation {
	out := make([]contests.Translation, 0, len(in))
	for lang, t := range in {
		out = append(out, contests.Translation{Lang: lang, Title: t.Title, Description: t.Description})
	}
	return out
}

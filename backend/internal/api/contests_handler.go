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

// URL parameters. contestIDParam matches what the authorisation middleware
// reads to scope a permission, so the two cannot drift apart.
const (
	contestIDParam  = "contestID"
	questionIDParam = "questionID"
	memberIDParam   = "userID"
)

// timeLayout is the timestamp format the API speaks. The one exception is a
// workspace document's updated_at, which keeps its fractional seconds because
// the interface compares it for equality — see versionLayout in
// participant_workspace.go.
const timeLayout = "2006-01-02T15:04:05Z"

// ContestsHandler serves the contest constructor: the contest itself, its
// content, its staff and its participants.
//
// Authorisation is two-level throughout (§7): creating a contest is an
// installation-wide permission, everything about one contest is scoped to it,
// so an organizer never reaches somebody else's olympiad.
type ContestsHandler struct {
	service *contests.Service
	mw      *auth.Middleware
	log     *slog.Logger
	// defaultLocale answers when a request expresses no usable preference and
	// the contest narrows nothing down.
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

		// Listing is scoped inside the handler rather than by a permission:
		// what a student sees and what staff see are different result sets,
		// not different rights over the same one.
		r.Get("/", h.list)
		r.With(h.mw.RequirePermission(rbac.PermissionContestCreate)).Post("/", h.create)

		r.Route("/{"+contestIDParam+"}", func(r chi.Router) {
			// Self-signup is the one participant action here, and it is open
			// to any authenticated account: the contest's own rules decide.
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
				// Reachable on a finished or archived contest, unlike
				// everything else in this group — ExtendGrace is the one
				// exception SettingsEditable itself carves out, and this
				// route exists so an organizer has more recourse than
				// hand-written SQL to use it (§2.4).
				r.Patch("/grace", h.extendGrace)
				r.Delete("/", h.delete)
				r.Put("/languages", h.setLanguages)
				r.Put("/translations", h.setTranslations)
				r.Put("/sql-policy", h.setPolicy)
				r.Put("/story", h.setStory)
				r.Post("/questions", h.addQuestion)
				r.Put("/questions/order", h.reorderQuestions)
				r.Patch("/questions/{"+questionIDParam+"}", h.updateQuestion)
				// The whole question in one request: its fields, its wording
				// and its reference answers, in one transaction. PATCH edits a
				// part; PUT replaces the thing.
				r.Put("/questions/{"+questionIDParam+"}", h.saveQuestion)
				r.Delete("/questions/{"+questionIDParam+"}", h.deleteQuestion)
				r.Put("/questions/{"+questionIDParam+"}/texts", h.setQuestionTexts)
				r.Put("/questions/{"+questionIDParam+"}/answers", h.setAnswers)

				// The contest as a file (contest_package.go). In this group
				// and not the contest.view one above, deliberately: the
				// package carries the reference answers, so it belongs to the
				// permission that means "may write those answers" rather than
				// the one that means "may look at this contest"
				// (docs/ARCHITECTURE.md §15, item 12). A GET among writes is
				// what that decision costs, and it is the cheaper of the two
				// prices.
				r.Get("/export", h.exportPackage)
			})

			// Appointing staff is the owner's alone.
			r.With(h.mw.RequireContestPermission(rbac.PermissionContestPublish)).
				Post("/status", h.setStatus)

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

				// Behind participant.manage rather than a permission of its own:
				// every contest role that may see this screen's staff and
				// participants already carries it (rbac's managerPermissions
				// grants contest.view and participant.manage together), so this
				// is not a wider door than the people screen itself.
				r.Get("/people/directory", h.directorySearch)
			})
		})
	})
}

// ContestResponse is a contest as its staff see it.
//
// A dedicated type rather than the domain object, for the same reason accounts
// have one: direct serialisation publishes whatever field is added next.
type ContestResponse struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Enrollment   string `json:"enrollment"`
	QuestionMode string `json:"question_mode"`
	// Progression decides the order questions may be answered in (§6.1.1):
	// contests.ProgressionFree or contests.ProgressionSequential.
	Progression string `json:"progression"`
	// Scoring decides how a result is derived from submissions (§6.1.1):
	// contests.ScoringPoints, contests.ScoringWinner or contests.ScoringICPC.
	Scoring     string `json:"scoring"`
	Timing      string `json:"timing"`
	DurationMin *int   `json:"duration_min,omitempty"`
	StartsAt    string `json:"starts_at,omitempty"`
	EndsAt      string `json:"ends_at,omitempty"`
	// ICPCPenaltyMin is the per-attempt penalty, in minutes, ICPC scoring
	// applies to a solved question — meaningless in every other mode, but
	// always present (docs/superpowers/specs/2026-09-13-icpc-scoring-design.md).
	ICPCPenaltyMin int                            `json:"icpc_penalty_min"`
	AllowedCIDRs   []string                       `json:"allowed_cidrs"`
	Settings       SettingsResponse               `json:"settings"`
	Leaderboard    LeaderboardSettingsResponse    `json:"leaderboard"`
	Languages      []LanguageResponse             `json:"languages"`
	Translations   map[string]TranslationResponse `json:"translations"`
	CreatedAt      string                         `json:"created_at"`
	UpdatedAt      string                         `json:"updated_at"`
	// MayMonitor says whether the caller holds contest.monitor on this
	// contest, decided by rbac as the monitoring routes decide it, so the
	// workspace offers its monitoring tab without restating the rule. Sent
	// only by GET /contests/{id}; the writes answer without it.
	MayMonitor *bool `json:"may_monitor,omitempty"`
}

// LeaderboardSettingsResponse is how the contest's table is shown.
//
// FreezeMin is sent as null rather than omitted when there is no freeze, so a
// client reads "no freeze" instead of guessing at a missing key.
type LeaderboardSettingsResponse struct {
	FreezeMin  *int   `json:"freeze_min"`
	Names      string `json:"names"`
	RevealedAt string `json:"revealed_at,omitempty"`
}

// leaderboardRequest changes the table's settings. Present means "set these";
// the whole object absent means "leave them alone". Inside it, freeze_min
// distinguishes three things a *int cannot: a number sets the freeze, null
// removes it, and no key leaves it as it was.
type leaderboardRequest struct {
	FreezeMin json.RawMessage `json:"freeze_min"`
	Names     string          `json:"names"`
}

// apply writes the request onto cmd.
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

// ContestSummary is a contest in a listing: one negotiated title rather than
// every translation, plus the language it was actually served in.
type ContestSummary struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Enrollment   string `json:"enrollment"`
	QuestionMode string `json:"question_mode"`
	// Scoring and ICPCPenaltyMin let the game screen tell ICPC scoring apart
	// from points and winner before it renders a question (design doc: no
	// points shown, a penalty note under the question list).
	Scoring        string `json:"scoring"`
	ICPCPenaltyMin int    `json:"icpc_penalty_min"`
	Lang           string `json:"lang"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	StartsAt       string `json:"starts_at,omitempty"`
	EndsAt         string `json:"ends_at,omitempty"`
	// Enrolled says whether the caller is registered for this contest, and
	// only ever about the caller: it is filled from the authenticated
	// identity, never from anything the request carries. Without it a
	// catalogue cannot tell "join" from "you are already in", and offering the
	// button anyway turns an ordinary state into an error message.
	Enrolled bool `json:"enrolled"`
	// CoverHash names the picture this contest wears, and is absent for one
	// wearing a drawn cover. It travels on the listing because the screen
	// that shows a picture above a story (design spec §10) already reads
	// this listing and opens under a timer — see contests.Contest.CoverHash.
	CoverHash string `json:"cover_hash,omitempty"`
	// CoverAttribution credits whoever made that picture; a drawn cover has
	// nobody to credit, and the field is absent there too.
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

// negotiate picks the language to answer in.
//
// A thin wrapper over negotiateLang, which every language-dependent handler
// in this package shares — see its doc for the resolution order (§6.2).
func (h *ContestsHandler) negotiate(r *http.Request, c contests.Contest) string {
	available := c.LanguageCodes()
	if len(available) == 0 {
		// A contest that has not chosen its languages yet still has authored
		// text; answering from what exists beats answering with nothing.
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

// list returns the contests the caller may see.
//
// Two different result sets, not two different rights: staff see the contests
// they run, everybody sees the ones they take part in and the open ones. An
// installation administrator sees all of them.
func (h *ContestsHandler) list(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	enrolled, err := boolParam(r, "enrolled")
	if err != nil {
		// Refused rather than dropped: ignoring it would answer a different
		// question from the one asked, and the screen would quietly show the
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

	// The narrowing into "mine" and "the rest of what is open to me" — the
	// participant's two screens. It only ever narrows what the scope above
	// already allows, so it cannot become a way to see more.
	//
	// Outside a participant scope there is no "me" for it to be about: an
	// organizer's register answers "what do I run". Applied there it would
	// compare against a null identity and quietly return nothing, which is the
	// same sin as ignoring a value that could not be parsed.
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

	// Which of this page the caller is on. One query for the page, and about
	// the caller alone: the identity comes from the session the middleware
	// authenticated, so no parameter can point it at anybody else.
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

// boolParam reads an optional true/false query parameter.
//
// Absent is nil, which is a third answer and not the same as false: "every
// contest I may see" and "the ones I am not on" are different questions.
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
	// Progression and Scoring follow Enrollment/QuestionMode's own rule: an
	// empty string on update means "leave it alone" (see UpdateCommand's
	// doc), and on create means "use the domain's default" (see
	// CreateCommand's doc) — neither is a value an organizer can mean to set.
	Progression string  `json:"progression"`
	Scoring     string  `json:"scoring"`
	Timing      string  `json:"timing"`
	DurationMin *int    `json:"duration_min"`
	StartsAt    *string `json:"starts_at"`
	EndsAt      *string `json:"ends_at"`
	// ICPCPenaltyMin follows DurationMin's own rule: nil on update means
	// "leave it alone", and nil on create means "use the domain's default"
	// (contests.DefaultICPCPenaltyMin) — a whole number of minutes is the
	// only value an organizer can mean to set.
	ICPCPenaltyMin *int                           `json:"icpc_penalty_min"`
	AllowedCIDRs   []string                       `json:"allowed_cidrs"`
	Settings       *SettingsResponse              `json:"settings"`
	Leaderboard    *leaderboardRequest            `json:"leaderboard"`
	Languages      []LanguageResponse             `json:"languages"`
	Translations   map[string]TranslationResponse `json:"translations"`
}

func (h *ContestsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req contestRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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
	// The same decoding as update, so the two cannot disagree about what a
	// freeze of null means; on create "clear" and "absent" are both no freeze.
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
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	c, err := h.service.ByID(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A second authorisation after the one that admitted the request: the
	// middleware keeps no decision to reuse, and the lookup is one indexed
	// row. It only decides whether a tab is offered, so a failure to decide
	// is logged and answered as "no" rather than costing the whole page —
	// the monitoring routes make their own decision anyway.
	may, err := h.mw.MayOnContest(r, rbac.PermissionContestMonitor, id)
	if err != nil {
		h.log.WarnContext(r.Context(), "could not decide whether the caller may monitor the contest", "error", err)
		may = false
	}
	// Every translation, not the negotiated one: staff are authoring them, and
	// showing only one would make the others invisible in the editor.
	out := toContestResponse(c)
	out.MayMonitor = &may
	httpx.JSON(w, r, http.StatusOK, out)
}

func (h *ContestsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req contestRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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

// graceRequest carries the one field extendGrace may change — a narrow body
// for a narrow endpoint, rather than routing it through contestRequest's
// full settings object, which would read as though the rest of settings were
// negotiable here too.
type graceRequest struct {
	GracePeriodMin int `json:"grace_period_min"`
}

// extendGrace lengthens a finished (or archived) contest's game-database
// grace period (§2.4, contests.Service.ExtendGrace) — the one exception to a
// finished contest's otherwise-frozen settings.
func (h *ContestsHandler) extendGrace(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req graceRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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
	id, ok := h.contestID(w, r)
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

// setStatus is mounted separately from editing: publishing and starting are
// the contest.publish permission, not contest.edit.
func (h *ContestsHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req statusRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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

// publishCheckResponse reports the remaining work.
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
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	err := h.service.CheckPublish(r.Context(), id)
	switch {
	case err == nil:
		httpx.JSON(w, r, http.StatusOK, publishCheckResponse{Ready: true, Problems: []problemPayload{}})
	case errors.Is(err, contests.ErrNotPublishable):
		// 200, not an error: being asked what is left is not a failure.
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
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req languagesRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req translationsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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
	id, ok := h.contestID(w, r)
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
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req PolicyResponse
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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
	if out.WritableTables == nil {
		out.WritableTables = []string{}
	}
	if !p.UpdatedAt.IsZero() {
		out.UpdatedAt = p.UpdatedAt.UTC().Format(timeLayout)
	}
	return out
}

// contestID reads and validates the contest in the URL.
func (h *ContestsHandler) contestID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, auth.CodeInvalidContestID, "Contest identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

// memberID reads the account named in the URL of a staff or participant route.
func (h *ContestsHandler) memberID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, memberIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidUserID, "User identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

// fail maps a domain error onto a response.
//
// The mapping is the API's contract: 404 for things that are not there, 400
// for a request that could never be right, 409 for one that is right but not
// now, 422 for a publication that is simply not ready yet.
func (h *ContestsHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, contests.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "Contest not found")
	case errors.Is(err, contests.ErrQuestionNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeQuestionNotFound, "Question not found")
	case errors.Is(err, contests.ErrStoryNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeStoryNotFound, "This contest has no story yet")
	case errors.Is(err, contests.ErrParticipantNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeParticipantNotFound, "Participant not found")
	case errors.Is(err, contests.ErrManagerNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeManagerNotFound, "This user does not staff the contest")
	case errors.Is(err, users.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeUserNotFound, "User not found")
	case errors.Is(err, users.ErrAccountDeleted):
		httpx.Error(w, r, http.StatusConflict, codeAccountDeleted, "This account is deleted")
	case errors.Is(err, users.ErrAccountBlocked):
		// The same wire code auth's own sign-in refusal answers with — the
		// client's dictionary already carries a message for it — reused
		// rather than declared a second time under a name of its own.
		httpx.Error(w, r, http.StatusConflict, codeAccountBlocked, "This account is blocked")

	case errors.Is(err, contests.ErrPackageTooLarge):
		// 422 rather than 500: the request was understood and the contest is
		// real, it simply carries more questions than one package holds. Its
		// own code rather than invalid_request, because the caller sent no
		// field to correct — what has to change is the contest.
		httpx.Error(w, r, http.StatusUnprocessableEntity, codePackageTooLarge, err.Error())

	case errors.Is(err, contests.ErrNotPublishable):
		// The gate answers with a code and the whole list of what is missing.
		// It goes through the same helper as every other error rather than
		// building its own envelope: writing one by hand is how this code
		// once reached clients undeclared, with no message in any language.
		httpx.ErrorWithDetails(w, r, http.StatusUnprocessableEntity,
			codeNotPublishable, "The contest is not ready to publish",
			map[string]any{"problems": problemsOf(err)})

	case errors.Is(err, contests.ErrInvalidTransition):
		httpx.Error(w, r, http.StatusConflict, codeInvalidTransition, err.Error())
	case errors.Is(err, contests.ErrStatusChanged):
		// Its own code, not invalid_transition: the caller asked for something
		// that was legal when they asked, so the interface tells them to look
		// again rather than that they were wrong.
		httpx.Error(w, r, http.StatusConflict, codeStatusChanged, err.Error())
	case errors.Is(err, contests.ErrNotEditable):
		httpx.Error(w, r, http.StatusConflict, codeNotEditable, err.Error())
	case errors.Is(err, contests.ErrFreezeAlreadyReached):
		httpx.Error(w, r, http.StatusConflict, codeFreezeAlreadyReached, err.Error())
	case errors.Is(err, contests.ErrICPCStartLocked):
		httpx.Error(w, r, http.StatusConflict, codeICPCStartLocked, err.Error())
	case errors.Is(err, contests.ErrOwnerImmutable):
		httpx.Error(w, r, http.StatusConflict, codeOwnerImmutable, err.Error())
	case errors.Is(err, contests.ErrAlreadyEnrolled):
		httpx.Error(w, r, http.StatusConflict, codeAlreadyEnrolled, err.Error())
	case errors.Is(err, contests.ErrEnrollmentClosed):
		httpx.Error(w, r, http.StatusConflict, codeEnrollmentClosed, err.Error())
	case errors.Is(err, contests.ErrParticipantStarted):
		httpx.Error(w, r, http.StatusConflict, codeParticipantStarted, err.Error())
	case errors.Is(err, contests.ErrStaffCannotParticipate):
		httpx.Error(w, r, http.StatusConflict, codeStaffCannotParticipate, err.Error())
	case errors.Is(err, contests.ErrParticipantCannotBeStaff):
		httpx.Error(w, r, http.StatusConflict, codeParticipantCannotBeStaff, err.Error())

	case errors.Is(err, contests.ErrAddressNotAllowed):
		// Deliberately explicit: "you are on the wrong network" is something
		// the participant can act on, unlike a bare 403.
		httpx.Error(w, r, http.StatusForbidden, codeAddressNotAllowed,
			"This contest is only available from the university network")

	case errors.Is(err, contests.ErrInvalidContest),
		errors.Is(err, contests.ErrInvalidQuestion),
		errors.Is(err, contests.ErrInvalidAnswer),
		errors.Is(err, contests.ErrInvalidPolicy),
		errors.Is(err, contests.ErrInvalidRole),
		errors.Is(err, contests.ErrUnknownLanguage),
		errors.Is(err, contests.ErrRosterTooLarge),
		errors.Is(err, contests.ErrQueryTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())

	default:
		h.log.ErrorContext(r.Context(), "contest operation failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

// window parses the schedule out of a request.
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

// parseCIDRs turns the request's networks into prefixes.
//
// A nil list means "leave the restriction alone" and an empty one means "clear
// it", which is why the empty slice is returned non-nil.
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

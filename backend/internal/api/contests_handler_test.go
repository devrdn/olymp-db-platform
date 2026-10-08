package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// contestFixture mounts the contest endpoints behind a session for an account
// holding the given installation-wide permissions.
type contestFixture struct {
	router   http.Handler
	service  *contests.Service
	stores   *conteststest.Fixture
	sessions *auth.SessionStore
	actor    users.User
	cookie   *http.Cookie
}

func newContestFixture(t *testing.T, permissions ...string) *contestFixture {
	t.Helper()
	return newContestFixtureDenying(t, "", permissions...)
}

// newContestFixtureDenying is newContestFixture with one permission refused
// on every contest, whatever the role: the real authoriser cannot express a
// contest role that sees a contest but may not monitor it, and a field that
// reports the difference needs a test that can make it.
func newContestFixtureDenying(t *testing.T, denied string, permissions ...string) *contestFixture {
	t.Helper()
	return newContestFixtureAuthz(t, func(inner auth.Authorizer) auth.Authorizer {
		return denying{inner: inner, denied: denied}
	}, permissions...)
}

// newContestFixtureAuthz is newContestFixture with the real authoriser
// wrapped by wrap.
func newContestFixtureAuthz(t *testing.T, wrap func(auth.Authorizer) auth.Authorizer, permissions ...string) *contestFixture {
	t.Helper()

	stores := conteststest.NewFixture()
	stores.Users.GrantRole("staff", permissions...)
	actor := stores.Users.Add(users.User{
		Login: "organizer", FullName: "Organizer", Status: users.StatusActive, Roles: []string{"staff"},
	})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users,
		Authorizer: wrap(rbac.New(contestRoles{stores})),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewContestsHandler(stores.Service, mw, log, "en").Mount(router)

	return &contestFixture{
		router:   router,
		service:  stores.Service,
		stores:   stores,
		sessions: sessions,
		actor:    actor,
		cookie:   &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

// denying refuses one permission on every contest and asks inner about the rest.
type denying struct {
	inner  auth.Authorizer
	denied string
}

func (d denying) Authorize(ctx context.Context, id rbac.Identity, permission string, contestID uuid.UUID) error {
	if permission == d.denied {
		return rbac.ErrForbidden
	}
	return d.inner.Authorize(ctx, id, permission, contestID)
}

// failing cannot decide one permission: the role lookup behind it failed.
type failing struct {
	inner      auth.Authorizer
	permission string
}

func (f failing) Authorize(ctx context.Context, id rbac.Identity, permission string, contestID uuid.UUID) error {
	if permission == f.permission {
		return errors.New("role lookup failed")
	}
	return f.inner.Authorize(ctx, id, permission, contestID)
}

// contestRoles resolves a contest role out of the in-memory staff store, so
// authorisation in these tests runs the real two-level check.
type contestRoles struct {
	stores *conteststest.Fixture
}

func (r contestRoles) ContestRole(ctx context.Context, userID, contestID uuid.UUID) (rbac.ContestRole, error) {
	m, err := r.stores.Managers.Get(ctx, contestID, userID)
	if err != nil {
		return rbac.RoleNone, nil
	}
	return m.Role, nil
}

func (f *contestFixture) do(method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// decode reads a JSON response body into a map.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

// ownedContest seeds a contest the fixture's actor owns.
func (f *contestFixture) ownedContest(t *testing.T, status string) contests.Contest {
	t.Helper()
	c := f.stores.SeedContest(status)
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: f.actor.ID, Role: rbac.RoleOwner, GrantedBy: f.actor.ID,
	}); err != nil {
		t.Fatalf("Grant() returned error: %v", err)
	}
	return c
}

func TestCreatingAContestReturnsIt(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestCreate)

	rec := f.do(http.MethodPost, "/contests", `{
		"languages": [{"code": "en", "is_default": true}],
		"translations": {"en": {"title": "The Library Murder"}}
	}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["status"] != contests.StatusDraft {
		t.Errorf("status = %v, want draft", body["status"])
	}
}

func TestCreatingAContestNeedsThePermission(t *testing.T) {
	f := newContestFixture(t)

	rec := f.do(http.MethodPost, "/contests", `{}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestReadingAContestNeedsToStaffIt(t *testing.T) {
	// The second level of authorisation: an organizer who did not create this
	// contest has no business reading its answers.
	f := newContestFixture(t, rbac.PermissionContestCreate)
	other := f.stores.SeedContest(contests.StatusDraft)

	rec := f.do(http.MethodGet, "/contests/"+other.ID.String(), "")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestStaffSeeEveryTranslationOfTheirContest(t *testing.T) {
	// They are authoring them: showing only the negotiated one would make the
	// others invisible in the editor.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	if err := f.service.SetLanguages(t.Context(), f.actor.ID, c.ID,
		[]contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}}); err != nil {
		t.Fatalf("SetLanguages() returned error: %v", err)
	}
	if err := f.service.SetTranslations(t.Context(), f.actor.ID, c.ID, []contests.Translation{
		{Lang: "en", Title: "The Library Murder"},
		{Lang: "ro", Title: "Crima din bibliotecă"},
	}); err != nil {
		t.Fatalf("SetTranslations() returned error: %v", err)
	}

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	translations, ok := decode(t, rec)["translations"].(map[string]any)
	if !ok || len(translations) != 2 {
		t.Errorf("translations = %v, want both languages", translations)
	}
}

// The workspace offers its monitoring tab from this field: it is the one
// authority on whether the viewer holds contest.monitor on this contest.
func TestReadingAContestSaysWhetherTheViewerMayMonitorIt(t *testing.T) {
	t.Run("the owner may", func(t *testing.T) {
		f := newContestFixture(t)
		c := f.ownedContest(t, contests.StatusDraft)

		rec := f.do(http.MethodGet, "/contests/"+c.ID.String(), "")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if got := decode(t, rec)["may_monitor"]; got != true {
			t.Errorf("may_monitor = %v, want true", got)
		}
	})

	t.Run("an administrator of every contest may", func(t *testing.T) {
		f := newContestFixture(t, rbac.PermissionContestAdminAll)
		c := f.stores.SeedContest(contests.StatusDraft)

		rec := f.do(http.MethodGet, "/contests/"+c.ID.String(), "")

		if got := decode(t, rec)["may_monitor"]; got != true {
			t.Errorf("may_monitor = %v, want true", got)
		}
	})

	// The field decides one tab; a failure to decide it must not cost the
	// whole contest page.
	t.Run("a decision that could not be made is a no, not a 500", func(t *testing.T) {
		f := newContestFixtureAuthz(t, func(inner auth.Authorizer) auth.Authorizer {
			return failing{inner: inner, permission: rbac.PermissionContestMonitor}
		})
		c := f.ownedContest(t, contests.StatusDraft)

		rec := f.do(http.MethodGet, "/contests/"+c.ID.String(), "")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if got, ok := decode(t, rec)["may_monitor"]; !ok || got != false {
			t.Errorf("may_monitor = %v (present %v), want false", got, ok)
		}
	})

	t.Run("staff without the permission may not", func(t *testing.T) {
		f := newContestFixtureDenying(t, rbac.PermissionContestMonitor)
		c := f.ownedContest(t, contests.StatusDraft)

		rec := f.do(http.MethodGet, "/contests/"+c.ID.String(), "")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if got, ok := decode(t, rec)["may_monitor"]; !ok || got != false {
			t.Errorf("may_monitor = %v (present %v), want false", got, ok)
		}
	})
}

func TestListingAnswersInTheRequestedLanguage(t *testing.T) {
	// The title lives only in the translations, so a listing has to negotiate
	// one — this is the single place that decision is made (§6.2).
	f := newContestFixture(t, rbac.PermissionContestCreate)
	c := f.ownedContest(t, contests.StatusDraft)
	// The contest has to offer the language, not merely have text in it: a
	// title in Romanian over a story in English is the mixture the declared
	// set exists to prevent.
	if err := f.service.SetLanguages(t.Context(), f.actor.ID, c.ID,
		[]contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}}); err != nil {
		t.Fatalf("SetLanguages() returned error: %v", err)
	}
	if err := f.service.SetTranslations(t.Context(), f.actor.ID, c.ID, []contests.Translation{
		{Lang: "en", Title: "The Library Murder"},
		{Lang: "ro", Title: "Crima din bibliotecă"},
	}); err != nil {
		t.Fatalf("SetTranslations() returned error: %v", err)
	}

	rec := f.do(http.MethodGet, "/contests?lang=ro", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	items, _ := decode(t, rec)["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no contests listed: %s", rec.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if first["title"] != "Crima din bibliotecă" {
		t.Errorf("title = %v, want the Romanian one", first["title"])
	}
}

func TestListingFallsBackWhenTheRequestedLanguageIsMissing(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestCreate)
	c := f.ownedContest(t, contests.StatusDraft)
	if err := f.service.SetTranslations(t.Context(), f.actor.ID, c.ID, []contests.Translation{
		{Lang: "en", Title: "The Library Murder"},
	}); err != nil {
		t.Fatalf("SetTranslations() returned error: %v", err)
	}

	rec := f.do(http.MethodGet, "/contests?lang=ru", "")

	items, _ := decode(t, rec)["items"].([]any)
	first, _ := items[0].(map[string]any)
	if first["title"] != "The Library Murder" {
		t.Errorf("title = %v, want the fallback language", first["title"])
	}
	if first["lang"] != "en" {
		t.Errorf("lang = %v, want the language actually served", first["lang"])
	}
}

// The picture above a story rides on the listing rather than on a request of
// its own (design spec §10). The screen that shows it is the participant's
// play screen, which opens under a timer and already reads this listing; a
// second round trip for one hash is one this product cannot spend there.
func TestListingCarriesTheCoverAContestWears(t *testing.T) {
	f := newContestFixture(t)
	_, cookie := f.asParticipant(t, "s.popescu")
	c := f.stores.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	c.CoverHash = "9f86d081884c7d65"
	c.CoverAttribution = "Photo: A. Organiser, CC BY 4.0"
	f.stores.Contests.Put(c)

	rec := f.asked(t, "/contests?scope=participant", cookie)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	items, _ := decode(t, rec)["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no contests listed: %s", rec.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if first["cover_hash"] != "9f86d081884c7d65" {
		t.Errorf("cover_hash = %v, want the hash the cover was stored under", first["cover_hash"])
	}
	if first["cover_attribution"] != "Photo: A. Organiser, CC BY 4.0" {
		t.Errorf("cover_attribution = %v, want the credit line the picture is published with",
			first["cover_attribution"])
	}
}

// A contest nobody uploaded a picture for wears a drawn cover, and the
// listing says nothing rather than sending two empty strings for every row
// of a register that shows no pictures at all (design spec §10).
func TestListingSaysNothingAboutTheCoverOfAContestThatWearsADrawnOne(t *testing.T) {
	f := newContestFixture(t)
	_, cookie := f.asParticipant(t, "s.popescu")
	open := f.stores.SeedContest(contests.StatusPublished)
	open.Enrollment = contests.EnrollmentOpen
	f.stores.Contests.Put(open)

	rec := f.asked(t, "/contests?scope=participant", cookie)

	items, _ := decode(t, rec)["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no contests listed: %s", rec.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if got, ok := first["cover_hash"]; ok {
		t.Errorf("cover_hash = %v, want it absent for a contest with no uploaded picture", got)
	}
	if got, ok := first["cover_attribution"]; ok {
		t.Errorf("cover_attribution = %v, want it absent for a contest with no uploaded picture", got)
	}
}

func TestPublishCheckListsWhatIsMissing(t *testing.T) {
	// The constructor screen shows the remaining work rather than making an
	// organizer discover it by being refused.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/publish-check", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["ready"] != false {
		t.Errorf("ready = %v, want false", body["ready"])
	}
	problems, _ := body["problems"].([]any)
	if len(problems) == 0 {
		t.Errorf("problems = %v, want the missing story and questions named", problems)
	}
}

func TestPublishingAnIncompleteContestIsRefusedWithItsReasons(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/status", `{"status": "published"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "not_publishable" {
		t.Errorf("error code = %q, want not_publishable", code)
	}
	// The refusal names what is missing. The shared table answers the same
	// status and code without it, so this is what keeps the typed error asked
	// for before the table (ContestsHandler.fail).
	problems, _ := decode(t, rec)["problems"].([]any)
	if len(problems) == 0 {
		t.Errorf("problems = %v, want the missing story and questions named (%s)", problems, rec.Body.String())
	}
}

func TestAnImpossibleTransitionIsAConflict(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/status", `{"status": "running"}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestEditingAFinishedContestIsAConflict(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusFinished)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"enrollment": "open"}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

// TestMovingEndsAtEarlierAfterTheFreezeIsAConflictWithItsOwnCode asserts the
// declared refusal, not the generic not_editable one: pulling ends_at in once
// the leaderboard freeze has already been reached gets its own wire code so
// the organiser is told why, not just that the contest is running.
func TestMovingEndsAtEarlierAfterTheFreezeIsAConflictWithItsOwnCode(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusRunning)
	freeze := 130 // SeedContest's EndsAt is +2h; this freeze already passed.
	c.LeaderboardFreezeMin = &freeze
	f.stores.Contests.Put(c)
	earlier := c.EndsAt.Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		fmt.Sprintf(`{"ends_at": %q}`, earlier))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "freeze_already_reached" {
		t.Errorf("error code = %q, want freeze_already_reached", code)
	}
}

// TestExtendingEndsAtAfterTheFreezeLengthensTheFreeze is the settings form's
// save after the table froze: a later ends_at and no freeze key (the locked
// field submits none). The save succeeds, and the answer carries a freeze
// lengthened by the extension, so the table stays frozen where it froze.
func TestExtendingEndsAtAfterTheFreezeLengthensTheFreeze(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusRunning)
	freeze := 130
	c.LeaderboardFreezeMin = &freeze
	f.stores.Contests.Put(c)
	later := c.EndsAt.Add(time.Hour).UTC().Format(time.RFC3339)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		fmt.Sprintf(`{"ends_at": %q, "leaderboard": {"names": "login"}}`, later))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		EndsAt      time.Time `json:"ends_at"`
		Leaderboard struct {
			FreezeMin *int `json:"freeze_min"`
		} `json:"leaderboard"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body.Leaderboard.FreezeMin == nil || *body.Leaderboard.FreezeMin != 190 {
		t.Errorf("freeze_min = %v, want 190", body.Leaderboard.FreezeMin)
	}
	if want := c.EndsAt.Add(time.Hour); !body.EndsAt.Equal(want) {
		t.Errorf("ends_at = %v, want %v", body.EndsAt, want)
	}
}

// TestMovingStartsAtOnARunningICPCContestIsAConflictWithItsOwnCode is the
// starts_at counterpart, for ICPC scoring.
func TestMovingStartsAtOnARunningICPCContestIsAConflictWithItsOwnCode(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusRunning)
	c.Scoring = contests.ScoringICPC
	f.stores.Contests.Put(c)
	earlier := f.stores.Now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		fmt.Sprintf(`{"starts_at": %q}`, earlier))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "icpc_start_locked" {
		t.Errorf("error code = %q, want icpc_start_locked", code)
	}
}

// The one exception a finished contest's otherwise-frozen settings carry
// (§2.4, contests.Service.ExtendGrace): an organizer who discovers late that
// they need more time still has an endpoint to reach for.
func TestExtendingTheGracePeriodOfAFinishedContestSucceeds(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusFinished)

	// c never set an explicit grace, so the grace actually in force is the
	// fixture's stand-in for the installation default
	// (conteststest.FixtureDefaultGraceMin, 24h) — the requested value has to
	// clear that, not merely be positive (finding 1).
	grace := conteststest.FixtureDefaultGraceMin + 60
	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String()+"/grace",
		fmt.Sprintf(`{"grace_period_min": %d}`, grace))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	settings, ok := body["settings"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no settings object: %s", rec.Body.String())
	}
	if settings["grace_period_min"] != float64(grace) {
		t.Errorf("settings.grace_period_min = %v, want %d", settings["grace_period_min"], grace)
	}
}

// The installation default is what actually governs a contest that never set
// an explicit grace, so a value that does not clear it must be refused the
// same as shortening an explicit one would be — not silently accepted as the
// contest's "first explicit" value (finding 1: this door used to read 0 → 60
// as an extension while it was really cutting a 24-hour default to one
// hour).
func TestExtendingTheGracePeriodBelowTheInstallationDefaultIsRejected(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusFinished)
	if c.Settings.GracePeriodMin != 0 {
		t.Fatalf("setup: GracePeriodMin = %d, want 0", c.Settings.GracePeriodMin)
	}

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String()+"/grace", `{"grace_period_min": 60}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// Every status but finished and archived already has its own ordinary
// settings door open; this one must not become a second, wider way in.
func TestExtendingTheGracePeriodOfARunningContestIsAConflict(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusRunning)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String()+"/grace", `{"grace_period_min": 120}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

// "Extend" means grow — shortening a grace an organizer already relied on
// must be refused, not silently accepted as an ordinary edit.
func TestShorteningAnAlreadyExtendedGracePeriodIsRejected(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusFinished)
	extended := conteststest.FixtureDefaultGraceMin + 60
	if _, err := f.service.ExtendGrace(t.Context(), f.actor.ID, c.ID, extended); err != nil {
		t.Fatalf("setup ExtendGrace() = %v", err)
	}

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String()+"/grace", `{"grace_period_min": 100}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAnUnsafeWritableTableIsRejected(t *testing.T) {
	// These names end up in GRANT statements, so the refusal has to reach the
	// organizer as a bad request rather than a 500 from the database.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/sql-policy", `{
		"mode": "read_write",
		"writable_tables": ["evidence; DROP TABLE users"],
		"disk_quota_ratio": 5
	}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAnUnknownContestIsNotFound(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString(), "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAMalformedContestIdentifierIsABadRequest(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodGet, "/contests/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestNetworkRestrictionsAreReadBackAsWritten(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		`{"allowed_cidrs": ["10.20.0.0/16", "192.168.1.42/32"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	cidrs, _ := decode(t, rec)["allowed_cidrs"].([]any)
	if len(cidrs) != 2 || cidrs[0] != "10.20.0.0/16" {
		t.Errorf("allowed_cidrs = %v, want both networks", cidrs)
	}
}

// Finding 1: progression and scoring were decided and enforced in the
// domain (§6.1.1) but had no field on the request or response DTO, so an
// organizer could never reach either through the API. Round-tripped here the
// same way the network restriction is above.
func TestProgressionAndScoringSurviveARoundTripThroughTheAPI(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		`{"progression": "sequential", "scoring": "winner"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	if body["progression"] != "sequential" {
		t.Errorf("progression = %v, want sequential", body["progression"])
	}
	if body["scoring"] != "winner" {
		t.Errorf("scoring = %v, want winner", body["scoring"])
	}
}

// An update that never mentions progression or scoring must leave both
// alone — the same "absent means unchanged" rule enrollment and
// question_mode already follow (contests.UpdateCommand's own doc).
func TestUpdatingAContestPreservesUnmentionedProgressionAndScoring(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	set := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		`{"progression": "sequential", "scoring": "winner"}`)
	if set.Code != http.StatusOK {
		t.Fatalf("setup PATCH status = %d, want 200 (%s)", set.Code, set.Body.String())
	}

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"enrollment": "open"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	if body["progression"] != "sequential" {
		t.Errorf("progression = %v, want it to survive an unrelated update", body["progression"])
	}
	if body["scoring"] != "winner" {
		t.Errorf("scoring = %v, want it to survive an unrelated update", body["scoring"])
	}
}

// The ICPC scoring mode (docs/ARCHITECTURE.md §6.1.1):
// icpc_penalty_min is a pointer on the wire the same way freeze_min is, so an
// update that never mentions it leaves it alone — round-tripped here the same
// way progression and scoring are above.
func TestICPCPenaltyMinSurvivesARoundTripThroughTheAPI(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		`{"scoring": "icpc", "icpc_penalty_min": 15}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	if body["scoring"] != "icpc" {
		t.Errorf("scoring = %v, want icpc", body["scoring"])
	}
	if body["icpc_penalty_min"] != float64(15) {
		t.Errorf("icpc_penalty_min = %v, want 15", body["icpc_penalty_min"])
	}

	rec = f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"enrollment": "open"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := decode(t, rec)["icpc_penalty_min"]; got != float64(15) {
		t.Errorf("icpc_penalty_min = %v, want it to survive an unrelated update", got)
	}
}

func TestAMalformedNetworkIsRejected(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"allowed_cidrs": ["10.20.0.0/64"]}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// asParticipant returns a session cookie for a fresh account holding no
// installation-wide permission, which is what a student is.
func (f *contestFixture) asParticipant(t *testing.T, login string) (users.User, *http.Cookie) {
	t.Helper()

	user := f.stores.Users.Add(users.User{
		Login: login, FullName: login, Status: users.StatusActive,
	})
	token, err := f.sessions.Create(t.Context(), auth.Principal{UserID: user.ID, Login: user.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	return user, &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

// asked sends a GET as whoever the cookie belongs to.
func (f *contestFixture) asked(t *testing.T, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// listed reads the items of a contest listing, keyed by identifier.
func listed(t *testing.T, rec *httptest.ResponseRecorder) map[string]map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	items, _ := body["items"].([]any)

	out := map[string]map[string]any{}
	for _, item := range items {
		row, _ := item.(map[string]any)
		id, _ := row["id"].(string)
		out[id] = row
	}
	return out
}

func TestTheListingSaysWhetherTheCallerIsOnEachContest(t *testing.T) {
	// The catalogue has to tell "join" from "you are already in". Without it,
	// the workaround it replaces — offer the button everywhere and let the API
	// answer already_enrolled — turns an ordinary state into an error message
	// as soon as the two lists become separate screens.
	f := newContestFixture(t)
	student, cookie := f.asParticipant(t, "s.popescu")
	mine := f.stores.SeedContest(contests.StatusPublished)
	// Open, so that it is offered to a student who is not on it: an
	// invitation-only contest they never joined is not listed to them at all.
	other := f.stores.SeedContest(contests.StatusPublished)
	other.Enrollment = contests.EnrollmentOpen
	f.stores.Contests.Put(other)
	if _, err := f.stores.Registrations.Add(t.Context(), mine.ID, student.ID); err != nil {
		t.Fatalf("Add() returned error: %v", err)
	}

	rows := listed(t, f.asked(t, "/contests?scope=participant", cookie))

	if rows[mine.ID.String()]["enrolled"] != true {
		t.Error("the contest the student is on is not marked as theirs")
	}
	if rows[other.ID.String()]["enrolled"] != false {
		t.Error("a contest the student never joined is marked as theirs")
	}
}

func TestTheEnrolmentFlagIsAlwaysAboutTheCaller(t *testing.T) {
	// It must come from the authenticated identity and from nothing a request
	// can carry. A parameter that steered it would make the listing a way to
	// ask who takes part in what.
	f := newContestFixture(t)
	_, cookie := f.asParticipant(t, "s.popescu")
	other, _ := f.asParticipant(t, "i.ivanov")
	// Open, so that the caller is offered it without being on it: the only
	// way the listing can say anything about it is the flag under test.
	shared := f.stores.SeedContest(contests.StatusPublished)
	shared.Enrollment = contests.EnrollmentOpen
	f.stores.Contests.Put(shared)
	if _, err := f.stores.Registrations.Add(t.Context(), shared.ID, other.ID); err != nil {
		t.Fatalf("Add() returned error: %v", err)
	}

	// The caller is not on the contest, so asking for the contests they are on
	// lists nothing; the other two list it, and not as theirs.
	for query, listedForCaller := range map[string]bool{
		"/contests?scope=participant&user_id=" + other.ID.String():               true,
		"/contests?scope=participant&enrolled=true&user_id=" + other.ID.String(): false,
		"/contests?scope=participant&visible_to=" + other.ID.String():            true,
	} {
		rows := listed(t, f.asked(t, query, cookie))
		row, present := rows[shared.ID.String()]
		if present != listedForCaller {
			t.Errorf("%s: contest listed = %v, want %v", query, present, listedForCaller)
		}
		if row["enrolled"] == true {
			t.Errorf("%s reported somebody else's registration as the caller's", query)
		}
	}
}

func TestAnUnreadableEnrolledParameterIsRefusedRatherThanIgnored(t *testing.T) {
	// Dropping it silently would answer a different question from the one
	// asked, and the screen would quietly show the wrong list.
	f := newContestFixture(t)
	_, cookie := f.asParticipant(t, "s.popescu")

	rec := f.asked(t, "/contests?scope=participant&enrolled=perhaps", cookie)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestStaffListingsCarryNoEnrolmentFlagWorthReading(t *testing.T) {
	// An organizer's register answers "what do I run", and marking those rows
	// with the organizer's own participation would be a fact about a different
	// question. It is reported honestly as false rather than invented.
	f := newContestFixture(t, rbac.PermissionContestCreate)
	owned := f.ownedContest(t, contests.StatusPublished)

	rows := listed(t, f.asked(t, "/contests", f.cookie))

	if rows[owned.ID.String()]["enrolled"] != false {
		t.Error("a contest the organizer runs but does not sit is marked as theirs")
	}
}

func TestTheEnrolmentNarrowingIsRefusedWhereItMeansNothing(t *testing.T) {
	// Outside the participant scope there is no "me" for it to be about: an
	// organizer's register answers "what do I run". Applied there the SQL
	// compares against a null identity and quietly returns an empty list —
	// which is the same sin as ignoring an unparseable value, answering a
	// different question from the one asked.
	f := newContestFixture(t, rbac.PermissionContestCreate)
	f.ownedContest(t, contests.StatusPublished)

	rec := f.asked(t, "/contests?enrolled=true", f.cookie)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// The leaderboard settings travel as one object, so that "no freeze" (null)
// and "leave the freeze alone" (no object at all) are both sayable.
func TestLeaderboardSettingsSurviveARoundTripThroughTheAPI(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		`{"leaderboard": {"freeze_min": 30, "names": "full_name"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	board, _ := decode(t, rec)["leaderboard"].(map[string]any)
	if board["freeze_min"] != float64(30) || board["names"] != "full_name" {
		t.Fatalf("leaderboard = %v, want freeze_min 30 and names full_name", board)
	}

	// An unrelated update leaves it alone.
	rec = f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"enrollment": "open"}`)
	board, _ = decode(t, rec)["leaderboard"].(map[string]any)
	if board["freeze_min"] != float64(30) {
		t.Fatalf("leaderboard = %v, want the freeze to survive an unrelated update", board)
	}

	// And null clears it.
	rec = f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"leaderboard": {"freeze_min": null}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	board, _ = decode(t, rec)["leaderboard"].(map[string]any)
	if value, present := board["freeze_min"]; !present || value != nil {
		t.Fatalf("leaderboard = %v, want freeze_min present and null", board)
	}
	if board["names"] != "full_name" {
		t.Fatalf("leaderboard = %v, want names to survive clearing the freeze", board)
	}
}

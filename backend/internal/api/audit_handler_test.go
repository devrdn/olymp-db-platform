package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// stubTrail answers with what it was given, and remembers the filter it was
// asked for — which is the part the handler is responsible for building.
type stubTrail struct {
	records []audit.Record
	total   int
	asked   audit.Filter
}

func (s *stubTrail) List(_ context.Context, f audit.Filter) ([]audit.Record, int, error) {
	s.asked = f
	return s.records, s.total, nil
}

func newAuditFixture(t *testing.T, trail audit.Reader, permissions ...string) (http.Handler, *http.Cookie) {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("staff", permissions...)
	actor := repo.Add(users.User{
		Login: "auditor", FullName: "Auditor", Status: users.StatusActive, Roles: []string{"staff"},
	})

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo, Authorizer: rbac.New(noRoles{}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewAuditHandler(trail, mw, log).Mount(router)
	return router, &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

func getAudit(t *testing.T, router http.Handler, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestReadingTheTrailNeedsThePermission(t *testing.T) {
	// A trail readable by anybody signed in would publish who blocked whom and
	// from which address.
	router, cookie := newAuditFixture(t, &stubTrail{})

	rec := getAudit(t, router, cookie, "/audit")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

func TestTheTrailIsReturnedWithItsTotal(t *testing.T) {
	actor := uuid.New()
	trail := &stubTrail{
		total: 42,
		records: []audit.Record{{
			ID: 7, ActorID: &actor, ActorLogin: "root", Action: "user.block",
			Entity: "user", EntityID: "u-1", IP: "10.0.0.1",
			Payload:   map[string]any{"login": "s.popescu"},
			CreatedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		}},
	}
	router, cookie := newAuditFixture(t, trail, rbac.PermissionAuditView)

	rec := getAudit(t, router, cookie, "/audit")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["total"] != float64(42) {
		t.Errorf("total = %v, want 42", body["total"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want one entry", items)
	}
	entry, _ := items[0].(map[string]any)
	if entry["actor_login"] != "root" || entry["action"] != "user.block" {
		t.Errorf("entry = %v, want the actor's login and the action", entry)
	}
	if entry["created_at"] != "2026-03-01T10:00:00Z" {
		t.Errorf("created_at = %v, want the one timestamp format the API speaks", entry["created_at"])
	}
}

func TestTheFiltersReachTheQuery(t *testing.T) {
	// Every one of them narrows an index the table already carries; a filter
	// the handler drops silently would be a panel that ignores what was asked.
	actor := uuid.New()
	trail := &stubTrail{}
	router, cookie := newAuditFixture(t, trail, rbac.PermissionAuditView)

	rec := getAudit(t, router, cookie,
		"/audit?actor="+actor.String()+"&action=user.block&entity=user&entity_id=u-1"+
			"&from=2026-03-01T00:00:00Z&to=2026-03-02T00:00:00Z&limit=5&offset=10")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	switch {
	case trail.asked.Actor != actor:
		t.Errorf("actor = %v, want %v", trail.asked.Actor, actor)
	case trail.asked.Action != "user.block":
		t.Errorf("action = %q", trail.asked.Action)
	case trail.asked.Entity != "user" || trail.asked.EntityID != "u-1":
		t.Errorf("entity = %q/%q", trail.asked.Entity, trail.asked.EntityID)
	case trail.asked.From == nil || trail.asked.To == nil:
		t.Errorf("window = %v..%v, want both bounds", trail.asked.From, trail.asked.To)
	case trail.asked.Limit != 5 || trail.asked.Offset != 10:
		t.Errorf("paging = %d/%d, want 5/10", trail.asked.Limit, trail.asked.Offset)
	}
}

func TestAMalformedActorIsABadRequest(t *testing.T) {
	// Silently ignoring it would answer with the whole trail for a question
	// about one person.
	router, cookie := newAuditFixture(t, &stubTrail{}, rbac.PermissionAuditView)

	rec := getAudit(t, router, cookie, "/audit?actor=not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAMalformedTimeIsABadRequest(t *testing.T) {
	router, cookie := newAuditFixture(t, &stubTrail{}, rbac.PermissionAuditView)

	rec := getAudit(t, router, cookie, "/audit?from=yesterday")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestTheTrailAnswersWithWhatWasActedUpon(t *testing.T) {
	// The identifier alone made the panel say "Contest" and nothing more.
	trail := &stubTrail{
		total: 1,
		records: []audit.Record{{
			ID: 1, Action: "contest.answers_change", Entity: "contest",
			EntityID: "c-1", EntityLabel: "Night in the archive",
			CreatedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		}},
	}
	router, cookie := newAuditFixture(t, trail, rbac.PermissionAuditView)

	rec := getAudit(t, router, cookie, "/audit")

	items, _ := decode(t, rec)["items"].([]any)
	entry, _ := items[0].(map[string]any)
	if entry["entity_label"] != "Night in the archive" {
		t.Errorf("entity_label = %v, want the contest's name", entry["entity_label"])
	}
	if entry["entity_id"] != "c-1" {
		t.Errorf("entity_id = %v, want it kept beside the name", entry["entity_id"])
	}
}

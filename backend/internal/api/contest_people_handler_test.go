package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

func TestTheStaffListNamesTheOwner(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/managers", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	items, _ := decode(t, rec)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("staff listed = %d, want 1 (%s)", len(items), rec.Body.String())
	}
	if items[0].(map[string]any)["role"] != string(rbac.RoleOwner) {
		t.Errorf("role = %v, want owner", items[0].(map[string]any)["role"])
	}
}

func TestTheOwnerAppointsAManager(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	assistant := f.addAccount("assistant")

	rec := f.do(http.MethodPut,
		"/contests/"+c.ID.String()+"/managers/"+assistant.ID.String(), `{"role": "manager"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := f.stores.Managers.Get(t.Context(), c.ID, assistant.ID); err != nil {
		t.Errorf("the assistant was not appointed: %v", err)
	}
}

func TestAManagerCannotAppointFurtherStaff(t *testing.T) {
	// The one power a manager must not be able to grant themselves more of.
	// contest.manage belongs to the owner alone, which is what separates the
	// two roles at all.
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusDraft)
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: f.actor.ID, Role: rbac.RoleManager, GrantedBy: uuid.New(),
	}); err != nil {
		t.Fatalf("Grant() returned error: %v", err)
	}
	assistant := f.addAccount("assistant")

	rec := f.do(http.MethodPut,
		"/contests/"+c.ID.String()+"/managers/"+assistant.ID.String(), `{"role": "manager"}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

func TestOwnershipIsNotHandedOverThroughTheStaffList(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	assistant := f.addAccount("assistant")

	rec := f.do(http.MethodPut,
		"/contests/"+c.ID.String()+"/managers/"+assistant.ID.String(), `{"role": "owner"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "owner_immutable" {
		t.Errorf("error code = %q, want owner_immutable", code)
	}
}

func TestImportingARosterReportsWhatItCouldNotUse(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusPublished)
	f.addAccount("s.popescu")

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/participants",
		`{"logins": ["s.popescu", "typo.name"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["added"] != float64(1) {
		t.Errorf("added = %v, want 1", body["added"])
	}
	skipped, _ := body["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the mistyped login reported", skipped)
	}
	if skipped[0].(map[string]any)["reason"] != contests.SkipUnknownAccount {
		t.Errorf("reason = %v, want %s", skipped[0], contests.SkipUnknownAccount)
	}
}

func TestParticipantsAreListedWithTheirAccounts(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusPublished)
	f.addAccount("s.popescu")
	if rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/participants",
		`{"logins": ["s.popescu"]}`); rec.Code != http.StatusOK {
		t.Fatalf("could not import: %d %s", rec.Code, rec.Body.String())
	}

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/participants", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	items, _ := decode(t, rec)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("participants listed = %d, want 1", len(items))
	}
	if items[0].(map[string]any)["login"] != "s.popescu" {
		t.Errorf("login = %v, want s.popescu", items[0].(map[string]any)["login"])
	}
}

func TestRemovingSomebodyWhoStartedIsRefused(t *testing.T) {
	// Their work is part of the record; excluding them is disqualification.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusRunning)
	student := f.addAccount("s.popescu")
	f.stores.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: student.ID, Status: contests.RegistrationActive,
	})

	rec := f.do(http.MethodDelete,
		"/contests/"+c.ID.String()+"/participants/"+student.ID.String(), "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "participant_started" {
		t.Errorf("error code = %q, want participant_started", code)
	}
}

func TestDisqualifyingKeepsTheParticipant(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusRunning)
	student := f.addAccount("s.popescu")
	f.stores.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: student.ID, Status: contests.RegistrationActive,
	})

	rec := f.do(http.MethodPost,
		"/contests/"+c.ID.String()+"/participants/"+student.ID.String()+"/disqualify", "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	p, err := f.stores.Registrations.ByUser(t.Context(), c.ID, student.ID)
	if err != nil {
		t.Fatalf("the registration is gone: %v", err)
	}
	if p.Status != contests.RegistrationDisqualified {
		t.Errorf("status = %q, want disqualified", p.Status)
	}
}

func TestSelfSignupIsRefusedForAnInviteOnlyContest(t *testing.T) {
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusPublished)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/enroll", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "enrollment_closed" {
		t.Errorf("error code = %q, want enrollment_closed", code)
	}
}

func TestSelfSignupNeedsNoStaffPermission(t *testing.T) {
	// A student holds none, and needs none: whether they may sign up is the
	// contest's decision, not a right somebody granted them.
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	f.stores.Contests.Put(c)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/enroll", "")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["status"] != contests.RegistrationRegistered {
		t.Errorf("status = %v, want registered", decode(t, rec)["status"])
	}
}

func TestSignupFromOutsideTheAllowedNetworkIsRefusedWithAReason(t *testing.T) {
	// "You are on the wrong network" is something the participant can act on,
	// unlike a bare 403.
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	c.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	f.stores.Contests.Put(c)

	rec := f.from("203.0.113.7:5000", http.MethodPost, "/contests/"+c.ID.String()+"/enroll", "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "address_not_allowed" {
		t.Errorf("error code = %q, want address_not_allowed", code)
	}
}

func TestSignupFromInsideTheAllowedNetworkIsAccepted(t *testing.T) {
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	c.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	f.stores.Contests.Put(c)

	rec := f.from("10.20.30.40:5000", http.MethodPost, "/contests/"+c.ID.String()+"/enroll", "")

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
}

// addAccount stores an account the contest endpoints can resolve.
func (f *contestFixture) addAccount(login string) users.User {
	return f.stores.Users.Add(users.User{
		Login: login, FullName: login, Status: users.StatusActive,
	})
}

// from issues a request that appears to come from a particular address, which
// is what the network restriction is checked against.
func (f *contestFixture) from(remoteAddr, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bodyReader(body))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// bodyReader returns an io.Reader, not a *strings.Reader: a typed nil pointer
// in an interface is not nil, and httptest would dereference it.
func bodyReader(body string) io.Reader {
	if body == "" {
		return nil
	}
	return strings.NewReader(body)
}

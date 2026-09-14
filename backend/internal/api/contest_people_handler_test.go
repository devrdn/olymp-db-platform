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

func TestAppointingADeletedAccountIsRefusedWithAConflict(t *testing.T) {
	// A deleted account can never sign in; appointing it would staff the
	// contest with somebody who can never act on it. contests.Service.GrantManager
	// refuses it with users.ErrAccountDeleted, which the handler's fail switch
	// must map to a declared code rather than an internal error.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	deleted := f.stores.Users.Add(users.User{
		Login: "gone", FullName: "gone", Status: users.StatusDeleted,
	})

	rec := f.do(http.MethodPut,
		"/contests/"+c.ID.String()+"/managers/"+deleted.ID.String(), `{"role": "manager"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "account_deleted" {
		t.Errorf("error code = %q, want account_deleted", code)
	}
}

func TestAppointingABlockedAccountIsRefusedWithAConflict(t *testing.T) {
	// A blocked account can never sign in either — auth.Service.Login and
	// auth.Middleware both refuse it — so appointing one is refused for the
	// same reason as a deleted account, just above, and answers with the
	// same wire code the login flow already uses for it.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	blocked := f.stores.Users.Add(users.User{
		Login: "blocked", FullName: "blocked", Status: users.StatusBlocked,
	})

	rec := f.do(http.MethodPut,
		"/contests/"+c.ID.String()+"/managers/"+blocked.ID.String(), `{"role": "manager"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "account_blocked" {
		t.Errorf("error code = %q, want account_blocked", code)
	}
}

// TestGrantManagerRefusesARegisteredParticipantWithAConflict is a
// handler-level test for one direction of the staff/participant overlap:
// appointing a contest's own participant is refused with a declared 409,
// not an internal error, and the roster entry stays unpromoted.
func TestGrantManagerRefusesARegisteredParticipantWithAConflict(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusPublished)
	student := f.addAccount("s.popescu")
	if _, err := f.stores.Registrations.Add(t.Context(), c.ID, student.ID); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	rec := f.do(http.MethodPut,
		"/contests/"+c.ID.String()+"/managers/"+student.ID.String(), `{"role": "manager"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "participant_cannot_be_staff" {
		t.Errorf("error code = %q, want participant_cannot_be_staff", code)
	}
	if _, err := f.stores.Managers.Get(t.Context(), c.ID, student.ID); err == nil {
		t.Error("the participant must not have been appointed")
	}
}

// TestStaffSelfEnrollIsRefusedWithAConflict is a handler-level test for the
// other direction of the overlap: a contest's own manager cannot
// self-enroll as its participant.
func TestStaffSelfEnrollIsRefusedWithAConflict(t *testing.T) {
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusPublished)
	c.Enrollment = contests.EnrollmentOpen
	f.stores.Contests.Put(c)
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: f.actor.ID, Role: rbac.RoleManager, GrantedBy: uuid.New(),
	}); err != nil {
		t.Fatalf("Grant() = %v", err)
	}

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/enroll", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "staff_cannot_participate" {
		t.Errorf("error code = %q, want staff_cannot_participate", code)
	}
	if _, err := f.stores.Registrations.ByUser(t.Context(), c.ID, f.actor.ID); err == nil {
		t.Error("a refused self-enrollment must not create a registration")
	}
}

// TestImportSkipsAStaffMemberWithAReason is a handler-level test for the
// roster import: a mistaken entry naming one of the contest's own staff is
// reported as skipped, not rejected as an unrelated failure, and the rest of
// the roster still goes in.
func TestImportSkipsAStaffMemberWithAReason(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusPublished)
	manager := f.addAccount("m.staff")
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: manager.ID, Role: rbac.RoleManager, GrantedBy: uuid.New(),
	}); err != nil {
		t.Fatalf("Grant() = %v", err)
	}
	f.addAccount("s.popescu")

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/participants",
		`{"logins": ["m.staff", "s.popescu"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["added"] != float64(1) {
		t.Errorf("added = %v, want 1", body["added"])
	}
	skipped, _ := body["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["reason"] != contests.SkipStaffMember {
		t.Fatalf("skipped = %v, want one entry reasoned %s", skipped, contests.SkipStaffMember)
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

// TestDirectorySearchIsRefusedWithoutParticipantManage pins the permission
// this endpoint was built behind: an account with no standing on the contest
// — not even a manager, let alone a stranger — gets a 403, not a directory.
func TestDirectorySearchIsRefusedWithoutParticipantManage(t *testing.T) {
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusDraft)
	f.addAccount("s.ivanov")

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/people/directory?q=ivanov", "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

// TestDirectorySearchIsUsableByAManagerNotOnlyTheOwner proves the permission
// choice: participant.manage, which every manager carries — not
// contest.manage, which only the owner does — because the participant
// picker on this same screen is a manager's ordinary work, not the owner's
// alone.
func TestDirectorySearchIsUsableByAManagerNotOnlyTheOwner(t *testing.T) {
	f := newContestFixture(t)
	c := f.stores.SeedContest(contests.StatusDraft)
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: f.actor.ID, Role: rbac.RoleManager, GrantedBy: uuid.New(),
	}); err != nil {
		t.Fatalf("Grant() returned error: %v", err)
	}
	f.addAccount("s.ivanov")

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/people/directory?q=ivanov", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// TestDirectorySearchFindsACandidateByLoginNameOrEmail pins the owner's own
// requirement (also proven at the domain level in
// internal/contests/directory_test.go): the picker matches a login, a name,
// or an email.
func TestDirectorySearchFindsACandidateByLoginNameOrEmail(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	candidate := f.stores.Users.Add(users.User{
		Login: "s.ivanov", FullName: "Ivanov Sergei", Email: "sergei@example.edu",
		Status: users.StatusActive,
	})

	for _, q := range []string{"ivanov", "Sergei", "sergei@example"} {
		rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/people/directory?q="+q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("query %q: status = %d, want 200 (%s)", q, rec.Code, rec.Body.String())
		}
		items, _ := decode(t, rec)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["user_id"] != candidate.ID.String() {
			t.Errorf("query %q: items = %v, want only %v", q, items, candidate.ID)
		}
	}
}

// TestDirectorySearchPublishesTheEmailAddress guards the response shape after
// the owner's decision to show the email in the picker (see PersonResponse's
// own comment for the trade they accepted): this endpoint reaches every
// contest's staff, not only users.manage, so returning the email here widens
// what that wider audience can read about any account in the installation —
// a cost the owner weighed against telling two same-named candidates apart
// and chose to pay.
func TestDirectorySearchPublishesTheEmailAddress(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	f.stores.Users.Add(users.User{
		Login: "s.ivanov", FullName: "Ivanov Sergei", Email: "sergei@example.edu",
		Status: users.StatusActive,
	})

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/people/directory?q=ivanov", "")

	items, _ := decode(t, rec)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want 1", items)
	}
	if got := items[0].(map[string]any)["email"]; got != "sergei@example.edu" {
		t.Errorf("email = %v, want the account's own address", got)
	}
}

// TestDirectorySearchOmitsAnEmptyEmailRatherThanPublishingAnEmptyString
// guards the other half of that decision: an account that never set an email
// must not come back as `"email": ""`, which reads as an address that was
// looked up and found blank rather than one that was never given.
func TestDirectorySearchOmitsAnEmptyEmailRatherThanPublishingAnEmptyString(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	f.addAccount("s.ivanov")

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/people/directory?q=ivanov", "")

	items, _ := decode(t, rec)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v, want 1", items)
	}
	if _, present := items[0].(map[string]any)["email"]; present {
		t.Errorf("an account with no email published the field anyway: %v", items[0])
	}
}

// TestDirectorySearchReturnsNothingBelowTheMinimumLength proves
// contests.MinDirectoryQueryLength reaches the client as an empty result,
// not an error: a debounced picker sends every keystroke including the
// first one, and a query below the minimum should look exactly like nothing
// having been typed yet, not like a request that failed.
func TestDirectorySearchReturnsNothingBelowTheMinimumLength(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	f.addAccount("a-ivanov")

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/people/directory?q=a", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	items, _ := decode(t, rec)["items"].([]any)
	if len(items) != 0 {
		t.Errorf("items = %v, want none below the minimum query length", items)
	}
}

// TestDirectorySearchRefusesAnOverlongQuery proves the domain's own bound
// (contests.MaxDirectoryQueryLength) reaches the client as a 400, not a 500.
func TestDirectorySearchRefusesAnOverlongQuery(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodGet,
		"/contests/"+c.ID.String()+"/people/directory?q="+strings.Repeat("a", contests.MaxDirectoryQueryLength+1), "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
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

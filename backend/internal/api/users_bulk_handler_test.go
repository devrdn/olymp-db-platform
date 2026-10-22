package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// TestBulkRoutesAreNotSwallowedByTheAccountIDRoute fails with a 400 or 404 if
// route ordering lets "bulk" be read as a userID.
func TestBulkRoutesAreNotSwallowedByTheAccountIDRoute(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/bulk/status",
		`{"ids":["`+target.ID.String()+`"],"status":"blocked","reason":"end of term"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestBulkStatusEndpointBlocksTheSelection(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/bulk/status",
		`{"ids":["`+target.ID.String()+`"],"status":"blocked","reason":"end of term"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, _ := f.repo.Get(target.ID)
	if stored.Status != users.StatusBlocked {
		t.Errorf("Status = %q, want blocked", stored.Status)
	}

	var body struct {
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(body.Changed) != 1 || body.Changed[0] != target.ID.String() {
		t.Errorf("Changed = %v, want [%s]", body.Changed, target.ID)
	}
}

func TestBulkReportsUnknownAccountsAsSkipped(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/bulk/status",
		`{"ids":["`+target.ID.String()+`","`+uuid.NewString()+`"],"status":"blocked","reason":"end of term"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Changed []string `json:"changed"`
		Skipped []struct {
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(body.Changed) != 1 {
		t.Errorf("Changed = %v, want exactly the known account", body.Changed)
	}
	if len(body.Skipped) != 1 || body.Skipped[0].Reason != users.SkipNotFound {
		t.Errorf("Skipped = %+v, want one entry reasoned %q", body.Skipped, users.SkipNotFound)
	}
}

func TestBulkAboveTheBoundIsABadRequest(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	ids := make([]string, users.MaxBulkAccounts+1)
	for i := range ids {
		ids[i] = uuid.NewString()
	}

	rec := f.do(http.MethodPost, "/users/bulk/status",
		`{"ids":["`+strings.Join(ids, `","`)+`"],"status":"blocked","reason":"end of term"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "too_many_accounts" {
		t.Errorf("code = %q, want too_many_accounts", code)
	}
}

func TestBulkRolesEndpointReplacesTheSet(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Roles: []string{"student"}})

	rec := f.do(http.MethodPost, "/users/bulk/roles",
		`{"ids":["`+target.ID.String()+`"],"roles":["organizer"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, _ := f.repo.Get(target.ID)
	if len(stored.Roles) != 1 || stored.Roles[0] != "organizer" {
		t.Errorf("Roles = %v, want [organizer]", stored.Roles)
	}
}

func TestBulkRolesEndpointAnswersAnUnknownRoleWith400(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Roles: []string{"student"}})

	rec := f.do(http.MethodPost, "/users/bulk/roles",
		`{"ids":["`+target.ID.String()+`"],"roles":["studnet"]}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "unknown_role" {
		t.Errorf("code = %q, want unknown_role", code)
	}
}

// TestBulkPasswordResetIssuesADistinctPasswordPerAccount: a shared password is
// a shared account.
func TestBulkPasswordResetIssuesADistinctPasswordPerAccount(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	first := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})
	second := f.repo.Add(users.User{Login: "ivanov", FullName: "Ivan"})

	rec := f.do(http.MethodPost, "/users/bulk/password-reset",
		`{"ids":["`+first.ID.String()+`","`+second.ID.String()+`"]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Issued []struct {
			ID              string `json:"id"`
			Login           string `json:"login"`
			OneTimePassword string `json:"one_time_password"`
		} `json:"issued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(body.Issued) != 2 {
		t.Fatalf("Issued has %d entries, want 2", len(body.Issued))
	}
	if body.Issued[0].OneTimePassword == "" || body.Issued[1].OneTimePassword == "" {
		t.Error("an issued entry carries no password to hand over")
	}
	if body.Issued[0].OneTimePassword == body.Issued[1].OneTimePassword {
		t.Error("both accounts got the same password; each must have its own")
	}
}

func TestBulkEndpointsRequireThePermission(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionReportsView)

	cases := []struct{ path, body string }{
		{"/users/bulk/status", `{"ids":["` + uuid.NewString() + `"],"status":"blocked","reason":"x"}`},
		{"/users/bulk/roles", `{"ids":["` + uuid.NewString() + `"],"roles":["student"]}`},
		{"/users/bulk/password-reset", `{"ids":["` + uuid.NewString() + `"]}`},
	}
	for _, c := range cases {
		if rec := f.do(http.MethodPost, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", c.path, rec.Code)
		}
	}
}

func TestBulkPasswordResetEmailsEachAccountWithAnAddress(t *testing.T) {
	f, queue := newEmailingFixture(t, true)
	withAddress := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Email: "petrov@usm.md"})
	without := f.repo.Add(users.User{Login: "ivanov", FullName: "Ivan"})

	rec := f.do(http.MethodPost, "/users/bulk/password-reset",
		`{"ids":["`+withAddress.ID.String()+`","`+without.ID.String()+`"],"send_by_email":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON[struct {
		Issued []emailed `json:"issued"`
	}](t, rec)
	if len(body.Issued) != 2 {
		t.Fatalf("issued %d, want 2", len(body.Issued))
	}
	for _, one := range body.Issued {
		switch one.Login {
		case "petrov":
			if !one.Emailed || one.SkippedReason != nil {
				t.Errorf("petrov reads emailed %v, reason %v", one.Emailed, one.SkippedReason)
			}
			assertQueuedTo(t, queue, "petrov@usm.md", one.OneTimePassword)
		case "ivanov":
			if one.Emailed || one.SkippedReason == nil || *one.SkippedReason != "no_address" {
				t.Errorf("ivanov reads emailed %v, reason %v; want no_address", one.Emailed, one.SkippedReason)
			}
			if one.OneTimePassword == "" {
				t.Error("ivanov's password was not handed to the administrator")
			}
		}
	}
	if len(queue.messages) != 1 {
		t.Errorf("queued %d messages, want 1", len(queue.messages))
	}
}

func TestBulkPasswordResetEmailsNoBlockedAccount(t *testing.T) {
	f, queue := newEmailingFixture(t, true)
	active := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Email: "petrov@usm.md"})
	blocked := f.repo.Add(users.User{Login: "ivanov", FullName: "Ivan", Email: "ivanov@usm.md",
		Status: users.StatusBlocked})
	other := f.repo.Add(users.User{Login: "rusu", FullName: "Ana", Email: "rusu@usm.md"})

	rec := f.do(http.MethodPost, "/users/bulk/password-reset",
		`{"ids":["`+active.ID.String()+`","`+blocked.ID.String()+`","`+other.ID.String()+`"],"send_by_email":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON[struct {
		Issued []emailed `json:"issued"`
	}](t, rec)
	if len(body.Issued) != 3 {
		t.Fatalf("issued %d, want 3", len(body.Issued))
	}
	for _, one := range body.Issued {
		if one.OneTimePassword == "" {
			t.Errorf("%s was handed no password", one.Login)
		}
		if one.Login == "ivanov" {
			if one.Emailed || one.SkippedReason == nil || *one.SkippedReason != "not_active" {
				t.Errorf("ivanov reads emailed %v, reason %v; want not_active", one.Emailed, one.SkippedReason)
			}
			continue
		}
		if !one.Emailed || one.SkippedReason != nil {
			t.Errorf("%s reads emailed %v, reason %v; want emailed", one.Login, one.Emailed, one.SkippedReason)
		}
	}
	for _, m := range queue.messages {
		if m.To == "ivanov@usm.md" {
			t.Error("a message was queued to the blocked account")
		}
	}
	if len(queue.messages) != 2 {
		t.Errorf("queued %d messages, want 2", len(queue.messages))
	}
}

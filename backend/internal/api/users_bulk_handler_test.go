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

// TestBulkRoutesAreNotSwallowedByTheAccountIDRoute proves that /users/bulk/...
// is served by the dedicated /bulk group rather than being read as
// /{userID}/... with userID literally "bulk". If the route ordering ever
// regresses, this fails with a 400 invalid_user_id or a 404 instead of the
// 200 a real bulk operation returns.
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

// TestBulkReportsUnknownAccountsAsSkipped proves the rule that governs every
// bulk response: one identifier naming no account is a row in the answer, not
// a failure of the request. The rest of the selection still applies.
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

// TestBulkPasswordResetIssuesADistinctPasswordPerAccount is the point of
// issuing one password per account rather than one for the whole group: a
// shared password is a shared account.
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

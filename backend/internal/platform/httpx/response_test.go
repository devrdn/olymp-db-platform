package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONWritesBodyWithContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	JSON(rec, req, http.StatusCreated, map[string]string{"id": "42"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body["id"] != "42" {
		t.Errorf("body = %v, want id=42", body)
	}
}

func TestErrorWritesStructuredErrorBody(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	Error(rec, req, http.StatusNotFound, NewCode("not_found", "Declared by a test."), "Contest not found")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body.Error.Code != "not_found" {
		t.Errorf("error.code = %q, want not_found", body.Error.Code)
	}
	if body.Error.Message != "Contest not found" {
		t.Errorf("error.message = %q, want %q", body.Error.Message, "Contest not found")
	}
}

func TestErrorIncludesRequestIDWhenPresent(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Error(w, r, http.StatusForbidden, NewCode("forbidden", "Declared by a test."), "Access denied")
	}))

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var body struct {
		Error struct {
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body.Error.RequestID == "" {
		t.Error("error body carries no request_id for support correlation")
	}
}

func TestNoContentWritesEmptyBody(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/", nil)

	NoContent(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestErrorAnswersWithTheDeclaredCode(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	Error(rec, req, http.StatusNotFound, NewCode("gone_test", "Declared by a test."), "Nothing here")

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Code != "gone_test" {
		t.Errorf("code = %q, want gone_test", body.Error.Code)
	}
	if body.Error.Message != "Nothing here" {
		t.Errorf("message = %q, want the one that was passed", body.Error.Message)
	}
}

func TestErrorWithDetailsKeepsTheErrorObjectAndAddsBesideIt(t *testing.T) {
	// The publish gate answers with a code and the list of what is missing.
	// Writing that envelope by hand is how a code once reached clients without
	// ever being declared; this is the one way to send more than the error
	// object.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	ErrorWithDetails(rec, req, http.StatusUnprocessableEntity,
		NewCode("not_ready_test", "Declared by a test."), "Not ready",
		map[string]any{"problems": []string{"no_story"}})

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Code != "not_ready_test" {
		t.Errorf("code = %q, want not_ready_test", body.Error.Code)
	}
	if len(body.Problems) != 1 || body.Problems[0] != "no_story" {
		t.Errorf("problems = %v, want the detail carried beside the error", body.Problems)
	}
}

func TestDetailsCannotOverwriteTheErrorObject(t *testing.T) {
	// A caller passing "error" would replace the very thing every client
	// parses, and the code would vanish from a response that still looked
	// well formed.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	ErrorWithDetails(rec, req, http.StatusConflict,
		NewCode("kept_test", "Declared by a test."), "Kept",
		map[string]any{"error": "hijacked"})

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Code != "kept_test" {
		t.Errorf("code = %q, want the declared code to survive", body.Error.Code)
	}
}

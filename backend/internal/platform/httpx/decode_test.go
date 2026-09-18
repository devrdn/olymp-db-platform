package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type payload struct {
	Login string `json:"login"`
	Count int    `json:"count"`
}

// decode runs DecodeJSON over a body, the way a handler does.
func decode(body string, v any) error {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	return DecodeJSON(httptest.NewRecorder(), req, v)
}

func TestDecodeReadsAValidBody(t *testing.T) {
	var got payload

	if err := decode(`{"login":"ivanov","count":3}`, &got); err != nil {
		t.Fatalf("DecodeJSON() returned error: %v", err)
	}
	if got.Login != "ivanov" || got.Count != 3 {
		t.Errorf("decoded %+v, want login=ivanov count=3", got)
	}
}

func TestDecodeRejectsAnOversizedBody(t *testing.T) {
	// The login endpoint is unauthenticated, so an unbounded body is memory
	// anyone who can reach the service may spend.
	var got payload
	huge := `{"login":"` + strings.Repeat("a", 2*maxBodyBytes) + `"}`

	err := decode(huge, &got)

	if err == nil {
		t.Fatal("DecodeJSON() accepted a body over the limit")
	}
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("err = %v, want it to wrap ErrBadRequest", err)
	}
}

// An endpoint whose bodies are small by nature takes a tighter bound, and
// can tell a body refused for its size from one refused for its shape.
func TestDecodeWithinATighterLimitNamesAnOversizedBody(t *testing.T) {
	const limit = 64
	var got payload

	fits := `{"login":"` + strings.Repeat("a", limit-20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fits))
	if err := DecodeJSONWithin(httptest.NewRecorder(), req, &got, limit); err != nil {
		t.Fatalf("a body under the limit: %v", err)
	}

	over := `{"login":"` + strings.Repeat("a", limit) + `"}`
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(over))
	err := DecodeJSONWithin(httptest.NewRecorder(), req, &got, limit)
	if !errors.Is(err, ErrBodyTooLarge) || !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a body over the limit: err = %v, want ErrBodyTooLarge and ErrBadRequest", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"login":`))
	if err := DecodeJSONWithin(httptest.NewRecorder(), req, &got, limit); errors.Is(err, ErrBodyTooLarge) || !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a malformed body: err = %v, want ErrBadRequest only", err)
	}
}

func TestDecodeRejectsAnUnknownField(t *testing.T) {
	// A client sending "new_pasword" must be told, not silently left with an
	// unchanged password.
	var got payload

	err := decode(`{"login":"ivanov","typo":"x"}`, &got)

	if err == nil {
		t.Fatal("DecodeJSON() ignored an unknown field")
	}
}

func TestDecodeRejectsASecondJSONDocument(t *testing.T) {
	// Acting on the first document and dropping the rest would let a client
	// believe a request was applied that never was.
	var got payload

	err := decode(`{"login":"ivanov"}{"login":"petrov"}`, &got)

	if err == nil {
		t.Fatal("DecodeJSON() accepted trailing content after the object")
	}
}

func TestDecodeRejectsAnEmptyBody(t *testing.T) {
	var got payload

	err := decode("", &got)

	if err == nil {
		t.Fatal("DecodeJSON() accepted an empty body")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v, want it to say the body is empty", err)
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	var got payload

	err := decode(`{"login":`, &got)

	if err == nil {
		t.Fatal("DecodeJSON() accepted malformed JSON")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("err = %v, want it to name the syntax problem", err)
	}
}

func TestDecodeReportsAWrongFieldType(t *testing.T) {
	// The message names the field, so the client can fix the right one.
	var got payload

	err := decode(`{"count":"not a number"}`, &got)

	if err == nil {
		t.Fatal("DecodeJSON() accepted a string for an int field")
	}
	if !strings.Contains(err.Error(), "count") {
		t.Errorf("err = %v, want it to name the offending field", err)
	}
}

func TestDecodeErrorNeverExposesInternals(t *testing.T) {
	// Messages reach the client verbatim in invalid_request responses.
	var got payload

	err := decode(`{"login":`, &got)

	for _, leaked := range []string{"httpx", "json.SyntaxError", "goroutine", ".go:"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error %q leaks %q", err, leaked)
		}
	}
}

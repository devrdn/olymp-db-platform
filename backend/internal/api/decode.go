package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/httpx"
)

// decodeBody reads a JSON body into v, answering 400 invalid_request itself
// when it cannot, and reports whether the handler may go on. A handler with its
// own body limit and refusal (participant_signals.go) calls
// httpx.DecodeJSONWithin directly.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := httpx.DecodeJSON(w, r, v); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return false
	}
	return true
}

// decodeBodyWithin is decodeBody for a route whose bodies are small: it
// refuses one over limit bytes as it arrives (CLAUDE.md rule 12).
func decodeBodyWithin(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if err := httpx.DecodeJSONWithin(w, r, v, limit); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return false
	}
	return true
}

// decodeOptionalBody is decodeBody for a route whose body may be left out,
// which leaves v as it was. Only a body with no bytes at all counts as absent:
// whitespace or a stray character is still a malformed one.
func decodeOptionalBody(w http.ResponseWriter, r *http.Request, v any) bool {
	buffered := bufio.NewReader(r.Body)
	if _, err := buffered.Peek(1); errors.Is(err, io.EOF) {
		return true
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{buffered, r.Body}
	return decodeBody(w, r, v)
}

// decodeVersionQuery reads expected_version from the query string into
// guard, for a route that takes no other parameter and no body to carry it (a
// delete, whose body a proxy may drop). It is as strict as the body's field:
// a query that does not parse, a parameter the route does not take (a
// misspelt or differently cased name among them), the parameter named twice
// and a value JSON would not read as a whole number, or would read only past
// padding, are 400 invalid_request; null is the parameter left out.
func decodeVersionQuery(w http.ResponseWriter, r *http.Request, guard *versionGuard) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil {
		err = readVersionQuery(query, guard)
	}
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return false
	}
	return true
}

func readVersionQuery(query url.Values, guard *versionGuard) error {
	for name := range query {
		if name != "expected_version" {
			return fmt.Errorf("this route takes no parameter %q", name)
		}
	}
	raw, present := query["expected_version"]
	switch {
	case !present:
		return nil
	case len(raw) != 1, raw[0] != strings.TrimSpace(raw[0]),
		json.Unmarshal([]byte(raw[0]), &guard.ExpectedVersion) != nil:
		return errors.New("expected_version must be one whole number")
	}
	return nil
}

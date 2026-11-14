package api

import (
	"bufio"
	"errors"
	"io"
	"net/http"

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

package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
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

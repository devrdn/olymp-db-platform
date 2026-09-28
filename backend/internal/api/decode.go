package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// decodeBody reads a JSON request body into v and answers 400
// invalid_request itself when it cannot, reporting whether the handler may
// go on. The one refusal every JSON write shares — thirty-nine handlers used
// to spell it out in full.
//
// A handler whose body has a limit of its own and its own refusal for an
// oversized one (participant_signals.go) keeps calling
// httpx.DecodeJSONWithin directly.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := httpx.DecodeJSON(w, r, v); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return false
	}
	return true
}

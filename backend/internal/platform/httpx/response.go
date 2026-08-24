package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
)

const contentTypeJSON = "application/json; charset=utf-8"

// errorBody is the single error shape returned by the whole API, so clients
// have one thing to parse and support has one identifier to trace.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// JSON writes v as a JSON response with the given status code.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)

	if v == nil {
		return
	}
	// The status line is already sent, so an encoding failure cannot be
	// reported to the client; the access log records the truncated response.
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a structured error response. The message is user-facing: it
// must explain the problem without exposing internals.
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	JSON(w, r, status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: logging.RequestIDFrom(r.Context()),
	}})
}

// NoContent replies with 204 and an empty body.
func NoContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

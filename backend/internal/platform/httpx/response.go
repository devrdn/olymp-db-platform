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

// Error writes a structured error response.
//
// The code is what a client acts on and the interface translates; the message
// beside it explains the problem to whoever is reading a log or writing that
// client, and must not expose internals. Only a declared Code can be passed,
// so nothing reaches a client that is missing from the published catalog.
func Error(w http.ResponseWriter, r *http.Request, status int, code Code, message string) {
	JSON(w, r, status, errorBody{Error: errorDetail{
		Code:      code.String(),
		Message:   message,
		RequestID: logging.RequestIDFrom(r.Context()),
	}})
}

// ErrorWithDetails writes the same error object with extra fields beside it.
//
// The publish gate needs it: alongside the code it returns the list of what is
// still missing. This exists so that answering with more than the error object
// does not mean writing the envelope by hand — which is how a code once
// reached clients without being declared at all, invisible to the check that
// existed to prevent exactly that.
//
// A "error" key in details is ignored: overwriting the object every client
// parses would make the code vanish from a response that still looked well
// formed.
func ErrorWithDetails(w http.ResponseWriter, r *http.Request, status int, code Code, message string, details map[string]any) {
	body := map[string]any{
		"error": errorDetail{
			Code:      code.String(),
			Message:   message,
			RequestID: logging.RequestIDFrom(r.Context()),
		},
	}
	for key, value := range details {
		if key == "error" {
			continue
		}
		body[key] = value
	}
	JSON(w, r, status, body)
}

// NoContent replies with 204 and an empty body.
func NoContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

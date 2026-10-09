package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
)

const contentTypeJSON = "application/json; charset=utf-8"

// errorBody is the single error shape returned by the whole API.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)

	if v == nil {
		return
	}
	// The status line is already sent, so an encoding failure cannot be
	// reported to the client.
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a structured error response. The message is for developers
// and must not expose internals; only a declared Code can be passed, so every
// code a client sees is in the published catalog.
func Error(w http.ResponseWriter, r *http.Request, status int, code Code, message string) {
	JSON(w, r, status, errorBody{Error: errorDetail{
		Code:      code.String(),
		Message:   message,
		RequestID: logging.RequestIDFrom(r.Context()),
	}})
}

// ErrorWithDetails writes the same error object with extra fields beside it,
// so no handler writes the envelope by hand. An "error" key in details is
// ignored, so it cannot replace the error object.
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

func NoContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

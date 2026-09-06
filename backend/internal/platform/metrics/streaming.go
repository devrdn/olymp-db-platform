package metrics

import (
	"net/http"
)

// streamingKey is the context key Middleware uses to let a handler mark its
// own response as long-lived (finding 5). Middleware plants a fresh *bool
// under this key before calling the next handler, and reads it back after —
// the request-scoped mailbox a handler that only learns of a decision partway
// through serving uses to report it upward, the same shape
// StatusRecorder.Status() reports the status code chosen deep inside a
// handler back to the logging and metrics middleware wrapped around it.
type streamingKey struct{}

// MarkStreaming tells the enclosing Middleware that this response is a
// long-lived stream — an SSE channel that can stay open for the length of a
// contest — so its eventual duration is recorded apart from the
// request-duration histogram every ordinary request shares (see
// Recorder.ObserveRequest's own doc). Call it once, as early as the handler
// can once it knows it will actually hold the connection open — after every
// admission check that might still answer with an ordinary 4xx has passed,
// not before.
//
// A no-op when r did not come through Middleware (a standalone handler test,
// for instance): the request then simply keeps counting toward the shared
// histogram, which is the correct default, not a hidden failure.
func MarkStreaming(r *http.Request) {
	if marker, ok := r.Context().Value(streamingKey{}).(*bool); ok {
		*marker = true
	}
}

package metrics

import (
	"net/http"
)

// streamingKey holds the *bool Middleware plants before the handler runs and
// reads back after it.
type streamingKey struct{}

// MarkStreaming tells the enclosing Middleware that this response is a
// long-lived stream, so its duration is recorded apart (see Recorder). Call it
// only after every check that might still answer with an ordinary 4xx. It is
// a no-op when r did not come through Middleware.
func MarkStreaming(r *http.Request) {
	if marker, ok := r.Context().Value(streamingKey{}).(*bool); ok {
		*marker = true
	}
}

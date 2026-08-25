package httpx

import "net/http"

// StatusRecorder captures the status code and response size of a response for
// logging and metrics. It is the single implementation both use, so a fix to
// response recording cannot land in one consumer and not the other.
//
// It assumes 200 until the handler says otherwise, matching net/http's
// behaviour when a handler writes a body without an explicit status.
type StatusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

// NewStatusRecorder wraps w for observation.
func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, status: http.StatusOK}
}

// Status reports the response status the handler produced.
func (r *StatusRecorder) Status() int { return r.status }

// BytesWritten reports the size of the body written so far.
func (r *StatusRecorder) BytesWritten() int { return r.written }

func (r *StatusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *StatusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// Unwrap exposes the wrapped writer to http.ResponseController, keeping
// streaming responses (SSE) usable behind the middleware chain.
func (r *StatusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

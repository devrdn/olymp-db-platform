package httpx

import "net/http"

// StatusRecorder captures the status code and response size for logging and
// metrics. It assumes 200 until the handler says otherwise, as net/http does.
type StatusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *StatusRecorder) Status() int { return r.status }

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

// Unwrap exposes the wrapped writer to http.ResponseController, so SSE works
// behind the middleware chain.
func (r *StatusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

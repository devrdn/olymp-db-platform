package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusRecorderCapturesExplicitStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := NewStatusRecorder(rec)

	sr.WriteHeader(http.StatusTeapot)

	if sr.Status() != http.StatusTeapot {
		t.Errorf("Status() = %d, want %d", sr.Status(), http.StatusTeapot)
	}
}

func TestStatusRecorderDefaultsToOKWithoutWriteHeader(t *testing.T) {
	// net/http sends 200 when a handler writes a body without a status; the
	// recorder must agree, or logs and metrics would report status 0.
	sr := NewStatusRecorder(httptest.NewRecorder())

	_, _ = sr.Write([]byte("body"))

	if sr.Status() != http.StatusOK {
		t.Errorf("Status() = %d, want %d", sr.Status(), http.StatusOK)
	}
}

func TestStatusRecorderCountsBytes(t *testing.T) {
	sr := NewStatusRecorder(httptest.NewRecorder())

	_, _ = sr.Write([]byte("hello"))
	_, _ = sr.Write([]byte(" world"))

	if sr.BytesWritten() != len("hello world") {
		t.Errorf("BytesWritten() = %d, want %d", sr.BytesWritten(), len("hello world"))
	}
}

func TestStatusRecorderUnwrapExposesTheUnderlyingWriter(t *testing.T) {
	// http.ResponseController relies on Unwrap to reach Flush and friends;
	// without it, streaming (SSE) breaks behind the middleware chain.
	inner := httptest.NewRecorder()
	sr := NewStatusRecorder(inner)

	if sr.Unwrap() != http.ResponseWriter(inner) {
		t.Error("Unwrap() does not return the wrapped writer")
	}
}

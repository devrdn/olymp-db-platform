package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
)

// metaSink captures the audit entries written while serving a request.
type metaSink struct{ entries []audit.Entry }

func (s *metaSink) Append(_ context.Context, e audit.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

func (s *metaSink) AppendMany(_ context.Context, entries []audit.Entry) error {
	s.entries = append(s.entries, entries...)
	return nil
}

// recordDuring serves one request through the middleware and returns the audit
// entry the handler wrote.
func recordDuring(t *testing.T, req *http.Request) audit.Entry {
	t.Helper()
	sink := &metaSink{}
	recorder := audit.New(sink)
	handler := requestMeta(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := recorder.Record(r.Context(), audit.Entry{Action: audit.ActionUserBlock}); err != nil {
			t.Errorf("Record returned error: %v", err)
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if len(sink.entries) != 1 {
		t.Fatalf("wrote %d entries, want 1", len(sink.entries))
	}
	return sink.entries[0]
}

func TestRequestMetaStampsTheRequestOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/users/x/block", nil)
	req.RemoteAddr = "203.0.113.7:41000"
	req.Header.Set("User-Agent", "Admin/1.0")

	got := recordDuring(t, req)

	if got.IP != "203.0.113.7" {
		t.Errorf("IP = %q, want the request peer", got.IP)
	}
	if got.UserAgent != "Admin/1.0" {
		t.Errorf("UserAgent = %q, want the request agent", got.UserAgent)
	}
}

func TestRequestMetaTruncatesAnAbsurdUserAgent(t *testing.T) {
	// The header is attacker-controlled and the column is kept for a year.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", strings.Repeat("x", 5000))

	got := recordDuring(t, req)

	if len(got.UserAgent) > audit.MaxUserAgentLength {
		t.Errorf("UserAgent is %d bytes, want at most %d", len(got.UserAgent), audit.MaxUserAgentLength)
	}
}

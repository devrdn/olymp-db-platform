package connector_test

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/connector"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/sentineltest"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".")
}

func TestTheHTTPClientHasATimeout(t *testing.T) {
	if got := connector.NewHTTPClient().Timeout; got <= 0 || got > 15*time.Second {
		t.Errorf("timeout = %s, want a few seconds", got)
	}
}

func TestAResponseLargerThanTheBoundIsRefusedAsItArrives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, rand.Reader, 300<<10)
	}))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := connector.NewHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("Do() = %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("reading 300 KiB = %v, want the bound's refusal", err)
	}
}

func TestCutNeverSplitsALetter(t *testing.T) {
	if got := connector.Cut("ab", 5); got != "ab" {
		t.Errorf("Cut(short) = %q", got)
	}
	if got := connector.Cut("aé", 2); got != "a" {
		t.Errorf("Cut(\"aé\", 2) = %q, want the letter dropped whole", got)
	}
}

package httpx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/google/uuid"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func decodeRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no log record was written")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log record is not valid JSON: %v (raw: %s)", err, line)
	}
	return rec
}

func TestRequestIDGeneratesIdentifierWhenHeaderAbsent(t *testing.T) {
	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.RequestIDFrom(r.Context())
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("no request id was placed in the request context")
	}
	if _, err := uuid.Parse(seen); err != nil {
		t.Errorf("generated request id %q is not a UUID: %v", seen, err)
	}
	if got := rec.Header().Get(RequestIDHeader); got != seen {
		t.Errorf("response header %s = %q, want %q", RequestIDHeader, got, seen)
	}
}

func TestRequestIDReusesValidIncomingIdentifier(t *testing.T) {
	incoming := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.RequestIDFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, incoming)

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen != incoming {
		t.Errorf("request id = %q, want the incoming %q", seen, incoming)
	}
}

func TestRequestIDReplacesMalformedIncomingIdentifier(t *testing.T) {
	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.RequestIDFrom(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "not-a-uuid; injected log line")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen == "not-a-uuid; injected log line" {
		t.Fatal("malformed client-supplied request id was trusted")
	}
	if _, err := uuid.Parse(seen); err != nil {
		t.Errorf("replacement request id %q is not a UUID: %v", seen, err)
	}
}

func TestRecovererTurnsPanicIntoInternalServerError(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New("info", &buf)
	handler := Recoverer(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("panic details leaked to the client: %s", rec.Body.String())
	}
	record := decodeRecord(t, &buf)
	if record["level"] != "ERROR" {
		t.Errorf("panic logged at level %v, want ERROR", record["level"])
	}
}

func TestRecovererLetsNormalResponsesThrough(t *testing.T) {
	var buf bytes.Buffer
	handler := Recoverer(logging.New("info", &buf))(okHandler)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestAccessLogRecordsRequestOutcome(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New("info", &buf)
	handler := AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/contests", nil))

	rec := decodeRecord(t, &buf)
	if rec["method"] != http.MethodPost {
		t.Errorf("method = %v, want POST", rec["method"])
	}
	if rec["path"] != "/contests" {
		t.Errorf("path = %v, want /contests", rec["path"])
	}
	if rec["status"] != float64(http.StatusTeapot) {
		t.Errorf("status = %v, want %d", rec["status"], http.StatusTeapot)
	}
	if _, ok := rec["duration_ms"]; !ok {
		t.Error("record has no duration_ms field")
	}
}

func TestAccessLogIncludesRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New("info", &buf)
	handler := RequestID(AccessLog(log)(okHandler))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	rec := decodeRecord(t, &buf)
	if _, ok := rec["request_id"]; !ok {
		t.Error("access log record carries no request_id")
	}
}

func TestStatusRecorderDefaultsToOKWhenHandlerNeverWritesHeader(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New("info", &buf)
	handler := AccessLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body without explicit WriteHeader"))
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	rec := decodeRecord(t, &buf)
	if rec["status"] != float64(http.StatusOK) {
		t.Errorf("status = %v, want %d", rec["status"], http.StatusOK)
	}
}

func TestAddressSubjectGroupsAnIPv6NetworkAndLeavesIPv4Alone(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"ipv4":                    {"203.0.113.7", "203.0.113.7"},
		"ipv4-mapped ipv6":        {"::ffff:203.0.113.7", "203.0.113.7"},
		"ipv6 host":               {"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		"another host, same /64":  {"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		"ipv6 with a zone":        {"fe80::1%eth0", "fe80::/64"},
		"empty":                   {"", ""},
		"unparseable is verbatim": {"not-an-address", "not-an-address"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := AddressSubject(tc.in); got != tc.want {
				t.Errorf("AddressSubject(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if AddressSubject("2001:db8:1:2::1") == AddressSubject("2001:db8:1:3::1") {
		t.Error("two different /64 networks share a subject")
	}
}

func TestClientSubjectIsTheSubjectOfTheClientAddress(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "[2001:db8:1:2::42]:443"

	if got := ClientSubject(req); got != "2001:db8:1:2::/64" {
		t.Errorf("ClientSubject() = %q, want the /64", got)
	}
}

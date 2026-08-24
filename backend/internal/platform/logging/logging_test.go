package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// decodeLine parses the single JSON log record written to buf.
func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	if buf.Len() == 0 {
		t.Fatal("no log record was written")
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log record is not valid JSON: %v (raw: %s)", err, buf.String())
	}
	return rec
}

func TestNewWritesStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	log := New("info", &buf)

	log.Info("service started", "addr", ":8080")

	rec := decodeLine(t, &buf)
	if rec["msg"] != "service started" {
		t.Errorf("msg = %v, want %q", rec["msg"], "service started")
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["addr"] != ":8080" {
		t.Errorf("addr = %v, want :8080", rec["addr"])
	}
	if _, ok := rec["time"]; !ok {
		t.Error("record has no time field")
	}
}

func TestNewSuppressesRecordsBelowConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New("warn", &buf)

	log.Info("this should not be logged")

	if buf.Len() != 0 {
		t.Errorf("info record was written at warn level: %s", buf.String())
	}
}

func TestLoggerAddsRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	log := New("info", &buf)
	ctx := WithRequestID(context.Background(), "req-123")

	log.InfoContext(ctx, "handled request")

	rec := decodeLine(t, &buf)
	if rec["request_id"] != "req-123" {
		t.Errorf("request_id = %v, want req-123", rec["request_id"])
	}
}

func TestLoggerAddsUserIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	log := New("info", &buf)
	ctx := WithUserID(context.Background(), "user-42")

	log.InfoContext(ctx, "handled request")

	rec := decodeLine(t, &buf)
	if rec["user_id"] != "user-42" {
		t.Errorf("user_id = %v, want user-42", rec["user_id"])
	}
}

func TestLoggerOmitsContextFieldsWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	log := New("info", &buf)

	log.InfoContext(context.Background(), "no context values")

	rec := decodeLine(t, &buf)
	if _, ok := rec["request_id"]; ok {
		t.Error("request_id present although context carried none")
	}
	if _, ok := rec["user_id"]; ok {
		t.Error("user_id present although context carried none")
	}
}

func TestRequestIDFromReturnsEmptyStringWhenAbsent(t *testing.T) {
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Errorf("RequestIDFrom(empty ctx) = %q, want empty string", got)
	}
}

func TestRequestIDFromReturnsStoredValue(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-7")

	if got := RequestIDFrom(ctx); got != "req-7" {
		t.Errorf("RequestIDFrom = %q, want req-7", got)
	}
}

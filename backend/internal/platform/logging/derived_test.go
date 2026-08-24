package logging

import (
	"bytes"
	"context"
	"testing"
)

// Handlers derive child loggers with With(...). If deriving dropped the
// context behaviour, correlation fields would silently disappear from exactly
// the components that log the most.
func TestChildLoggerFromWithKeepsContextFields(t *testing.T) {
	var buf bytes.Buffer
	log := New("info", &buf).With("component", "queryproxy")
	ctx := WithRequestID(context.Background(), "req-9")

	log.InfoContext(ctx, "derived logger")

	rec := decodeLine(t, &buf)
	if rec["request_id"] != "req-9" {
		t.Errorf("request_id = %v, want req-9 (child logger lost the context handler)", rec["request_id"])
	}
	if rec["component"] != "queryproxy" {
		t.Errorf("component = %v, want queryproxy", rec["component"])
	}
}

func TestGroupedLoggerKeepsContextFields(t *testing.T) {
	var buf bytes.Buffer
	log := New("info", &buf).WithGroup("db")
	ctx := WithRequestID(context.Background(), "req-10")

	log.InfoContext(ctx, "grouped logger")

	rec := decodeLine(t, &buf)
	if rec["request_id"] != "req-10" {
		t.Errorf("request_id = %v, want req-10 (grouped logger lost the context handler)", rec["request_id"])
	}
}

func TestLevelNamesMapToTheirSeverity(t *testing.T) {
	cases := []struct {
		level    string
		logged   bool
		emitFunc func(l loggerFuncs)
	}{
		{level: "debug", logged: true, emitFunc: func(l loggerFuncs) { l.debug() }},
		{level: "info", logged: false, emitFunc: func(l loggerFuncs) { l.debug() }},
		{level: "warn", logged: false, emitFunc: func(l loggerFuncs) { l.info() }},
		{level: "warn", logged: true, emitFunc: func(l loggerFuncs) { l.warn() }},
		{level: "error", logged: false, emitFunc: func(l loggerFuncs) { l.warn() }},
		{level: "error", logged: true, emitFunc: func(l loggerFuncs) { l.error() }},
	}

	for _, tc := range cases {
		var buf bytes.Buffer
		log := New(tc.level, &buf)
		tc.emitFunc(loggerFuncs{
			debug: func() { log.Debug("m") },
			info:  func() { log.Info("m") },
			warn:  func() { log.Warn("m") },
			error: func() { log.Error("m") },
		})

		if got := buf.Len() > 0; got != tc.logged {
			t.Errorf("level %q: record written = %v, want %v", tc.level, got, tc.logged)
		}
	}
}

type loggerFuncs struct {
	debug func()
	info  func()
	warn  func()
	error func()
}

func TestUnknownLevelFallsBackToInfo(t *testing.T) {
	// Configuration rejects unknown levels, but the logger must still behave
	// predictably if one slips through.
	var buf bytes.Buffer
	log := New("verbose", &buf)

	log.Debug("suppressed at info")
	if buf.Len() != 0 {
		t.Errorf("debug record written at fallback level: %s", buf.String())
	}

	log.Info("emitted at info")
	if buf.Len() == 0 {
		t.Error("info record suppressed at fallback level")
	}
}

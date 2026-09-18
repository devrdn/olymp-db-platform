package monitor_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/google/uuid"
)

func TestASaveWithinThirtySecondsOfARevisionsStartExtendsIt(t *testing.T) {
	started := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	for offset, want := range map[time.Duration]bool{
		0:                                     true,
		29*time.Second + 999*time.Millisecond: true,
		30 * time.Second:                      false,
		time.Minute:                           false,
		// A clock that stepped back still extends the open revision.
		-time.Second: true,
	} {
		if got := monitor.Extends(started, started.Add(offset)); got != want {
			t.Errorf("Extends(start, start%+v) = %t, want %t", offset, got, want)
		}
	}
}

func TestARevisionNamesItsDocument(t *testing.T) {
	good := monitor.Revision{Registration: uuid.New(), Document: monitor.DocumentNotes, Body: "x", At: time.Now()}
	if err := good.Validate(); err != nil {
		t.Fatalf("notes: %v", err)
	}
	tab := monitor.Revision{Registration: uuid.New(), Document: monitor.TabDocument(uuid.New()), Title: "Query 1", At: time.Now()}
	if err := tab.Validate(); err != nil {
		t.Fatalf("a tab: %v", err)
	}

	for name, rev := range map[string]monitor.Revision{
		"no registration":   {Document: monitor.DocumentNotes, At: time.Now()},
		"unknown document":  {Registration: uuid.New(), Document: "scratchpad", At: time.Now()},
		"no time":           {Registration: uuid.New(), Document: monitor.DocumentNotes},
		"body too long":     {Registration: uuid.New(), Document: monitor.DocumentNotes, Body: strings.Repeat("x", monitor.MaxRevisionBodyBytes+1), At: time.Now()},
		"title too long":    {Registration: uuid.New(), Document: monitor.TabDocument(uuid.New()), Title: strings.Repeat("t", monitor.MaxTabTitleRunes+1), At: time.Now()},
		"notes with a name": {Registration: uuid.New(), Document: monitor.DocumentNotes, Title: "notes", At: time.Now()},
	} {
		if err := rev.Validate(); !errors.Is(err, monitor.ErrRevisionInvalid) {
			t.Errorf("%s: err = %v, want ErrRevisionInvalid", name, err)
		}
	}
}

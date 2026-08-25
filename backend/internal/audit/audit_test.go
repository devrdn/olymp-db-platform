package audit

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// recordingSink captures entries instead of writing them to a database.
type recordingSink struct {
	entries []Entry
	err     error
}

func (s *recordingSink) Append(_ context.Context, e Entry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, e)
	return nil
}

func TestRecordStoresTheAction(t *testing.T) {
	sink := &recordingSink{}
	recorder := New(sink)
	actor := uuid.New()

	err := recorder.Record(context.Background(), Entry{
		ActorID: &actor,
		Action:  ActionAuthLogin,
		Entity:  "user",
	})

	if err != nil {
		t.Fatalf("Record() returned error: %v", err)
	}
	if len(sink.entries) != 1 {
		t.Fatalf("sink holds %d entries, want 1", len(sink.entries))
	}
	if sink.entries[0].Action != ActionAuthLogin {
		t.Errorf("Action = %q, want %q", sink.entries[0].Action, ActionAuthLogin)
	}
}

func TestRecordAcceptsASystemEntryWithoutAnActor(t *testing.T) {
	// Scheduled transitions and provisioning have no human behind them.
	sink := &recordingSink{}

	err := New(sink).Record(context.Background(), Entry{Action: "contest.auto_finished"})

	if err != nil {
		t.Fatalf("Record() returned error: %v", err)
	}
	if sink.entries[0].ActorID != nil {
		t.Error("a system entry was given an actor")
	}
}

func TestRecordRejectsAnEntryWithoutAnAction(t *testing.T) {
	// An entry that does not say what happened is worse than none: it looks
	// like coverage while carrying nothing.
	sink := &recordingSink{}

	err := New(sink).Record(context.Background(), Entry{Entity: "user"})

	if err == nil {
		t.Fatal("Record() accepted an entry with no action")
	}
	if len(sink.entries) != 0 {
		t.Error("the invalid entry was written anyway")
	}
}

func TestRecordStripsSensitiveFieldsFromThePayload(t *testing.T) {
	// Handlers pass request data through; a stray password or token must never
	// reach a table that is kept for a year and read by administrators.
	sink := &recordingSink{}

	_ = New(sink).Record(context.Background(), Entry{
		Action: "user.create",
		Payload: map[string]any{
			"login":         "ivanov",
			"password":      "hunter2",
			"new_password":  "hunter3",
			"token":         "abc",
			"secret":        "s3cr3t",
			"password_hash": "$argon2id$...",
		},
	})

	stored := sink.entries[0].Payload
	for _, key := range []string{"password", "new_password", "token", "secret", "password_hash"} {
		if _, present := stored[key]; present {
			t.Errorf("payload still carries the sensitive key %q", key)
		}
	}
	if stored["login"] != "ivanov" {
		t.Errorf("payload lost the harmless field: %v", stored)
	}
}

func TestRecordRedactsNestedSensitiveFields(t *testing.T) {
	sink := &recordingSink{}

	_ = New(sink).Record(context.Background(), Entry{
		Action: "user.create",
		Payload: map[string]any{
			"user": map[string]any{"login": "ivanov", "password": "hunter2"},
		},
	})

	nested := sink.entries[0].Payload["user"].(map[string]any)
	if _, present := nested["password"]; present {
		t.Errorf("nested password survived redaction: %v", nested)
	}
	if nested["login"] != "ivanov" {
		t.Errorf("nested payload lost the harmless field: %v", nested)
	}
}

func TestRecordPropagatesAStorageFailure(t *testing.T) {
	// The caller decides what a failed audit write means; for anything
	// security-relevant it must fail the whole action.
	sink := &recordingSink{err: context.DeadlineExceeded}

	err := New(sink).Record(context.Background(), Entry{Action: "user.block"})

	if err == nil {
		t.Fatal("Record() hid a storage failure")
	}
}

func TestRecordFillsOriginFromTheRequestContext(t *testing.T) {
	// Handlers pass ctx everywhere already; carrying the origin in it means
	// every audit write — including future ones — names where the action came
	// from without each call site remembering to.
	sink := &recordingSink{}
	ctx := WithRequestMeta(context.Background(), "203.0.113.7", "Mozilla/5.0")

	_ = New(sink).Record(ctx, Entry{Action: ActionUserBlock})

	got := sink.entries[0]
	if got.IP != "203.0.113.7" {
		t.Errorf("IP = %q, want the context origin", got.IP)
	}
	if got.UserAgent != "Mozilla/5.0" {
		t.Errorf("UserAgent = %q, want the context agent", got.UserAgent)
	}
}

func TestExplicitOriginWinsOverTheContext(t *testing.T) {
	// The login flow resolves its own origin before the session exists; an
	// explicit value must not be overwritten by ambient data.
	sink := &recordingSink{}
	ctx := WithRequestMeta(context.Background(), "10.0.0.1", "ctx-agent")

	_ = New(sink).Record(ctx, Entry{Action: ActionAuthLogin, IP: "203.0.113.7", UserAgent: "explicit"})

	got := sink.entries[0]
	if got.IP != "203.0.113.7" || got.UserAgent != "explicit" {
		t.Errorf("origin = %q/%q, want the explicit values to win", got.IP, got.UserAgent)
	}
}

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// writeTrail stores a few entries for one actor and returns them.
func writeTrail(t *testing.T, ctx context.Context, actor uuid.UUID) {
	t.Helper()

	sink := NewAuditSink(testPool)
	for _, e := range []audit.Entry{
		{ActorID: &actor, Action: "user.create", Entity: "user", EntityID: "u-1", IP: "10.0.0.1"},
		{ActorID: &actor, Action: "user.block", Entity: "user", EntityID: "u-1"},
		{Action: "contest.status_change", Entity: "contest", EntityID: "c-1",
			Payload: map[string]any{"to": "published"}},
	} {
		if err := sink.Append(ctx, e); err != nil {
			t.Fatalf("Append() = %v", err)
		}
	}
}

func TestTheTrailComesBackNewestFirst(t *testing.T) {
	// An administrator opening the panel is looking at what just happened, not
	// at what happened a year ago.
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-order")
		writeTrail(t, ctx, actor.ID)

		found, _, err := NewAuditTrail(testPool).List(ctx, audit.Filter{Limit: 3}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 3 {
			t.Fatalf("listed %d entries, want 3", len(found))
		}
		for i := 1; i < len(found); i++ {
			if found[i-1].CreatedAt.Before(found[i].CreatedAt) {
				t.Errorf("entry %d is older than the one after it", i-1)
			}
		}
	})
}

func TestTheTrailNamesTheActorRatherThanItsIdentifier(t *testing.T) {
	// A page of UUIDs answers nothing; the question is who did it.
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-login")
		writeTrail(t, ctx, actor.ID)

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Actor: actor.ID, Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 2 {
			t.Fatalf("listed %d entries for the actor, want 2", len(found))
		}
		for _, r := range found {
			if r.ActorLogin != "auditor-login" {
				t.Errorf("actor login = %q, want auditor-login", r.ActorLogin)
			}
		}
	})
}

func TestASystemEventHasNoActorAndSaysSo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-system")
		writeTrail(t, ctx, actor.ID)

		found, _, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Action: "contest.status_change", Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 {
			t.Fatalf("listed %d entries, want 1", len(found))
		}
		if found[0].ActorID != nil || found[0].ActorLogin != "" {
			t.Errorf("a system event reported actor %v/%q, want neither",
				found[0].ActorID, found[0].ActorLogin)
		}
		if found[0].Payload["to"] != "published" {
			t.Errorf("payload = %v, want it read back", found[0].Payload)
		}
	})
}

func TestTheTrailFiltersToOneThing(t *testing.T) {
	// "What happened to this contest" is the other question the panel is for,
	// and the table carries an index for exactly it.
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-entity")
		writeTrail(t, ctx, actor.ID)

		found, total, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Entity: "user", EntityID: "u-1", Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total != 2 || len(found) != 2 {
			t.Errorf("found %d of %d, want both entries about that user", len(found), total)
		}
	})
}

func TestTheTrailFiltersByTime(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-time")
		writeTrail(t, ctx, actor.ID)
		tomorrow := time.Now().Add(24 * time.Hour)

		_, total, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{From: &tomorrow, Limit: 10}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total != 0 {
			t.Errorf("total = %d for a window that has not started, want 0", total)
		}
	})
}

func TestTheTotalCountsEveryMatchNotJustThePage(t *testing.T) {
	// The panel pages through history; a total that counted only the page
	// would tell an administrator there is nothing more to look at.
	withTx(t, func(ctx context.Context) {
		actor := makeUser(t, ctx, "auditor-total")
		writeTrail(t, ctx, actor.ID)

		found, total, err := NewAuditTrail(testPool).List(ctx,
			audit.Filter{Actor: actor.ID, Limit: 1}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if len(found) != 1 || total != 2 {
			t.Errorf("page of %d with total %d, want 1 of 2", len(found), total)
		}
	})
}

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/registrations"
	"github.com/google/uuid"
)

func eventRegistration(email, code string) registrations.Registration {
	return registrations.Registration{FirstName: "Ana", LastName: "Popescu", Email: email, EmailKey: email, Code: code}
}

func TestEventRegistrationsInsertAndFindByEmail(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewEventRegistrations(testPool)
		user := makeUser(t, ctx, "ana-event@example.com")
		r := eventRegistration("ana@example.com", "K7M2QX")
		r.Email = "Ana@example.com"
		r.UserID = &user.ID

		stored, err := repo.Insert(ctx, r, registrations.Capacity, true)
		if err != nil {
			t.Fatalf("Insert() = %v", err)
		}
		if stored.ID.String() == "" || stored.CreatedAt.IsZero() || stored.LastSentAt == nil || stored.CheckedInAt != nil {
			t.Errorf("stored = %+v, want an id, a creation time and a sending time", stored)
		}

		found, err := repo.ByEmailKey(ctx, "ana@example.com")
		if err != nil {
			t.Fatalf("ByEmailKey() = %v", err)
		}
		if found.ID != stored.ID || found.Code != "K7M2QX" || found.Email != "Ana@example.com" || found.Login != user.Login {
			t.Errorf("found = %+v, want the stored row with its account's login", found)
		}
		if _, err := repo.ByEmailKey(ctx, "bob@example.com"); !errors.Is(err, registrations.ErrNotFound) {
			t.Errorf("unknown email: ByEmailKey() = %v, want ErrNotFound", err)
		}
	})
}

// Neither refusal may abort the caller's transaction: the next statement in
// it must still run.
func TestEventRegistrationsInsertRefusesAHeldEmailOrCodeAndAFullTable(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewEventRegistrations(testPool)
		if _, err := repo.Insert(ctx, eventRegistration("ana@example.com", "K7M2QX"), registrations.Capacity, false); err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		if _, err := repo.Insert(ctx, eventRegistration("ana@example.com", "AAAAAA"), registrations.Capacity, false); !errors.Is(err, registrations.ErrAlreadyRegistered) {
			t.Errorf("a held email: Insert() = %v, want ErrAlreadyRegistered", err)
		}
		if _, err := repo.Insert(ctx, eventRegistration("bob@example.com", "K7M2QX"), registrations.Capacity, false); !errors.Is(err, registrations.ErrCodeTaken) {
			t.Errorf("a held code: Insert() = %v, want ErrCodeTaken", err)
		}
		if _, err := repo.Insert(ctx, eventRegistration("bob@example.com", "BBBBBB"), 1, false); !errors.Is(err, registrations.ErrFull) {
			t.Errorf("a full table: Insert() = %v, want ErrFull", err)
		}
		n, err := repo.Count(ctx)
		if err != nil || n != 1 {
			t.Errorf("Count() = %d, %v; want the one row, in a transaction still usable", n, err)
		}
	})
}

func TestEventRegistrationsResendWaitsForTheCooldown(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewEventRegistrations(testPool)
		stored, err := repo.Insert(ctx, eventRegistration("ana@example.com", "K7M2QX"), registrations.Capacity, true)
		if err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		if claimed, err := repo.ClaimResend(ctx, stored.ID, registrations.ResendCooldown); err != nil || claimed {
			t.Errorf("just sent: ClaimResend() = %v, %v; want it held back", claimed, err)
		}

		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE event_registrations SET last_sent_at = now() - interval '5 minutes' WHERE id = $1`, stored.ID); err != nil {
			t.Fatalf("age the sending: %v", err)
		}
		if claimed, err := repo.ClaimResend(ctx, stored.ID, registrations.ResendCooldown); err != nil || !claimed {
			t.Errorf("after the cooldown: ClaimResend() = %v, %v; want it claimed", claimed, err)
		}
		if claimed, err := repo.ClaimResend(ctx, stored.ID, registrations.ResendCooldown); err != nil || claimed {
			t.Errorf("claimed again at once: ClaimResend() = %v, %v; want it held back", claimed, err)
		}

		never, err := repo.Insert(ctx, eventRegistration("bob@example.com", "BBBBBB"), registrations.Capacity, false)
		if err != nil {
			t.Fatalf("Insert() = %v", err)
		}
		if claimed, err := repo.ClaimResend(ctx, never.ID, time.Hour); err != nil || !claimed {
			t.Errorf("never sent: ClaimResend() = %v, %v; want it claimed", claimed, err)
		}
	})
}

func TestEventRegistrationsListNewestFirstAndCheckIn(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewEventRegistrations(testPool)
		older, err := repo.Insert(ctx, eventRegistration("ana@example.com", "AAAAAA"), registrations.Capacity, false)
		if err != nil {
			t.Fatalf("Insert() = %v", err)
		}
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE event_registrations SET created_at = now() - interval '1 minute' WHERE id = $1`, older.ID); err != nil {
			t.Fatalf("age the row: %v", err)
		}
		newer, err := repo.Insert(ctx, eventRegistration("bob@example.com", "BBBBBB"), registrations.Capacity, false)
		if err != nil {
			t.Fatalf("Insert() = %v", err)
		}

		list, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if len(list) != 2 || list[0].ID != newer.ID || list[1].ID != older.ID {
			t.Errorf("List() = %+v, want newest first", list)
		}

		in, err := repo.SetCheckedIn(ctx, older.ID, true)
		if err != nil || in.CheckedInAt == nil {
			t.Fatalf("SetCheckedIn(true) = %+v, %v", in, err)
		}
		again, err := repo.SetCheckedIn(ctx, older.ID, true)
		if err != nil || again.CheckedInAt == nil || !again.CheckedInAt.Equal(*in.CheckedInAt) {
			t.Errorf("checked in twice: %+v, %v; want the first arrival kept", again, err)
		}
		out, err := repo.SetCheckedIn(ctx, older.ID, false)
		if err != nil || out.CheckedInAt != nil {
			t.Errorf("SetCheckedIn(false) = %+v, %v", out, err)
		}
		if _, err := repo.SetCheckedIn(ctx, uuid.New(), true); !errors.Is(err, registrations.ErrNotFound) {
			t.Errorf("unknown id: SetCheckedIn() = %v, want ErrNotFound", err)
		}
	})
}

func TestEventRegistrationsAttachAnAccountOnlyToOneWithout(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewEventRegistrations(testPool)
		if _, err := repo.Insert(ctx, eventRegistration("ana@example.com", "AAAAAA"), registrations.Capacity, false); err != nil {
			t.Fatalf("Insert() = %v", err)
		}
		first := makeUser(t, ctx, "ana-first@example.com")
		second := makeUser(t, ctx, "ana-second@example.com")

		attached, err := repo.AttachAccount(ctx, "ana@example.com", first.ID)
		if err != nil || attached.UserID == nil || *attached.UserID != first.ID || attached.Login != first.Login {
			t.Fatalf("AttachAccount() = %+v, %v; want the account linked", attached, err)
		}
		if _, err := repo.AttachAccount(ctx, "ana@example.com", second.ID); !errors.Is(err, registrations.ErrNotFound) {
			t.Errorf("already linked: AttachAccount() = %v, want ErrNotFound", err)
		}
		if _, err := repo.AttachAccount(ctx, "bob@example.com", second.ID); !errors.Is(err, registrations.ErrNotFound) {
			t.Errorf("unknown email: AttachAccount() = %v, want ErrNotFound", err)
		}
	})
}

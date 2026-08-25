package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

func TestParticipantCarriesTheAccountItNames(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-reg")
		student := makeUser(t, ctx, "student-reg")
		id := makeContest(t, ctx, author.ID)

		added, err := repo.Add(ctx, id, student.ID)
		if err != nil {
			t.Fatalf("Add() = %v", err)
		}
		if added.Status != contests.RegistrationRegistered {
			t.Errorf("status = %q, want registered", added.Status)
		}

		got, err := repo.ByUser(ctx, id, student.ID)
		if err != nil {
			t.Fatalf("ByUser() = %v", err)
		}
		if got.Login != "student-reg" {
			t.Errorf("login = %q, want student-reg", got.Login)
		}
	})
}

func TestRegisteringTheSamePersonTwiceIsReportedAsAlreadyEnrolled(t *testing.T) {
	// The unique index is the real guarantee against two staff adding the same
	// student at the same moment; it has to arrive as something the caller can
	// act on rather than an opaque constraint violation.
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-dup")
		student := makeUser(t, ctx, "student-dup")
		id := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, id, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		_, err := repo.Add(ctx, id, student.ID)

		if !errors.Is(err, contests.ErrAlreadyEnrolled) {
			t.Errorf("Add() twice = %v, want ErrAlreadyEnrolled", err)
		}
	})
}

func TestParticipantsAreFilteredByStatus(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-filter")
		staying := makeUser(t, ctx, "student-staying")
		excluded := makeUser(t, ctx, "student-excluded")
		id := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, id, staying.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}
		out, err := repo.Add(ctx, id, excluded.ID)
		if err != nil {
			t.Fatalf("Add() = %v", err)
		}
		if err := repo.SetStatus(ctx, out.ID, contests.RegistrationDisqualified); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		found, total, err := repo.List(ctx, id, contests.ParticipantFilter{
			Status: contests.RegistrationDisqualified, Limit: 10,
		})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total != 1 || len(found) != 1 || found[0].UserID != excluded.ID {
			t.Errorf("found %d of %d, want only the disqualified participant", len(found), total)
		}
	})
}

func TestRemovingAParticipantTakesTheRegistrationAway(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-remove")
		student := makeUser(t, ctx, "student-remove")
		id := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, id, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		if err := repo.Remove(ctx, id, student.ID); err != nil {
			t.Fatalf("Remove() = %v", err)
		}

		if _, err := repo.ByUser(ctx, id, student.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("ByUser() = %v, want ErrParticipantNotFound", err)
		}
	})
}

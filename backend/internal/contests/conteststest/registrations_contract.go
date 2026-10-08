package conteststest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// RegistrationTarget is what one case of the contract runs against: a
// repository holding no registrations yet, and the means to create what a
// registration hangs off. A real schema needs an account and a contest to
// exist before a registration can name them, so each implementation fills
// these its own way: the in-memory store mints identifiers, PostgreSQL
// inserts rows.
type RegistrationTarget struct {
	Repo contests.RegistrationRepository
	// NewUser creates an account and returns its identifier. The login and
	// full name are what a participant carries once registered.
	NewUser func(login, fullName string) uuid.UUID
	// NewContest creates a contest and returns its identifier.
	NewContest func() uuid.UUID
	// GrantAdminAll gives the account rbac.PermissionContestAdminAll, the
	// way an implementation's own accounts hold a permission.
	GrantAdminAll func(user uuid.UUID)
	// RecordWork leaves something of the participant's own behind the
	// registration — a note, a query — so that HasWork has an answer to give.
	RecordWork func(registration uuid.UUID)
	// Now is what the store's clock reads when a row is written. A
	// registration's CreatedAt is that clock, so the contract can only state
	// it in its terms.
	Now func() time.Time
}

// RegistrationRepositoryContract is what every
// contests.RegistrationRepository must do, run as subtests against one
// implementation. Both the in-memory Registrations and postgres.Registrations
// run it, so the store the service tests trust and the store production uses
// are held to the same answers: a rule the fake got wrong would otherwise
// pass every service test and fail only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repository with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here; two first actions racing for
// one start time is a property of the real statement and is proven against
// PostgreSQL alone.
//
// Logins are lower-case letters and digits throughout, because the roster is
// ordered by them and the order of anything else depends on the database's
// collation.
func RegistrationRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, RegistrationTarget))) {
	started := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	add := func(t *testing.T, ctx context.Context, target RegistrationTarget, contest, user uuid.UUID) contests.Participant {
		t.Helper()
		p, err := target.Repo.Add(ctx, contest, user)
		if err != nil {
			t.Fatalf("Add() = %v", err)
		}
		return p
	}
	byUser := func(t *testing.T, ctx context.Context, target RegistrationTarget, contest, user uuid.UUID) contests.Participant {
		t.Helper()
		p, err := target.Repo.ByUser(ctx, contest, user)
		if err != nil {
			t.Fatalf("ByUser() = %v", err)
		}
		return p
	}
	// enrol creates an account and registers it, returning the registration.
	enrol := func(t *testing.T, ctx context.Context, target RegistrationTarget, contest uuid.UUID, login string) contests.Participant {
		t.Helper()
		return add(t, ctx, target, contest, target.NewUser(login, login))
	}
	notFound := func(t *testing.T, err error, what string) {
		t.Helper()
		if !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("%s error = %v, want ErrParticipantNotFound", what, err)
		}
	}
	list := func(t *testing.T, ctx context.Context, target RegistrationTarget, contest uuid.UUID, f contests.ParticipantFilter) ([]string, int) {
		t.Helper()
		found, total, err := target.Repo.List(ctx, contest, f)
		if err != nil {
			t.Fatalf("List(%+v) = %v", f, err)
		}
		logins := make([]string, 0, len(found))
		for _, p := range found {
			logins = append(logins, p.Login)
		}
		return logins, total
	}

	t.Run("Add returns the participation it wrote", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			user := target.NewUser("alice", "Alice Liddell")

			got := add(t, ctx, target, contest, user)

			if got.ID == uuid.Nil {
				t.Error("ID is nil")
			}
			if got.ContestID != contest || got.UserID != user {
				t.Errorf("written for (%s, %s), want (%s, %s)", got.ContestID, got.UserID, contest, user)
			}
			if got.Login != "alice" || got.FullName != "Alice Liddell" {
				t.Errorf("account = (%q, %q), want (alice, Alice Liddell)", got.Login, got.FullName)
			}
			if got.Status != contests.RegistrationRegistered {
				t.Errorf("Status = %q, want %q", got.Status, contests.RegistrationRegistered)
			}
			if got.StartedAt != nil || got.FinishedAt != nil {
				t.Errorf("StartedAt, FinishedAt = %v, %v, want neither", got.StartedAt, got.FinishedAt)
			}
			if got.TotalScore != 0 {
				t.Errorf("TotalScore = %d, want 0", got.TotalScore)
			}
			if want := target.Now(); !got.CreatedAt.Equal(want) {
				t.Errorf("CreatedAt = %v, want the store's own clock, %v", got.CreatedAt, want)
			}
		})
	})

	t.Run("ByUser reads back what Add wrote", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			user := target.NewUser("alice", "Alice Liddell")
			added := add(t, ctx, target, contest, user)

			got := byUser(t, ctx, target, contest, user)

			if got.ID != added.ID || got.Login != "alice" || got.FullName != "Alice Liddell" ||
				got.Status != contests.RegistrationRegistered || !got.CreatedAt.Equal(added.CreatedAt) {
				t.Errorf("ByUser() = %+v, want what Add returned, %+v", got, added)
			}
		})
	})

	t.Run("ByUser finds nobody who is not registered for that contest", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			member := target.NewUser("alice", "alice")
			stranger := target.NewUser("bob", "bob")
			add(t, ctx, target, ours, member)

			_, err := target.Repo.ByUser(ctx, ours, stranger)
			notFound(t, err, "an account never registered:")
			_, err = target.Repo.ByUser(ctx, theirs, member)
			notFound(t, err, "a registration for another contest:")
			_, err = target.Repo.ByUser(ctx, uuid.New(), member)
			notFound(t, err, "a contest that does not exist:")
		})
	})

	t.Run("Add refuses to register the same person twice", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest, other := target.NewContest(), target.NewContest()
			user := target.NewUser("alice", "alice")
			first := add(t, ctx, target, contest, user)

			// The same person in a second contest is a second participant.
			if second := add(t, ctx, target, other, user); second.ID == first.ID {
				t.Error("a second contest reused the first registration")
			}

			// Last, because a database refuses it by failing the statement,
			// and a transaction cannot be read from after that.
			if _, err := target.Repo.Add(ctx, contest, user); !errors.Is(err, contests.ErrAlreadyEnrolled) {
				t.Errorf("Add() twice error = %v, want ErrAlreadyEnrolled", err)
			}
		})
	})

	t.Run("Remove takes that registration away and no other", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest, other := target.NewContest(), target.NewContest()
			leaving := target.NewUser("alice", "alice")
			staying := target.NewUser("bob", "bob")
			add(t, ctx, target, contest, leaving)
			add(t, ctx, target, contest, staying)
			add(t, ctx, target, other, leaving)

			if err := target.Repo.Remove(ctx, contest, leaving); err != nil {
				t.Fatalf("Remove() = %v", err)
			}

			_, err := target.Repo.ByUser(ctx, contest, leaving)
			notFound(t, err, "ByUser() after Remove:")
			byUser(t, ctx, target, contest, staying)
			byUser(t, ctx, target, other, leaving)

			// Gone means free to register again.
			add(t, ctx, target, contest, leaving)
		})
	})

	t.Run("Remove reports a registration that is not there", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest, other := target.NewContest(), target.NewContest()
			member := target.NewUser("alice", "alice")
			add(t, ctx, target, other, member)

			notFound(t, target.Repo.Remove(ctx, contest, member), "Remove() for another contest's member:")
			byUser(t, ctx, target, other, member)

			add(t, ctx, target, contest, member)
			if err := target.Repo.Remove(ctx, contest, member); err != nil {
				t.Fatalf("Remove() = %v", err)
			}
			notFound(t, target.Repo.Remove(ctx, contest, member), "a second Remove():")
		})
	})

	t.Run("SetStatus changes the status and nothing else", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			out := enrol(t, ctx, target, contest, "alice")
			in := enrol(t, ctx, target, contest, "bob")

			if err := target.Repo.SetStatus(ctx, out.ID, contests.RegistrationDisqualified); err != nil {
				t.Fatalf("SetStatus() = %v", err)
			}

			got := byUser(t, ctx, target, contest, out.UserID)
			if got.Status != contests.RegistrationDisqualified {
				t.Errorf("Status = %q, want %q", got.Status, contests.RegistrationDisqualified)
			}
			if got.ID != out.ID || got.Login != "alice" || got.StartedAt != nil || got.TotalScore != 0 {
				t.Errorf("after SetStatus %+v, want the same registration otherwise untouched", got)
			}
			if other := byUser(t, ctx, target, contest, in.UserID); other.Status != contests.RegistrationRegistered {
				t.Errorf("another participant's Status = %q, want %q", other.Status, contests.RegistrationRegistered)
			}
		})
	})

	t.Run("SetStatus reports a registration that is not there", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			notFound(t, target.Repo.SetStatus(ctx, uuid.New(), contests.RegistrationDisqualified), "SetStatus():")
		})
	})

	t.Run("Start records the time and moves the participant to active", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			p := enrol(t, ctx, target, contest, "alice")

			got, err := target.Repo.Start(ctx, p.ID, started)
			if err != nil {
				t.Fatalf("Start() = %v", err)
			}
			if got.StartedAt == nil || !got.StartedAt.Equal(started) {
				t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
			}
			if got.Status != contests.RegistrationActive {
				t.Errorf("Status = %q, want %q", got.Status, contests.RegistrationActive)
			}
			if got.ID != p.ID || got.Login != "alice" {
				t.Errorf("Start() returned %+v, want the registration it started, with its account", got)
			}

			read := byUser(t, ctx, target, contest, p.UserID)
			if read.StartedAt == nil || !read.StartedAt.Equal(started) || read.Status != contests.RegistrationActive {
				t.Errorf("ByUser() after Start = %+v, want it started at %v and active", read, started)
			}
		})
	})

	t.Run("Start keeps the first time however often it is called", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			p := enrol(t, ctx, target, contest, "alice")
			if _, err := target.Repo.Start(ctx, p.ID, started); err != nil {
				t.Fatalf("first Start() = %v", err)
			}

			got, err := target.Repo.Start(ctx, p.ID, started.Add(time.Hour))
			if err != nil {
				t.Fatalf("second Start() = %v", err)
			}
			if got.StartedAt == nil || !got.StartedAt.Equal(started) {
				t.Errorf("StartedAt after a second Start() = %v, want the first, %v", got.StartedAt, started)
			}
			if read := byUser(t, ctx, target, contest, p.UserID); read.StartedAt == nil || !read.StartedAt.Equal(started) {
				t.Errorf("stored StartedAt = %v, want the first, %v", read.StartedAt, started)
			}
		})
	})

	t.Run("Start leaves a registration that is not merely registered as it found it", func(t *testing.T) {
		for _, status := range []string{contests.RegistrationDisqualified, contests.RegistrationFinished} {
			t.Run(status, func(t *testing.T) {
				each(t, func(ctx context.Context, target RegistrationTarget) {
					contest := target.NewContest()
					p := enrol(t, ctx, target, contest, "alice")
					if err := target.Repo.SetStatus(ctx, p.ID, status); err != nil {
						t.Fatalf("SetStatus() = %v", err)
					}

					got, err := target.Repo.Start(ctx, p.ID, started)
					if err != nil {
						t.Fatalf("Start() = %v", err)
					}
					if got.Status != status {
						t.Errorf("Status after Start() = %q, want %q unchanged", got.Status, status)
					}
					if got.StartedAt != nil {
						t.Errorf("StartedAt after Start() = %v, want nil", got.StartedAt)
					}
					if read := byUser(t, ctx, target, contest, p.UserID); read.Status != status || read.StartedAt != nil {
						t.Errorf("stored (%q, %v), want (%q, nil)", read.Status, read.StartedAt, status)
					}
				})
			})
		}
	})

	t.Run("Start reports a registration that is not there", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			_, err := target.Repo.Start(ctx, uuid.New(), started)
			notFound(t, err, "Start():")
		})
	})

	t.Run("AddScore adds to the total of that registration only", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			scoring := enrol(t, ctx, target, contest, "alice")
			idle := enrol(t, ctx, target, contest, "bob")

			for _, delta := range []int{3, 4} {
				if err := target.Repo.AddScore(ctx, scoring.ID, delta); err != nil {
					t.Fatalf("AddScore(%d) = %v", delta, err)
				}
			}

			if got := byUser(t, ctx, target, contest, scoring.UserID).TotalScore; got != 7 {
				t.Errorf("TotalScore = %d, want 7 (3 + 4)", got)
			}
			if got := byUser(t, ctx, target, contest, idle.UserID).TotalScore; got != 0 {
				t.Errorf("another participant's TotalScore = %d, want 0", got)
			}
		})
	})

	t.Run("AddScore reports a registration that is not there", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			notFound(t, target.Repo.AddScore(ctx, uuid.New(), 5), "AddScore():")
		})
	})

	t.Run("EnrolledIn names the contests asked about that the user is on", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			joined, joinedToo, notJoined, unasked := target.NewContest(), target.NewContest(), target.NewContest(), target.NewContest()
			user := target.NewUser("alice", "alice")
			somebody := target.NewUser("bob", "bob")
			for _, c := range []uuid.UUID{joined, joinedToo, unasked} {
				add(t, ctx, target, c, user)
			}
			add(t, ctx, target, notJoined, somebody)

			got, err := target.Repo.EnrolledIn(ctx, user, []uuid.UUID{joined, joinedToo, notJoined, uuid.New()})
			if err != nil {
				t.Fatalf("EnrolledIn() = %v", err)
			}

			if len(got) != 2 || !got[joined] || !got[joinedToo] {
				t.Errorf("EnrolledIn() = %v, want exactly the two contests the user joined", got)
			}
			if got[notJoined] || got[unasked] {
				t.Errorf("EnrolledIn() = %v, want neither a contest only somebody else joined nor one not asked about", got)
			}
		})
	})

	t.Run("EnrolledIn answers an empty question with an empty map", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			user := target.NewUser("alice", "alice")
			add(t, ctx, target, contest, user)

			for name, tc := range map[string]struct {
				user     uuid.UUID
				contests []uuid.UUID
			}{
				"no contests asked about": {user, nil},
				"the nil user":            {uuid.Nil, []uuid.UUID{contest}},
				"a stranger":              {uuid.New(), []uuid.UUID{contest}},
			} {
				got, err := target.Repo.EnrolledIn(ctx, tc.user, tc.contests)
				if err != nil {
					t.Fatalf("%s: EnrolledIn() = %v", name, err)
				}
				if len(got) != 0 {
					t.Errorf("%s: EnrolledIn() = %v, want nothing", name, got)
				}
			}
		})
	})

	t.Run("HasWork follows what is recorded against the registration", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			worked := enrol(t, ctx, target, contest, "alice")
			idle := enrol(t, ctx, target, contest, "bob")

			has, err := target.Repo.HasWork(ctx, worked.ID)
			if err != nil || has {
				t.Fatalf("HasWork() before anything is recorded = %v, %v, want false, nil", has, err)
			}

			target.RecordWork(worked.ID)

			if has, err := target.Repo.HasWork(ctx, worked.ID); err != nil || !has {
				t.Errorf("HasWork() after work = %v, %v, want true, nil", has, err)
			}
			if has, err := target.Repo.HasWork(ctx, idle.ID); err != nil || has {
				t.Errorf("HasWork() for another registration = %v, %v, want false, nil", has, err)
			}
		})
	})

	t.Run("HasWork says no for a registration that is not there", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			if has, err := target.Repo.HasWork(ctx, uuid.New()); err != nil || has {
				t.Errorf("HasWork() = %v, %v, want false, nil", has, err)
			}
		})
	})

	t.Run("RegisteredWithPermission names the holders, ordered by login", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			for _, login := range []string{"zed", "mia", "abe"} {
				p := enrol(t, ctx, target, contest, login)
				if login != "mia" {
					target.GrantAdminAll(p.UserID)
				}
			}

			got, err := target.Repo.RegisteredWithPermission(ctx, contest, rbac.PermissionContestAdminAll)
			if err != nil {
				t.Fatalf("RegisteredWithPermission() = %v", err)
			}
			if want := []string{"abe", "zed"}; !slices.Equal(got, want) {
				t.Errorf("logins = %v, want %v", got, want)
			}
		})
	})

	t.Run("RegisteredWithPermission reads what the account holds now", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			p := enrol(t, ctx, target, contest, "alice")

			got, err := target.Repo.RegisteredWithPermission(ctx, contest, rbac.PermissionContestAdminAll)
			if err != nil || len(got) != 0 {
				t.Fatalf("RegisteredWithPermission() before the grant = %v, %v, want none", got, err)
			}

			// Granted after the registration was made: the case the publish
			// gate exists for.
			target.GrantAdminAll(p.UserID)

			got, err = target.Repo.RegisteredWithPermission(ctx, contest, rbac.PermissionContestAdminAll)
			if err != nil {
				t.Fatalf("RegisteredWithPermission() = %v", err)
			}
			if want := []string{"alice"}; !slices.Equal(got, want) {
				t.Errorf("logins after the grant = %v, want %v", got, want)
			}
		})
	})

	t.Run("RegisteredWithPermission names only what it was asked for", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			target.GrantAdminAll(enrol(t, ctx, target, contest, "alice").UserID)

			got, err := target.Repo.RegisteredWithPermission(ctx, contest, "no.such.permission")
			if err != nil {
				t.Fatalf("RegisteredWithPermission() = %v", err)
			}
			if len(got) != 0 {
				t.Errorf("logins = %v, want none for a permission nobody holds", got)
			}
		})
	})

	t.Run("RegisteredWithPermission looks only at its own contest's roster", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			inspector := enrol(t, ctx, target, theirs, "alice")
			target.GrantAdminAll(inspector.UserID)
			// Holds the permission but is on nobody's roster.
			target.GrantAdminAll(target.NewUser("bob", "bob"))

			got, err := target.Repo.RegisteredWithPermission(ctx, ours, rbac.PermissionContestAdminAll)
			if err != nil {
				t.Fatalf("RegisteredWithPermission() = %v", err)
			}
			if len(got) != 0 {
				t.Errorf("logins = %v, want none: one is on another contest's roster, the other on none", got)
			}
		})
	})

	t.Run("RegisteredWithPermission stops at MaxReportedStaff", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			var all []string
			for i := 0; i <= contests.MaxReportedStaff; i++ {
				login := fmt.Sprintf("staff%02d", i)
				all = append(all, login)
				target.GrantAdminAll(enrol(t, ctx, target, contest, login).UserID)
			}

			got, err := target.Repo.RegisteredWithPermission(ctx, contest, rbac.PermissionContestAdminAll)
			if err != nil {
				t.Fatalf("RegisteredWithPermission() = %v", err)
			}
			// One more holder than the bound, so the answer is the first
			// MaxReportedStaff logins in order and not the whole roster.
			if want := all[:contests.MaxReportedStaff]; !slices.Equal(got, want) {
				t.Errorf("logins = %v, want the first %d, %v", got, contests.MaxReportedStaff, want)
			}
		})
	})

	t.Run("List pages the roster in login order and counts all of it", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			// Registered out of order, so that the order read is the login's.
			for _, login := range []string{"cy", "ada", "eve", "ben", "dee"} {
				enrol(t, ctx, target, contest, login)
			}

			for _, tc := range []struct {
				name   string
				filter contests.ParticipantFilter
				want   []string
			}{
				{"everyone", contests.ParticipantFilter{Limit: 10}, []string{"ada", "ben", "cy", "dee", "eve"}},
				{"the first page", contests.ParticipantFilter{Limit: 2}, []string{"ada", "ben"}},
				{"the second page", contests.ParticipantFilter{Limit: 2, Offset: 2}, []string{"cy", "dee"}},
				{"a short last page", contests.ParticipantFilter{Limit: 2, Offset: 4}, []string{"eve"}},
				// Empty, and still counting everyone: a screen that went one
				// page too far must be able to step back.
				{"a page past the end", contests.ParticipantFilter{Limit: 2, Offset: 6}, nil},
			} {
				got, total := list(t, ctx, target, contest, tc.filter)
				if !slices.Equal(got, tc.want) || total != 5 {
					t.Errorf("%s: List() = %v of %d, want %v of 5", tc.name, got, total, tc.want)
				}
			}
		})
	})

	t.Run("List shows one contest's roster", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			ours, theirs, empty := target.NewContest(), target.NewContest(), target.NewContest()
			enrol(t, ctx, target, ours, "alice")
			enrol(t, ctx, target, theirs, "bob")

			got, total := list(t, ctx, target, ours, contests.ParticipantFilter{Limit: 10})
			if !slices.Equal(got, []string{"alice"}) || total != 1 {
				t.Errorf("List() = %v of %d, want [alice] of 1", got, total)
			}
			got, total = list(t, ctx, target, empty, contests.ParticipantFilter{Limit: 10})
			if len(got) != 0 || total != 0 {
				t.Errorf("List() of a contest nobody joined = %v of %d, want none of 0", got, total)
			}
		})
	})

	t.Run("List filters by status", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			enrol(t, ctx, target, contest, "alice")
			excluded := enrol(t, ctx, target, contest, "bob")
			if err := target.Repo.SetStatus(ctx, excluded.ID, contests.RegistrationDisqualified); err != nil {
				t.Fatalf("SetStatus() = %v", err)
			}

			got, total := list(t, ctx, target, contest, contests.ParticipantFilter{Status: contests.RegistrationDisqualified, Limit: 10})
			if !slices.Equal(got, []string{"bob"}) || total != 1 {
				t.Errorf("disqualified: List() = %v of %d, want [bob] of 1", got, total)
			}
			got, total = list(t, ctx, target, contest, contests.ParticipantFilter{Status: contests.RegistrationRegistered, Limit: 10})
			if !slices.Equal(got, []string{"alice"}) || total != 1 {
				t.Errorf("registered: List() = %v of %d, want [alice] of 1", got, total)
			}
		})
	})

	t.Run("List searches the login and the full name, ignoring case", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			add(t, ctx, target, contest, target.NewUser("alice", "Alice Liddell"))
			add(t, ctx, target, contest, target.NewUser("carol", "Dr Smith"))
			add(t, ctx, target, contest, target.NewUser("smithers", "Waylon"))

			for _, tc := range []struct {
				query string
				want  []string
			}{
				{"ALI", []string{"alice"}},
				{"liddell", []string{"alice"}},
				{"SMITH", []string{"carol", "smithers"}},
				{"nobody", nil},
			} {
				got, total := list(t, ctx, target, contest, contests.ParticipantFilter{Query: tc.query, Limit: 10})
				if !slices.Equal(got, tc.want) || total != len(tc.want) {
					t.Errorf("Query %q: List() = %v of %d, want %v of %d", tc.query, got, total, tc.want, len(tc.want))
				}
			}
		})
	})

	t.Run("List takes the characters of a search as characters", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			add(t, ctx, target, contest, target.NewUser("alice", "50% done"))
			add(t, ctx, target, contest, target.NewUser("bob", "fifty done"))
			add(t, ctx, target, contest, target.NewUser("carol", "snake_case"))
			add(t, ctx, target, contest, target.NewUser("dave", "snakeXcase"))
			add(t, ctx, target, contest, target.NewUser("erin", `back\slash`))
			add(t, ctx, target, contest, target.NewUser("frank", "backslash"))

			for _, tc := range []struct {
				query string
				want  []string
			}{
				{"%", []string{"alice"}},
				{"e_c", []string{"carol"}},
				{`\`, []string{"erin"}},
			} {
				got, total := list(t, ctx, target, contest, contests.ParticipantFilter{Query: tc.query, Limit: 10})
				if !slices.Equal(got, tc.want) || total != len(tc.want) {
					t.Errorf("Query %q: List() = %v of %d, want %v of %d", tc.query, got, total, tc.want, len(tc.want))
				}
			}
		})
	})

	t.Run("List applies the status and the search together", func(t *testing.T) {
		each(t, func(ctx context.Context, target RegistrationTarget) {
			contest := target.NewContest()
			anna := add(t, ctx, target, contest, target.NewUser("anna", "anna"))
			annie := add(t, ctx, target, contest, target.NewUser("annie", "annie"))
			add(t, ctx, target, contest, target.NewUser("annika", "annika"))
			bob := add(t, ctx, target, contest, target.NewUser("bob", "bob"))
			for _, p := range []contests.Participant{anna, annie, bob} {
				if err := target.Repo.SetStatus(ctx, p.ID, contests.RegistrationDisqualified); err != nil {
					t.Fatalf("SetStatus() = %v", err)
				}
			}

			// annika matches the search but not the status, bob the status
			// but not the search.
			got, total := list(t, ctx, target, contest, contests.ParticipantFilter{
				Status: contests.RegistrationDisqualified, Query: "ann", Limit: 10,
			})
			if !slices.Equal(got, []string{"anna", "annie"}) || total != 2 {
				t.Errorf("List() = %v of %d, want [anna annie] of 2", got, total)
			}
		})
	})
}

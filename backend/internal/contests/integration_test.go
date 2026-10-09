package contests_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// The audit write must sit inside the unit of work: after the commit it can be
// lost, before it can outlive a rollback. Nothing enforces that at a new call
// site, so every mutating operation is asserted here. They run in sequence
// because that is the only order in which all of them are legal.
func TestEveryChangeIsRecordedInsideItsTransaction(t *testing.T) {
	ctx := context.Background()
	f := conteststest.NewFixture()
	actor := uuid.New()
	c := f.SeedPublishableContest()
	staff := f.AddUser("t.manager")
	student := f.AddUser("s.popescu")
	doomed := f.SeedContest(contests.StatusDraft)

	var added contests.Question

	operations := []struct {
		name string
		run  func() error
	}{
		{"Create", func() error {
			_, err := f.Service.Create(ctx, contests.CreateCommand{
				ActorID:      actor,
				Languages:    []contests.ContestLanguage{{Code: "en", IsDefault: true}},
				Translations: []contests.Translation{{Lang: "en", Title: "A Second Case"}},
			})
			return err
		}},
		// Opens signup for Enroll below; it cannot change once running.
		{"Update", func() error {
			_, err := f.Service.Update(ctx, contests.UpdateCommand{
				ActorID:    actor,
				ContestID:  c.ID,
				Enrollment: contests.EnrollmentOpen,
			})
			return err
		}},
		{"SetLanguages", func() error {
			return f.Service.SetLanguages(ctx, actor, c.ID,
				[]contests.ContestLanguage{{Code: "en", IsDefault: true}})
		}},
		{"SetTranslations", func() error {
			return f.Service.SetTranslations(ctx, actor, c.ID,
				[]contests.Translation{{Lang: "en", Title: "The Library Murder"}})
		}},
		{"SetStory", func() error {
			_, err := f.Service.SetStory(ctx, actor, c.ID, map[string]string{"en": "A body in the stacks."})
			return err
		}},
		{"AddQuestion", func() error {
			var err error
			added, err = f.Service.AddQuestion(ctx, contests.QuestionCommand{
				ActorID:   actor,
				ContestID: c.ID,
				Kind:      contests.KindText,
				Points:    5,
				Texts:     map[string]contests.QuestionText{"en": {BodyMD: "Where was the key?"}},
			})
			return err
		}},
		{"UpdateQuestion", func() error {
			_, err := f.Service.UpdateQuestion(ctx, contests.QuestionCommand{
				ActorID:    actor,
				ContestID:  c.ID,
				QuestionID: added.ID,
				Kind:       contests.KindText,
				Points:     7,
			})
			return err
		}},
		{"SetQuestionTexts", func() error {
			return f.Service.SetQuestionTexts(ctx, actor, c.ID, added.ID,
				map[string]contests.QuestionText{"en": {BodyMD: "Where was the second key?"}})
		}},
		{"SetAnswers", func() error {
			return f.Service.SetAnswers(ctx, actor, c.ID, added.ID,
				[]contests.Answer{{MatchKind: contests.MatchExactCI, Value: "under the mat"}})
		}},
		{"ReorderQuestions", func() error {
			existing, err := f.Service.Questions(ctx, c.ID)
			if err != nil {
				return err
			}
			order := make([]uuid.UUID, 0, len(existing))
			for _, q := range existing {
				order = append(order, q.ID)
			}
			return f.Service.ReorderQuestions(ctx, actor, c.ID, order)
		}},
		{"DeleteQuestion", func() error {
			return f.Service.DeleteQuestion(ctx, actor, c.ID, added.ID)
		}},
		{"SetPolicy", func() error {
			return f.Service.SetPolicy(ctx, actor, c.ID, contests.SQLPolicy{
				ContestID:      c.ID,
				Mode:           contests.ModeReadOnly,
				DiskQuotaRatio: 2,
			})
		}},
		{"GrantManager", func() error {
			return f.Service.GrantManager(ctx, actor, c.ID, staff.ID, rbac.RoleManager)
		}},
		{"RevokeManager", func() error {
			return f.Service.RevokeManager(ctx, actor, c.ID, staff.ID)
		}},
		{"AddParticipants", func() error {
			_, err := f.Service.AddParticipants(ctx, contests.AddParticipantsCommand{
				ActorID:   actor,
				ContestID: c.ID,
				Logins:    []string{staff.Login},
			})
			return err
		}},
		{"RemoveParticipant", func() error {
			return f.Service.RemoveParticipant(ctx, actor, c.ID, staff.ID)
		}},
		{"Transition", func() error {
			return f.Service.Transition(ctx, actor, c.ID, contests.StatusPublished)
		}},
		{"Enroll", func() error {
			_, err := f.Service.Enroll(ctx, contests.EnrollCommand{
				UserID:    student.ID,
				ContestID: c.ID,
				Address:   netip.MustParseAddr("10.20.30.40"),
			})
			return err
		}},
		{"DisqualifyParticipant", func() error {
			return f.Service.DisqualifyParticipant(ctx, actor, c.ID, student.ID)
		}},
		{"Delete", func() error {
			return f.Service.Delete(ctx, actor, doomed.ID)
		}},
	}

	for _, operation := range operations {
		name, run := operation.name, operation.run
		entries, loose := len(f.Audit.Entries), len(f.Audit.Loose)

		if err := run(); err != nil {
			t.Errorf("%s returned error: %v", name, err)
			continue
		}

		switch {
		case len(f.Audit.Entries) == entries:
			t.Errorf("%s changed something and recorded nothing", name)
		case len(f.Audit.Loose) != loose:
			t.Errorf("%s recorded outside the transaction: %v", name, f.Audit.Loose[loose:])
		}
	}
}

// A refusal changes nothing to be atomic with, and a rollback must not lose
// the record of the attempt.
func TestARefusalIsRecordedWithoutATransaction(t *testing.T) {
	ctx := context.Background()
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()
	student := f.AddUser("s.popescu")

	if _, err := f.Service.Update(ctx, contests.UpdateCommand{
		ActorID:      uuid.New(),
		ContestID:    c.ID,
		Enrollment:   contests.EnrollmentOpen,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
	}); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if err := f.Service.Transition(ctx, uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition() = %v", err)
	}

	_, err := f.Service.Enroll(ctx, contests.EnrollCommand{
		UserID:    student.ID,
		ContestID: c.ID,
		Address:   netip.MustParseAddr("203.0.113.7"),
	})

	if err == nil {
		t.Fatal("Enroll from an address outside the contest's networks succeeded")
	}
	if len(f.Audit.Loose) != 1 || f.Audit.Loose[0].Action != audit.ActionContestAccessDenied {
		t.Errorf("entries recorded outside a transaction = %v, want one refusal", f.Audit.Loose)
	}
}

// Transition checks, runs the publish gate, then writes; the write must be
// conditional on the status it checked, or a gate that passed against a stale
// state lets a contest with no story start.
func TestTwoTransitionsRaceAndOnlyOneWins(t *testing.T) {
	ctx := context.Background()
	f := conteststest.NewFixture()
	actor := uuid.New()
	c := f.SeedPublishableContest()

	if err := f.Service.Transition(ctx, actor, c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition() to published = %v", err)
	}

	// The second caller holds a snapshot taken before the first committed.
	f.Contests.SetStatusRaces(contests.StatusRunning)

	err := f.Service.Transition(ctx, actor, c.ID, contests.StatusRunning)

	if !errors.Is(err, contests.ErrStatusChanged) {
		t.Errorf("Transition() = %v, want it to refuse a status that moved underneath it", err)
	}
}

func TestATransitionThatWasNotRacedStillLands(t *testing.T) {
	ctx := context.Background()
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()

	if err := f.Service.Transition(ctx, uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition() = %v", err)
	}

	stored, err := f.Contests.ByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if stored.Status != contests.StatusPublished {
		t.Errorf("status = %q, want published", stored.Status)
	}
}

package contests_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// TestEveryChangeIsRecordedInsideItsTransaction pins the placement of the
// audit write.
//
// The record sits inside the unit of work on purpose, and it is the one
// arrangement that makes the trail evidence: written after the commit it can
// be lost while the change survives, and written before it can outlive a
// rollback. That placement is a convention repeated at twenty call sites, and
// nothing about the twenty-first enforces it — so it is asserted here for
// every operation that changes anything, rather than trusted.
//
// The operations run in one sequence against one fixture because that is the
// only order in which they are all legal: a question cannot be reordered
// before it exists, and nobody can enroll in a contest that was never
// published.
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
		// Opened for signup here so that Enroll, far below, has something to
		// join: the setting cannot be changed once the contest is running.
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

// TestARefusalIsRecordedWithoutATransaction is the exception the test above
// allows for, stated so that it is a decision rather than a gap.
//
// Turning somebody away changes nothing, so there is nothing for the entry to
// be atomic with — and the attempt is exactly what an administrator wants to
// find afterwards, which a rollback-shaped write would be free to lose.
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

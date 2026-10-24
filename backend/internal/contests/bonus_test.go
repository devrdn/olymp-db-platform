package contests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func contestScoring(f *conteststest.Fixture, scoring string) contests.Contest {
	c := f.SeedContest(contests.StatusDraft)
	c.Scoring = scoring
	return f.Contests.Put(c)
}

func bonusCmd(contestID uuid.UUID) contests.QuestionCommand {
	cmd := questionCmd(contestID)
	bonus := true
	cmd.IsBonus = &bonus
	return cmd
}

func TestABonusQuestionIsAddedToAPointsContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := contestScoring(f, contests.ScoringPoints)

	created, err := f.Service.AddQuestion(context.Background(), bonusCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}
	if !created.IsBonus {
		t.Error("IsBonus = false, want true")
	}
	if stored, _ := f.Questions.ByID(context.Background(), created.ID); !stored.IsBonus {
		t.Error("the stored question is not a bonus")
	}
}

func TestANewQuestionIsNotABonusUnlessAskedFor(t *testing.T) {
	f := conteststest.NewFixture()
	c := contestScoring(f, contests.ScoringPoints)

	created, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}
	if created.IsBonus {
		t.Error("IsBonus = true for a question nobody marked, want false")
	}
}

func TestABonusQuestionIsRefusedOutsidePointsScoring(t *testing.T) {
	for _, scoring := range []string{contests.ScoringWinner, contests.ScoringICPC} {
		t.Run(scoring, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := contestScoring(f, scoring)

			if _, err := f.Service.AddQuestion(context.Background(), bonusCmd(c.ID)); !errors.Is(err, contests.ErrBonusNeedsPoints) {
				t.Errorf("AddQuestion() = %v, want ErrBonusNeedsPoints", err)
			}
			if all, _ := f.Questions.List(context.Background(), c.ID); len(all) != 0 {
				t.Errorf("%d questions stored, want none", len(all))
			}

			regular, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
			if err != nil {
				t.Fatalf("AddQuestion(regular) = %v", err)
			}
			update := bonusCmd(c.ID)
			update.QuestionID = regular.ID
			if _, err := f.Service.UpdateQuestion(context.Background(), update); !errors.Is(err, contests.ErrBonusNeedsPoints) {
				t.Errorf("UpdateQuestion() = %v, want ErrBonusNeedsPoints", err)
			}
			bonus := true
			if _, err := f.Service.SaveQuestion(context.Background(), contests.SaveQuestionCommand{
				ActorID: uuid.New(), ContestID: c.ID, QuestionID: regular.ID,
				Kind: contests.KindText, Points: 5, IsBonus: &bonus,
			}); !errors.Is(err, contests.ErrBonusNeedsPoints) {
				t.Errorf("SaveQuestion() = %v, want ErrBonusNeedsPoints", err)
			}
			if stored, _ := f.Questions.ByID(context.Background(), regular.ID); stored.IsBonus {
				t.Error("a refused edit marked the question a bonus")
			}
		})
	}
}

// Like PenaltyPct, a client that does not send the flag must not clear it.
func TestEditingAQuestionKeepsAnUnmentionedBonusFlag(t *testing.T) {
	f := conteststest.NewFixture()
	c := contestScoring(f, contests.ScoringPoints)
	created, err := f.Service.AddQuestion(context.Background(), bonusCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	updated, err := f.Service.UpdateQuestion(context.Background(), contests.QuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: created.ID, Kind: contests.KindText, Points: 7,
	})
	if err != nil {
		t.Fatalf("UpdateQuestion() = %v", err)
	}
	if !updated.IsBonus {
		t.Error("UpdateQuestion() cleared a bonus flag it was not sent")
	}
	saved, err := f.Service.SaveQuestion(context.Background(), contests.SaveQuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: created.ID, Kind: contests.KindText, Points: 7,
	})
	if err != nil {
		t.Fatalf("SaveQuestion() = %v", err)
	}
	if !saved.IsBonus {
		t.Error("SaveQuestion() cleared a bonus flag it was not sent")
	}

	regular := false
	saved, err = f.Service.SaveQuestion(context.Background(), contests.SaveQuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: created.ID, Kind: contests.KindText, Points: 7,
		IsBonus: &regular,
	})
	if err != nil {
		t.Fatalf("SaveQuestion(not a bonus) = %v", err)
	}
	if saved.IsBonus {
		t.Error("SaveQuestion() kept a bonus flag it was told to clear")
	}
}

func TestMarkingABonusIsRecordedAmongTheFieldsThatMoved(t *testing.T) {
	f := conteststest.NewFixture()
	c := contestScoring(f, contests.ScoringPoints)
	created, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	update := bonusCmd(c.ID)
	update.QuestionID = created.ID
	if _, err := f.Service.UpdateQuestion(context.Background(), update); err != nil {
		t.Fatalf("UpdateQuestion() = %v", err)
	}

	entry := f.Audit.Entries[len(f.Audit.Entries)-1]
	changes, _ := entry.Payload["changes"].(map[string]any)
	if entry.Action != audit.ActionQuestionUpdate || changes["is_bonus"] == nil {
		t.Errorf("last entry = %s %v, want a question update naming is_bonus", entry.Action, entry.Payload)
	}
}

func TestAContestWithABonusQuestionCannotLeavePointsScoring(t *testing.T) {
	for _, scoring := range []string{contests.ScoringWinner, contests.ScoringICPC} {
		t.Run(scoring, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := contestScoring(f, contests.ScoringPoints)
			if _, err := f.Service.AddQuestion(context.Background(), bonusCmd(c.ID)); err != nil {
				t.Fatalf("AddQuestion() = %v", err)
			}

			_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
				ActorID: uuid.New(), ContestID: c.ID, Scoring: scoring,
			})
			if !errors.Is(err, contests.ErrBonusNeedsPoints) {
				t.Errorf("Update(scoring %s) = %v, want ErrBonusNeedsPoints", scoring, err)
			}
			if stored, _ := f.Service.ByID(context.Background(), c.ID); stored.Scoring != contests.ScoringPoints {
				t.Errorf("scoring = %s after a refused change, want points", stored.Scoring)
			}
		})
	}
}

func TestAContestWithoutABonusQuestionMayLeavePointsScoring(t *testing.T) {
	f := conteststest.NewFixture()
	c := contestScoring(f, contests.ScoringPoints)
	if _, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID)); err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, Scoring: contests.ScoringWinner,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.Scoring != contests.ScoringWinner {
		t.Errorf("scoring = %s, want winner", updated.Scoring)
	}
}

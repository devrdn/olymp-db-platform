package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

func TestQuestionsGetConsecutivePositions(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewQuestions(testPool)
		author := makeUser(t, ctx, "author-q-order")
		id := makeContest(t, ctx, author.ID)

		first, err := repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		second, err := repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		if first.Ord != 1 || second.Ord != 2 {
			t.Errorf("positions = %d, %d; want 1, 2", first.Ord, second.Ord)
		}
	})
}

func TestQuestionCarriesItsTextAndAnswers(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewQuestions(testPool)
		author := makeUser(t, ctx, "author-q-text")
		id := makeContest(t, ctx, author.ID)
		q, err := repo.Create(ctx, contests.Question{
			ContestID: id, Kind: contests.KindChoice, Points: 7, IsVisible: false,
			ChoiceIDs: []string{"a", "b"},
		})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		if err := repo.ReplaceTexts(ctx, q.ID, map[string]contests.QuestionText{
			"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
		}); err != nil {
			t.Fatalf("ReplaceTexts() = %v", err)
		}
		if err := repo.ReplaceAnswers(ctx, q.ID, []contests.Answer{
			{MatchKind: contests.MatchExact, Value: "a"},
		}); err != nil {
			t.Fatalf("ReplaceAnswers() = %v", err)
		}

		listed, err := repo.List(ctx, id)
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if len(listed) != 1 {
			t.Fatalf("listed %d questions, want 1", len(listed))
		}
		got := listed[0]
		switch {
		case got.Points != 7:
			t.Errorf("points = %d, want 7", got.Points)
		case got.IsVisible:
			t.Error("IsVisible = true, want the hidden flag to survive")
		case len(got.ChoiceIDs) != 2:
			t.Errorf("choice ids = %v, want two", got.ChoiceIDs)
		case got.Texts["en"].Choices["b"] != "The gardener":
			t.Errorf("choice label = %q, want The gardener", got.Texts["en"].Choices["b"])
		case len(got.Answers) != 1 || got.Answers[0].Value != "a":
			t.Errorf("answers = %+v, want one answer of a", got.Answers)
		}
	})
}

func TestReplacingAnswersRemovesTheOldOnes(t *testing.T) {
	// A stale reference answer would keep accepting something the organizer
	// has already decided is wrong.
	withTx(t, func(ctx context.Context) {
		repo := NewQuestions(testPool)
		author := makeUser(t, ctx, "author-q-answers")
		id := makeContest(t, ctx, author.ID)
		q, _ := repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})

		if err := repo.ReplaceAnswers(ctx, q.ID, []contests.Answer{
			{MatchKind: contests.MatchExactCI, Value: "the butler"},
			{MatchKind: contests.MatchExactCI, Value: "butler"},
		}); err != nil {
			t.Fatalf("ReplaceAnswers() = %v", err)
		}
		if err := repo.ReplaceAnswers(ctx, q.ID, []contests.Answer{
			{MatchKind: contests.MatchExactCI, Value: "the gardener"},
		}); err != nil {
			t.Fatalf("ReplaceAnswers() = %v", err)
		}

		loaded, _ := repo.ByID(ctx, q.ID)
		if len(loaded.Answers) != 1 || loaded.Answers[0].Value != "the gardener" {
			t.Errorf("answers = %+v, want only the gardener", loaded.Answers)
		}
	})
}

func TestDeletingAQuestionClosesTheGapInThePositions(t *testing.T) {
	// Positions are what the participant's view is built from; a hole in them
	// would show up as a numbering that skips.
	withTx(t, func(ctx context.Context) {
		repo := NewQuestions(testPool)
		author := makeUser(t, ctx, "author-q-delete")
		id := makeContest(t, ctx, author.ID)
		first, _ := repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})
		_, _ = repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})
		third, _ := repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})

		if err := repo.Delete(ctx, first.ID); err != nil {
			t.Fatalf("Delete() = %v", err)
		}

		remaining, _ := repo.List(ctx, id)
		if len(remaining) != 2 {
			t.Fatalf("remaining = %d, want 2", len(remaining))
		}
		if remaining[0].Ord != 1 || remaining[1].Ord != 2 {
			t.Errorf("positions = %d, %d; want 1, 2", remaining[0].Ord, remaining[1].Ord)
		}
		if remaining[1].ID != third.ID {
			t.Errorf("last question = %v, want %v", remaining[1].ID, third.ID)
		}
	})
}

func TestReorderSwapsTwoQuestions(t *testing.T) {
	// The case the deferrable constraint exists for: halfway through the swap
	// both questions hold the same position, and an immediate check would
	// refuse it.
	withTx(t, func(ctx context.Context) {
		repo := NewQuestions(testPool)
		author := makeUser(t, ctx, "author-q-reorder")
		id := makeContest(t, ctx, author.ID)
		first, _ := repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})
		second, _ := repo.Create(ctx, contests.Question{ContestID: id, Kind: contests.KindText, IsVisible: true})

		if err := repo.Reorder(ctx, id, []uuid.UUID{second.ID, first.ID}); err != nil {
			t.Fatalf("Reorder() = %v", err)
		}

		ordered, _ := repo.List(ctx, id)
		if ordered[0].ID != second.ID || ordered[1].ID != first.ID {
			t.Errorf("order = %v, %v; want %v, %v", ordered[0].ID, ordered[1].ID, second.ID, first.ID)
		}
	})
}

func TestReorderOutsideATransactionIsRefused(t *testing.T) {
	// It relies on deferring the constraint, and SET CONSTRAINTS outside a
	// transaction is silently ignored — the reorder would then work or fail
	// depending on the order rows happened to be visited.
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}

	err := NewQuestions(testPool).Reorder(context.Background(), uuid.New(), []uuid.UUID{uuid.New()})

	if err == nil {
		t.Fatal("Reorder() outside a transaction = nil, want a refusal")
	}
	if errors.Is(err, contests.ErrQuestionNotFound) {
		t.Errorf("Reorder() = %v, want the missing transaction reported, not a lookup failure", err)
	}
}

func TestDeletingAQuestionThatIsNotThereIsReported(t *testing.T) {
	// Silently succeeding would let a stale editor tab report that it removed
	// something it did not.
	withTx(t, func(ctx context.Context) {
		err := NewQuestions(testPool).Delete(ctx, uuid.New())

		if !errors.Is(err, contests.ErrQuestionNotFound) {
			t.Errorf("Delete() of an unknown question = %v, want ErrQuestionNotFound", err)
		}
	})
}

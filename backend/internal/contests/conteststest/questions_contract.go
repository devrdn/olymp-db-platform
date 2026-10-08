package conteststest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// QuestionTarget is what one case of the contract runs against: a repository
// and the means to create what a question hangs off. Contests are the unit of
// isolation: the repository may hold other questions already, so a case only
// ever looks at the contests it created itself. A real schema needs a contest
// to exist before a question can name it, so each implementation fills
// NewContest its own way: the in-memory store mints an identifier, PostgreSQL
// inserts a row.
type QuestionTarget struct {
	Repo contests.QuestionRepository
	// Visible is the participant-facing read over the same questions as
	// Repo: what Repo wrote is what Visible serves.
	Visible contests.VisibleQuestionRepository
	// NewContest creates a contest and returns its identifier.
	NewContest func() uuid.UUID
	// Outside is a context that is not inside a unit of work. The context
	// every other case is run with is inside one.
	Outside context.Context
}

// QuestionRepositoryContract is what every contests.QuestionRepository and
// contests.VisibleQuestionRepository must do, run as subtests against one
// implementation. Both the in-memory Questions and postgres.Questions run it,
// so the store the service tests trust and the store production uses are held
// to the same answers: a rule the fake got wrong would otherwise pass every
// service test and fail only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repository with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here. What needs a second
// transaction or the real database (two authors adding a question at the
// same moment, the bounds on a column) is outside it, and is asked of
// PostgreSQL alone where a test exists.
//
// Texts use the language codes "en", "ru" and "ro", which the real schema
// seeds. A question is told apart from its neighbours by its Points, which
// the cases choose to be distinct.
func QuestionRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, QuestionTarget))) {
	// textual is the ordinary visible text question the cases build on.
	textual := func(contest uuid.UUID, points int) contests.Question {
		return contests.Question{ContestID: contest, Kind: contests.KindText, Points: points, IsVisible: true}
	}
	create := func(t *testing.T, ctx context.Context, target QuestionTarget, q contests.Question) contests.Question {
		t.Helper()
		created, err := target.Repo.Create(ctx, q)
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		return created
	}
	byID := func(t *testing.T, ctx context.Context, target QuestionTarget, id uuid.UUID) contests.Question {
		t.Helper()
		q, err := target.Repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		return q
	}
	list := func(t *testing.T, ctx context.Context, target QuestionTarget, contest uuid.UUID) []contests.Question {
		t.Helper()
		found, err := target.Repo.List(ctx, contest)
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		return found
	}
	// layout is the contest's questions as (points, position) in the order
	// List returned them.
	layout := func(t *testing.T, ctx context.Context, target QuestionTarget, contest uuid.UUID) (points, positions []int) {
		t.Helper()
		for _, q := range list(t, ctx, target, contest) {
			points = append(points, q.Points)
			positions = append(positions, q.Ord)
		}
		return points, positions
	}
	wantLayout := func(t *testing.T, ctx context.Context, target QuestionTarget, contest uuid.UUID, wantPoints, wantPositions []int) {
		t.Helper()
		points, positions := layout(t, ctx, target, contest)
		if !slices.Equal(points, wantPoints) || !slices.Equal(positions, wantPositions) {
			t.Errorf("questions (points, positions) = %v, %v; want %v, %v", points, positions, wantPoints, wantPositions)
		}
	}
	notFound := func(t *testing.T, err error, what string) {
		t.Helper()
		if !errors.Is(err, contests.ErrQuestionNotFound) {
			t.Errorf("%s error = %v, want ErrQuestionNotFound", what, err)
		}
	}
	replaceTexts := func(t *testing.T, ctx context.Context, target QuestionTarget, id uuid.UUID, texts map[string]contests.QuestionText) {
		t.Helper()
		if err := target.Repo.ReplaceTexts(ctx, id, texts); err != nil {
			t.Fatalf("ReplaceTexts() = %v", err)
		}
	}
	replaceAnswers := func(t *testing.T, ctx context.Context, target QuestionTarget, id uuid.UUID, answers []contests.Answer) {
		t.Helper()
		if err := target.Repo.ReplaceAnswers(ctx, id, answers); err != nil {
			t.Fatalf("ReplaceAnswers() = %v", err)
		}
	}
	// answersOf is what a question accepts as (kind, value) pairs, which is
	// all the caller chose about an answer.
	answersOf := func(q contests.Question) [][2]string {
		var got [][2]string
		for _, a := range q.Answers {
			got = append(got, [2]string{a.MatchKind, a.Value})
		}
		return got
	}
	forContest := func(t *testing.T, ctx context.Context, target QuestionTarget, contest uuid.UUID, lang string) []contests.VisibleQuestion {
		t.Helper()
		found, err := target.Visible.ForContest(ctx, contest, lang)
		if err != nil {
			t.Fatalf("ForContest(%q) = %v", lang, err)
		}
		return found
	}
	// scribble overwrites everything reachable through a question that a
	// caller could write to, to show whether the repository handed it a view
	// of its own storage.
	scribble := func(q contests.Question) {
		if q.MaxAttempts != nil {
			*q.MaxAttempts = 99
		}
		for i := range q.ChoiceIDs {
			q.ChoiceIDs[i] = "scribbled"
		}
		for lang, text := range q.Texts {
			for id := range text.Choices {
				text.Choices[id] = "scribbled"
			}
			delete(q.Texts, lang)
		}
		for i := range q.Answers {
			q.Answers[i].Value = "scribbled"
		}
	}

	t.Run("Create returns the question it wrote", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			attempts := 3

			got := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 7, MaxAttempts: &attempts,
				PenaltyPct: 25, IsVisible: false, ChoiceIDs: []string{"b", "a"},
			})

			if got.ID == uuid.Nil {
				t.Error("ID is nil")
			}
			if got.ContestID != contest || got.Ord != 1 {
				t.Errorf("(contest, position) = (%s, %d), want (%s, 1)", got.ContestID, got.Ord, contest)
			}
			if got.Kind != contests.KindChoice || got.Points != 7 || got.PenaltyPct != 25 || got.IsVisible {
				t.Errorf("(kind, points, penalty, visible) = (%q, %d, %d, %v), want (choice, 7, 25, false)",
					got.Kind, got.Points, got.PenaltyPct, got.IsVisible)
			}
			if got.MaxAttempts == nil || *got.MaxAttempts != 3 {
				t.Errorf("MaxAttempts = %v, want 3", got.MaxAttempts)
			}
			if !slices.Equal(got.ChoiceIDs, []string{"b", "a"}) {
				t.Errorf("ChoiceIDs = %v, want [b a], in the order given", got.ChoiceIDs)
			}
			if len(got.Texts) != 0 || len(got.Answers) != 0 {
				t.Errorf("a new question carries %d texts and %d answers, want none", len(got.Texts), len(got.Answers))
			}
		})
	})

	t.Run("Create leaves a limit that was not set unset", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			got := create(t, ctx, target, textual(target.NewContest(), 1))

			if got.MaxAttempts != nil {
				t.Errorf("MaxAttempts = %d, want none", *got.MaxAttempts)
			}
			if len(got.ChoiceIDs) != 0 {
				t.Errorf("ChoiceIDs = %v, want none", got.ChoiceIDs)
			}
		})
	})

	t.Run("Create assigns the next position within the contest", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			ours, theirs := target.NewContest(), target.NewContest()

			create(t, ctx, target, textual(ours, 10))
			create(t, ctx, target, textual(ours, 20))
			create(t, ctx, target, textual(theirs, 30))

			wantLayout(t, ctx, target, ours, []int{10, 20}, []int{1, 2})
			wantLayout(t, ctx, target, theirs, []int{30}, []int{1})
		})
	})

	t.Run("Create takes the position and the identity for itself", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			claimed := uuid.New()
			q := textual(contest, 5)
			q.ID, q.Ord = claimed, 9

			got := create(t, ctx, target, q)

			if got.ID == claimed {
				t.Error("the identifier the caller supplied was kept")
			}
			if got.Ord != 1 {
				t.Errorf("Ord = %d, want 1 whatever the caller supplied", got.Ord)
			}
		})
	})

	t.Run("Create stores no text or answers it is handed", func(t *testing.T) {
		// Text and answers have their own operations; a question that
		// arrives carrying some is stored without them.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			q := textual(contest, 5)
			q.Texts = map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}}
			q.Answers = []contests.Answer{{MatchKind: contests.MatchExact, Value: "the butler"}}

			created := create(t, ctx, target, q)

			got := byID(t, ctx, target, created.ID)
			if len(got.Texts) != 0 || len(got.Answers) != 0 {
				t.Errorf("stored %d texts and %d answers, want none", len(got.Texts), len(got.Answers))
			}
			if found := forContest(t, ctx, target, contest, "en"); len(found) != 0 {
				t.Errorf("ForContest() = %+v, want nothing: the question has no text", found)
			}
		})
	})

	t.Run("Create after a delete takes the first free position", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			create(t, ctx, target, textual(contest, 10))
			middle := create(t, ctx, target, textual(contest, 20))
			create(t, ctx, target, textual(contest, 30))
			if err := target.Repo.Delete(ctx, middle.ID); err != nil {
				t.Fatalf("Delete() = %v", err)
			}

			got := create(t, ctx, target, textual(contest, 40))

			if got.Ord != 3 {
				t.Errorf("Ord = %d, want 3", got.Ord)
			}
			wantLayout(t, ctx, target, contest, []int{10, 30, 40}, []int{1, 2, 3})
		})
	})

	t.Run("ByID reads back what Create wrote", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			attempts := 2
			created := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 7, MaxAttempts: &attempts,
				PenaltyPct: 25, IsVisible: false, ChoiceIDs: []string{"a", "b"},
			})

			got := byID(t, ctx, target, created.ID)

			if got.ID != created.ID || got.ContestID != contest || got.Ord != 1 {
				t.Errorf("(id, contest, position) = (%s, %s, %d), want (%s, %s, 1)", got.ID, got.ContestID, got.Ord, created.ID, contest)
			}
			if got.Kind != contests.KindChoice || got.Points != 7 || got.PenaltyPct != 25 || got.IsVisible {
				t.Errorf("(kind, points, penalty, visible) = (%q, %d, %d, %v), want (choice, 7, 25, false)",
					got.Kind, got.Points, got.PenaltyPct, got.IsVisible)
			}
			if got.MaxAttempts == nil || *got.MaxAttempts != 2 {
				t.Errorf("MaxAttempts = %v, want 2", got.MaxAttempts)
			}
			if !slices.Equal(got.ChoiceIDs, []string{"a", "b"}) {
				t.Errorf("ChoiceIDs = %v, want [a b]", got.ChoiceIDs)
			}
		})
	})

	t.Run("ByID of a question that is not there is reported", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			_, err := target.Repo.ByID(ctx, uuid.New())

			notFound(t, err, "ByID() of an unknown question")
		})
	})

	t.Run("List is empty for a contest with no questions", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			elsewhere := target.NewContest()
			create(t, ctx, target, textual(elsewhere, 1))

			if got := list(t, ctx, target, target.NewContest()); len(got) != 0 {
				t.Errorf("List() = %d questions, want none", len(got))
			}
		})
	})

	t.Run("List carries each question's text and answers", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			first := create(t, ctx, target, textual(contest, 10))
			second := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 20, IsVisible: false, ChoiceIDs: []string{"a", "b"},
			})
			replaceTexts(t, ctx, target, first.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "Who did it?"},
				"ru": {BodyMD: "Who did it, in Russian?"},
			})
			replaceAnswers(t, ctx, target, first.ID, []contests.Answer{
				{MatchKind: contests.MatchExactCI, Value: "the butler"},
			})
			replaceTexts(t, ctx, target, second.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "What weapon?", Choices: map[string]string{"a": "The rope", "b": "The candlestick"}},
			})

			got := list(t, ctx, target, contest)

			if len(got) != 2 {
				t.Fatalf("List() = %d questions, want 2", len(got))
			}
			if len(got[0].Texts) != 2 || got[0].Texts["en"].BodyMD != "Who did it?" || got[0].Texts["ru"].BodyMD != "Who did it, in Russian?" {
				t.Errorf("first question's texts = %+v, want its two", got[0].Texts)
			}
			if want := [][2]string{{contests.MatchExactCI, "the butler"}}; !slices.Equal(answersOf(got[0]), want) {
				t.Errorf("first question's answers = %v, want %v", answersOf(got[0]), want)
			}
			if len(got[1].Texts) != 1 || got[1].Texts["en"].BodyMD != "What weapon?" || len(got[1].Answers) != 0 {
				t.Errorf("second question = (%+v, %v), want its one text and no answers", got[1].Texts, got[1].Answers)
			}
			if got[1].Kind != contests.KindChoice || got[1].IsVisible || !slices.Equal(got[1].ChoiceIDs, []string{"a", "b"}) {
				t.Errorf("second question (kind, visible, options) = (%q, %v, %v), want (choice, false, [a b])",
					got[1].Kind, got[1].IsVisible, got[1].ChoiceIDs)
			}
			if labels := got[1].Texts["en"].Choices; len(labels) != 2 || labels["a"] != "The rope" || labels["b"] != "The candlestick" {
				t.Errorf("second question's labels = %v, want the rope and the candlestick", labels)
			}
		})
	})

	t.Run("Update saves the question's own fields", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			created := create(t, ctx, target, textual(contest, 10))
			attempts := 4

			err := target.Repo.Update(ctx, contests.Question{
				ID: created.ID, ContestID: contest, Kind: contests.KindChoice, Points: 15, MaxAttempts: &attempts,
				PenaltyPct: 50, IsVisible: false, ChoiceIDs: []string{"x", "y", "z"},
			})
			if err != nil {
				t.Fatalf("Update() = %v", err)
			}

			got := byID(t, ctx, target, created.ID)
			if got.Kind != contests.KindChoice || got.Points != 15 || got.PenaltyPct != 50 || got.IsVisible {
				t.Errorf("(kind, points, penalty, visible) = (%q, %d, %d, %v), want (choice, 15, 50, false)",
					got.Kind, got.Points, got.PenaltyPct, got.IsVisible)
			}
			if got.MaxAttempts == nil || *got.MaxAttempts != 4 {
				t.Errorf("MaxAttempts = %v, want 4", got.MaxAttempts)
			}
			if !slices.Equal(got.ChoiceIDs, []string{"x", "y", "z"}) {
				t.Errorf("ChoiceIDs = %v, want [x y z]", got.ChoiceIDs)
			}
		})
	})

	t.Run("Update can lift a limit and empty the options", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			attempts := 4
			created := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 10, MaxAttempts: &attempts,
				IsVisible: true, ChoiceIDs: []string{"a", "b"},
			})

			err := target.Repo.Update(ctx, contests.Question{
				ID: created.ID, ContestID: contest, Kind: contests.KindText, Points: 10, IsVisible: true,
			})
			if err != nil {
				t.Fatalf("Update() = %v", err)
			}

			got := byID(t, ctx, target, created.ID)
			if got.MaxAttempts != nil {
				t.Errorf("MaxAttempts = %d, want none", *got.MaxAttempts)
			}
			if len(got.ChoiceIDs) != 0 {
				t.Errorf("ChoiceIDs = %v, want none", got.ChoiceIDs)
			}
		})
	})

	t.Run("Update leaves position, contest, text and answers alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest, elsewhere := target.NewContest(), target.NewContest()
			create(t, ctx, target, textual(contest, 10))
			created := create(t, ctx, target, textual(contest, 20))
			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}})
			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{{MatchKind: contests.MatchExact, Value: "the butler"}})

			// The values for the fields Update does not own are the ones a
			// careless caller would send: another contest, another position,
			// and no text or answers at all.
			err := target.Repo.Update(ctx, contests.Question{
				ID: created.ID, ContestID: elsewhere, Ord: 7, Kind: contests.KindText, Points: 25, IsVisible: true,
			})
			if err != nil {
				t.Fatalf("Update() = %v", err)
			}

			got := byID(t, ctx, target, created.ID)
			if got.ContestID != contest || got.Ord != 2 {
				t.Errorf("(contest, position) = (%s, %d), want (%s, 2)", got.ContestID, got.Ord, contest)
			}
			if got.Points != 25 {
				t.Errorf("Points = %d, want the update's 25", got.Points)
			}
			if got.Texts["en"].BodyMD != "Who did it?" || len(got.Texts) != 1 {
				t.Errorf("Texts = %+v, want the one that was there", got.Texts)
			}
			if want := [][2]string{{contests.MatchExact, "the butler"}}; !slices.Equal(answersOf(got), want) {
				t.Errorf("answers = %v, want %v", answersOf(got), want)
			}
			if found := list(t, ctx, target, elsewhere); len(found) != 0 {
				t.Errorf("the other contest gained %d questions, want none", len(found))
			}
		})
	})

	t.Run("Update of a question that is not there is reported", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			err := target.Repo.Update(ctx, contests.Question{
				ID: uuid.New(), ContestID: target.NewContest(), Kind: contests.KindText, IsVisible: true,
			})

			notFound(t, err, "Update() of an unknown question")
		})
	})

	t.Run("Delete removes the question and closes the gap", func(t *testing.T) {
		// Positions are what the participant's view is built from; a hole in
		// them would show up as a numbering that skips.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			first := create(t, ctx, target, textual(contest, 10))
			create(t, ctx, target, textual(contest, 20))
			create(t, ctx, target, textual(contest, 30))

			if err := target.Repo.Delete(ctx, first.ID); err != nil {
				t.Fatalf("Delete() = %v", err)
			}

			wantLayout(t, ctx, target, contest, []int{20, 30}, []int{1, 2})
			_, err := target.Repo.ByID(ctx, first.ID)
			notFound(t, err, "ByID() of the deleted question")
		})
	})

	t.Run("Delete of the last question leaves the others where they were", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			create(t, ctx, target, textual(contest, 10))
			create(t, ctx, target, textual(contest, 20))
			last := create(t, ctx, target, textual(contest, 30))

			if err := target.Repo.Delete(ctx, last.ID); err != nil {
				t.Fatalf("Delete() = %v", err)
			}

			wantLayout(t, ctx, target, contest, []int{10, 20}, []int{1, 2})
		})
	})

	t.Run("Delete renumbers only its own contest", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			doomed := create(t, ctx, target, textual(ours, 10))
			create(t, ctx, target, textual(ours, 20))
			create(t, ctx, target, textual(theirs, 30))
			create(t, ctx, target, textual(theirs, 40))

			if err := target.Repo.Delete(ctx, doomed.ID); err != nil {
				t.Fatalf("Delete() = %v", err)
			}

			wantLayout(t, ctx, target, ours, []int{20}, []int{1})
			wantLayout(t, ctx, target, theirs, []int{30, 40}, []int{1, 2})
		})
	})

	t.Run("Delete of a question that is not there is reported", func(t *testing.T) {
		// Silently succeeding would let a stale editor tab report that it
		// removed something it did not.
		each(t, func(ctx context.Context, target QuestionTarget) {
			err := target.Repo.Delete(ctx, uuid.New())

			notFound(t, err, "Delete() of an unknown question")
		})
	})

	t.Run("Reorder sets the display order", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			a := create(t, ctx, target, textual(contest, 10))
			b := create(t, ctx, target, textual(contest, 20))
			c := create(t, ctx, target, textual(contest, 30))

			if err := target.Repo.Reorder(ctx, contest, []uuid.UUID{c.ID, a.ID, b.ID}); err != nil {
				t.Fatalf("Reorder() = %v", err)
			}

			wantLayout(t, ctx, target, contest, []int{30, 10, 20}, []int{1, 2, 3})
			if got := byID(t, ctx, target, c.ID); got.Ord != 1 {
				t.Errorf("ByID() position = %d, want 1", got.Ord)
			}
		})
	})

	t.Run("Reorder swaps two questions", func(t *testing.T) {
		// The case the real constraint on positions has to tolerate: halfway
		// through, two questions would hold the same one.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			first := create(t, ctx, target, textual(contest, 10))
			second := create(t, ctx, target, textual(contest, 20))

			if err := target.Repo.Reorder(ctx, contest, []uuid.UUID{second.ID, first.ID}); err != nil {
				t.Fatalf("Reorder() = %v", err)
			}

			wantLayout(t, ctx, target, contest, []int{20, 10}, []int{1, 2})
		})
	})

	t.Run("Reorder leaves other contests alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			a := create(t, ctx, target, textual(ours, 10))
			b := create(t, ctx, target, textual(ours, 20))
			create(t, ctx, target, textual(theirs, 30))
			create(t, ctx, target, textual(theirs, 40))

			if err := target.Repo.Reorder(ctx, ours, []uuid.UUID{b.ID, a.ID}); err != nil {
				t.Fatalf("Reorder() = %v", err)
			}

			wantLayout(t, ctx, target, theirs, []int{30, 40}, []int{1, 2})
		})
	})

	t.Run("Reorder names no question that is not there", func(t *testing.T) {
		// Nothing is read after the refusal: whether the questions before the
		// stranger were already moved is the unit of work's to undo.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			a := create(t, ctx, target, textual(contest, 10))
			b := create(t, ctx, target, textual(contest, 20))

			err := target.Repo.Reorder(ctx, contest, []uuid.UUID{b.ID, uuid.New(), a.ID})

			notFound(t, err, "Reorder() naming an unknown question")
		})
	})

	t.Run("Reorder names no question of another contest", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			mine := create(t, ctx, target, textual(ours, 10))
			foreign := create(t, ctx, target, textual(theirs, 20))

			err := target.Repo.Reorder(ctx, ours, []uuid.UUID{foreign.ID, mine.ID})

			notFound(t, err, "Reorder() naming another contest's question")
		})
	})

	t.Run("Create and Reorder refuse to run outside a unit of work", func(t *testing.T) {
		// Both rely on a transaction for what they promise (the lock that
		// serialises position allocation, the deferred ordering constraint),
		// and a silent no-op outside one is a race that only appears under
		// load. The refusal is a plain error, not a lookup failure: a missing
		// transaction must not read as a missing question.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			first := create(t, ctx, target, textual(contest, 10))

			_, err := target.Repo.Create(target.Outside, textual(contest, 20))
			if err == nil {
				t.Error("Create() outside a unit of work = nil, want an error")
			} else if errors.Is(err, contests.ErrQuestionNotFound) {
				t.Errorf("Create() outside a unit of work = %v, want the missing transaction reported, not a lookup failure", err)
			}

			err = target.Repo.Reorder(target.Outside, contest, []uuid.UUID{first.ID})
			if err == nil {
				t.Error("Reorder() outside a unit of work = nil, want an error")
			} else if errors.Is(err, contests.ErrQuestionNotFound) {
				t.Errorf("Reorder() outside a unit of work = %v, want the missing transaction reported, not a lookup failure", err)
			}
		})
	})

	t.Run("ReplaceTexts sets the text per language", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			created := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 10, IsVisible: true, ChoiceIDs: []string{"a", "b"},
			})

			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
				"ro": {BodyMD: "Cine a facut-o?", Choices: map[string]string{"a": "Majordomul", "b": "Gradinarul"}},
			})

			got := byID(t, ctx, target, created.ID).Texts
			if len(got) != 2 {
				t.Fatalf("Texts has %d languages, want 2", len(got))
			}
			if got["en"].BodyMD != "Who did it?" || got["en"].Choices["a"] != "The butler" || got["en"].Choices["b"] != "The gardener" || len(got["en"].Choices) != 2 {
				t.Errorf("en = %+v, want its body and two labels", got["en"])
			}
			if got["ro"].BodyMD != "Cine a facut-o?" || got["ro"].Choices["b"] != "Gradinarul" || len(got["ro"].Choices) != 2 {
				t.Errorf("ro = %+v, want its body and two labels", got["ro"])
			}
		})
	})

	t.Run("ReplaceTexts replaces what was there", func(t *testing.T) {
		// A language the new set does not name is removed, one it names is
		// overwritten, and one it adds is added.
		each(t, func(ctx context.Context, target QuestionTarget) {
			created := create(t, ctx, target, textual(target.NewContest(), 10))
			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "Old English"},
				"ru": {BodyMD: "Old Russian"},
			})

			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "New English"},
				"ro": {BodyMD: "New Romanian"},
			})

			got := byID(t, ctx, target, created.ID).Texts
			if len(got) != 2 || got["en"].BodyMD != "New English" || got["ro"].BodyMD != "New Romanian" {
				t.Errorf("Texts = %+v, want only new English and new Romanian", got)
			}
		})
	})

	t.Run("ReplaceTexts with none removes every language", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			created := create(t, ctx, target, textual(target.NewContest(), 10))
			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}})

			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{})

			if got := byID(t, ctx, target, created.ID).Texts; len(got) != 0 {
				t.Errorf("Texts = %+v, want none", got)
			}
		})
	})

	t.Run("ReplaceTexts of one question leaves the others alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			mine := create(t, ctx, target, textual(contest, 10))
			other := create(t, ctx, target, textual(contest, 20))
			replaceTexts(t, ctx, target, other.ID, map[string]contests.QuestionText{"en": {BodyMD: "Theirs"}})

			replaceTexts(t, ctx, target, mine.ID, map[string]contests.QuestionText{"en": {BodyMD: "Mine"}})
			replaceTexts(t, ctx, target, mine.ID, map[string]contests.QuestionText{})

			if got := byID(t, ctx, target, other.ID).Texts; len(got) != 1 || got["en"].BodyMD != "Theirs" {
				t.Errorf("the other question's Texts = %+v, want its own", got)
			}
		})
	})

	t.Run("ReplaceAnswers sets the reference answers", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			created := create(t, ctx, target, textual(target.NewContest(), 10))

			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{
				{MatchKind: contests.MatchRegex, Value: "but+ler"},
				{MatchKind: contests.MatchExactCI, Value: "the butler"},
			})

			got := byID(t, ctx, target, created.ID)
			want := [][2]string{{contests.MatchRegex, "but+ler"}, {contests.MatchExactCI, "the butler"}}
			if !slices.Equal(answersOf(got), want) {
				t.Errorf("answers = %v, want %v", answersOf(got), want)
			}
		})
	})

	t.Run("ReplaceAnswers identifies each answer with its question", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			created := create(t, ctx, target, textual(target.NewContest(), 10))

			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{
				{MatchKind: contests.MatchExact, Value: "one"},
				{MatchKind: contests.MatchExact, Value: "two"},
			})

			got := byID(t, ctx, target, created.ID).Answers
			if len(got) != 2 {
				t.Fatalf("answers = %d, want 2", len(got))
			}
			for _, a := range got {
				if a.ID == uuid.Nil || a.QuestionID != created.ID {
					t.Errorf("answer %q = (id %s, question %s), want an identifier and question %s", a.Value, a.ID, a.QuestionID, created.ID)
				}
			}
			if got[0].ID == got[1].ID {
				t.Errorf("both answers carry the identifier %s", got[0].ID)
			}
		})
	})

	t.Run("ReplaceAnswers lists the answers by value", func(t *testing.T) {
		// Whatever order they were authored in, a question's answers come
		// back in the order of their values, so a re-read does not reshuffle
		// what the organizer sees.
		each(t, func(ctx context.Context, target QuestionTarget) {
			created := create(t, ctx, target, textual(target.NewContest(), 10))

			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{
				{MatchKind: contests.MatchExact, Value: "gamma"},
				{MatchKind: contests.MatchExact, Value: "alpha"},
				{MatchKind: contests.MatchExact, Value: "beta"},
			})

			var values []string
			for _, a := range byID(t, ctx, target, created.ID).Answers {
				values = append(values, a.Value)
			}
			if want := []string{"alpha", "beta", "gamma"}; !slices.Equal(values, want) {
				t.Errorf("values = %v, want %v", values, want)
			}
		})
	})

	t.Run("ReplaceAnswers removes the old ones", func(t *testing.T) {
		// A stale reference answer would keep accepting something the
		// organizer has already decided is wrong.
		each(t, func(ctx context.Context, target QuestionTarget) {
			created := create(t, ctx, target, textual(target.NewContest(), 10))
			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{
				{MatchKind: contests.MatchExactCI, Value: "the butler"},
				{MatchKind: contests.MatchExactCI, Value: "butler"},
			})

			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{
				{MatchKind: contests.MatchExactCI, Value: "the gardener"},
			})

			want := [][2]string{{contests.MatchExactCI, "the gardener"}}
			if got := answersOf(byID(t, ctx, target, created.ID)); !slices.Equal(got, want) {
				t.Errorf("answers = %v, want %v", got, want)
			}
		})
	})

	t.Run("ReplaceAnswers with none removes them all", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			created := create(t, ctx, target, textual(target.NewContest(), 10))
			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{{MatchKind: contests.MatchExact, Value: "the butler"}})

			replaceAnswers(t, ctx, target, created.ID, nil)

			if got := byID(t, ctx, target, created.ID).Answers; len(got) != 0 {
				t.Errorf("answers = %+v, want none", got)
			}
		})
	})

	t.Run("ReplaceAnswers of one question leaves the others alone", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			mine := create(t, ctx, target, textual(contest, 10))
			other := create(t, ctx, target, textual(contest, 20))
			replaceAnswers(t, ctx, target, other.ID, []contests.Answer{{MatchKind: contests.MatchExact, Value: "theirs"}})

			replaceAnswers(t, ctx, target, mine.ID, []contests.Answer{{MatchKind: contests.MatchExact, Value: "mine"}})
			replaceAnswers(t, ctx, target, mine.ID, nil)

			want := [][2]string{{contests.MatchExact, "theirs"}}
			if got := answersOf(byID(t, ctx, target, other.ID)); !slices.Equal(got, want) {
				t.Errorf("the other question's answers = %v, want %v", got, want)
			}
		})
	})

	t.Run("a question handed back is the caller's own copy", func(t *testing.T) {
		// A caller edits what it is given — the service builds the next
		// version of a question from the one it read — and that must not
		// reach the stored question until the caller saves it.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			attempts := 3
			created := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 10, MaxAttempts: &attempts,
				IsVisible: true, ChoiceIDs: []string{"a", "b"},
			})
			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
			})
			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}})

			scribble(created)
			scribble(byID(t, ctx, target, created.ID))
			scribble(list(t, ctx, target, contest)[0])

			got := byID(t, ctx, target, created.ID)
			if got.MaxAttempts == nil || *got.MaxAttempts != 3 {
				t.Errorf("MaxAttempts = %v, want 3", got.MaxAttempts)
			}
			if !slices.Equal(got.ChoiceIDs, []string{"a", "b"}) {
				t.Errorf("ChoiceIDs = %v, want [a b]", got.ChoiceIDs)
			}
			if len(got.Texts) != 1 || got.Texts["en"].Choices["a"] != "The butler" || got.Texts["en"].Choices["b"] != "The gardener" {
				t.Errorf("Texts = %+v, want the English text with both labels", got.Texts)
			}
			if want := [][2]string{{contests.MatchExact, "a"}}; !slices.Equal(answersOf(got), want) {
				t.Errorf("answers = %v, want %v", answersOf(got), want)
			}
		})
	})

	t.Run("what the repository was handed is kept as a copy", func(t *testing.T) {
		// The converse: a caller that goes on editing what it passed in has
		// not saved anything.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			attempts := 3
			choiceIDs := []string{"a", "b"}
			q := contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 10, MaxAttempts: &attempts,
				IsVisible: true, ChoiceIDs: choiceIDs,
			}
			created := create(t, ctx, target, q)
			texts := map[string]contests.QuestionText{
				"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
			}
			answers := []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}}
			replaceTexts(t, ctx, target, created.ID, texts)
			replaceAnswers(t, ctx, target, created.ID, answers)

			attempts = 99
			choiceIDs[0] = "scribbled"
			texts["en"].Choices["a"] = "scribbled"
			delete(texts, "en")
			answers[0].Value = "scribbled"

			got := byID(t, ctx, target, created.ID)
			if got.MaxAttempts == nil || *got.MaxAttempts != 3 {
				t.Errorf("MaxAttempts = %v, want 3", got.MaxAttempts)
			}
			if !slices.Equal(got.ChoiceIDs, []string{"a", "b"}) {
				t.Errorf("ChoiceIDs = %v, want [a b]", got.ChoiceIDs)
			}
			if len(got.Texts) != 1 || got.Texts["en"].Choices["a"] != "The butler" {
				t.Errorf("Texts = %+v, want the English text as it was saved", got.Texts)
			}
			if want := [][2]string{{contests.MatchExact, "a"}}; !slices.Equal(answersOf(got), want) {
				t.Errorf("answers = %v, want %v", answersOf(got), want)
			}
		})
	})

	t.Run("ForContest serves a visible question in the language asked for", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			attempts := 5
			created := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 12, MaxAttempts: &attempts,
				IsVisible: true, ChoiceIDs: []string{"b", "a"},
			})
			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
				"ro": {BodyMD: "Cine a facut-o?", Choices: map[string]string{"a": "Majordomul", "b": "Gradinarul"}},
			})
			replaceAnswers(t, ctx, target, created.ID, []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}})

			got := forContest(t, ctx, target, contest, "ro")

			if len(got) != 1 {
				t.Fatalf("ForContest() = %d questions, want 1", len(got))
			}
			v := got[0]
			if v.ID != created.ID || v.Kind != contests.KindChoice || v.Points != 12 {
				t.Errorf("(id, kind, points) = (%s, %q, %d), want (%s, choice, 12)", v.ID, v.Kind, v.Points, created.ID)
			}
			if v.MaxAttempts == nil || *v.MaxAttempts != 5 {
				t.Errorf("MaxAttempts = %v, want 5", v.MaxAttempts)
			}
			if !slices.Equal(v.ChoiceIDs, []string{"b", "a"}) {
				t.Errorf("ChoiceIDs = %v, want [b a]", v.ChoiceIDs)
			}
			if v.BodyMD != "Cine a facut-o?" {
				t.Errorf("BodyMD = %q, want the Romanian wording", v.BodyMD)
			}
			if len(v.Choices) != 2 || v.Choices["a"] != "Majordomul" || v.Choices["b"] != "Gradinarul" {
				t.Errorf("Choices = %v, want the two Romanian labels", v.Choices)
			}
		})
	})

	t.Run("ForContest leaves out a hidden question", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			visible := create(t, ctx, target, textual(contest, 10))
			hidden := textual(contest, 20)
			hidden.IsVisible = false
			hiddenQuestion := create(t, ctx, target, hidden)
			for _, id := range []uuid.UUID{visible.ID, hiddenQuestion.ID} {
				replaceTexts(t, ctx, target, id, map[string]contests.QuestionText{"en": {BodyMD: "Text"}})
			}

			got := forContest(t, ctx, target, contest, "en")

			if len(got) != 1 || got[0].ID != visible.ID {
				t.Errorf("ForContest() = %+v, want only the visible question", got)
			}
		})
	})

	t.Run("ForContest follows a question being hidden and shown", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			created := create(t, ctx, target, textual(contest, 10))
			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{"en": {BodyMD: "Text"}})
			hide, show := textual(contest, 10), textual(contest, 10)
			hide.ID, hide.IsVisible = created.ID, false
			show.ID = created.ID

			if err := target.Repo.Update(ctx, hide); err != nil {
				t.Fatalf("Update() hiding = %v", err)
			}
			if got := forContest(t, ctx, target, contest, "en"); len(got) != 0 {
				t.Errorf("ForContest() after hiding = %+v, want nothing", got)
			}

			if err := target.Repo.Update(ctx, show); err != nil {
				t.Fatalf("Update() showing = %v", err)
			}
			if got := forContest(t, ctx, target, contest, "en"); len(got) != 1 {
				t.Errorf("ForContest() after showing = %d questions, want 1", len(got))
			}
		})
	})

	t.Run("ForContest leaves out a question with no text in the language", func(t *testing.T) {
		// Served with an empty body it would show an empty page as if that
		// were the question.
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			translated := create(t, ctx, target, textual(contest, 10))
			untranslated := create(t, ctx, target, textual(contest, 20))
			create(t, ctx, target, textual(contest, 30))
			replaceTexts(t, ctx, target, translated.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "English"}, "ru": {BodyMD: "Russian"},
			})
			replaceTexts(t, ctx, target, untranslated.ID, map[string]contests.QuestionText{"ru": {BodyMD: "Russian only"}})

			got := forContest(t, ctx, target, contest, "en")

			if len(got) != 1 || got[0].ID != translated.ID || got[0].BodyMD != "English" {
				t.Errorf("ForContest(en) = %+v, want only the question with English text", got)
			}
		})
	})

	t.Run("ForContest keeps display order and the contest apart", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			a := create(t, ctx, target, textual(ours, 10))
			b := create(t, ctx, target, textual(ours, 20))
			c := create(t, ctx, target, textual(ours, 30))
			foreign := create(t, ctx, target, textual(theirs, 40))
			for _, id := range []uuid.UUID{a.ID, b.ID, c.ID, foreign.ID} {
				replaceTexts(t, ctx, target, id, map[string]contests.QuestionText{"en": {BodyMD: "Text"}})
			}
			if err := target.Repo.Reorder(ctx, ours, []uuid.UUID{c.ID, a.ID, b.ID}); err != nil {
				t.Fatalf("Reorder() = %v", err)
			}

			var points []int
			for _, v := range forContest(t, ctx, target, ours, "en") {
				points = append(points, v.Points)
			}

			if want := []int{30, 10, 20}; !slices.Equal(points, want) {
				t.Errorf("points in order = %v, want %v", points, want)
			}
		})
	})

	t.Run("ForContest is empty for a contest with no questions", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			if got := forContest(t, ctx, target, target.NewContest(), "en"); len(got) != 0 {
				t.Errorf("ForContest() = %+v, want nothing", got)
			}
		})
	})

	t.Run("ForContest hands back a copy of what it serves", func(t *testing.T) {
		each(t, func(ctx context.Context, target QuestionTarget) {
			contest := target.NewContest()
			attempts := 3
			created := create(t, ctx, target, contests.Question{
				ContestID: contest, Kind: contests.KindChoice, Points: 10, MaxAttempts: &attempts,
				IsVisible: true, ChoiceIDs: []string{"a", "b"},
			})
			replaceTexts(t, ctx, target, created.ID, map[string]contests.QuestionText{
				"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
			})

			servedAll := forContest(t, ctx, target, contest, "en")
			if len(servedAll) != 1 {
				t.Fatalf("ForContest() = %d questions, want 1", len(servedAll))
			}
			served := servedAll[0]
			*served.MaxAttempts = 99
			served.ChoiceIDs[0] = "scribbled"
			served.Choices["a"] = "scribbled"

			again := forContest(t, ctx, target, contest, "en")
			if len(again) != 1 {
				t.Fatalf("ForContest() again = %d questions, want 1", len(again))
			}
			got := again[0]
			if got.MaxAttempts == nil || *got.MaxAttempts != 3 || !slices.Equal(got.ChoiceIDs, []string{"a", "b"}) || got.Choices["a"] != "The butler" {
				t.Errorf("ForContest() after editing a result = %+v, want it as saved", got)
			}
		})
	})
}

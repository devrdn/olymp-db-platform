package contests_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func TestTextQuestionIsValid(t *testing.T) {
	q := contests.Question{Kind: contests.KindText, Points: 3}

	if err := q.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestQuestionRejectsAnUnknownKind(t *testing.T) {
	q := contests.Question{Kind: "essay"}

	if err := q.Validate(); !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidQuestion", err)
	}
}

func TestQuestionRejectsNegativePoints(t *testing.T) {
	q := contests.Question{Kind: contests.KindText, Points: -1}

	if err := q.Validate(); !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidQuestion", err)
	}
}

// Scoring multiplies the penalty in int4; an unbounded value makes a wrong
// attempt fail with "integer out of range".
func TestQuestionRejectsAnOverlargePointsValue(t *testing.T) {
	q := contests.Question{Kind: contests.KindText, Points: 10_000_001}

	if err := q.Validate(); !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidQuestion", err)
	}
}

func TestQuestionAcceptsPointsAtTheCeiling(t *testing.T) {
	q := contests.Question{Kind: contests.KindText, Points: 10_000_000}

	if err := q.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestQuestionRejectsAnAttemptLimitOfZero(t *testing.T) {
	// "Unlimited" is expressed by leaving it unset.
	zero := 0
	q := contests.Question{Kind: contests.KindText, MaxAttempts: &zero}

	if err := q.Validate(); !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidQuestion", err)
	}
}

func TestChoiceQuestionNeedsAtLeastTwoOptions(t *testing.T) {
	q := contests.Question{Kind: contests.KindChoice, ChoiceIDs: []string{"a"}}

	if err := q.Validate(); !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidQuestion", err)
	}
}

func TestChoiceQuestionRejectsDuplicateOptionIdentifiers(t *testing.T) {
	// Duplicates make a submission ambiguous and the label map lossy.
	q := contests.Question{Kind: contests.KindChoice, ChoiceIDs: []string{"a", "a"}}

	if err := q.Validate(); !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidQuestion", err)
	}
}

func TestNonChoiceQuestionMustNotCarryOptions(t *testing.T) {
	q := contests.Question{Kind: contests.KindText, ChoiceIDs: []string{"a", "b"}}

	if err := q.Validate(); !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidQuestion", err)
	}
}

func TestChoiceQuestionKnowsItsOwnOptions(t *testing.T) {
	q := contests.Question{Kind: contests.KindChoice, ChoiceIDs: []string{"a", "b"}}

	if !q.HasChoice("b") {
		t.Error("HasChoice(b) = false for a declared option, want true")
	}
	if q.HasChoice("c") {
		t.Error("HasChoice(c) = true for an undeclared option, want false")
	}
}

// Options are counted by the matcher grading uses.
func TestCorrectChoicesCountsEachAcceptedOptionOnce(t *testing.T) {
	q := contests.Question{Kind: contests.KindChoice, ChoiceIDs: []string{"a", "B", "c", "d"}, Answers: []contests.Answer{
		{MatchKind: contests.MatchExact, Value: "a"},
		{MatchKind: contests.MatchExact, Value: "a"},
		{MatchKind: contests.MatchExactCI, Value: "b"},
	}}

	if got := q.CorrectChoices(); got != 2 {
		t.Errorf("CorrectChoices() = %d, want 2", got)
	}
}

// Grading matches the whole answer, so "b" does not accept "abc".
func TestCorrectChoicesMatchesARegexAgainstTheWholeOption(t *testing.T) {
	q := contests.Question{Kind: contests.KindChoice, ChoiceIDs: []string{"a", "b", "abc"}, Answers: []contests.Answer{
		{MatchKind: contests.MatchRegex, Value: "b"},
	}}

	if got := q.CorrectChoices(); got != 1 {
		t.Errorf("CorrectChoices() = %d, want 1", got)
	}
}

func TestAnswerRejectsAnEmptyValue(t *testing.T) {
	a := contests.Answer{MatchKind: contests.MatchExactCI, Value: "  "}

	if err := a.Validate(); !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidAnswer", err)
	}
}

func TestAnswerRejectsAnUnknownMatchKind(t *testing.T) {
	a := contests.Answer{MatchKind: "fuzzy", Value: "the butler"}

	if err := a.Validate(); !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidAnswer", err)
	}
}

func TestAnswerRejectsARegularExpressionThatDoesNotCompile(t *testing.T) {
	// A broken pattern found mid-contest would break grading for everybody.
	a := contests.Answer{MatchKind: contests.MatchRegex, Value: "the (butler"}

	if err := a.Validate(); !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidAnswer", err)
	}
}

// A pattern is wrapped as a whole-answer group; text that balances only once
// wrapped would close the group early and leave part unanchored.
func TestAnswerRejectsAPatternThatOnlyCompilesOnceWrapped(t *testing.T) {
	a := contests.Answer{MatchKind: contests.MatchRegex, Value: "butler)|(?:gardener"}

	if err := a.Validate(); !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidAnswer", err)
	}
}

func TestAnswerAcceptsAWorkingRegularExpression(t *testing.T) {
	a := contests.Answer{MatchKind: contests.MatchRegex, Value: "^(the )?butler$"}

	if err := a.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// CLAUDE.md rule 2. The bound matches maxAnswerRunes: a reference answer is
// never longer than what it matches.
func TestAnswerRejectsAnOverlongValue(t *testing.T) {
	a := contests.Answer{MatchKind: contests.MatchExact, Value: strings.Repeat("a", 1001)}

	if err := a.Validate(); !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidAnswer", err)
	}
}

func TestAnswerRejectsAnOverlongRegularExpression(t *testing.T) {
	a := contests.Answer{MatchKind: contests.MatchRegex, Value: strings.Repeat("a", 1001)}

	if err := a.Validate(); !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidAnswer", err)
	}
}

func TestQuestionsAreAppendedInOrder(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	first, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}
	second, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	if first.Ord != 1 || second.Ord != 2 {
		t.Errorf("positions = %d, %d; want 1, 2", first.Ord, second.Ord)
	}
}

func TestANewQuestionIsVisibleUnlessHidingIsAskedFor(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	created, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	if !created.IsVisible {
		t.Error("IsVisible = false for a new question, want true")
	}
}

func TestAQuestionCanBeHiddenDeliberately(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	hidden := false

	cmd := questionCmd(c.ID)
	cmd.IsVisible = &hidden
	created, err := f.Service.AddQuestion(context.Background(), cmd)
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	if created.IsVisible {
		t.Error("IsVisible = true for a question created hidden, want false")
	}
}

func TestQuestionsCannotBeAddedOnceTheContestIsRunning(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	_, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("AddQuestion() = %v, want ErrNotEditable", err)
	}
}

func TestAQuestionOfAnotherContestCannotBeReached(t *testing.T) {
	// Permission is per contest, so a guessed id from another contest must
	// not reach its question.
	f := conteststest.NewFixture()
	mine := f.SeedContest(contests.StatusDraft)
	theirs := f.SeedContest(contests.StatusDraft)

	victim, err := f.Service.AddQuestion(context.Background(), questionCmd(theirs.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	err = f.Service.DeleteQuestion(context.Background(), uuid.New(), mine.ID, victim.ID)

	if !errors.Is(err, contests.ErrQuestionNotFound) {
		t.Errorf("DeleteQuestion() across contests = %v, want ErrQuestionNotFound", err)
	}
}

func TestDeletingAQuestionClosesTheGapInTheOrder(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	first, _ := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	_, _ = f.Service.AddQuestion(context.Background(), questionCmd(c.ID))

	if err := f.Service.DeleteQuestion(context.Background(), uuid.New(), c.ID, first.ID); err != nil {
		t.Fatalf("DeleteQuestion() = %v", err)
	}

	remaining, _ := f.Service.Questions(context.Background(), c.ID)
	if len(remaining) != 1 || remaining[0].Ord != 1 {
		t.Errorf("remaining = %+v, want one question at position 1", remaining)
	}
}

func TestReorderMustNameEveryQuestion(t *testing.T) {
	// A partial list would leave positions duplicated or missing.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	first, _ := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	_, _ = f.Service.AddQuestion(context.Background(), questionCmd(c.ID))

	err := f.Service.ReorderQuestions(context.Background(), uuid.New(), c.ID, []uuid.UUID{first.ID})

	if !errors.Is(err, contests.ErrInvalidQuestion) {
		t.Errorf("ReorderQuestions() with a partial list = %v, want ErrInvalidQuestion", err)
	}
}

func TestReorderPutsTheQuestionsInTheGivenOrder(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	first, _ := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	second, _ := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))

	if err := f.Service.ReorderQuestions(context.Background(), uuid.New(), c.ID,
		[]uuid.UUID{second.ID, first.ID}); err != nil {
		t.Fatalf("ReorderQuestions() = %v", err)
	}

	ordered, _ := f.Service.Questions(context.Background(), c.ID)
	if ordered[0].ID != second.ID {
		t.Errorf("first question = %v, want %v", ordered[0].ID, second.ID)
	}
}

func TestChoiceAnswerMustNameOneOfTheQuestionsOptions(t *testing.T) {
	// Anything else is an answer nobody can submit.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	cmd := questionCmd(c.ID)
	cmd.Kind = contests.KindChoice
	cmd.ChoiceIDs = []string{"a", "b"}
	q, err := f.Service.AddQuestion(context.Background(), cmd)
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	err = f.Service.SetAnswers(context.Background(), uuid.New(), c.ID, q.ID,
		[]contests.Answer{{MatchKind: contests.MatchExact, Value: "c"}})

	if !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("SetAnswers() = %v, want ErrInvalidAnswer", err)
	}
}

func TestChoiceAnswerNamingAnOptionIsAccepted(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	cmd := questionCmd(c.ID)
	cmd.Kind = contests.KindChoice
	cmd.ChoiceIDs = []string{"a", "b"}
	q, _ := f.Service.AddQuestion(context.Background(), cmd)

	err := f.Service.SetAnswers(context.Background(), uuid.New(), c.ID, q.ID,
		[]contests.Answer{{MatchKind: contests.MatchExact, Value: "b"}})

	if err != nil {
		t.Errorf("SetAnswers() = %v, want nil", err)
	}
}

func TestAnswersRefuseAPatternThatDoesNotCompile(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	q, _ := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))

	err := f.Service.SetAnswers(context.Background(), uuid.New(), c.ID, q.ID,
		[]contests.Answer{{MatchKind: contests.MatchRegex, Value: "the (butler"}})

	if !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Errorf("SetAnswers() = %v, want ErrInvalidAnswer", err)
	}
}

func TestQuestionTextRefusesALanguageTheInstallationDoesNotOffer(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	q, _ := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))

	err := f.Service.SetQuestionTexts(context.Background(), uuid.New(), c.ID, q.ID,
		map[string]contests.QuestionText{"rus": {BodyMD: "Кто это сделал?"}})

	if !errors.Is(err, contests.ErrUnknownLanguage) {
		t.Errorf("SetQuestionTexts() = %v, want ErrUnknownLanguage", err)
	}
}

func questionCmd(contestID uuid.UUID) contests.QuestionCommand {
	return contests.QuestionCommand{
		ActorID:   uuid.New(),
		ContestID: contestID,
		Kind:      contests.KindText,
		Points:    5,
	}
}

func TestChangingAQuestionRecordsWhichFieldMoved(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	created, err := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	hidden := false
	if _, err := f.Service.UpdateQuestion(context.Background(), contests.QuestionCommand{
		ActorID:    uuid.New(),
		ContestID:  c.ID,
		QuestionID: created.ID,
		Kind:       contests.KindText,
		Points:     20,
		IsVisible:  &hidden,
	}); err != nil {
		t.Fatalf("UpdateQuestion() = %v", err)
	}

	var payload map[string]any
	for i := len(f.Audit.Entries) - 1; i >= 0; i-- {
		if f.Audit.Entries[i].Action == audit.ActionQuestionUpdate {
			payload = f.Audit.Entries[i].Payload
			break
		}
	}
	changes, ok := payload["changes"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v, want a changes map", payload)
	}
	points, ok := changes["points"].(map[string]any)
	if !ok || points["from"] != 5 || points["to"] != 20 {
		t.Errorf("points = %v, want 5 → 20", changes["points"])
	}
	if _, present := changes["is_visible"]; !present {
		t.Errorf("changes = %v, want the hidden flag recorded", changes)
	}
	if payload["question_id"] != created.ID.String() {
		t.Errorf("payload = %v, want it to still name the question", payload)
	}
}

// A nil PenaltyPct leaves the stored value alone, as nil IsVisible does.
func TestUpdateQuestionPreservesAnUnmentionedPenalty(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	penalty := 40
	cmd := questionCmd(c.ID)
	cmd.PenaltyPct = &penalty
	created, err := f.Service.AddQuestion(context.Background(), cmd)
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	// PenaltyPct nil, as a client unaware of penalties sends.
	updated, err := f.Service.UpdateQuestion(context.Background(), contests.QuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: created.ID,
		Kind: contests.KindText, Points: 12,
	})
	if err != nil {
		t.Fatalf("UpdateQuestion() = %v", err)
	}
	if updated.PenaltyPct != 40 {
		t.Errorf("PenaltyPct = %d, want 40 (unchanged by an edit that never mentioned it)", updated.PenaltyPct)
	}
	if updated.Points != 12 {
		t.Errorf("Points = %d, want 12 (the field the command did mention)", updated.Points)
	}
}

func TestUpdateQuestionCanChangeThePenalty(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	penalty := 40
	cmd := questionCmd(c.ID)
	cmd.PenaltyPct = &penalty
	created, err := f.Service.AddQuestion(context.Background(), cmd)
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	zero := 0
	updated, err := f.Service.UpdateQuestion(context.Background(), contests.QuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: created.ID,
		Kind: contests.KindText, Points: 5, PenaltyPct: &zero,
	})
	if err != nil {
		t.Fatalf("UpdateQuestion() = %v", err)
	}
	if updated.PenaltyPct != 0 {
		t.Errorf("PenaltyPct = %d, want 0 (explicitly set)", updated.PenaltyPct)
	}
}

func TestSaveQuestionPreservesAnUnmentionedPenalty(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	penalty := 30
	cmd := questionCmd(c.ID)
	cmd.PenaltyPct = &penalty
	created, err := f.Service.AddQuestion(context.Background(), cmd)
	if err != nil {
		t.Fatalf("AddQuestion() = %v", err)
	}

	saved, err := f.Service.SaveQuestion(context.Background(), contests.SaveQuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: created.ID,
		Kind: contests.KindText, Points: 8,
	})
	if err != nil {
		t.Fatalf("SaveQuestion() = %v", err)
	}
	if saved.PenaltyPct != 30 {
		t.Errorf("PenaltyPct = %d, want 30 (unchanged by a save that never mentioned it)", saved.PenaltyPct)
	}
}

func TestSettingTheAnswersStillRecordsOnlyHowMany(t *testing.T) {
	// Organizers read the trail; it must not reveal the answers.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	created, _ := f.Service.AddQuestion(context.Background(), questionCmd(c.ID))

	if err := f.Service.SetAnswers(context.Background(), uuid.New(), c.ID, created.ID,
		[]contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}}); err != nil {
		t.Fatalf("SetAnswers() = %v", err)
	}

	for _, entry := range f.Audit.Entries {
		if entry.Action != audit.ActionAnswersChange {
			continue
		}
		encoded := fmt.Sprint(entry.Payload)
		if strings.Contains(encoded, "butler") {
			t.Fatalf("payload = %v, want no answer value in it", entry.Payload)
		}
		if entry.Payload["count"] != 1 {
			t.Errorf("payload = %v, want the count", entry.Payload)
		}
		return
	}
	t.Fatalf("no %s entry", audit.ActionAnswersChange)
}

func TestSavingAQuestionWholeWritesEveryPart(t *testing.T) {
	// One request, so a question is never left half saved.
	ctx := context.Background()
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, IsVisible: true,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "Old wording"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	saved, err := f.Service.SaveQuestion(ctx, contests.SaveQuestionCommand{
		ActorID:    uuid.New(),
		ContestID:  c.ID,
		QuestionID: q.ID,
		Kind:       contests.KindText,
		Points:     9,
		Texts:      map[string]contests.QuestionText{"en": {BodyMD: "New wording"}},
		Answers:    []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the gardener"}},
	})
	if err != nil {
		t.Fatalf("SaveQuestion() = %v", err)
	}

	if saved.Points != 9 {
		t.Errorf("points = %d, want 9", saved.Points)
	}
	stored, err := f.Service.Question(ctx, c.ID, q.ID)
	if err != nil {
		t.Fatalf("Question() = %v", err)
	}
	if stored.Texts["en"].BodyMD != "New wording" {
		t.Errorf("text = %q, want the new wording", stored.Texts["en"].BodyMD)
	}
	if len(stored.Answers) != 1 || stored.Answers[0].Value != "the gardener" {
		t.Errorf("answers = %+v, want the one that was saved", stored.Answers)
	}
}

func TestAQuestionMayChangeItsKindAndItsAnswersTogether(t *testing.T) {
	// Saved separately, each half would be checked against the other's old
	// state and refused.
	ctx := context.Background()
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	saved, err := f.Service.SaveQuestion(ctx, contests.SaveQuestionCommand{
		ActorID:    uuid.New(),
		ContestID:  c.ID,
		QuestionID: q.ID,
		Kind:       contests.KindChoice,
		Points:     5,
		ChoiceIDs:  []string{"a", "b"},
		Texts: map[string]contests.QuestionText{
			"en": {BodyMD: "Who?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
		},
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "b"}},
	})
	if err != nil {
		t.Fatalf("SaveQuestion() = %v", err)
	}

	if saved.Kind != contests.KindChoice {
		t.Errorf("kind = %q, want choice", saved.Kind)
	}
}

func TestARefusedSaveLeavesTheQuestionExactlyAsItWas(t *testing.T) {
	// Everything is checked before anything is written.
	ctx := context.Background()
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindChoice, Points: 5, IsVisible: true,
		ChoiceIDs: []string{"a", "b"},
		Answers:   []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}},
	})

	_, err := f.Service.SaveQuestion(ctx, contests.SaveQuestionCommand{
		ActorID:    uuid.New(),
		ContestID:  c.ID,
		QuestionID: q.ID,
		Kind:       contests.KindChoice,
		Points:     99,
		ChoiceIDs:  []string{"a", "b"},
		Answers:    []contests.Answer{{MatchKind: contests.MatchExact, Value: "z"}},
	})

	if !errors.Is(err, contests.ErrInvalidAnswer) {
		t.Fatalf("SaveQuestion() = %v, want it refused for an answer with no such option", err)
	}
	stored, _ := f.Service.Question(ctx, c.ID, q.ID)
	if stored.Points != 5 {
		t.Errorf("points = %d, want 5: a refused save must change nothing", stored.Points)
	}
}

func TestSavingAQuestionStillNamesTheAnswerChangeOnItsOwn(t *testing.T) {
	// The trail filters answer changes by action (§9), so they keep their own
	// entry, in the same transaction.
	ctx := context.Background()
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, IsVisible: true,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	if _, err := f.Service.SaveQuestion(ctx, contests.SaveQuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: q.ID,
		Kind: contests.KindText, Points: 5,
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the gardener"}},
	}); err != nil {
		t.Fatalf("SaveQuestion() = %v", err)
	}

	if !f.Audit.Recorded(audit.ActionAnswersChange) {
		t.Errorf("actions = %v, want the answer change named on its own", f.Audit.Actions())
	}
	if !f.Audit.Recorded(audit.ActionQuestionUpdate) {
		t.Errorf("actions = %v, want the question's own change recorded too", f.Audit.Actions())
	}
}

func TestSavingAQuestionWithoutTouchingTheAnswersSaysNothingAboutThem(t *testing.T) {
	ctx := context.Background()
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	answers := []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}}
	q := f.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindText, Points: 5, IsVisible: true,
		Answers: answers,
	})

	if _, err := f.Service.SaveQuestion(ctx, contests.SaveQuestionCommand{
		ActorID: uuid.New(), ContestID: c.ID, QuestionID: q.ID,
		Kind: contests.KindText, Points: 6, Answers: answers,
	}); err != nil {
		t.Fatalf("SaveQuestion() = %v", err)
	}

	if f.Audit.Recorded(audit.ActionAnswersChange) {
		t.Errorf("actions = %v, want no answer change recorded", f.Audit.Actions())
	}
}

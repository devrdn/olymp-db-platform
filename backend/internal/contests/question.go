package contests

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// Question kinds.
const (
	// KindText expects free text typed by the participant.
	KindText = "text"
	// KindChoice expects one of the question's own choice identifiers.
	KindChoice = "choice"
	// KindFinal is the verdict: who did it.
	KindFinal = "final"
)

// How a submitted answer is compared with a reference one.
const (
	MatchExact   = "exact"
	MatchExactCI = "exact_ci"
	MatchRegex   = "regex"
)

// maxChoices bounds a choice question's options (CLAUDE.md rule 2).
const maxChoices = 50

// maxPenaltyPct bounds questions.penalty_pct, a percentage (CLAUDE.md rule 2).
const maxPenaltyPct = 100

// maxPoints bounds questions.points, an int4 column. It keeps points_awarded
// inside int4, but not penalty times attempts, which postgres.Submissions
// computes in bigint for that reason.
const maxPoints = 10_000_000

// Errors about questions and their answers.
var (
	ErrQuestionNotFound = errors.New("question not found")
	ErrInvalidQuestion  = errors.New("question is not valid")
	ErrInvalidAnswer    = errors.New("reference answer is not valid")
)

// Question is one thing a contest asks.
type Question struct {
	ID        uuid.UUID
	ContestID uuid.UUID
	// Ord is the display position within the contest.
	Ord    int
	Kind   string
	Points int
	// MaxAttempts is nil when the participant may keep trying.
	MaxAttempts *int
	// PenaltyPct is the percent of Points a wrong attempt costs (§6.1.1),
	// 0..100. It is applied when answering and never recomputed.
	PenaltyPct int
	// IsVisible decides whether the question text is shown. A hidden
	// question still scores: working out what is asked is part of the
	// puzzle (§6.1).
	IsVisible bool
	// ChoiceIDs are the language-independent option identifiers a
	// submission carries, never a label.
	ChoiceIDs []string
	// Texts hold the authored question per language code.
	Texts map[string]QuestionText
	// Answers are the reference answers. They never reach a participant.
	Answers []Answer
}

// QuestionText is a question as authored in one language.
type QuestionText struct {
	BodyMD string
	// Choices maps a choice identifier to its label in this language.
	Choices map[string]string
}

// Answer is one accepted response to a question. Several per question is how
// spelling variants are handled, such as a name transliterated in a
// translated story.
type Answer struct {
	ID         uuid.UUID
	QuestionID uuid.UUID
	MatchKind  string
	Value      string
}

// Validate checks a question's own fields.
func (q Question) Validate() error {
	if !slices.Contains([]string{KindText, KindChoice, KindFinal}, q.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidQuestion, q.Kind)
	}
	if q.Points < 0 || q.Points > maxPoints {
		return fmt.Errorf("%w: points must be between 0 and %d", ErrInvalidQuestion, maxPoints)
	}
	// "Unlimited" is nil; zero would be a question nobody can answer.
	if q.MaxAttempts != nil && *q.MaxAttempts <= 0 {
		return fmt.Errorf("%w: the attempt limit must be positive", ErrInvalidQuestion)
	}
	if q.PenaltyPct < 0 || q.PenaltyPct > maxPenaltyPct {
		return fmt.Errorf("%w: penalty_pct must be between 0 and %d", ErrInvalidQuestion, maxPenaltyPct)
	}
	return q.validateChoices()
}

// validateChoices enforces what a set of options must satisfy to be gradable.
func (q Question) validateChoices() error {
	if q.Kind != KindChoice {
		if len(q.ChoiceIDs) > 0 {
			return fmt.Errorf("%w: only a choice question has options", ErrInvalidQuestion)
		}
		return nil
	}

	if len(q.ChoiceIDs) < 2 {
		return fmt.Errorf("%w: a choice question needs at least two options", ErrInvalidQuestion)
	}
	if len(q.ChoiceIDs) > maxChoices {
		return fmt.Errorf("%w: at most %d options", ErrInvalidQuestion, maxChoices)
	}

	seen := make(map[string]struct{}, len(q.ChoiceIDs))
	for _, id := range q.ChoiceIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: an option identifier must not be empty", ErrInvalidQuestion)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: option %q listed twice", ErrInvalidQuestion, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// HasChoice reports whether id is one of the question's options.
func (q Question) HasChoice(id string) bool {
	return slices.Contains(q.ChoiceIDs, id)
}

// CorrectChoices counts the options that grade as correct, each at most once,
// using the same matcher Submit grades with.
func (q Question) CorrectChoices() int {
	n := 0
	for _, id := range q.ChoiceIDs {
		for _, a := range q.Answers {
			if ok, err := matchAnswer(a, id); err == nil && ok {
				n++
				break
			}
		}
	}
	return n
}

// Validate checks a reference answer. A pattern is compiled here, at
// authoring time, so it cannot break grading during a running contest.
func (a Answer) Validate() error {
	if strings.TrimSpace(a.Value) == "" {
		return fmt.Errorf("%w: the value must not be empty", ErrInvalidAnswer)
	}
	// The same bound a submitted answer carries (CLAUDE.md rule 2).
	if utf8.RuneCountInString(a.Value) > maxAnswerRunes {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidAnswer, maxAnswerRunes)
	}
	if !slices.Contains([]string{MatchExact, MatchExactCI, MatchRegex}, a.MatchKind) {
		return fmt.Errorf("%w: unknown match kind %q", ErrInvalidAnswer, a.MatchKind)
	}
	if a.MatchKind == MatchRegex {
		if _, err := compileAnswerPattern(a.Value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAnswer, err)
		}
	}
	return nil
}

// compileAnswerPattern compiles a regex reference answer anchored to the
// whole answer. Matched as a substring, one answer listing every candidate
// would grade correct. Every caller compiles through here.
//
// The bare pattern must parse first: otherwise text like "a)|(b" compiles
// inside the group while escaping it, leaving part unanchored. Inline flags
// stay scoped to the group.
func compileAnswerPattern(pattern string) (*regexp.Regexp, error) {
	if _, err := syntax.Parse(pattern, syntax.Perl); err != nil {
		return nil, err
	}
	return regexp.Compile(`^(?:` + pattern + `)$`)
}

// validateAnswersFor checks a whole set of reference answers against the
// question they belong to.
func validateAnswersFor(q Question, answers []Answer) error {
	for _, a := range answers {
		if err := a.Validate(); err != nil {
			return err
		}
		// Anything else is an answer nobody can submit.
		if q.Kind == KindChoice && !q.HasChoice(a.Value) {
			return fmt.Errorf("%w: %q is not one of the question's options", ErrInvalidAnswer, a.Value)
		}
	}
	return nil
}

// QuestionRepository stores questions, their authored text and their reference
// answers. Every read is a staff read carrying the answers; participants go
// through VisibleQuestionRepository, a separate query that never selects them.
type QuestionRepository interface {
	// List returns the contest's questions in display order, with their text
	// and reference answers.
	List(ctx context.Context, contestID uuid.UUID) ([]Question, error)
	// ByID returns one question, or ErrQuestionNotFound.
	ByID(ctx context.Context, questionID uuid.UUID) (Question, error)
	// Create appends a question, assigning it the next free position.
	Create(ctx context.Context, q Question) (Question, error)
	// Update saves a question's own fields.
	Update(ctx context.Context, q Question) error
	// Delete removes a question and closes the gap in the ordering.
	Delete(ctx context.Context, questionID uuid.UUID) error
	// Reorder sets the display order to exactly this sequence, which must name
	// every question of the contest.
	Reorder(ctx context.Context, contestID uuid.UUID, ordered []uuid.UUID) error
	// ReplaceTexts sets the question's authored text to exactly these
	// languages.
	ReplaceTexts(ctx context.Context, questionID uuid.UUID, texts map[string]QuestionText) error
	// ReplaceAnswers sets the reference answers to exactly these.
	ReplaceAnswers(ctx context.Context, questionID uuid.UUID, answers []Answer) error
}

// QuestionCommand describes a question to create or replace.
type QuestionCommand struct {
	ActorID   uuid.UUID
	ContestID uuid.UUID
	// QuestionID is empty when creating.
	QuestionID  uuid.UUID
	Kind        string
	Points      int
	MaxAttempts *int
	// PenaltyPct is nil to leave the stored penalty alone on update (zero
	// means no penalty), and no penalty on create.
	PenaltyPct *int
	// IsVisible is nil for visible: hiding a question must be stated.
	IsVisible *bool
	ChoiceIDs []string
	// Texts, when given, replace the question's authored text.
	Texts map[string]QuestionText
}

// Questions returns the contest's questions in display order.
func (s *Service) Questions(ctx context.Context, contestID uuid.UUID) ([]Question, error) {
	return s.questions.List(ctx, contestID)
}

// Question returns one question of a contest.
func (s *Service) Question(ctx context.Context, contestID, questionID uuid.UUID) (Question, error) {
	return s.questionOf(ctx, contestID, questionID)
}

// questionOf loads a question and proves it belongs to the contest, since
// permission is granted per contest. Another contest's question is "not
// found", not "forbidden", so its existence is not disclosed.
func (s *Service) questionOf(ctx context.Context, contestID, questionID uuid.UUID) (Question, error) {
	q, err := s.questions.ByID(ctx, questionID)
	if err != nil {
		return Question{}, err
	}
	if q.ContestID != contestID {
		return Question{}, ErrQuestionNotFound
	}
	return q, nil
}

// AddQuestion appends a question to a contest.
func (s *Service) AddQuestion(ctx context.Context, cmd QuestionCommand) (Question, error) {
	c, err := s.editableContest(ctx, cmd.ContestID)
	if err != nil {
		return Question{}, err
	}

	q := Question{
		ContestID:   c.ID,
		Kind:        orDefault(cmd.Kind, KindText),
		Points:      cmd.Points,
		MaxAttempts: cmd.MaxAttempts,
		IsVisible:   cmd.IsVisible == nil || *cmd.IsVisible,
		ChoiceIDs:   cmd.ChoiceIDs,
	}
	if cmd.PenaltyPct != nil {
		q.PenaltyPct = *cmd.PenaltyPct
	}
	if err := q.Validate(); err != nil {
		return Question{}, err
	}
	if err := s.checkLanguageCodes(ctx, textLanguages(cmd.Texts)); err != nil {
		return Question{}, err
	}

	var created Question
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if created, err = s.questions.Create(ctx, q); err != nil {
			return err
		}
		if len(cmd.Texts) > 0 {
			if err := s.questions.ReplaceTexts(ctx, created.ID, cmd.Texts); err != nil {
				return err
			}
			created.Texts = cmd.Texts
		}
		return s.record(ctx, cmd.ActorID, audit.ActionQuestionCreate, c.ID, map[string]any{
			"question_id": created.ID.String(),
			"kind":        created.Kind,
		})
	})
	if err != nil {
		return Question{}, err
	}
	return created, nil
}

// UpdateQuestion replaces a question's own fields.
func (s *Service) UpdateQuestion(ctx context.Context, cmd QuestionCommand) (Question, error) {
	if _, err := s.editableContest(ctx, cmd.ContestID); err != nil {
		return Question{}, err
	}
	current, err := s.questionOf(ctx, cmd.ContestID, cmd.QuestionID)
	if err != nil {
		return Question{}, err
	}

	updated := current
	updated.Kind = orDefault(cmd.Kind, current.Kind)
	updated.Points = cmd.Points
	updated.MaxAttempts = cmd.MaxAttempts
	// A caller that does not send a penalty must not reset it.
	if cmd.PenaltyPct != nil {
		updated.PenaltyPct = *cmd.PenaltyPct
	}
	updated.ChoiceIDs = cmd.ChoiceIDs
	if cmd.IsVisible != nil {
		updated.IsVisible = *cmd.IsVisible
	}
	if err := updated.Validate(); err != nil {
		return Question{}, err
	}
	// New options must still cover the stored answers.
	if err := validateAnswersFor(updated, current.Answers); err != nil {
		return Question{}, err
	}

	payload := audit.Between(current.auditFields(), updated.auditFields()).Payload()
	payload["question_id"] = updated.ID.String()

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.questions.Update(ctx, updated); err != nil {
			return err
		}
		return s.record(ctx, cmd.ActorID, audit.ActionQuestionUpdate, cmd.ContestID, payload)
	})
	if err != nil {
		return Question{}, err
	}
	return updated, nil
}

// SaveQuestionCommand is a question as its author edits it: its own fields,
// its wording in every language and its reference answers, together.
type SaveQuestionCommand struct {
	ActorID     uuid.UUID
	ContestID   uuid.UUID
	QuestionID  uuid.UUID
	Kind        string
	Points      int
	MaxAttempts *int
	// PenaltyPct is nil to leave the stored penalty alone.
	PenaltyPct *int
	IsVisible  *bool
	ChoiceIDs  []string
	Texts      map[string]QuestionText
	Answers    []Answer
}

// SaveQuestion replaces a question whole: fields, texts and answers are
// validated together and written in one transaction. Only this way can a
// change that touches both, such as turning a text question into a choice
// one, check the new answers against the new question.
func (s *Service) SaveQuestion(ctx context.Context, cmd SaveQuestionCommand) (Question, error) {
	if _, err := s.editableContest(ctx, cmd.ContestID); err != nil {
		return Question{}, err
	}
	current, err := s.questionOf(ctx, cmd.ContestID, cmd.QuestionID)
	if err != nil {
		return Question{}, err
	}

	updated := current
	updated.Kind = orDefault(cmd.Kind, current.Kind)
	updated.Points = cmd.Points
	updated.MaxAttempts = cmd.MaxAttempts
	if cmd.PenaltyPct != nil {
		updated.PenaltyPct = *cmd.PenaltyPct
	}
	updated.ChoiceIDs = cmd.ChoiceIDs
	if cmd.IsVisible != nil {
		updated.IsVisible = *cmd.IsVisible
	}

	if err := updated.Validate(); err != nil {
		return Question{}, err
	}
	if err := validateAnswersFor(updated, cmd.Answers); err != nil {
		return Question{}, err
	}
	if err := s.checkLanguageCodes(ctx, textLanguages(cmd.Texts)); err != nil {
		return Question{}, err
	}

	payload := audit.Between(current.auditFields(), updated.auditFields()).Payload()
	payload["question_id"] = updated.ID.String()
	if len(cmd.Texts) > 0 {
		payload["languages"] = textLanguages(cmd.Texts)
	}

	answersMoved := !sameAnswers(current.Answers, cmd.Answers)

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.questions.Update(ctx, updated); err != nil {
			return err
		}
		if cmd.Texts != nil {
			if err := s.questions.ReplaceTexts(ctx, updated.ID, cmd.Texts); err != nil {
				return err
			}
			updated.Texts = cmd.Texts
		}
		if answersMoved {
			if err := s.questions.ReplaceAnswers(ctx, updated.ID, cmd.Answers); err != nil {
				return err
			}
			updated.Answers = cmd.Answers
		}

		if err := s.record(ctx, cmd.ActorID, audit.ActionQuestionUpdate, cmd.ContestID, payload); err != nil {
			return err
		}
		if !answersMoved {
			return nil
		}
		// A separate entry so answer changes can be filtered by action (§9);
		// only the count, never the values (§9.2).
		return s.record(ctx, cmd.ActorID, audit.ActionAnswersChange, cmd.ContestID, map[string]any{
			"question_id": updated.ID.String(),
			"count":       len(cmd.Answers),
		})
	})
	if err != nil {
		return Question{}, err
	}
	return updated, nil
}

// sameAnswers reports whether two sets of reference answers are the same, in
// the same order: a reorder is worth a trail entry even though it scores the
// same.
func sameAnswers(before, after []Answer) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i].MatchKind != after[i].MatchKind || before[i].Value != after[i].Value {
			return false
		}
	}
	return true
}

// DeleteQuestion removes a question from a contest.
func (s *Service) DeleteQuestion(ctx context.Context, actorID, contestID, questionID uuid.UUID) error {
	if _, err := s.editableContest(ctx, contestID); err != nil {
		return err
	}
	if _, err := s.questionOf(ctx, contestID, questionID); err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.questions.Delete(ctx, questionID); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionQuestionDelete, contestID, map[string]any{
			"question_id": questionID.String(),
		})
	})
}

// ReorderQuestions sets the display order. The list must name every question
// exactly once, or positions would be duplicated or missing.
func (s *Service) ReorderQuestions(ctx context.Context, actorID, contestID uuid.UUID, ordered []uuid.UUID) error {
	if _, err := s.editableContest(ctx, contestID); err != nil {
		return err
	}
	existing, err := s.questions.List(ctx, contestID)
	if err != nil {
		return err
	}
	if err := checkCompleteOrder(existing, ordered); err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.questions.Reorder(ctx, contestID, ordered); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionQuestionReorder, contestID, nil)
	})
}

// checkCompleteOrder reports an ordering that is not a permutation of the
// contest's questions.
func checkCompleteOrder(existing []Question, ordered []uuid.UUID) error {
	if len(ordered) != len(existing) {
		return fmt.Errorf("%w: the order must name all %d questions, got %d",
			ErrInvalidQuestion, len(existing), len(ordered))
	}

	known := make(map[uuid.UUID]struct{}, len(existing))
	for _, q := range existing {
		known[q.ID] = struct{}{}
	}
	seen := make(map[uuid.UUID]struct{}, len(ordered))
	for _, id := range ordered {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("%w: %s does not belong to this contest", ErrQuestionNotFound, id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: %s listed twice", ErrInvalidQuestion, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// SetQuestionTexts replaces a question's authored text.
func (s *Service) SetQuestionTexts(ctx context.Context, actorID, contestID, questionID uuid.UUID, texts map[string]QuestionText) error {
	if _, err := s.editableContest(ctx, contestID); err != nil {
		return err
	}
	if _, err := s.questionOf(ctx, contestID, questionID); err != nil {
		return err
	}
	if err := s.checkLanguageCodes(ctx, textLanguages(texts)); err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.questions.ReplaceTexts(ctx, questionID, texts); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionQuestionUpdate, contestID, map[string]any{
			"question_id": questionID.String(),
			"languages":   textLanguages(texts),
		})
	})
}

// SetAnswers replaces a question's reference answers.
func (s *Service) SetAnswers(ctx context.Context, actorID, contestID, questionID uuid.UUID, answers []Answer) error {
	if _, err := s.editableContest(ctx, contestID); err != nil {
		return err
	}
	q, err := s.questionOf(ctx, contestID, questionID)
	if err != nil {
		return err
	}
	if err := validateAnswersFor(q, answers); err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.questions.ReplaceAnswers(ctx, questionID, answers); err != nil {
			return err
		}
		// Only the count: the trail must not be a place to read answers (§9.2).
		return s.record(ctx, actorID, audit.ActionAnswersChange, contestID, map[string]any{
			"question_id": questionID.String(),
			"count":       len(answers),
		})
	})
}

// textLanguages lists the language codes a text map carries.
func textLanguages(texts map[string]QuestionText) []string {
	codes := make([]string, 0, len(texts))
	for code := range texts {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	return codes
}

// auditFields is the part of a question that may be written to the audit
// trail: neither the wording nor the reference answers (§9.2).
func (q Question) auditFields() map[string]any {
	return map[string]any{
		"kind":         q.Kind,
		"points":       q.Points,
		"max_attempts": q.MaxAttempts,
		"penalty_pct":  q.PenaltyPct,
		"is_visible":   q.IsVisible,
		"choice_ids":   q.ChoiceIDs,
	}
}

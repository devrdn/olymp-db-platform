package contests

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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

// maxChoices bounds a choice question. Every option is labelled in every
// language, so an unbounded list is unbounded work for the organizer and an
// unbounded payload for the participant.
const maxChoices = 50

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
	Ord  int
	Kind string
	// Points awarded for a correct answer.
	Points int
	// MaxAttempts is nil when the participant may keep trying.
	MaxAttempts *int
	// IsVisible decides whether the participant is shown the question text at
	// all. A hidden question still scores: working out what is being asked is
	// then part of the puzzle (see §6.1).
	IsVisible bool
	// ChoiceIDs are the stable, language-independent identifiers of a choice
	// question's options ("a", "b", …). A submission carries one of these,
	// never a label, so grading does not depend on the language the
	// participant read.
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

// Answer is one accepted response to a question.
//
// Several rows per question are normal, and that is how spelling variants are
// handled: the game database is English, so a translated story that
// transliterates a name simply adds another accepted spelling. Refusing it
// would score language rather than detection.
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
	if q.Points < 0 {
		return fmt.Errorf("%w: points must not be negative", ErrInvalidQuestion)
	}
	// Zero attempts is a question nobody can answer, which is never what was
	// meant; "unlimited" is expressed by leaving the field unset.
	if q.MaxAttempts != nil && *q.MaxAttempts <= 0 {
		return fmt.Errorf("%w: the attempt limit must be positive", ErrInvalidQuestion)
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
			// Duplicated identifiers make a submission ambiguous and the label
			// map lossy.
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

// Validate checks a reference answer.
//
// A regular expression is compiled here, at authoring time, rather than when
// it is first used. A pattern that fails to compile during a running contest
// would break grading for everybody who reached that question, at the one
// moment nobody can fix it.
func (a Answer) Validate() error {
	if strings.TrimSpace(a.Value) == "" {
		return fmt.Errorf("%w: the value must not be empty", ErrInvalidAnswer)
	}
	// The same bound a submitted answer carries (maxAnswerRunes,
	// submission.go): a reference answer is a name, a short phrase or a
	// choice identifier, never more than a sentence, and this is the one
	// place CLAUDE.md rule 2 asks the bound to live — every column this
	// value reaches is unbounded text, and nothing before storage otherwise
	// stops an authored regex pattern from being arbitrarily long (finding
	// 6). RE2 compiles in time linear in pattern length with no
	// backtracking blow-up, so this is a bound on storage and on staff
	// authoring effort, not a defence against a compilation attack.
	if utf8.RuneCountInString(a.Value) > maxAnswerRunes {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidAnswer, maxAnswerRunes)
	}
	if !slices.Contains([]string{MatchExact, MatchExactCI, MatchRegex}, a.MatchKind) {
		return fmt.Errorf("%w: unknown match kind %q", ErrInvalidAnswer, a.MatchKind)
	}
	if a.MatchKind == MatchRegex {
		if _, err := regexp.Compile(a.Value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAnswer, err)
		}
	}
	return nil
}

// validateAnswersFor checks a whole set of reference answers against the
// question they belong to.
func validateAnswersFor(q Question, answers []Answer) error {
	for _, a := range answers {
		if err := a.Validate(); err != nil {
			return err
		}
		// The answer to a choice question is one of its option identifiers.
		// Anything else authors an answer nobody can submit, and nobody finds
		// out until the contest is scored.
		if q.Kind == KindChoice && !q.HasChoice(a.Value) {
			return fmt.Errorf("%w: %q is not one of the question's options", ErrInvalidAnswer, a.Value)
		}
	}
	return nil
}

// QuestionRepository stores questions, their authored text and their reference
// answers.
//
// Every read here is a staff read and carries the reference answers with it.
// The participant-facing path does not reuse these methods: it asks
// VisibleQuestionRepository instead, which is a different query — one
// language, is_visible only, no reference answer selected at all — rather
// than this one with a flag.
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
	// IsVisible is a pointer so that "not stated" means visible. Hiding a
	// question is the deliberate choice, and the ordinary case must not depend
	// on remembering to say so.
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

// questionOf loads a question and proves it belongs to the contest.
//
// Permission is granted per contest, so every operation naming a question has
// to check this: without it, the owner of one contest could reach another's
// questions by guessing an identifier, and the permission check on the URL
// would have passed. The answer is "not found" rather than "forbidden", since
// the existence of another contest's question is itself not their business.
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
	updated.ChoiceIDs = cmd.ChoiceIDs
	if cmd.IsVisible != nil {
		updated.IsVisible = *cmd.IsVisible
	}
	if err := updated.Validate(); err != nil {
		return Question{}, err
	}
	// Changing the options out from under the reference answers would leave
	// answers nobody can submit, which is the same failure the answer check
	// exists to prevent.
	if err := validateAnswersFor(updated, current.Answers); err != nil {
		return Question{}, err
	}

	// Which question, and what was done to it. The identifier alone answered
	// only the first half, which is not the half anybody asks about.
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
	IsVisible   *bool
	ChoiceIDs   []string
	Texts       map[string]QuestionText
	Answers     []Answer
}

// SaveQuestion replaces a question whole, in one transaction.
//
// The three narrower operations remain, and each is still useful on its own —
// fixing a reference answer without reopening the wording, for instance. What
// they could not do is the change that touches more than one of them at once,
// and that was not merely awkward:
//
// Converting a text question into a choice question was impossible. Updating
// the question checked the *existing* answers against the *new* kind and
// refused; saving the answers checked the *new* answers against the *old* kind
// and refused. Whichever way round an author went, one half of the change
// rejected the other. Here both halves are known at once, so the answers are
// checked against the question as it will be.
//
// Everything is validated before anything is written, and the writes share one
// transaction — so a save either lands whole or leaves the question exactly as
// it was. Three requests from a browser could not promise that: the second was
// free to fail after the first had committed, leaving a half-saved question
// under a button that had already said "saved".
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
	updated.ChoiceIDs = cmd.ChoiceIDs
	if cmd.IsVisible != nil {
		updated.IsVisible = *cmd.IsVisible
	}

	if err := updated.Validate(); err != nil {
		return Question{}, err
	}
	// Against the question as it will be, which is the whole point.
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

	// Whether the reference answers actually moved. Compared before the write,
	// because afterwards there is nothing left to compare against.
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
		// Its own line, in the same transaction. "Who changed the reference
		// answers after publication" is a question the trail is built to
		// answer with an indexed filter (section 9), and folding this into the
		// entry above would take that away. The values never appear — only how
		// many there are (section 9.2).
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
// the same order.
//
// Order counts because it is what an author sees, and reordering is a change
// worth a line in the trail even though it scores identically.
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

// ReorderQuestions sets the display order.
//
// The list must name every question exactly once: a partial one would leave
// positions duplicated or missing, and the participant's view is built from
// exactly that order.
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
		// The values are the answers themselves and never reach the trail: an
		// audit log readable by staff must not be a place to look them up.
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
// trail. The wording and the reference answers are not in it: the first is
// authored text, and the second must never become something the trail can be
// read for (§9.2).
func (q Question) auditFields() map[string]any {
	return map[string]any{
		"kind":         q.Kind,
		"points":       q.Points,
		"max_attempts": q.MaxAttempts,
		"is_visible":   q.IsVisible,
		"choice_ids":   q.ChoiceIDs,
	}
}

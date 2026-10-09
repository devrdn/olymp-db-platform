package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Questions implements contests.QuestionRepository and
// contests.VisibleQuestionRepository, with separate queries (see ForContest).
var (
	_ contests.QuestionRepository        = (*Questions)(nil)
	_ contests.VisibleQuestionRepository = (*Questions)(nil)
)

// questionColumns is the staff projection, reference answers included.
// Participants are served by ForContest, which never reads the answers.
const questionColumns = `
	q.id, q.contest_id, q.ord, q.kind, q.points, q.max_attempts, q.penalty_pct, q.is_visible, q.choice_ids,
	COALESCE((
		SELECT json_object_agg(qt.lang, json_build_object('body_md', qt.body_md,
		                                                  'choices', COALESCE(qt.choices, '{}'::jsonb)))
		FROM question_translations qt WHERE qt.question_id = q.id
	), '{}'::json),
	COALESCE((
		SELECT json_agg(json_build_object('id', qa.id, 'match_kind', qa.match_kind, 'value', qa.value)
		                ORDER BY qa.value)
		FROM question_answers qa WHERE qa.question_id = q.id
	), '[]'::json)`

// Questions stores questions, their authored text and their reference answers.
type Questions struct {
	pool *pgxpool.Pool
}

// NewQuestions returns the question repository.
func NewQuestions(pool *pgxpool.Pool) *Questions {
	return &Questions{pool: pool}
}

func (r *Questions) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// questionTextRow and answerRow are the JSON shapes questionColumns produces.
type questionTextRow struct {
	BodyMD  string            `json:"body_md"`
	Choices map[string]string `json:"choices"`
}

type answerRow struct {
	ID        uuid.UUID `json:"id"`
	MatchKind string    `json:"match_kind"`
	Value     string    `json:"value"`
}

func scanQuestion(row pgx.Row) (contests.Question, error) {
	var (
		q       contests.Question
		texts   []byte
		answers []byte
	)
	err := row.Scan(&q.ID, &q.ContestID, &q.Ord, &q.Kind, &q.Points, &q.MaxAttempts, &q.PenaltyPct,
		&q.IsVisible, &q.ChoiceIDs, &texts, &answers)
	if errors.Is(err, pgx.ErrNoRows) {
		return contests.Question{}, contests.ErrQuestionNotFound
	}
	if err != nil {
		return contests.Question{}, fmt.Errorf("scan question: %w", err)
	}
	return hydrateQuestion(q, texts, answers)
}

func hydrateQuestion(q contests.Question, texts, answers []byte) (contests.Question, error) {
	var byLang map[string]questionTextRow
	if err := json.Unmarshal(texts, &byLang); err != nil {
		return contests.Question{}, fmt.Errorf("decode question translations: %w", err)
	}
	q.Texts = make(map[string]contests.QuestionText, len(byLang))
	for lang, text := range byLang {
		q.Texts[lang] = contests.QuestionText{BodyMD: text.BodyMD, Choices: text.Choices}
	}

	var rows []answerRow
	if err := json.Unmarshal(answers, &rows); err != nil {
		return contests.Question{}, fmt.Errorf("decode reference answers: %w", err)
	}
	q.Answers = make([]contests.Answer, 0, len(rows))
	for _, a := range rows {
		q.Answers = append(q.Answers, contests.Answer{
			ID: a.ID, QuestionID: q.ID, MatchKind: a.MatchKind, Value: a.Value,
		})
	}
	return q, nil
}

// List returns the contest's questions in display order.
func (r *Questions) List(ctx context.Context, contestID uuid.UUID) ([]contests.Question, error) {
	rows, err := r.querier(ctx).Query(ctx,
		`SELECT `+questionColumns+` FROM questions q WHERE q.contest_id = $1 ORDER BY q.ord`, contestID)
	if err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	defer rows.Close()

	var found []contests.Question
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	return found, nil
}

// ForContest implements contests.VisibleQuestionRepository: the visible
// questions in one language, for participants. The statement never names
// question_answers, so reference answers cannot reach a participant through
// it, and hidden questions and other languages never leave the database.
//
// A question with no translation in lang is dropped by the inner join rather
// than returned with an empty body, as Story answers ErrStoryNotFound.
func (r *Questions) ForContest(ctx context.Context, contestID uuid.UUID, lang string) ([]contests.VisibleQuestion, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT q.id, q.kind, q.points, q.max_attempts, q.choice_ids,
		       qt.body_md, COALESCE(qt.choices, '{}'::jsonb)
		FROM questions q
		JOIN question_translations qt ON qt.question_id = q.id AND qt.lang = $2
		WHERE q.contest_id = $1 AND q.is_visible
		ORDER BY q.ord`, contestID, lang)
	if err != nil {
		return nil, fmt.Errorf("list visible questions: %w", err)
	}
	defer rows.Close()

	var found []contests.VisibleQuestion
	for rows.Next() {
		var (
			q       contests.VisibleQuestion
			choices []byte
		)
		if err := rows.Scan(&q.ID, &q.Kind, &q.Points, &q.MaxAttempts, &q.ChoiceIDs, &q.BodyMD, &choices); err != nil {
			return nil, fmt.Errorf("scan visible question: %w", err)
		}
		if err := json.Unmarshal(choices, &q.Choices); err != nil {
			return nil, fmt.Errorf("decode choice labels: %w", err)
		}
		found = append(found, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list visible questions: %w", err)
	}
	return found, nil
}

// ByID returns one question.
func (r *Questions) ByID(ctx context.Context, questionID uuid.UUID) (contests.Question, error) {
	return scanQuestion(r.querier(ctx).QueryRow(ctx,
		`SELECT `+questionColumns+` FROM questions q WHERE q.id = $1`, questionID))
}

// Create appends a question at the next free position. A missing contest is
// contests.ErrNotFound, reported by lockContest; once locked, the contest
// cannot be deleted before the insert.
func (r *Questions) Create(ctx context.Context, q contests.Question) (contests.Question, error) {
	querier := r.querier(ctx)

	// Locking the contest row serialises MAX(ord) allocation; otherwise two
	// concurrent authors read the same MAX and one insert fails the unique
	// (contest_id, ord). The lock is a separate statement: under READ
	// COMMITTED each statement takes its own snapshot, and a MAX folded into
	// the locking statement would read the snapshot from before the wait.
	if err := lockContest(ctx, querier, q.ContestID); err != nil {
		return contests.Question{}, err
	}

	return scanQuestion(querier.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO questions (contest_id, ord, kind, points, max_attempts, penalty_pct, is_visible, choice_ids)
			VALUES ($1,
			        (SELECT COALESCE(MAX(ord), 0) + 1 FROM questions WHERE contest_id = $1),
			        $2, $3, $4, $5, $6, $7)
			RETURNING *
		)
		SELECT `+questionColumns+` FROM inserted q`,
		q.ContestID, q.Kind, q.Points, q.MaxAttempts, q.PenaltyPct, q.IsVisible, stringList(q.ChoiceIDs)))
}

// lockContest locks the contest row to serialise position allocation within
// one contest, and reports contests.ErrNotFound when there is no row. It must
// run inside a transaction, or the lock is gone before the INSERT.
func lockContest(ctx context.Context, q storage.Querier, contestID uuid.UUID) error {
	if !storage.InTx(ctx) {
		return errors.New("adding a question must run inside a transaction")
	}
	tag, err := q.Exec(ctx, `SELECT id FROM contests WHERE id = $1 FOR UPDATE`, contestID)
	if err != nil {
		return fmt.Errorf("lock contest for a new question: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrNotFound
	}
	return nil
}

// Update saves a question's own fields. Position, text and answers each have
// their own operation.
func (r *Questions) Update(ctx context.Context, q contests.Question) error {
	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE questions
		SET kind = $2, points = $3, max_attempts = $4, penalty_pct = $5, is_visible = $6, choice_ids = $7
		WHERE id = $1`,
		q.ID, q.Kind, q.Points, q.MaxAttempts, q.PenaltyPct, q.IsVisible, stringList(q.ChoiceIDs))
	if err != nil {
		return fmt.Errorf("update question: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrQuestionNotFound
	}
	return nil
}

// Delete removes a question and closes the gap it leaves in the ordering.
func (r *Questions) Delete(ctx context.Context, questionID uuid.UUID) error {
	if err := deferQuestionOrder(ctx, r.querier(ctx)); err != nil {
		return err
	}

	// One statement, so no reader sees the gap. Existence is counted from
	// removed: the renumbering touches nothing when the last question goes.
	var deleted int
	err := r.querier(ctx).QueryRow(ctx, `
		WITH removed AS (
			DELETE FROM questions WHERE id = $1 RETURNING contest_id, ord
		), renumbered AS (
			UPDATE questions q
			SET ord = q.ord - 1
			FROM removed
			WHERE q.contest_id = removed.contest_id AND q.ord > removed.ord
			RETURNING q.id
		)
		SELECT count(*) FROM removed`, questionID).Scan(&deleted)
	if err != nil {
		return fmt.Errorf("delete question: %w", err)
	}
	if deleted == 0 {
		return contests.ErrQuestionNotFound
	}
	return nil
}

// Reorder sets the display order to exactly this sequence.
func (r *Questions) Reorder(ctx context.Context, contestID uuid.UUID, ordered []uuid.UUID) error {
	if err := deferQuestionOrder(ctx, r.querier(ctx)); err != nil {
		return err
	}

	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE questions q
		SET ord = wanted.position
		FROM (
			SELECT id, position
			FROM unnest($2::uuid[]) WITH ORDINALITY AS t(id, position)
		) AS wanted
		WHERE q.id = wanted.id AND q.contest_id = $1`, contestID, ordered)
	if err != nil {
		return fmt.Errorf("reorder questions: %w", err)
	}
	if int(tag.RowsAffected()) != len(ordered) {
		return fmt.Errorf("%w: %d of %d questions belong to this contest",
			contests.ErrQuestionNotFound, tag.RowsAffected(), len(ordered))
	}
	return nil
}

// deferQuestionOrder defers the unique (contest_id, ord) check to commit,
// since mid-swap two questions share a position. SET CONSTRAINTS is ignored
// outside a transaction, so it refuses to run there.
func deferQuestionOrder(ctx context.Context, q storage.Querier) error {
	if !storage.InTx(ctx) {
		return errors.New("reordering questions must run inside a transaction")
	}
	if _, err := q.Exec(ctx, `SET CONSTRAINTS questions_contest_id_ord_key DEFERRED`); err != nil {
		return fmt.Errorf("defer the question ordering constraint: %w", err)
	}
	return nil
}

// ReplaceTexts sets the question's authored text to exactly these languages.
func (r *Questions) ReplaceTexts(ctx context.Context, questionID uuid.UUID, texts map[string]contests.QuestionText) error {
	langs := make([]string, 0, len(texts))
	bodies := make([]string, 0, len(texts))
	choices := make([][]byte, 0, len(texts))
	for lang, text := range texts {
		encoded, err := json.Marshal(orEmpty(text.Choices))
		if err != nil {
			return fmt.Errorf("encode choice labels: %w", err)
		}
		langs = append(langs, lang)
		bodies = append(bodies, text.BodyMD)
		choices = append(choices, encoded)
	}

	const stale = `DELETE FROM question_translations WHERE question_id = $1 AND NOT (lang = ANY($2))`
	if len(langs) == 0 {
		exists, err := clearChildren(ctx, r.querier(ctx), stale, "questions", questionID, langs)
		if err != nil {
			return fmt.Errorf("replace question translations: %w", err)
		}
		if !exists {
			return contests.ErrQuestionNotFound
		}
		return nil
	}
	if _, err := r.querier(ctx).Exec(ctx, stale, questionID, langs); err != nil {
		return fmt.Errorf("replace question translations: %w", err)
	}

	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO question_translations (question_id, lang, body_md, choices)
		SELECT $1, lang, body, choices::jsonb
		FROM unnest($2::text[], $3::text[], $4::text[]) AS t(lang, body, choices)
		ON CONFLICT (question_id, lang) DO UPDATE
		SET body_md = EXCLUDED.body_md, choices = EXCLUDED.choices, updated_at = now()`,
		questionID, langs, bodies, choices)
	if err != nil {
		// A question deleted meanwhile trips the foreign key.
		return fmt.Errorf("replace question translations: %w", missingParent(err, map[string]error{
			"question_translations_question_id_fkey": contests.ErrQuestionNotFound,
		}))
	}
	return nil
}

// ReplaceAnswers sets the reference answers to exactly these, by delete and
// reinsert: nothing outside the table refers to an answer row.
func (r *Questions) ReplaceAnswers(ctx context.Context, questionID uuid.UUID, answers []contests.Answer) error {
	const all = `DELETE FROM question_answers WHERE question_id = $1`
	if len(answers) == 0 {
		exists, err := clearChildren(ctx, r.querier(ctx), all, "questions", questionID)
		if err != nil {
			return fmt.Errorf("replace reference answers: %w", err)
		}
		if !exists {
			return contests.ErrQuestionNotFound
		}
		return nil
	}
	if _, err := r.querier(ctx).Exec(ctx, all, questionID); err != nil {
		return fmt.Errorf("replace reference answers: %w", err)
	}

	kinds := make([]string, 0, len(answers))
	values := make([]string, 0, len(answers))
	for _, a := range answers {
		kinds = append(kinds, a.MatchKind)
		values = append(values, a.Value)
	}

	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO question_answers (question_id, match_kind, value)
		SELECT $1, kind, value FROM unnest($2::text[], $3::text[]) AS t(kind, value)`,
		questionID, kinds, values)
	if err != nil {
		return fmt.Errorf("replace reference answers: %w", missingParent(err, map[string]error{
			"question_answers_question_id_fkey": contests.ErrQuestionNotFound,
		}))
	}
	return nil
}

// stringList keeps a nil slice out of a NOT NULL array column.
func stringList(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func orEmpty(choices map[string]string) map[string]string {
	if choices == nil {
		return map[string]string{}
	}
	return choices
}

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

// Questions implements contests.QuestionRepository.
var _ contests.QuestionRepository = (*Questions)(nil)

// questionColumns is the projection every question read shares.
//
// The reference answers ride along because every read here is a staff read.
// The participant-facing path does not reuse this query: it needs a
// language-resolved projection with the answers absent by construction.
const questionColumns = `
	q.id, q.contest_id, q.ord, q.kind, q.points, q.max_attempts, q.is_visible, q.choice_ids,
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

// questionTextRow and answerRow are the JSON shapes the projection produces.
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
	err := row.Scan(&q.ID, &q.ContestID, &q.Ord, &q.Kind, &q.Points, &q.MaxAttempts,
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
		var (
			q       contests.Question
			texts   []byte
			answers []byte
		)
		if err := rows.Scan(&q.ID, &q.ContestID, &q.Ord, &q.Kind, &q.Points, &q.MaxAttempts,
			&q.IsVisible, &q.ChoiceIDs, &texts, &answers); err != nil {
			return nil, fmt.Errorf("scan question: %w", err)
		}
		hydrated, err := hydrateQuestion(q, texts, answers)
		if err != nil {
			return nil, err
		}
		found = append(found, hydrated)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	return found, nil
}

// ByID returns one question.
func (r *Questions) ByID(ctx context.Context, questionID uuid.UUID) (contests.Question, error) {
	return scanQuestion(r.querier(ctx).QueryRow(ctx,
		`SELECT `+questionColumns+` FROM questions q WHERE q.id = $1`, questionID))
}

// Create appends a question, assigning it the next free position.
//
// The position is computed in the statement rather than read first and written
// back: two organizers adding a question at the same moment would otherwise
// both read the same number, and one of the inserts would fail.
func (r *Questions) Create(ctx context.Context, q contests.Question) (contests.Question, error) {
	return scanQuestion(r.querier(ctx).QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO questions (contest_id, ord, kind, points, max_attempts, is_visible, choice_ids)
			VALUES ($1,
			        (SELECT COALESCE(MAX(ord), 0) + 1 FROM questions WHERE contest_id = $1),
			        $2, $3, $4, $5, $6)
			RETURNING *
		)
		SELECT `+questionColumns+` FROM inserted q`,
		q.ContestID, q.Kind, q.Points, q.MaxAttempts, q.IsVisible, stringList(q.ChoiceIDs)))
}

// Update saves a question's own fields. Position, text and answers each have
// their own operation.
func (r *Questions) Update(ctx context.Context, q contests.Question) error {
	tag, err := r.querier(ctx).Exec(ctx, `
		UPDATE questions
		SET kind = $2, points = $3, max_attempts = $4, is_visible = $5, choice_ids = $6
		WHERE id = $1`,
		q.ID, q.Kind, q.Points, q.MaxAttempts, q.IsVisible, stringList(q.ChoiceIDs))
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

	// One statement: the delete and the renumbering of everything after it are
	// the same change, and a reader between the two would see a hole.
	tag, err := r.querier(ctx).Exec(ctx, `
		WITH removed AS (
			DELETE FROM questions WHERE id = $1 RETURNING contest_id, ord
		)
		UPDATE questions q
		SET ord = q.ord - 1
		FROM removed
		WHERE q.contest_id = removed.contest_id AND q.ord > removed.ord`, questionID)
	if err != nil {
		return fmt.Errorf("delete question: %w", err)
	}
	// The UPDATE touches no rows when the deleted question was the last one,
	// so its count says nothing about whether the question existed.
	_ = tag
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

// deferQuestionOrder moves the uniqueness check on (contest_id, ord) to the
// end of the transaction.
//
// Halfway through a swap two questions hold the same position, which is not a
// state an immediate check tolerates and not one that can be avoided. Outside
// a transaction SET CONSTRAINTS is silently ignored, so this refuses rather
// than proceeding on an assumption that does not hold — the alternative is an
// operation that works or fails depending on the order rows happen to be
// visited.
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

	if _, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM question_translations WHERE question_id = $1 AND NOT (lang = ANY($2))`,
		questionID, langs); err != nil {
		return fmt.Errorf("replace question translations: %w", err)
	}
	if len(langs) == 0 {
		return nil
	}

	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO question_translations (question_id, lang, body_md, choices)
		SELECT $1, lang, body, choices::jsonb
		FROM unnest($2::text[], $3::text[], $4::text[]) AS t(lang, body, choices)
		ON CONFLICT (question_id, lang) DO UPDATE
		SET body_md = EXCLUDED.body_md, choices = EXCLUDED.choices, updated_at = now()`,
		questionID, langs, bodies, choices)
	if err != nil {
		return fmt.Errorf("replace question translations: %w", err)
	}
	return nil
}

// ReplaceAnswers sets the reference answers to exactly these.
//
// Delete and reinsert rather than a diff: a stale reference answer would keep
// accepting something the organizer has already decided is wrong, and the
// rows carry no identity anybody outside this table refers to.
func (r *Questions) ReplaceAnswers(ctx context.Context, questionID uuid.UUID, answers []contests.Answer) error {
	if _, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM question_answers WHERE question_id = $1`, questionID); err != nil {
		return fmt.Errorf("replace reference answers: %w", err)
	}
	if len(answers) == 0 {
		return nil
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
		return fmt.Errorf("replace reference answers: %w", err)
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

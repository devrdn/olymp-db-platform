package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Participant finds one registration of the contest, by primary key. Another
// contest's registration is the same answer as none at all:
// monitor.ErrParticipantNotFound.
func (w *Watch) Participant(ctx context.Context, contest, registration uuid.UUID) (monitor.Participant, error) {
	var p monitor.Participant
	err := w.querier(ctx).QueryRow(ctx, `
		SELECT r.id, r.contest_id, r.user_id, u.login, u.full_name, r.status, r.started_at, r.finished_at
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.id = $1 AND r.contest_id = $2`, registration, contest).
		Scan(&p.Registration, &p.Contest, &p.User, &p.Login, &p.FullName, &p.Status, &p.StartedAt, &p.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return monitor.Participant{}, monitor.ErrParticipantNotFound
	}
	if err != nil {
		return monitor.Participant{}, fmt.Errorf("find participant %s: %w", registration, err)
	}
	return p, nil
}

// Queries reads one page of a participant's queries, newest first, whole.
//
// One range of query_log (registration_id, executed_at), past the cursor,
// with the status and the search applied as filters inside it. The search is
// a substring match without case (ILIKE, the text escaped by escapeLike so a
// % or _ the organiser typed is text: CLAUDE.md rule 3) rather than the GIN
// full-text index: the registration's own range is already the narrowest
// index there is, and a word match would not find "suspects" in
// "suspects.name". Bounded by the registration's rows and by
// monitor.MaxQuerySearchRunes.
func (w *Watch) Queries(ctx context.Context, q monitor.QueriesQuery) (monitor.QueriesPage, error) {
	q, err := q.Normalize()
	if err != nil {
		return monitor.QueriesPage{}, err
	}
	var a args
	where := "q.registration_id = " + a.add(q.Registration)
	if q.Status != "" {
		where += " AND q.status = " + a.add(q.Status)
	}
	if q.Search != "" {
		where += " AND q.sql_text ILIKE '%' || " + a.add(escapeLike(q.Search)) + " || '%'"
	}
	if q.Before != nil {
		id, err := strconv.ParseInt(q.Before.ID, 10, 64)
		if err != nil {
			return monitor.QueriesPage{}, monitor.ErrInvalidQueryFilter
		}
		where += fmt.Sprintf(" AND (q.executed_at, q.id) < (%s, %s)", a.add(q.Before.At), a.add(id))
	}
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT q.id, q.executed_at, q.sql_text, false, q.status, COALESCE(q.error_text, ''),
		       q.duration_ms, q.row_count, COALESCE(host(q.ip), '')
		FROM query_log q
		WHERE `+where+`
		ORDER BY q.executed_at DESC, q.id DESC
		LIMIT `+a.add(q.Limit+1), a...)
	if err != nil {
		return monitor.QueriesPage{}, fmt.Errorf("read the queries of %s: %w", q.Registration, err)
	}
	items, err := pgx.CollectRows(rows, scanLoggedQuery)
	if err != nil {
		return monitor.QueriesPage{}, fmt.Errorf("read the queries of %s: %w", q.Registration, err)
	}
	page := monitor.QueriesPage{Items: items}
	if len(items) > q.Limit {
		page.Items, page.More = items[:q.Limit], true
	}
	if page.Items == nil {
		page.Items = []monitor.LoggedQuery{}
	}
	return page, nil
}

func scanLoggedQuery(row pgx.CollectableRow) (monitor.LoggedQuery, error) {
	var item monitor.LoggedQuery
	err := row.Scan(&item.ID, &item.At, &item.SQL, &item.SQLTruncated, &item.Status, &item.Error,
		&item.DurationMs, &item.RowCount, &item.IP)
	return item, err
}

// Answers reads every attempt of a participant with, for each, at most
// perAttempt of the queries that led to it (design §3) and how many more
// there were.
//
// One statement. The attempts are the registration's range of submissions
// (registration_id, submitted_at), each with the time of the one before it
// on any question; each attempt's window is then a range of query_log
// (registration_id, executed_at) — counted, and read up to perAttempt rows.
// The statements are cut to queryrunner.MaxHistorySQLChars as the history
// page cuts them (the queries tab has them whole), so the bytes that reach
// this process are bounded by attempts × perAttempt × that (CLAUDE.md rule
// 12).
func (w *Watch) Answers(ctx context.Context, contest, registration uuid.UUID, perAttempt int) (monitor.Answers, error) {
	rows, err := w.querier(ctx).Query(ctx, `
		WITH attempts AS (
		    SELECT s.id, s.question_id, COALESCE(qu.ord, 0) AS ord, s.attempt_no, s.value, s.is_correct,
		           s.points_awarded, s.submitted_at,
		           lag(s.submitted_at) OVER (ORDER BY s.submitted_at, s.id) AS previous
		    FROM submissions s
		    LEFT JOIN questions qu ON qu.id = s.question_id AND qu.contest_id = $2
		    WHERE s.registration_id = $1
		    ORDER BY s.submitted_at, s.id
		    LIMIT $3
		)
		SELECT a.id, a.question_id, a.ord, a.attempt_no, a.value, a.is_correct, a.points_awarded, a.submitted_at,
		       n.total,
		       q.id, q.executed_at, left(q.sql_text, $5), char_length(q.sql_text) > $5, q.status,
		       COALESCE(q.error_text, ''), q.duration_ms, q.row_count, COALESCE(host(q.ip), '')
		FROM attempts a
		CROSS JOIN LATERAL (
		    SELECT count(*) AS total FROM query_log q
		    WHERE q.registration_id = $1 AND q.executed_at < a.submitted_at
		      AND (a.previous IS NULL OR q.executed_at >= a.previous)
		) n
		LEFT JOIN LATERAL (
		    SELECT q.* FROM query_log q
		    WHERE q.registration_id = $1 AND q.executed_at < a.submitted_at
		      AND (a.previous IS NULL OR q.executed_at >= a.previous)
		    ORDER BY q.executed_at, q.id
		    LIMIT $4
		) q ON true
		ORDER BY a.submitted_at, a.id, q.executed_at, q.id`,
		registration, contest, monitor.MaxAnswerAttempts+1, perAttempt, queryrunner.MaxHistorySQLChars)
	if err != nil {
		return monitor.Answers{}, fmt.Errorf("read the answers of %s: %w", registration, err)
	}
	defer rows.Close()

	var attempts []monitor.Attempt
	for rows.Next() {
		var (
			a     monitor.Attempt
			total int
			id    *int64
			q     monitor.LoggedQuery
			at    *time.Time
		)
		var (
			sql       *string
			truncated *bool
			status    *string
			errText   *string
			ip        *string
		)
		if err := rows.Scan(&a.ID, &a.QuestionID, &a.QuestionOrd, &a.AttemptNo, &a.Value, &a.Correct,
			&a.PointsAwarded, &a.At, &total,
			&id, &at, &sql, &truncated, &status, &errText, &q.DurationMs, &q.RowCount, &ip); err != nil {
			return monitor.Answers{}, fmt.Errorf("scan an answer: %w", err)
		}
		if n := len(attempts); n == 0 || attempts[n-1].ID != a.ID {
			a.MoreQueries = total
			a.Queries = []monitor.LoggedQuery{}
			attempts = append(attempts, a)
		}
		if id == nil {
			continue
		}
		q.ID, q.At, q.SQL, q.SQLTruncated, q.Status, q.Error, q.IP = *id, *at, *sql, *truncated, *status, *errText, *ip
		last := &attempts[len(attempts)-1]
		last.Queries = append(last.Queries, q)
		last.MoreQueries--
	}
	if err := rows.Err(); err != nil {
		return monitor.Answers{}, fmt.Errorf("read the answers of %s: %w", registration, err)
	}
	out := monitor.Answers{}
	if len(attempts) > monitor.MaxAnswerAttempts {
		attempts, out.Truncated = attempts[:monitor.MaxAnswerAttempts], true
	}
	out.Questions = monitor.GroupAttempts(attempts)
	if out.Questions == nil {
		out.Questions = []monitor.QuestionAttempts{}
	}
	return out, nil
}

// Workspace reads the participant's notes and tabs as they are, and the list
// of their revisions, newest first, without bodies. Unlike the participant's
// own load it creates nothing: an organiser looking must not change what they
// look at.
func (w *Watch) Workspace(ctx context.Context, registration uuid.UUID) (monitor.Workspace, error) {
	querier := w.querier(ctx)
	var ws monitor.Workspace
	err := querier.QueryRow(ctx,
		`SELECT body, updated_at FROM participant_notes WHERE registration_id = $1`, registration).
		Scan(&ws.Notes.Body, &ws.Notes.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return monitor.Workspace{}, fmt.Errorf("read the notes of %s: %w", registration, err)
	}

	rows, err := querier.Query(ctx, `
		SELECT id, title, position, body, updated_at
		FROM participant_sql_tabs
		WHERE registration_id = $1
		ORDER BY position, id`, registration)
	if err != nil {
		return monitor.Workspace{}, fmt.Errorf("read the tabs of %s: %w", registration, err)
	}
	ws.Tabs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitor.Tab, error) {
		var tab monitor.Tab
		err := row.Scan(&tab.ID, &tab.Title, &tab.Position, &tab.Body, &tab.UpdatedAt)
		return tab, err
	})
	if err != nil {
		return monitor.Workspace{}, fmt.Errorf("read the tabs of %s: %w", registration, err)
	}

	// The registration's range of workspace_revisions (registration_id,
	// document, id), sorted by id: at most two revisions a minute per
	// document, and cut at MaxRevisionsListed.
	rows, err = querier.Query(ctx, `
		SELECT id, document, COALESCE(title, ''), started_at, updated_at, octet_length(body)
		FROM workspace_revisions
		WHERE registration_id = $1
		ORDER BY id DESC
		LIMIT $2`, registration, monitor.MaxRevisionsListed+1)
	if err != nil {
		return monitor.Workspace{}, fmt.Errorf("list the revisions of %s: %w", registration, err)
	}
	ws.Revisions, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitor.RevisionInfo, error) {
		var r monitor.RevisionInfo
		err := row.Scan(&r.ID, &r.Document, &r.Title, &r.StartedAt, &r.UpdatedAt, &r.Size)
		return r, err
	})
	if err != nil {
		return monitor.Workspace{}, fmt.Errorf("list the revisions of %s: %w", registration, err)
	}
	if len(ws.Revisions) > monitor.MaxRevisionsListed {
		ws.Revisions, ws.Truncated = ws.Revisions[:monitor.MaxRevisionsListed], true
	}
	if ws.Tabs == nil {
		ws.Tabs = []monitor.Tab{}
	}
	if ws.Revisions == nil {
		ws.Revisions = []monitor.RevisionInfo{}
	}
	return ws, nil
}

// Revision reads one revision of the participant, whole. Another
// participant's revision is monitor.ErrRevisionNotFound.
func (w *Watch) Revision(ctx context.Context, registration uuid.UUID, id int64) (monitor.RevisionBody, error) {
	var r monitor.RevisionBody
	err := w.querier(ctx).QueryRow(ctx, `
		SELECT id, document, COALESCE(title, ''), started_at, updated_at, octet_length(body), body
		FROM workspace_revisions
		WHERE id = $1 AND registration_id = $2`, id, registration).
		Scan(&r.ID, &r.Document, &r.Title, &r.StartedAt, &r.UpdatedAt, &r.Size, &r.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return monitor.RevisionBody{}, monitor.ErrRevisionNotFound
	}
	if err != nil {
		return monitor.RevisionBody{}, fmt.Errorf("read revision %d: %w", id, err)
	}
	return r, nil
}

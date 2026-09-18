package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Feed reads one page of the feed (design §4): each source as its own index
// range past the cursor, in the page's direction, with at most Limit+1 rows,
// merged by monitor.MergeFeed.
//
// The whole contest's query log and answers are read per registration
// (LATERAL over the contest's registrations), each registration its own
// range of query_log (registration_id, executed_at) or submissions
// (registration_id, submitted_at) with its own LIMIT: neither table carries
// the contest, and a range per registration is what keeps an old contest's
// feed from walking every query anybody ran since. participant_events carries
// the contest and is one range of its own index (migration 000033).
//
// Sign-ins and sign-outs are the participant's account's own audit entries
// (audit_log (actor_id, created_at)), failed sign-ins the entries naming its
// login (audit_log_failed_login_idx), both only since the registration was
// created: an account's sign-ins before it joined the contest are not the
// contest's business. Disqualifications are the contest's own entries
// (audit_log_entity_idx).
func (w *Watch) Feed(ctx context.Context, q monitor.FeedQuery) (monitor.FeedPage, error) {
	q, err := q.Normalize()
	if err != nil {
		return monitor.FeedPage{}, err
	}
	var items []monitor.FeedItem
	for source := monitor.SourceAudit; source <= monitor.SourceFinish; source++ {
		if !q.Reads(source) {
			continue
		}
		found, err := w.readSource(ctx, q, source)
		if err != nil {
			return monitor.FeedPage{}, err
		}
		items = append(items, found...)
	}
	page := monitor.MergeFeed(q, items)
	if err := w.name(ctx, q.Contest, page.Items); err != nil {
		return monitor.FeedPage{}, err
	}
	return page, nil
}

// FeedSource reads one source of the feed past q's cursor, in q's
// direction, at most q.Limit+1 items, named: what monitor.StreamFeed merges
// an export from, one source at a time.
func (w *Watch) FeedSource(ctx context.Context, q monitor.FeedQuery, source monitor.Source) ([]monitor.FeedItem, error) {
	q, err := q.Normalize()
	if err != nil {
		return nil, err
	}
	items, err := w.readSource(ctx, q, source)
	if err != nil {
		return nil, err
	}
	if err := w.name(ctx, q.Contest, items); err != nil {
		return nil, err
	}
	return items, nil
}

// FeedRegistrations lists the contest's registrations, for a contest-wide
// stream to read its per-registration sources by. Bounded like the
// participants table: a contest past monitor.MaxRosterRows is refused rather
// than streamed with some of its participants missing.
func (w *Watch) FeedRegistrations(ctx context.Context, contest uuid.UUID) ([]uuid.UUID, error) {
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT id FROM registrations WHERE contest_id = $1 ORDER BY id LIMIT $2`,
		contest, monitor.MaxRosterRows+1)
	if err != nil {
		return nil, fmt.Errorf("list the registrations of %s: %w", contest, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("list the registrations of %s: %w", contest, err)
	}
	if len(ids) > monitor.MaxRosterRows {
		return nil, fmt.Errorf("contest %s has more than %d registrations to stream", contest, monitor.MaxRosterRows)
	}
	return ids, nil
}

// readSource reads one source's range.
func (w *Watch) readSource(ctx context.Context, q monitor.FeedQuery, source monitor.Source) ([]monitor.FeedItem, error) {
	switch source {
	case monitor.SourceAudit:
		return w.feedAudit(ctx, q)
	case monitor.SourceStart, monitor.SourceFinish:
		return w.feedClock(ctx, q, source)
	case monitor.SourceEvent:
		return w.feedEvents(ctx, q)
	case monitor.SourceQuery:
		return w.feedQueries(ctx, q)
	case monitor.SourceAnswer:
		return w.feedAnswers(ctx, q)
	}
	return nil, fmt.Errorf("no feed source %d", source)
}

// args collects the positional parameters of one statement.
type args []any

func (a *args) add(value any) string {
	*a = append(*a, value)
	return "$" + strconv.Itoa(len(*a))
}

// feedBounds is the WHERE fragment that keeps a source's rows past the
// cursor and inside the time range, and the ORDER BY direction.
//
// Past the cursor (t, s, id) in the page's direction, for a source S: when S
// is s, the rows past (t, id); when S sorts after s, the rows at t or later
// (at t they already come after the cursor); when S sorts before s, the rows
// strictly after t. Mirrored for a page read backwards. Each is a plain range
// on the time column, so it is an index range.
func feedBounds(a *args, q monitor.FeedQuery, source monitor.Source, timeCol, idCol, idType string) (where, dir string) {
	var parts []string
	cursor, ascending := q.Before, false
	if q.After != nil {
		cursor, ascending = q.After, true
	}
	if cursor != nil {
		op := "<"
		if ascending {
			op = ">"
		}
		t := a.add(cursor.At)
		switch {
		case source == cursor.Source:
			parts = append(parts, fmt.Sprintf("(%s, %s) %s (%s, %s::%s)", timeCol, idCol, op, t, a.add(cursor.ID), idType))
		case (source > cursor.Source) == ascending:
			parts = append(parts, fmt.Sprintf("%s %s= %s", timeCol, op, t))
		default:
			parts = append(parts, fmt.Sprintf("%s %s %s", timeCol, op, t))
		}
	}
	if ascending {
		// Read forwards, the newest monitor.FeedSettle is left for the next
		// read: rows stamped inside it may still be joined by older stamps
		// that commit later.
		parts = append(parts, fmt.Sprintf("%s < statement_timestamp() - make_interval(secs => %s)",
			timeCol, a.add(monitor.FeedSettle.Seconds())))
	}
	if !q.From.IsZero() {
		parts = append(parts, fmt.Sprintf("%s >= %s", timeCol, a.add(q.From)))
	}
	if !q.Until.IsZero() {
		parts = append(parts, fmt.Sprintf("%s < %s", timeCol, a.add(q.Until)))
	}
	dir = "DESC"
	if ascending {
		dir = "ASC"
	}
	if len(parts) == 0 {
		return "", dir
	}
	return " AND " + strings.Join(parts, " AND "), dir
}

// registrationScope narrows the contest's registrations (alias r) to the one
// the feed is about, if it is about one.
func registrationScope(a *args, q monitor.FeedQuery) string {
	scope := "r.contest_id = " + a.add(q.Contest)
	if q.Registration != uuid.Nil {
		scope += " AND r.id = " + a.add(q.Registration)
	}
	return scope
}

func (w *Watch) feedQueries(ctx context.Context, q monitor.FeedQuery) ([]monitor.FeedItem, error) {
	var a args
	scope := registrationScope(&a, q)
	bounds, dir := feedBounds(&a, q, monitor.SourceQuery, "q.executed_at", "q.id", "bigint")
	limit := a.add(q.Limit + 1)
	chars := a.add(queryrunner.MaxHistorySQLChars)
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT q.id, q.registration_id, q.executed_at, left(q.sql_text, `+chars+`),
		       char_length(q.sql_text) > `+chars+`, q.status, COALESCE(q.error_text, ''),
		       q.duration_ms, q.row_count, COALESCE(host(q.ip), '')
		FROM registrations r
		CROSS JOIN LATERAL (
		    SELECT q.* FROM query_log q
		    WHERE q.registration_id = r.id`+bounds+`
		    ORDER BY q.executed_at `+dir+`, q.id `+dir+`
		    LIMIT `+limit+`
		) q
		WHERE `+scope+`
		ORDER BY q.executed_at `+dir+`, q.id `+dir+`
		LIMIT `+limit, a...)
	if err != nil {
		return nil, fmt.Errorf("read the feed's queries: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitor.FeedItem, error) {
		var (
			item monitor.FeedItem
			data monitor.QueryData
		)
		if err := row.Scan(&data.ID, &item.Registration, &item.At, &data.SQL, &data.SQLTruncated, &data.Status,
			&data.Error, &data.DurationMs, &data.RowCount, &data.IP); err != nil {
			return item, fmt.Errorf("scan a feed query: %w", err)
		}
		data.Error = monitor.StaffErrorText(data.Status, data.Error)
		item.Source, item.ID, item.Kind, item.Data = monitor.SourceQuery, strconv.FormatInt(data.ID, 10), monitor.FeedKindQuery, data
		return item, nil
	})
}

func (w *Watch) feedAnswers(ctx context.Context, q monitor.FeedQuery) ([]monitor.FeedItem, error) {
	var a args
	scope := registrationScope(&a, q)
	bounds, dir := feedBounds(&a, q, monitor.SourceAnswer, "s.submitted_at", "s.id", "uuid")
	limit := a.add(q.Limit + 1)
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT s.id, s.registration_id, s.submitted_at, s.question_id, COALESCE(qu.ord, 0),
		       s.attempt_no, s.value, s.is_correct, s.points_awarded
		FROM registrations r
		CROSS JOIN LATERAL (
		    SELECT s.* FROM submissions s
		    WHERE s.registration_id = r.id`+bounds+`
		    ORDER BY s.submitted_at `+dir+`, s.id `+dir+`
		    LIMIT `+limit+`
		) s
		LEFT JOIN questions qu ON qu.id = s.question_id
		WHERE `+scope+`
		ORDER BY s.submitted_at `+dir+`, s.id `+dir+`
		LIMIT `+limit, a...)
	if err != nil {
		return nil, fmt.Errorf("read the feed's answers: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitor.FeedItem, error) {
		var (
			item monitor.FeedItem
			data monitor.AnswerData
		)
		if err := row.Scan(&data.ID, &item.Registration, &item.At, &data.QuestionID, &data.QuestionOrd,
			&data.AttemptNo, &data.Value, &data.Correct, &data.PointsAwarded); err != nil {
			return item, fmt.Errorf("scan a feed answer: %w", err)
		}
		item.Source, item.ID, item.Kind, item.Data = monitor.SourceAnswer, data.ID.String(), monitor.FeedKindAnswer, data
		return item, nil
	})
}

func (w *Watch) feedEvents(ctx context.Context, q monitor.FeedQuery) ([]monitor.FeedItem, error) {
	var a args
	scope := "e.contest_id = " + a.add(q.Contest)
	if q.Registration != uuid.Nil {
		// The registration's own index, with the contest checked beside it.
		scope += " AND e.registration_id = " + a.add(q.Registration)
	}
	if kinds := q.KindsOf(monitor.SourceEvent); len(kinds) > 0 {
		scope += " AND e.kind = ANY(" + a.add(kinds) + "::text[])"
	}
	bounds, dir := feedBounds(&a, q, monitor.SourceEvent, "e.created_at", "e.id", "bigint")
	limit := a.add(q.Limit + 1)
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT e.id, e.registration_id, e.created_at, e.kind, e.payload
		FROM participant_events e
		WHERE `+scope+bounds+`
		ORDER BY e.created_at `+dir+`, e.id `+dir+`
		LIMIT `+limit, a...)
	if err != nil {
		return nil, fmt.Errorf("read the feed's events: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitor.FeedItem, error) {
		var (
			item    monitor.FeedItem
			id      int64
			payload []byte
		)
		if err := row.Scan(&id, &item.Registration, &item.At, &item.Kind, &payload); err != nil {
			return item, fmt.Errorf("scan a feed event: %w", err)
		}
		item.Source, item.ID, item.Data = monitor.SourceEvent, strconv.FormatInt(id, 10), json.RawMessage(payload)
		return item, nil
	})
}

// feedClock reads the participants' clock starting or finishing, off
// registrations. The contest's roster is bounded and has no journal's size,
// so this is a filter over the contest's registrations.
func (w *Watch) feedClock(ctx context.Context, q monitor.FeedQuery, source monitor.Source) ([]monitor.FeedItem, error) {
	column, kind := "r.started_at", monitor.FeedStarted
	if source == monitor.SourceFinish {
		column, kind = "r.finished_at", monitor.FeedFinished
	}
	var a args
	scope := registrationScope(&a, q)
	bounds, dir := feedBounds(&a, q, source, column, "r.id", "uuid")
	limit := a.add(q.Limit + 1)
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT r.id, `+column+`
		FROM registrations r
		WHERE `+scope+` AND `+column+` IS NOT NULL`+bounds+`
		ORDER BY `+column+` `+dir+`, r.id `+dir+`
		LIMIT `+limit, a...)
	if err != nil {
		return nil, fmt.Errorf("read the feed's %s: %w", kind, err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitor.FeedItem, error) {
		var item monitor.FeedItem
		if err := row.Scan(&item.Registration, &item.At); err != nil {
			return item, fmt.Errorf("scan a feed %s: %w", kind, err)
		}
		item.Source, item.ID, item.Kind = source, item.Registration.String(), kind
		return item, nil
	})
}

// auditFeedKinds maps the trail's actions to the feed's kinds.
var auditFeedKinds = map[string]string{
	audit.ActionAuthLogin:             monitor.FeedSignIn,
	audit.ActionAuthLogout:            monitor.FeedSignOut,
	audit.ActionAuthLoginFailed:       monitor.FeedSignInFailed,
	audit.ActionParticipantDisqualify: monitor.FeedDisqualified,
}

func (w *Watch) feedAudit(ctx context.Context, q monitor.FeedQuery) ([]monitor.FeedItem, error) {
	wanted := map[string]bool{}
	for _, kind := range q.KindsOf(monitor.SourceAudit) {
		wanted[kind] = true
	}
	all := len(wanted) == 0

	var a args
	scope := registrationScope(&a, q)
	bounds, dir := feedBounds(&a, q, monitor.SourceAudit, "a.created_at", "a.id", "bigint")
	limit := a.add(q.Limit + 1)
	// A participant's own sign-ins, sign-outs and failed sign-ins count from
	// their registration to monitor.SignInGrace past their finish, or the
	// contest's end; with neither known, up to now.
	// Built on first use: a parameter no branch names cannot be typed.
	var untilSQL string
	until := func() string {
		if untilSQL == "" {
			untilSQL = ` AND (COALESCE(r.finished_at, c.ends_at) IS NULL
		      OR a.created_at < COALESCE(r.finished_at, c.ends_at) + make_interval(secs => ` +
				a.add(monitor.SignInGrace.Seconds()) + `))`
		}
		return untilSQL
	}
	// Each branch is its own index range with its own LIMIT, and the union
	// is cut once more.
	var branches []string
	var sessionActions []string
	if all || wanted[monitor.FeedSignIn] {
		sessionActions = append(sessionActions, audit.ActionAuthLogin)
	}
	if all || wanted[monitor.FeedSignOut] {
		sessionActions = append(sessionActions, audit.ActionAuthLogout)
	}
	if len(sessionActions) > 0 {
		branches = append(branches, `
		SELECT a.id, r.id AS registration_id, a.created_at, a.action, host(a.ip), a.user_agent, a.payload
		FROM registrations r
		JOIN contests c ON c.id = r.contest_id
		CROSS JOIN LATERAL (
		    SELECT a.* FROM audit_log a
		    WHERE a.actor_id = r.user_id AND a.action = ANY(`+a.add(sessionActions)+`::text[])
		      AND a.created_at >= r.created_at`+until()+bounds+`
		    ORDER BY a.created_at `+dir+`, a.id `+dir+`
		    LIMIT `+limit+`
		) a
		WHERE `+scope)
	}
	if all || wanted[monitor.FeedSignInFailed] {
		// Matched by the login the attempt typed against the account's login
		// now, since a failed sign-in records no account. Two consequences
		// follow and are accepted. Anybody mistyping a participant's login is
		// shown as that participant's failed sign-in, which is what an
		// organiser watching for a guessed password wants to see. And an
		// account whose login was changed loses the failed sign-ins typed
		// under its old one: the trail does not say the two logins were the
		// same account, and guessing would attribute strangers' attempts.
		branches = append(branches, `
		SELECT a.id, r.id AS registration_id, a.created_at, a.action, host(a.ip), a.user_agent, a.payload
		FROM registrations r
		JOIN contests c ON c.id = r.contest_id
		JOIN users u ON u.id = r.user_id
		CROSS JOIN LATERAL (
		    SELECT a.* FROM audit_log a
		    WHERE a.action = '`+audit.ActionAuthLoginFailed+`'
		      AND lower(a.payload->>'login') = lower(u.login)
		      AND a.created_at >= r.created_at`+until()+bounds+`
		    ORDER BY a.created_at `+dir+`, a.id `+dir+`
		    LIMIT `+limit+`
		) a
		WHERE `+scope)
	}
	if all || wanted[monitor.FeedDisqualified] {
		// One participant's timeline narrows the contest's disqualifications
		// to theirs before the limit, or the others' could fill it.
		whose := ""
		if q.Registration != uuid.Nil {
			whose = ` AND a.payload->>'user_id' = (SELECT user_id::text FROM registrations WHERE id = ` +
				a.add(q.Registration) + `)`
		}
		branches = append(branches, `
		SELECT a.id, r.id AS registration_id, a.created_at, a.action, host(a.ip), a.user_agent, a.payload
		FROM (
		    SELECT a.* FROM audit_log a
		    WHERE a.entity = 'contest' AND a.entity_id = `+a.add(q.Contest.String())+`
		      AND a.action = '`+audit.ActionParticipantDisqualify+`'`+whose+bounds+`
		    ORDER BY a.created_at `+dir+`, a.id `+dir+`
		    LIMIT `+limit+`
		) a
		JOIN registrations r ON r.user_id::text = a.payload->>'user_id'
		WHERE `+scope)
	}
	if len(branches) == 0 {
		return nil, nil
	}
	sql := "SELECT * FROM (" + strings.Join(parenthesize(branches), " UNION ALL ") + ") a" +
		" ORDER BY a.created_at " + dir + ", a.id " + dir + " LIMIT " + limit
	rows, err := w.querier(ctx).Query(ctx, sql, a...)
	if err != nil {
		return nil, fmt.Errorf("read the feed's sign-ins: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (monitor.FeedItem, error) {
		var (
			item      monitor.FeedItem
			id        int64
			action    string
			ip        *string
			userAgent *string
			payload   []byte
		)
		if err := row.Scan(&id, &item.Registration, &item.At, &action, &ip, &userAgent, &payload); err != nil {
			return item, fmt.Errorf("scan a feed sign-in: %w", err)
		}
		data := monitor.AuditData{}
		if ip != nil {
			data.IP = *ip
		}
		if userAgent != nil {
			data.UserAgent = *userAgent
		}
		if action == audit.ActionAuthLoginFailed {
			var fields struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(payload, &fields)
			data.Reason = fields.Reason
		}
		item.Source, item.ID, item.Kind, item.Data = monitor.SourceAudit, strconv.FormatInt(id, 10), auditFeedKinds[action], data
		return item, nil
	})
}

func parenthesize(parts []string) []string {
	out := make([]string, len(parts))
	for i, part := range parts {
		out[i] = "(" + part + ")"
	}
	return out
}

// name fills in the participants' logins and names, in one read by primary
// key for the whole page.
func (w *Watch) name(ctx context.Context, contest uuid.UUID, items []monitor.FeedItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	seen := map[uuid.UUID]bool{}
	for _, item := range items {
		if !seen[item.Registration] {
			seen[item.Registration] = true
			ids = append(ids, item.Registration)
		}
	}
	rows, err := w.querier(ctx).Query(ctx, `
		SELECT r.id, u.login, u.full_name
		FROM registrations r JOIN users u ON u.id = r.user_id
		WHERE r.id = ANY($1) AND r.contest_id = $2`, ids, contest)
	if err != nil {
		return fmt.Errorf("name the feed's participants: %w", err)
	}
	type name struct{ login, full string }
	names := map[uuid.UUID]name{}
	for rows.Next() {
		var (
			id uuid.UUID
			n  name
		)
		if err := rows.Scan(&id, &n.login, &n.full); err != nil {
			rows.Close()
			return fmt.Errorf("name the feed's participants: %w", err)
		}
		names[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("name the feed's participants: %w", err)
	}
	for i := range items {
		n := names[items[i].Registration]
		items[i].Login, items[i].FullName = n.login, n.full
	}
	return nil
}

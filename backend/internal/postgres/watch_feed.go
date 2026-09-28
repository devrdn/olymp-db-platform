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
// The whole contest's query log, answers and events are each one range of one
// index, on (contest_id, time, id): every journal carries the contest it
// belongs to, participant_events since migration 000033 and the other two
// since 000034, so a page of a three-hundred-participant contest reads the
// page and not a page per participant. One participant's timeline reads their
// own range instead, on (registration_id, time, id), with the contest checked
// on the same row — the narrower range of the two, and the same guarantee
// that another contest's rows can never appear.
//
// Sign-ins and sign-outs are the participant's account's own audit entries
// (audit_log (actor_id, created_at)), failed sign-ins the entries naming its
// login (audit_log_failed_login_idx), both only within the participant's
// part in the contest, give or take monitor.SignInGrace, and never before
// they registered: an account's sign-ins outside it are not the contest's
// business. Disqualifications are the contest's own entries
// (audit_log_entity_idx).
func (w *Watch) Feed(ctx context.Context, q monitor.FeedQuery) (monitor.FeedPage, error) {
	q, err := q.Normalize()
	if err != nil {
		return monitor.FeedPage{}, err
	}
	// The sources are independent reads, so they go as one batch: one round
	// trip on every poll rather than one per source. Their callbacks run in
	// the order they were queued, as the loop used to.
	var (
		items []monitor.FeedItem
		batch pgx.Batch
	)
	for source := monitor.SourceAudit; source <= monitor.SourceFinish; source++ {
		if !q.Reads(source) {
			continue
		}
		st, err := sourceStatement(q, source)
		if err != nil {
			return monitor.FeedPage{}, err
		}
		if st.sql == "" {
			continue
		}
		batch.Queue(st.sql, st.args...).Query(func(rows pgx.Rows) error {
			found, err := st.collect(rows)
			items = append(items, found...)
			return err
		})
	}
	if batch.Len() > 0 {
		if err := w.querier(ctx).SendBatch(ctx, &batch).Close(); err != nil {
			return monitor.FeedPage{}, fmt.Errorf("read the feed: %w", err)
		}
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
	st, err := sourceStatement(q, source)
	if err != nil || st.sql == "" {
		return nil, err
	}
	rows, err := w.querier(ctx).Query(ctx, st.sql, st.args...)
	if err != nil {
		return nil, fmt.Errorf("read the feed's %s: %w", st.what, err)
	}
	items, err := st.collect(rows)
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
		return nil, fmt.Errorf("contest %s: %w", contest, monitor.ErrExportTooWide)
	}
	return ids, nil
}

// feedStatement is one source's read: the statement, its parameters, and how
// a row of it becomes a feed item. Built without a database, so Feed can send
// every source it reads as one batch and FeedSource can send one alone. A
// zero statement (no sql) reads nothing: the filter left the source no kind
// to read.
type feedStatement struct {
	what string
	sql  string
	args []any
	scan func(pgx.CollectableRow) (monitor.FeedItem, error)
}

// collect reads the statement's rows into feed items.
func (st feedStatement) collect(rows pgx.Rows) ([]monitor.FeedItem, error) {
	items, err := pgx.CollectRows(rows, st.scan)
	if err != nil {
		return nil, fmt.Errorf("read the feed's %s: %w", st.what, err)
	}
	return items, nil
}

// sourceStatement builds one source's read of its range.
func sourceStatement(q monitor.FeedQuery, source monitor.Source) (feedStatement, error) {
	switch source {
	case monitor.SourceAudit:
		return feedAudit(q), nil
	case monitor.SourceStart, monitor.SourceFinish:
		return feedClock(q, source), nil
	case monitor.SourceEvent:
		return feedEvents(q), nil
	case monitor.SourceQuery:
		return feedQueries(q), nil
	case monitor.SourceAnswer:
		return feedAnswers(q), nil
	}
	return feedStatement{}, fmt.Errorf("no feed source %d", source)
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
	if ascending || cursor == nil {
		// Read forwards, or as the newest page a live screen starts polling
		// from, the newest monitor.FeedSettle is left for the next read:
		// rows stamped inside it may still be joined by older stamps that
		// commit later, and a cursor past them would never see those.
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

// journalScope narrows a journal that carries its own contest (query_log,
// submissions, participant_events under the given alias) to what the feed is
// about.
//
// A contest-wide page is the contest's range. One participant's timeline is
// that registration's range, and that the registration is this contest's is
// asked once, of registrations, rather than of every row of the range: the
// two columns agree by construction (migration 000034), and a second
// condition on the row would only tell the planner that the range is less
// selective than it is — enough for it to give up the ordered index scan the
// keyset page depends on and read the whole range into a sort.
func journalScope(a *args, q monitor.FeedQuery, alias string) string {
	if q.Registration != uuid.Nil {
		registration := a.add(q.Registration)
		return alias + ".registration_id = " + registration +
			" AND EXISTS (SELECT 1 FROM registrations r WHERE r.id = " + registration +
			" AND r.contest_id = " + a.add(q.Contest) + ")"
	}
	return alias + ".contest_id = " + a.add(q.Contest)
}

func feedQueries(q monitor.FeedQuery) feedStatement {
	var a args
	scope := journalScope(&a, q, "q")
	bounds, dir := feedBounds(&a, q, monitor.SourceQuery, "q.executed_at", "q.id", "bigint")
	limit := a.add(q.Limit + 1)
	chars := a.add(queryrunner.MaxHistorySQLChars)
	st := feedStatement{what: "queries", args: a, sql: `
		SELECT q.id, q.registration_id, q.executed_at, left(q.sql_text, ` + chars + `),
		       char_length(q.sql_text) > ` + chars + `, q.status, COALESCE(q.error_text, ''),
		       q.duration_ms, q.row_count, COALESCE(host(q.ip), '')
		FROM query_log q
		WHERE ` + scope + bounds + `
		ORDER BY q.executed_at ` + dir + `, q.id ` + dir + `
		LIMIT ` + limit}
	st.scan = func(row pgx.CollectableRow) (monitor.FeedItem, error) {
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
	}
	return st
}

func feedAnswers(q monitor.FeedQuery) feedStatement {
	var a args
	scope := journalScope(&a, q, "s")
	bounds, dir := feedBounds(&a, q, monitor.SourceAnswer, "s.submitted_at", "s.id", "uuid")
	limit := a.add(q.Limit + 1)
	// The question is named after the page is cut, not before: the join is a
	// lookup by primary key for the page's own rows.
	st := feedStatement{what: "answers", args: a, sql: `
		SELECT s.id, s.registration_id, s.submitted_at, s.question_id, COALESCE(qu.ord, 0),
		       s.attempt_no, s.value, s.is_correct, s.points_awarded
		FROM (
		    SELECT s.* FROM submissions s
		    WHERE ` + scope + bounds + `
		    ORDER BY s.submitted_at ` + dir + `, s.id ` + dir + `
		    LIMIT ` + limit + `
		) s
		LEFT JOIN questions qu ON qu.id = s.question_id
		ORDER BY s.submitted_at ` + dir + `, s.id ` + dir + `
		LIMIT ` + limit}
	st.scan = func(row pgx.CollectableRow) (monitor.FeedItem, error) {
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
	}
	return st
}

func feedEvents(q monitor.FeedQuery) feedStatement {
	var a args
	// The same narrowing the other two journals get, for the same reason:
	// participant_events carries both columns and this read is keyset-paged
	// off participant_events_registration_time_idx exactly as they are off
	// theirs. Only the kind filter is this source's own.
	scope := journalScope(&a, q, "e")
	if kinds := q.KindsOf(monitor.SourceEvent); len(kinds) > 0 {
		scope += " AND e.kind = ANY(" + a.add(kinds) + "::text[])"
	}
	bounds, dir := feedBounds(&a, q, monitor.SourceEvent, "e.created_at", "e.id", "bigint")
	limit := a.add(q.Limit + 1)
	st := feedStatement{what: "events", args: a, sql: `
		SELECT e.id, e.registration_id, e.created_at, e.kind, e.payload
		FROM participant_events e
		WHERE ` + scope + bounds + `
		ORDER BY e.created_at ` + dir + `, e.id ` + dir + `
		LIMIT ` + limit}
	st.scan = func(row pgx.CollectableRow) (monitor.FeedItem, error) {
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
	}
	return st
}

// feedClock reads the participants' clock starting or finishing, off
// registrations. The contest's roster is bounded and has no journal's size,
// so this is a filter over the contest's registrations.
func feedClock(q monitor.FeedQuery, source monitor.Source) feedStatement {
	column, kind := "r.started_at", monitor.FeedStarted
	if source == monitor.SourceFinish {
		column, kind = "r.finished_at", monitor.FeedFinished
	}
	var a args
	scope := registrationScope(&a, q)
	bounds, dir := feedBounds(&a, q, source, column, "r.id", "uuid")
	limit := a.add(q.Limit + 1)
	st := feedStatement{what: kind, args: a, sql: `
		SELECT r.id, ` + column + `
		FROM registrations r
		WHERE ` + scope + ` AND ` + column + ` IS NOT NULL` + bounds + `
		ORDER BY ` + column + ` ` + dir + `, r.id ` + dir + `
		LIMIT ` + limit}
	st.scan = func(row pgx.CollectableRow) (monitor.FeedItem, error) {
		var item monitor.FeedItem
		if err := row.Scan(&item.Registration, &item.At); err != nil {
			return item, fmt.Errorf("scan a feed %s: %w", kind, err)
		}
		item.Source, item.ID, item.Kind = source, item.Registration.String(), kind
		return item, nil
	}
	return st
}

// auditFeedKinds maps the trail's actions to the feed's kinds.
var auditFeedKinds = map[string]string{
	audit.ActionAuthLogin:             monitor.FeedSignIn,
	audit.ActionAuthLogout:            monitor.FeedSignOut,
	audit.ActionAuthLoginFailed:       monitor.FeedSignInFailed,
	audit.ActionParticipantDisqualify: monitor.FeedDisqualified,
}

func feedAudit(q monitor.FeedQuery) feedStatement {
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
	// monitor.SignInGrace before the start of their part in the contest to
	// monitor.SignInGrace past its end.
	//
	// The start is the contest's own start whenever it has one, in either
	// timing: under individual timing a participant who starts late signed
	// in after the window opened for a reason the organiser may want to see.
	// Without a contest start it is their own start, else their
	// registration, and it is never earlier than the registration. An
	// invite-only contest can enrol a student weeks ahead, and those weeks
	// of sign-ins, addresses and browsers — failed sign-ins carrying
	// strangers' addresses among them — are not the contest's business.
	//
	// The end is their finish; else their own deadline under individual
	// timing — the start plus the duration, or the contest's end when that
	// comes first, contests.Deadline's formula — else the contest's end.
	// With none of them known, up to now. That leaves one case unbounded:
	// individual timing, a participant who never started, and a contest with
	// no end. They have no deadline to measure from — and, never having
	// started, nothing of theirs in the contest but these sign-ins, which is
	// what an organiser asking why they never began wants to see.
	const participantStart = `COALESCE(c.starts_at, r.started_at, r.created_at)`
	const participantEnd = `COALESCE(r.finished_at,
		      CASE WHEN c.timing = 'individual'
		           THEN LEAST(r.started_at + make_interval(mins => c.duration_min), c.ends_at) END,
		      c.ends_at)`
	// Built on first use: a parameter no branch names cannot be typed.
	var windowSQL string
	window := func() string {
		if windowSQL == "" {
			grace := a.add(monitor.SignInGrace.Seconds())
			windowSQL = `
		      AND a.created_at >= GREATEST(r.created_at,
		          ` + participantStart + ` - make_interval(secs => ` + grace + `))
		      AND (` + participantEnd + ` IS NULL
		      OR a.created_at < ` + participantEnd + ` + make_interval(secs => ` + grace + `))`
		}
		return windowSQL
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
		    WHERE a.actor_id = r.user_id AND a.action = ANY(`+a.add(sessionActions)+`::text[])`+window()+bounds+`
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
		      AND lower(a.payload->>'login') = lower(u.login)`+window()+bounds+`
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
		return feedStatement{}
	}
	st := feedStatement{what: "sign-ins", args: a,
		sql: "SELECT * FROM (" + strings.Join(parenthesize(branches), " UNION ALL ") + ") a" +
			" ORDER BY a.created_at " + dir + ", a.id " + dir + " LIMIT " + limit}
	st.scan = func(row pgx.CollectableRow) (monitor.FeedItem, error) {
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
	}
	return st
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

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Registrations implements contests.RegistrationRepository.
var _ contests.RegistrationRepository = (*Registrations)(nil)

// Registrations also answers queryproxy's combined lookup (see ForRun below):
// the SQL console's hot path reads one round trip where it used to read
// three.
var _ queryproxy.Lookup = (*Registrations)(nil)

// participantColumns joins the account for the same reason the staff list
// does: a roster of identifiers is unreadable.
const participantColumns = `
	r.id, r.contest_id, r.user_id, u.login, u.full_name, r.status,
	r.started_at, r.finished_at, r.total_score, r.created_at`

// Registrations stores who takes part in a contest.
type Registrations struct {
	pool *pgxpool.Pool
}

// NewRegistrations returns the registration repository.
func NewRegistrations(pool *pgxpool.Pool) *Registrations {
	return &Registrations{pool: pool}
}

func (r *Registrations) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// participantScanTargets returns pointers matching participantColumns' own
// column order, so a wider projection (ForRun below) can share this list
// with scanParticipant instead of repeating it.
func participantScanTargets(p *contests.Participant) []any {
	return []any{&p.ID, &p.ContestID, &p.UserID, &p.Login, &p.FullName, &p.Status,
		&p.StartedAt, &p.FinishedAt, &p.TotalScore, &p.CreatedAt}
}

func scanParticipant(row pgx.Row) (contests.Participant, error) {
	var p contests.Participant
	err := row.Scan(participantScanTargets(&p)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return contests.Participant{}, contests.ErrParticipantNotFound
	}
	if err != nil {
		return contests.Participant{}, fmt.Errorf("scan participant: %w", err)
	}
	return p, nil
}

// List returns a page of the contest's participants and the total.
func (r *Registrations) List(ctx context.Context, contestID uuid.UUID, f contests.ParticipantFilter) ([]contests.Participant, int, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT `+participantColumns+`, COUNT(*) OVER() AS total
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.contest_id = $1
		  AND ($2 = '' OR r.status = $2)
		  AND ($3 = '' OR u.login ILIKE '%' || $3 || '%' OR u.full_name ILIKE '%' || $3 || '%')
		ORDER BY u.login
		LIMIT $4 OFFSET $5`,
		// Typed by a person and used as an ILIKE pattern, so escaped (see like.go).
		contestID, f.Status, escapeLike(f.Query), f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list participants: %w", err)
	}
	defer rows.Close()

	var (
		found []contests.Participant
		total int
	)
	for rows.Next() {
		var p contests.Participant
		if err := rows.Scan(&p.ID, &p.ContestID, &p.UserID, &p.Login, &p.FullName, &p.Status,
			&p.StartedAt, &p.FinishedAt, &p.TotalScore, &p.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan participant: %w", err)
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list participants: %w", err)
	}
	return found, total, nil
}

// ByUser returns one participation.
func (r *Registrations) ByUser(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error) {
	return scanParticipant(r.querier(ctx).QueryRow(ctx, `
		SELECT `+participantColumns+`
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.contest_id = $1 AND r.user_id = $2`, contestID, userID))
}

// ForRun implements queryproxy.Lookup: the participant, the contest they are
// asking about, and its game, in one round trip — where Run used to open
// three, one apiece against registrations, contests and (through
// GameInstances.Game) contests again.
//
// The INNER JOIN against contests is what keeps "never registered" and "no
// such contest" one answer rather than two: a registration's own foreign key
// guarantees the contest it names exists, so there is no separate not-found
// case to invent for it — the WHERE below simply matches no row for either
// reason, exactly as the old, separate People.ByUser lookup already did (see
// lookupParticipant's own doc in queryproxy). The game half is LEFT JOINed
// rather than INNER, deliberately: a contest with no ready template must
// still come back with its participant and its own columns populated, only
// its game reading as absent (provisioning.ErrNoGame) — see
// queryproxy.LookupResult.
func (r *Registrations) ForRun(ctx context.Context, contestID, userID uuid.UUID) (queryproxy.LookupResult, error) {
	var (
		p                                                              contests.Participant
		c                                                              contests.Contest
		settings, languages, translations                              []byte
		templateDB                                                     *string
		version                                                        *int
		mode                                                           string
		writableTables                                                 []string
		allowCreateView, allowOwnTables, allowTempTables, allowCatalog bool
		diskQuotaRatio                                                 int
	)
	targets := participantScanTargets(&p)
	targets = append(targets, contestScanTargets(&c, &settings, &languages, &translations)...)
	targets = append(targets, &templateDB, &version, &mode, &writableTables,
		&allowCreateView, &allowOwnTables, &allowTempTables, &allowCatalog, &diskQuotaRatio)

	err := r.querier(ctx).QueryRow(ctx, `
		SELECT `+participantColumns+`,
		       `+contestColumns+`,
		       t.template_db, t.version,
		       `+policyProjectionColumns+`
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		JOIN contests c ON c.id = r.contest_id
		LEFT JOIN game_templates t ON t.contest_id = c.id AND t.status = 'ready'
		LEFT JOIN contest_sql_policies p ON p.contest_id = c.id
		WHERE r.contest_id = $1 AND r.user_id = $2`, contestID, userID).Scan(targets...)
	if errors.Is(err, pgx.ErrNoRows) {
		return queryproxy.LookupResult{}, contests.ErrParticipantNotFound
	}
	if err != nil {
		return queryproxy.LookupResult{}, fmt.Errorf("scan participant, contest and game: %w", err)
	}

	contest, err := hydrate(c, settings, languages, translations)
	if err != nil {
		return queryproxy.LookupResult{}, err
	}
	result := queryproxy.LookupResult{Participant: p, Contest: contest}
	if templateDB == nil {
		result.GameErr = provisioning.ErrNoGame
		return result, nil
	}
	result.Game = provisioning.Contest{
		ID:       contest.ID,
		Template: *templateDB,
		Version:  *version,
		Policy: sqlpolicy.Policy{
			Mode:            sqlpolicy.Mode(mode),
			WritableTables:  writableTables,
			AllowCreateView: allowCreateView,
			AllowOwnTables:  allowOwnTables,
			AllowTempTables: allowTempTables,
			AllowCatalog:    allowCatalog,
			DiskQuotaRatio:  diskQuotaRatio,
		},
	}
	return result, nil
}

// EnrolledIn reports which of these contests the user is registered for.
//
// One query for a whole page: a catalogue of twenty rows must not become
// twenty lookups, and `= ANY($2)` keeps the identifiers as parameters rather
// than building a list into the statement.
//
// The user is a parameter the HTTP layer fills from the authenticated
// identity, never from the request, so this cannot be asked about anybody
// else. Absent contests simply have no key: a caller reads the map with `[id]`
// and gets false, which is the right answer for "not registered".
func (r *Registrations) EnrolledIn(ctx context.Context, userID uuid.UUID, contestIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	on := make(map[uuid.UUID]bool, len(contestIDs))
	if userID == uuid.Nil || len(contestIDs) == 0 {
		return on, nil
	}

	rows, err := r.querier(ctx).Query(ctx,
		`SELECT contest_id FROM registrations WHERE user_id = $1 AND contest_id = ANY($2)`,
		userID, contestIDs)
	if err != nil {
		return nil, fmt.Errorf("read the caller's registrations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan registration: %w", err)
		}
		on[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the caller's registrations: %w", err)
	}
	return on, nil
}

// Add registers a user for a contest.
func (r *Registrations) Add(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error) {
	p, err := scanParticipant(r.querier(ctx).QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO registrations (contest_id, user_id) VALUES ($1, $2) RETURNING *
		)
		SELECT `+participantColumns+`
		FROM inserted r JOIN users u ON u.id = r.user_id`, contestID, userID))
	if err != nil {
		// The unique index is the real guarantee against two staff adding the
		// same student at the same moment, and the caller has to be able to
		// act on it rather than be handed a constraint name.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return contests.Participant{}, contests.ErrAlreadyEnrolled
		}
		return contests.Participant{}, err
	}
	return p, nil
}

// Remove deletes a registration.
func (r *Registrations) Remove(ctx context.Context, contestID, userID uuid.UUID) error {
	tag, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM registrations WHERE contest_id = $1 AND user_id = $2`, contestID, userID)
	if err != nil {
		return fmt.Errorf("remove participant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrParticipantNotFound
	}
	return nil
}

// Start records now as the participant's first deliberate action against the
// game, if they have not already begun (finding 1).
//
// The UPDATE is the whole guarantee for the write: it can only ever set
// started_at once per row, because its own WHERE re-reads started_at under
// the row lock it takes, so a second transaction racing for the same
// registration blocks on that lock and, once the first commits, finds
// started_at no longer null and updates nothing — no read-then-write gap for
// either side to land in. Called on every action and a no-op after the
// first, so an individual participant's later queries pay no further write —
// the same discipline CLAUDE.md rule 6 asks of a session touch.
//
// The WHERE also requires status = 'registered' (finding 4), not only
// started_at IS NULL: a registration disqualified before it ever started
// still has started_at IS NULL — SetStatus never touches that column — and
// without this second guard a disqualification landing between a caller's
// lookup of the participant and this call would be silently undone, moving
// them straight to 'active' as if nothing had happened. 'registered' is the
// only status this method's own WHERE ever has to match against, because
// every other status either already has started_at set or, for
// 'disqualified', must not be reopened by this method at all — an explicit
// allow-list rather than excluding 'disqualified' by name, so a future status
// this method was never taught about is refused rather than silently started.
//
// A caller who loses the race still has to learn the start time the winner
// set, and that read must be its own statement, not folded into the same one
// as the UPDATE: PostgreSQL takes one snapshot per statement under READ
// COMMITTED, and a plain SELECT sharing the UPDATE's statement would read
// against the snapshot from before the UPDATE blocked on the row lock — the
// version with started_at still null — even after the UPDATE itself
// re-checks the lock and correctly sees the winner's commit. A first version
// of this method folded both into one statement with a UNION ALL and passed
// every test run alone; under real concurrency it handed some racers back a
// participant with no StartedAt at all, silently reintroducing the bug this
// method exists to close. The fix is the second, separate statement below,
// which gets a fresh snapshot of its own.
//
// This whole argument is specific to READ COMMITTED, which is what the pool
// this repository shares actually runs at today because nothing in
// internal/platform/storage ever raises it. Under REPEATABLE READ or
// SERIALIZABLE a transaction's first statement fixes its snapshot for every
// statement after it, so a losing racer's second statement would keep
// reading the pre-UPDATE snapshot rather than picking up the winner's
// commit — the version with started_at still null — and the guarantee this
// doc comment argues for would silently stop holding. Anybody changing the
// pool's isolation level has to re-read this method, not only trust that its
// tests still pass.
func (r *Registrations) Start(ctx context.Context, registrationID uuid.UUID, now time.Time) (contests.Participant, error) {
	querier := r.querier(ctx)
	p, err := scanParticipant(querier.QueryRow(ctx, `
		WITH updated AS (
			UPDATE registrations
			SET started_at = $2, status = $3
			WHERE id = $1 AND started_at IS NULL AND status = $4
			RETURNING *
		)
		SELECT `+participantColumns+`
		FROM updated r JOIN users u ON u.id = r.user_id`,
		registrationID, now, contests.RegistrationActive, contests.RegistrationRegistered))
	switch {
	case err == nil:
		return p, nil
	case errors.Is(err, contests.ErrParticipantNotFound):
		// Lost the race, or this is not the first call for this registration.
		// A fresh statement, so it reads whatever is committed now rather
		// than what was committed when this call started (see the doc above).
		return scanParticipant(querier.QueryRow(ctx, `
			SELECT `+participantColumns+`
			FROM registrations r JOIN users u ON u.id = r.user_id
			WHERE r.id = $1`, registrationID))
	default:
		return contests.Participant{}, err
	}
}

// AddScore adds delta to the registration's total_score with a single atomic
// UPDATE, never a read of the current value followed by a write — two
// submissions scoring at the same moment (different questions answered
// concurrently, or submission.go's own retry after losing the attempt race)
// each issue their own `total_score = total_score + $2`, and PostgreSQL
// serialises the two statements against the same row rather than letting the
// second overwrite what the first added.
func (r *Registrations) AddScore(ctx context.Context, registrationID uuid.UUID, delta int) error {
	tag, err := r.querier(ctx).Exec(ctx,
		`UPDATE registrations SET total_score = total_score + $2 WHERE id = $1`, registrationID, delta)
	if err != nil {
		return fmt.Errorf("add to registration score: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrParticipantNotFound
	}
	return nil
}

// HasWork reports whether anything of the participant's own hangs off this
// registration.
//
// Four EXISTS over four indexes, each stopping at the first row it finds:
// every one of these tables leads its serving index with registration_id
// (query_log_registration_executed_idx, submissions_registration_submitted_idx,
// participant_events_registration_time_idx, participant_sql_tabs_registration_idx,
// and the notes' own primary key), so the whole question costs four index
// probes however long the contest has been running. It is asked once, when an
// organiser removes somebody from a roster.
func (r *Registrations) HasWork(ctx context.Context, registrationID uuid.UUID) (bool, error) {
	var has bool
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM query_log WHERE registration_id = $1)
		    OR EXISTS (SELECT 1 FROM submissions WHERE registration_id = $1)
		    OR EXISTS (SELECT 1 FROM participant_events WHERE registration_id = $1)
		    OR EXISTS (SELECT 1 FROM participant_notes WHERE registration_id = $1)
		    OR EXISTS (SELECT 1 FROM participant_sql_tabs WHERE registration_id = $1)`,
		registrationID).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("look for a participant's record: %w", err)
	}
	return has, nil
}

// SetStatus changes a registration's status.
func (r *Registrations) SetStatus(ctx context.Context, registrationID uuid.UUID, status string) error {
	tag, err := r.querier(ctx).Exec(ctx,
		`UPDATE registrations SET status = $2 WHERE id = $1`, registrationID, status)
	if err != nil {
		return fmt.Errorf("set participant status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrParticipantNotFound
	}
	return nil
}

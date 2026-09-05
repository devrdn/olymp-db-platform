package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Registrations implements contests.RegistrationRepository.
var _ contests.RegistrationRepository = (*Registrations)(nil)

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

func scanParticipant(row pgx.Row) (contests.Participant, error) {
	var p contests.Participant
	err := row.Scan(&p.ID, &p.ContestID, &p.UserID, &p.Login, &p.FullName, &p.Status,
		&p.StartedAt, &p.FinishedAt, &p.TotalScore, &p.CreatedAt)
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
func (r *Registrations) Start(ctx context.Context, registrationID uuid.UUID, now time.Time) (contests.Participant, error) {
	querier := r.querier(ctx)
	p, err := scanParticipant(querier.QueryRow(ctx, `
		WITH updated AS (
			UPDATE registrations
			SET started_at = $2, status = $3
			WHERE id = $1 AND started_at IS NULL
			RETURNING *
		)
		SELECT `+participantColumns+`
		FROM updated r JOIN users u ON u.id = r.user_id`,
		registrationID, now, contests.RegistrationActive))
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

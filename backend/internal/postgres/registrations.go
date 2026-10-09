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
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Registrations implements contests.RegistrationRepository.
var _ contests.RegistrationRepository = (*Registrations)(nil)

// Registrations also answers queryproxy's combined lookups, one round trip
// each.
var _ queryproxy.Lookup = (*Registrations)(nil)

// participantColumns joins the account so a roster shows logins and names.
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

// participantScanTargets returns pointers in participantColumns' order, shared
// with wider projections such as ForRun.
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

// participantListFrom is Registrations.List's filter, with the contest,
// status and search as $1 to $3. The page and the count past the end share it.
const participantListFrom = `
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.contest_id = $1
		  AND ($2 = '' OR r.status = $2)
		  AND ($3 = '' OR u.login ILIKE '%' || $3 || '%' OR u.full_name ILIKE '%' || $3 || '%')`

// List returns a page of the contest's participants and the total, counted
// separately only for a page past the end.
func (r *Registrations) List(ctx context.Context, contestID uuid.UUID, f contests.ParticipantFilter) ([]contests.Participant, int, error) {
	// The search lands in an ILIKE pattern (CLAUDE.md rule 3).
	args := []any{contestID, f.Status, escapeLike(f.Query)}
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT `+participantColumns+`, COUNT(*) OVER() AS total`+participantListFrom+`
		ORDER BY u.login
		LIMIT $4 OFFSET $5`,
		append(args, f.Limit, f.Offset)...)
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
		if err := rows.Scan(append(participantScanTargets(&p), &total)...); err != nil {
			return nil, 0, fmt.Errorf("scan participant: %w", err)
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list participants: %w", err)
	}
	if len(found) == 0 && f.Offset > 0 {
		if err := r.querier(ctx).QueryRow(ctx,
			`SELECT COUNT(*)`+participantListFrom, args...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count participants: %w", err)
		}
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

// ForRun implements queryproxy.Lookup: the participant, the contest, its game
// and the participant's own copy of it, in one round trip.
//
// A registration's foreign key guarantees its contest exists, so "never
// registered" and "no such contest" are both ErrParticipantNotFound. The
// template and instance are LEFT JOINed: a contest with no ready template
// reads as provisioning.ErrNoGame, a registration with no copy yet as
// provisioning.ErrNoInstance. registration_id is unique in game_instances, so
// the join cannot multiply the row.
func (r *Registrations) ForRun(ctx context.Context, contestID, userID uuid.UUID) (queryproxy.LookupResult, error) {
	var (
		p                                 contests.Participant
		c                                 contests.Contest
		settings, languages, translations []byte
		templateDB                        *string
		version                           *int
		mode                              string
		policy                            sqlpolicy.Policy
		instanceDB, instanceStatus        *string
		instanceVersion                   *int
	)
	targets := participantScanTargets(&p)
	targets = append(targets, contestScanTargets(&c, &settings, &languages, &translations)...)
	targets = append(targets, &templateDB, &version)
	targets = append(targets, policyScanTargets(&policy, &mode)...)
	targets = append(targets, &instanceDB, &instanceVersion, &instanceStatus)

	err := r.querier(ctx).QueryRow(ctx, `
		SELECT `+participantColumns+`,
		       `+contestColumns+`,
		       t.template_db, t.version,
		       `+policyProjectionColumns+`,
		       gi.db_name, gi.template_version, gi.status
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		JOIN contests c ON c.id = r.contest_id
		LEFT JOIN game_templates t ON t.contest_id = c.id AND t.status = 'ready'
		LEFT JOIN contest_sql_policies p ON p.contest_id = c.id
		LEFT JOIN game_instances gi ON gi.registration_id = r.id
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
	if instanceDB == nil {
		result.InstanceErr = provisioning.ErrNoInstance
	} else {
		result.Instance = provisioning.Instance{
			Database: *instanceDB, TemplateVersion: *instanceVersion, Status: *instanceStatus,
		}
	}
	if templateDB == nil {
		result.GameErr = provisioning.ErrNoGame
		return result, nil
	}
	policy.Mode = sqlpolicy.Mode(mode)
	result.Game = provisioning.Contest{
		ID:       contest.ID,
		Template: *templateDB,
		Version:  *version,
		Policy:   policy,
	}
	return result, nil
}

// ForAccess implements queryproxy.Lookup: the participant and the contest in
// one round trip, for read endpoints that need no game. Not-found is answered
// as in ForRun.
func (r *Registrations) ForAccess(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error) {
	var (
		p                                 contests.Participant
		c                                 contests.Contest
		settings, languages, translations []byte
	)
	targets := append(participantScanTargets(&p), contestScanTargets(&c, &settings, &languages, &translations)...)
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT `+participantColumns+`,
		       `+contestColumns+`
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		JOIN contests c ON c.id = r.contest_id
		WHERE r.contest_id = $1 AND r.user_id = $2`, contestID, userID).Scan(targets...)
	if errors.Is(err, pgx.ErrNoRows) {
		return contests.Participant{}, contests.Contest{}, contests.ErrParticipantNotFound
	}
	if err != nil {
		return contests.Participant{}, contests.Contest{}, fmt.Errorf("scan participant and contest: %w", err)
	}
	contest, err := hydrate(c, settings, languages, translations)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	return p, contest, nil
}

// EnrolledIn reports which of these contests the user is registered for, in
// one query for a whole page. Contests the user is not registered for have no
// key. The HTTP layer passes the authenticated user, never one from the
// request.
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

// Add registers a user for a contest. A missing contest or account is
// contests.ErrNotFound or users.ErrNotFound, decided by the foreign keys at
// the insert.
//
// An existing registration is contests.ErrAlreadyEnrolled, decided by the
// unique (contest_id, user_id) through ON CONFLICT DO NOTHING rather than a
// failed insert: a failed statement would abort the caller's transaction, and
// the roster import skips this row and continues in the same transaction.
func (r *Registrations) Add(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error) {
	p, err := scanParticipant(r.querier(ctx).QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO registrations (contest_id, user_id) VALUES ($1, $2)
			ON CONFLICT (contest_id, user_id) DO NOTHING
			RETURNING *
		)
		SELECT `+participantColumns+`
		FROM inserted r JOIN users u ON u.id = r.user_id`, contestID, userID))
	if err != nil {
		// No row can only be the conflict, as long as the join filters
		// nothing. A filter added to it would be misreported as "already
		// enrolled".
		if errors.Is(err, contests.ErrParticipantNotFound) {
			return contests.Participant{}, contests.ErrAlreadyEnrolled
		}
		return contests.Participant{}, missingParent(err, map[string]error{
			"registrations_contest_id_fkey": contests.ErrNotFound,
			"registrations_user_id_fkey":    users.ErrNotFound,
		})
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

// Start records now as the participant's start, if they have not begun yet.
//
// The UPDATE's WHERE re-reads started_at under the row lock, so of two racing
// transactions only the first sets it. After the first call it writes
// nothing (CLAUDE.md rule 6). It also requires status = 'registered', an
// allow-list, so a participant disqualified before starting, or in a status
// added later, is not moved to 'active'.
//
// The loser reads the winner's start time in a separate statement. Under
// READ COMMITTED each statement takes its own snapshot; a SELECT folded into
// the UPDATE's statement would see started_at still null. This relies on the
// pool running at READ COMMITTED: under REPEATABLE READ or SERIALIZABLE the
// second statement would also see the old snapshot.
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
		// Lost the race or already started: a fresh statement sees the
		// committed start.
		return scanParticipant(querier.QueryRow(ctx, `
			SELECT `+participantColumns+`
			FROM registrations r JOIN users u ON u.id = r.user_id
			WHERE r.id = $1`, registrationID))
	default:
		return contests.Participant{}, err
	}
}

// AddScore adds delta to the registration's total_score in one UPDATE, so
// concurrent submissions serialise on the row instead of overwriting each
// other's addition.
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

// HasWork reports whether the participant left any record under this
// registration. Each table has an index leading with registration_id (the
// notes their primary key), so each EXISTS is one index probe.
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

// RegisteredWithPermission names this contest's participants whose account
// holds the permission through its current roles, for the publish gate.
// EXISTS rather than a join, so an account with several such roles appears
// once.
func (r *Registrations) RegisteredWithPermission(ctx context.Context, contestID uuid.UUID, permission string) ([]string, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT u.login
		FROM registrations r
		JOIN users u ON u.id = r.user_id
		WHERE r.contest_id = $1
		  AND EXISTS (
		      SELECT 1 FROM user_roles ur
		      JOIN role_permissions rp ON rp.role_id = ur.role_id
		      JOIN permissions p ON p.id = rp.permission_id
		      WHERE ur.user_id = u.id AND p.code = $2
		  )
		ORDER BY u.login
		LIMIT $3`,
		contestID, permission, contests.MaxReportedStaff)
	if err != nil {
		return nil, fmt.Errorf("find participants holding %s: %w", permission, err)
	}
	defer rows.Close()

	logins, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan participants holding %s: %w", permission, err)
	}
	return logins, nil
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

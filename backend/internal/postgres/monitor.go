package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Monitor stores what is recorded about a participant beyond their queries
// and answers: participant_events and workspace_revisions.
//
// Both methods join the caller's transaction when the context carries one, so
// a workspace save commits its revision atomically; without one,
// RecordRevision opens its own and InsertEvents is a single statement.
type Monitor struct {
	pool *pgxpool.Pool
	uow  *storage.PgxUnitOfWork
}

// NewMonitor returns the monitoring store over pool.
func NewMonitor(pool *pgxpool.Pool) *Monitor {
	return &Monitor{pool: pool, uow: storage.NewUnitOfWork(pool)}
}

func (m *Monitor) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, m.pool)
}

// InsertEvents stores a batch of events in one statement, in the order given,
// or none of them.
//
// Every event is normalised again here so the jsonb bounds do not depend on
// the caller. A batch over monitor.MaxBatchEvents, or with an event that
// cannot be stored, is refused whole with the domain's sentinel; a caller
// that wants to drop odd events filters the batch first.
func (m *Monitor) InsertEvents(ctx context.Context, events []monitor.Event) error {
	if err := monitor.CheckBatch(events); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	if err := m.checkStored(ctx, events); err != nil {
		return err
	}

	var (
		contests      = make([]uuid.UUID, len(events))
		registrations = make([]uuid.UUID, len(events))
		kinds         = make([]string, len(events))
		payloads      = make([]string, len(events))
		claimed       = make([]*time.Time, len(events))
	)
	for i, event := range events {
		event, err := event.Normalize()
		if err != nil {
			return fmt.Errorf("event %d of the batch: %w", i, err)
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return fmt.Errorf("encode the payload of event %d: %w", i, err)
		}
		contests[i] = event.Contest
		registrations[i] = event.Registration
		kinds[i] = string(event.Kind())
		payloads[i] = string(payload)
		claimed[i] = event.ClientAt
	}

	// WITH ORDINALITY and ORDER BY assign ids in batch order, so reading by id
	// follows the events as they happened.
	if _, err := m.querier(ctx).Exec(ctx, `
		INSERT INTO participant_events (contest_id, registration_id, kind, payload, client_at)
		SELECT contest_id, registration_id, kind, payload::jsonb, client_at
		FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $5::timestamptz[])
		     WITH ORDINALITY AS batch (contest_id, registration_id, kind, payload, client_at, position)
		ORDER BY position`,
		contests, registrations, kinds, payloads, claimed); err != nil {
		return fmt.Errorf("store %d participant events: %w", len(events), err)
	}
	return nil
}

// checkStored refuses a batch whose browser events would take a registration
// past monitor.MaxStoredEvents stored events.
//
// Only browser events are refused (monitor.Kind.FromBrowser). Server-observed
// events (an address change, a second session, a tab change) are always
// stored, or a participant could fill the budget and then act unrecorded;
// those are bounded where they are written. They still count toward the
// total, which only costs the browser.
//
// It reads registration_activity by primary key rather than counting
// participant_events, the table the limit bounds. No summary row means
// nothing stored. Two racing batches can overshoot by one batch; the limit
// bounds table size, so that is not worth a lock across the insert.
func (m *Monitor) checkStored(ctx context.Context, events []monitor.Event) error {
	arriving := make(map[uuid.UUID]int, len(events))
	for _, event := range events {
		if event.Payload.Kind().FromBrowser() {
			arriving[event.Registration]++
		}
	}
	if len(arriving) == 0 {
		return nil
	}
	registrations := make([]uuid.UUID, 0, len(arriving))
	for registration := range arriving {
		registrations = append(registrations, registration)
	}

	rows, err := m.querier(ctx).Query(ctx, `
		SELECT registration_id, events FROM registration_activity
		WHERE registration_id = ANY($1::uuid[])`, registrations)
	if err != nil {
		return fmt.Errorf("read how much %d registrations have stored: %w", len(registrations), err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			registration uuid.UUID
			stored       int64
		)
		if err := rows.Scan(&registration, &stored); err != nil {
			return fmt.Errorf("read how much a registration has stored: %w", err)
		}
		if stored+int64(arriving[registration]) > monitor.MaxStoredEvents {
			return fmt.Errorf("registration %s has stored %d events: %w",
				registration, stored, monitor.ErrTooManyEvents)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read how much %d registrations have stored: %w", len(registrations), err)
	}
	return nil
}

// RecordRevision folds one save of a document into its history:
//
//   - a body equal to the latest revision's writes nothing;
//   - a save within monitor.RevisionWindow of the latest revision's start
//     (monitor.Extends) rewrites it in place; updated_at only moves forward,
//     so a clock stepping back never puts it before started_at;
//   - anything else starts a new revision.
//
// The latest revision is read FOR UPDATE, so concurrent saves take turns. Two
// racing first saves can each insert a revision; that adds an extra revision,
// never loses state, and is not worth a lock on every save.
func (m *Monitor) RecordRevision(ctx context.Context, revision monitor.Revision) error {
	if err := revision.Validate(); err != nil {
		return err
	}
	return m.uow.Do(ctx, func(ctx context.Context) error {
		querier := m.querier(ctx)

		// The database compares the bodies: they are up to 64 KiB and
		// autosave is frequent, so reading one back to compare is wasteful.
		var (
			latest    int64
			unchanged bool
			startedAt time.Time
		)
		err := querier.QueryRow(ctx, `
			SELECT id, body = $3, started_at
			FROM workspace_revisions
			WHERE registration_id = $1 AND document = $2
			ORDER BY id DESC
			LIMIT 1
			FOR UPDATE`,
			revision.Registration, revision.Document, revision.Body).Scan(&latest, &unchanged, &startedAt)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return m.insertRevision(ctx, querier, revision)
		case err != nil:
			return fmt.Errorf("read the latest revision of %s: %w", revision.Document, err)
		case unchanged:
			return nil
		case monitor.Extends(startedAt, revision.At):
			if _, err := querier.Exec(ctx, `
				UPDATE workspace_revisions
				SET body = $2, title = nullif($3, ''), updated_at = greatest(updated_at, $4)
				WHERE id = $1`,
				latest, revision.Body, revision.Title, revision.At); err != nil {
				return fmt.Errorf("extend revision %d: %w", latest, err)
			}
			return nil
		default:
			return m.insertRevision(ctx, querier, revision)
		}
	})
}

func (m *Monitor) insertRevision(ctx context.Context, querier storage.Querier, revision monitor.Revision) error {
	if _, err := querier.Exec(ctx, `
		INSERT INTO workspace_revisions (registration_id, document, title, body, started_at, updated_at)
		VALUES ($1, $2, nullif($3, ''), $4, $5, $5)`,
		revision.Registration, revision.Document, revision.Title, revision.Body, revision.At); err != nil {
		return fmt.Errorf("start a revision of %s: %w", revision.Document, err)
	}
	return nil
}

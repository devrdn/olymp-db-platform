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
// and answers: participant_events and workspace_revisions (migration 000033).
//
// Both methods run inside the caller's transaction when the context carries
// one (storage.QuerierFrom), which is how a workspace save writes its
// revision atomically with the save itself; without one, RecordRevision opens
// its own, and InsertEvents is a single statement.
//
// The interfaces over this type are declared by its consumers, each with the
// methods it uses (CLAUDE.md, Go layout rule 3).
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

// InsertEvents stores a batch of events in one multi-row insert, in the order
// given, or none of them.
//
// Every event is normalised here again (monitor.Event.Normalize) rather than
// trusted to have been: the bounds are what keeps the table's jsonb from
// holding whatever a browser sent, and they must not depend on each caller
// remembering them. A batch larger than monitor.MaxBatchEvents, or holding an
// event that cannot be stored, is refused whole with the domain's sentinel;
// a caller that wants to drop the odd event (a too-short absence) filters the
// batch first.
//
// One statement whatever the batch size: the columns travel as five arrays
// and are unnested server-side, so a batch costs one round trip and one
// statement plan.
func (m *Monitor) InsertEvents(ctx context.Context, events []monitor.Event) error {
	if err := monitor.CheckBatch(events); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
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

	// WITH ORDINALITY and the ORDER BY keep the ids in the order the batch
	// gave, so a timeline read by id reads the events as they happened.
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

// RecordRevision folds one save of a document into its history
// (design §2.4):
//
//   - a body equal to the latest revision's writes nothing;
//   - a save while the latest revision is younger than monitor.RevisionWindow
//     (monitor.Extends) rewrites that revision in place — its body, its title
//     and its updated_at, which only ever moves forward, so a clock that
//     stepped back never leaves it before started_at;
//   - anything else starts a new revision.
//
// The latest revision is read FOR UPDATE, so two saves of the same document
// in separate transactions take turns rather than both rewriting it. Two
// first saves of a document racing each other can each find no revision and
// each insert one; that costs an extra revision in the history, never a lost
// state, and is not worth a lock on every save.
//
// Within the caller's transaction when there is one, so the revision commits
// or rolls back with the save it records.
func (m *Monitor) RecordRevision(ctx context.Context, revision monitor.Revision) error {
	if err := revision.Validate(); err != nil {
		return err
	}
	return m.uow.Do(ctx, func(ctx context.Context) error {
		querier := m.querier(ctx)

		// The comparison is made by the database: the body is up to 64 KiB
		// and autosave may ask forty times a minute, so reading it back only
		// to compare would move every byte twice for a yes or a no.
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

// insertRevision starts a new revision at revision.At.
func (m *Monitor) insertRevision(ctx context.Context, querier storage.Querier, revision monitor.Revision) error {
	if _, err := querier.Exec(ctx, `
		INSERT INTO workspace_revisions (registration_id, document, title, body, started_at, updated_at)
		VALUES ($1, $2, nullif($3, ''), $4, $5, $5)`,
		revision.Registration, revision.Document, revision.Title, revision.Body, revision.At); err != nil {
		return fmt.Errorf("start a revision of %s: %w", revision.Document, err)
	}
	return nil
}

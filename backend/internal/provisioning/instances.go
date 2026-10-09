package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// Why an organizer's request about one database could not be served.
var (
	// ErrInstanceNotFound is a database name that is not one of this
	// contest's, including one that belongs to another contest, so a
	// contest-scoped permission does not leak other contests' rows.
	ErrInstanceNotFound = errors.New("no such database in this contest")
	// ErrInstanceAlreadyDropped is a row whose database is already gone, by the
	// reclaim sweep or another organizer. It tells a stale list to reload,
	// unlike ErrInstanceNotFound.
	ErrInstanceAlreadyDropped = errors.New("the database is already dropped")
)

// MaxInstancesListed bounds one read of a contest's databases (CLAUDE.md rule
// 2): dropped rows are kept as history, so a contest's rows are unbounded.
// 500 is well past one olympiad's roster.
const MaxInstancesListed = 500

// MaxDatabaseNameBytes is PostgreSQL's identifier limit, and the bound on the
// database name this package takes from a URL.
const MaxDatabaseNameBytes = 63

// InstanceRecord is one row of game_instances as an organizer sees it: which
// database, whose it is, what it was made from and whether it is still there.
type InstanceRecord struct {
	Database string
	// Registration is nil for an unclaimed spare copy.
	Registration *uuid.UUID
	// ParticipantLogin and ParticipantName identify the holder. Both are
	// empty for a spare and for a participant whose account was deleted.
	ParticipantLogin string
	ParticipantName  string
	TemplateVersion  int
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// SizeBytes is the database's disk size; SizeKnown is false when the game
	// cluster could not be asked, so zero never means both "empty" and "unknown".
	SizeBytes int64
	SizeKnown bool
}

// Spare reports whether the copy is still in the pool, waiting for whoever
// registers next.
func (r InstanceRecord) Spare() bool { return r.Registration == nil }

// Dropped reports whether the database is gone, the row kept as history.
func (r InstanceRecord) Dropped() bool { return r.Status == InstanceStatusDropped }

// InstanceList is one contest's databases, and whether the list was cut at
// MaxInstancesListed.
type InstanceList struct {
	Instances []InstanceRecord
	Truncated bool
}

// Instances lists one contest's databases: spares, participants' copies and
// rows of dropped databases.
//
// Sizes are best effort: if the game cluster is unreachable every SizeKnown
// is false and the list is still served, since the rows come from the core
// database and matter most when the cluster is sick.
func (s *Service) Instances(ctx context.Context, contestID uuid.UUID) (InstanceList, error) {
	// One more than the bound, so Truncated is a fact, not a guess.
	rows, err := s.repo.Instances(ctx, contestID, MaxInstancesListed+1)
	if err != nil {
		return InstanceList{}, fmt.Errorf("list the contest's databases: %w", err)
	}

	list := InstanceList{Instances: rows}
	if len(rows) > MaxInstancesListed {
		list.Instances, list.Truncated = rows[:MaxInstancesListed], true
	}

	s.fillSizes(ctx, list.Instances)
	return list, nil
}

// fillSizes asks the cluster for the size of each database that still
// exists. Dropped rows are skipped: pg_database_size on a missing name is an
// error that would fail the whole batch.
func (s *Service) fillSizes(ctx context.Context, records []InstanceRecord) {
	names := make([]string, 0, len(records))
	for _, r := range records {
		if !r.Dropped() {
			names = append(names, r.Database)
		}
	}
	if len(names) == 0 {
		return
	}

	sizes, err := s.cluster.DatabaseSizes(ctx, names)
	if err != nil {
		return
	}
	for i, r := range records {
		if size, ok := sizes[r.Database]; ok {
			records[i].SizeBytes, records[i].SizeKnown = size, true
		}
	}
}

// DropInstance removes one of a contest's databases at an organizer's
// request, and records who did it.
//
// Unlike Reclaim it uses Drop (WITH FORCE), not DropIdle. The Query Runner
// keeps a participant's connection between queries, so a participant retrying
// against a broken copy would make DropIdle refuse for as long as they keep
// trying. The cost is at most the one query in flight; the participant's next
// action rebuilds the database under the same name (Ensure, rebuildExisting).
// Submissions, score and timing live in the core database and are untouched.
//
// The row is marked after the cluster drop, as in Reclaim. A failure between
// the two leaves 'ready' over a missing database, which a second press
// repairs (DROP DATABASE IF EXISTS). Marking first would leave 'dropped' over
// a live database, which Reclaimable skips, leaking the disk for good.
func (s *Service) DropInstance(ctx context.Context, actorID, contestID uuid.UUID, database string) (InstanceRecord, error) {
	// Bounded before storage (CLAUDE.md rule 2). A longer name cannot be a
	// database, so it is simply not found.
	if len(database) == 0 || len(database) > MaxDatabaseNameBytes {
		return InstanceRecord{}, ErrInstanceNotFound
	}

	record, err := s.repo.InstanceNamed(ctx, contestID, database)
	if err != nil {
		return InstanceRecord{}, err
	}
	if record.Dropped() {
		return InstanceRecord{}, ErrInstanceAlreadyDropped
	}

	if err := s.cluster.Drop(ctx, record.Database); err != nil {
		return InstanceRecord{}, fmt.Errorf("drop %s: %w", record.Database, err)
	}

	if err := s.markDropped(ctx, actorID, contestID, record); err != nil {
		return InstanceRecord{}, err
	}

	record.Status = InstanceStatusDropped
	return record, nil
}

// markDropped writes the row and its audit entry in one core-database
// transaction, so both land or neither does.
func (s *Service) markDropped(ctx context.Context, actorID, contestID uuid.UUID, record InstanceRecord) error {
	write := func(ctx context.Context) error {
		if err := s.repo.MarkDropped(ctx, record.Database); err != nil {
			return err
		}
		if s.audit == nil {
			return nil
		}
		return s.audit.Record(ctx, dropEntry(actorID, contestID, record))
	}

	if s.audit == nil || s.uow == nil {
		return write(ctx)
	}
	return s.uow.Do(ctx, write)
}

// dropEntry records an organizer's removal of a database on the contest's
// trail. It has its own action code, apart from the sweep's actorless
// ActionGameInstanceReclaim, so the trail can tell the two apart.
func dropEntry(actorID, contestID uuid.UUID, record InstanceRecord) audit.Entry {
	payload := map[string]any{
		"database":         record.Database,
		"template_version": record.TemplateVersion,
		"spare":            record.Spare(),
	}
	if record.Registration != nil {
		payload["registration_id"] = record.Registration.String()
	}
	if record.ParticipantLogin != "" {
		payload["participant"] = record.ParticipantLogin
	}
	return audit.Entry{
		ActorID:  &actorID,
		Action:   audit.ActionGameInstanceDrop,
		Entity:   "contest",
		EntityID: contestID.String(),
		Payload:  payload,
	}
}

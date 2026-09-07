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
	// contest's. Also the answer when the name belongs to another contest:
	// that a database exists elsewhere is not the caller's business, and a
	// contest-scoped permission that leaked the existence of another
	// contest's rows would not be contest-scoped at all.
	ErrInstanceNotFound = errors.New("no such database in this contest")
	// ErrInstanceAlreadyDropped is a row whose database is already gone — the
	// reclaim sweep took it, or another organizer pressed the same button
	// while this page was open. Named apart from ErrInstanceNotFound because
	// the two are different sentences to whoever is looking at a stale list:
	// one means "reload, it is already gone", the other means "that is not a
	// database of this contest".
	ErrInstanceAlreadyDropped = errors.New("the database is already dropped")
)

// MaxInstancesListed bounds one read of a contest's databases (CLAUDE.md rule
// 2). A contest owns one row per participant plus its spare pool, and the
// table keeps every dropped row for ever as history, so "every row of this
// contest" is unbounded by construction — an olympiad run three times on one
// installation has three generations of rows under the same contest only if
// somebody re-ran it, but the reclaimed rows of a long contest alone can
// outnumber its participants.
//
// 500 is well past a single olympiad's roster (§4.2 sizes the pool in spares
// per contest, not hundreds) and small enough that the whole answer is one
// modest JSON document. Past it the list says so rather than silently
// stopping, so nobody reads a short list as "that is all there is".
const MaxInstancesListed = 500

// MaxDatabaseNameBytes is PostgreSQL's own identifier limit, and the bound on
// the one free-text field this package takes from a URL. instanceName and
// spareName are built to fit inside it (their own doc); nothing longer has
// ever been a database on the cluster.
const MaxDatabaseNameBytes = 63

// InstanceRecord is one row of game_instances as an organizer sees it: which
// database, whose it is, what it was made from and whether it is still there.
//
// Wider than Instance, which is what Ensure hands the console — that one
// answers "where do I run this participant's query", and nothing on this
// screen is on that path.
type InstanceRecord struct {
	Database string
	// Registration is nil for a spare copy nobody has claimed — Stale's own
	// convention, because it is the same fact about the same column.
	Registration *uuid.UUID
	// ParticipantLogin and ParticipantName identify the holder. Both empty
	// for a spare, and for a participant whose account was deleted: a row
	// outlives the person on it, and inventing a name for somebody who is
	// gone would be inventing a record (audit.Record.EntityLabel's own rule).
	ParticipantLogin string
	ParticipantName  string
	TemplateVersion  int
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// SizeBytes is how much disk the database occupies, and SizeKnown says
	// whether it was read at all. Two fields rather than a zero that means
	// both "empty" and "not asked": the sizes come from the game cluster,
	// which is a second system that can be down while the rows this screen
	// lists are perfectly readable.
	SizeBytes int64
	SizeKnown bool
}

// Spare reports whether the copy is still in the pool, waiting for whoever
// registers next.
func (r InstanceRecord) Spare() bool { return r.Registration == nil }

// Dropped reports whether the database behind this row is already gone from
// the cluster, the row surviving only as history (Repository.MarkDropped).
func (r InstanceRecord) Dropped() bool { return r.Status == InstanceStatusDropped }

// InstanceList is one contest's databases, and whether that is all of them.
//
// Truncated rather than a page cursor: this screen exists so an organizer can
// find one database, not to page through history, and a list that quietly
// stopped at five hundred would be read as "that is all there is".
type InstanceList struct {
	Instances []InstanceRecord
	Truncated bool
}

// Instances lists one contest's databases: its spare pool, its participants'
// own copies, and the rows left behind by whatever has already been dropped.
//
// Sizes are read from the game cluster in one round trip and are best effort.
// A cluster that cannot be reached leaves every SizeKnown false and the list
// is served anyway: the rows come from the core database, they are what an
// organizer came here for, and refusing the whole screen because a decorative
// column could not be filled would deny them the one page that says what
// exists — exactly when a sick cluster makes them need it.
func (s *Service) Instances(ctx context.Context, contestID uuid.UUID) (InstanceList, error) {
	// One more than the bound, so "there are more" is a fact rather than the
	// guess `len(rows) == limit` would be for a contest with exactly that
	// many.
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

// fillSizes asks the cluster how large each database that still exists is.
//
// A dropped row is not asked about: its database is gone, and pg_database_size
// on a name that is not there is an error rather than a zero — one that would
// otherwise cost the whole batch its sizes.
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
// # Why this forces connections closed where the reclaim sweep never does
//
// Cluster.Drop and Cluster.DropIdle differ in exactly one way: Drop passes
// PostgreSQL's WITH (FORCE) and severs whatever is connected, DropIdle takes
// PostgreSQL's refusal ("database is being accessed by other users") as its
// answer and reports "not dropped, try later". The reclaim sweep uses
// DropIdle and this uses Drop, and the difference is not caution against
// recklessness — it is that the two callers know different things.
//
// The sweep is a timer. Nobody asked it for anything, nothing is waiting on
// its result, and it cannot tell a forgotten psql session from a query the
// Query Runner admitted a second ago; leaving a busy database for the next
// tick costs nothing, because there is always a next tick. This is a person
// pressing a button on a database they have decided is broken, usually in the
// middle of an olympiad with a participant sitting in front of a copy that
// does not work. "Somebody is connected, try again later" is not an answer
// they can act on: connections to a game database are opened per query (§4.3),
// so a participant retrying against a broken copy holds one almost
// continuously, and the case that most needs this button is precisely the one
// where DropIdle would refuse for as long as they keep trying. An organizer
// who cannot fix a broken database is a worse outcome than a lost query, and
// a refusal with no way to ever succeed is worse still.
//
// # What is actually lost
//
// The most a participant loses is the one query in flight, which comes back
// as an error they can run again — and the result they lost would have come
// from the copy their organizer has just judged broken. Their next action
// rebuilds the database: Ensure reads a row in status 'dropped' and repairs
// it through rebuildExisting under its own name, so nothing that points at
// that name goes stale.
//
// Nothing else of theirs is touched, and this is the guarantee that makes the
// button safe to offer at all: their answers (submissions), their score and
// their clock (registrations.total_score, started_at, finished_at) live in
// the core database, and the only core-database write here is the instance
// row's own status. An olympiad is not replayed because a database was
// remade.
//
// The row is marked after the cluster has actually dropped, the same order
// Reclaim uses. A failure between the two leaves the row saying 'ready' over
// a database that is gone, which the same button repairs on a second press —
// the lookup finds the row, DROP DATABASE IF EXISTS is content that it is
// already gone, and the mark succeeds. Marking first would leave the opposite
// inconsistency, a row saying 'dropped' over a database that is still there,
// and that one nothing repairs: Reclaimable skips dropped rows, so the disk
// would be leaked for good.
func (s *Service) DropInstance(ctx context.Context, actorID, contestID uuid.UUID, database string) (InstanceRecord, error) {
	// The name comes off a URL and lands in a WHERE clause, so it is bounded
	// before it reaches storage (CLAUDE.md rule 2). PostgreSQL cannot hold an
	// identifier longer than this, so anything longer names nothing that
	// could ever have been a database — reported as "not this contest's"
	// rather than as its own refusal, because to the caller it is the same
	// fact.
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
// transaction, the same arrangement markReclaimed uses and for the same
// reason audit's package doc gives: the record and the action it describes
// either both land or neither does.
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

// dropEntry is one organizer's removal of a database, in the shape every
// other contest-scoped entry takes: entity "contest", so it surfaces on that
// contest's own trail, with the database named in the payload because that
// string is what anybody looking for it afterwards actually has in hand.
//
// Its own action code rather than ActionGameInstanceReclaim's: the sweep's
// entry has no actor and means "the grace ran out", this one names a person
// and means "somebody decided this copy was broken". A trail that folded them
// together could not answer which.
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

package provisioning

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// DatabaseRecord is one database the core database knows about, and its
// status. Instances and templates share the shape so that liveness is decided
// over one set of names.
type DatabaseRecord struct {
	Database  string
	Status    string
	ContestID uuid.UUID
	Template  bool
}

// Live reports whether this row still claims a database on the cluster.
// Every status but 'dropped' does: 'building' or 'failed' may have a
// half-built database behind it.
func (r DatabaseRecord) Live() bool { return r.Status != InstanceStatusDropped }

// Orphan is a database recorded as dropped that is still on the game cluster.
type Orphan struct {
	Database  string
	ContestID uuid.UUID
	Template  bool
	SizeBytes int64
}

// OrphanStore lists every recorded database of the installation in one read.
type OrphanStore interface {
	RecordedDatabases(ctx context.Context) ([]DatabaseRecord, error)
}

// OrphanCluster is the game cluster as the orphan sweep sees it. It has no
// Drop (WITH FORCE), so the sweep cannot cut off a connection by construction.
type OrphanCluster interface {
	// DatabaseSizes reports the size of each name that exists and omits the
	// rest, so it doubles as the existence check.
	DatabaseSizes(ctx context.Context, names []string) (map[string]int64, error)
	// DropIdle removes a database only if nobody is connected to it, and says
	// whether it did.
	DropIdle(ctx context.Context, name string) (dropped bool, err error)
}

// OrphanSweeper finds and removes databases that the core database records as
// 'dropped' but that are still on the game cluster. Reclaim skips dropped rows,
// so nothing else ever removes them.
//
// It is run by hand from cmd/gameorphans and not offered by the API: deciding
// a database is safe to destroy needs someone who can look at the cluster.
type OrphanSweeper struct {
	store   OrphanStore
	cluster OrphanCluster
	// audit is nil only in tests; the sweep then records nothing.
	audit *audit.Recorder
}

// NewOrphanSweeper assembles the sweep.
func NewOrphanSweeper(store OrphanStore, cluster OrphanCluster) *OrphanSweeper {
	return &OrphanSweeper{store: store, cluster: cluster}
}

// WithAudit lets the sweep record what it removed. It needs no unit of work:
// the row already says 'dropped', so the entry is the only core-database write.
func (s *OrphanSweeper) WithAudit(rec *audit.Recorder) *OrphanSweeper {
	s.audit = rec
	return s
}

// OrphanSweepResult is one removal pass's outcome.
type OrphanSweepResult struct {
	// Removed counts databases dropped, Busy those left because something was
	// connected, Failed attempts that errored.
	Removed, Busy, Failed int
	// FreedBytes is the removed databases' size as measured by Find.
	FreedBytes int64
}

// Find returns the orphans to remove, decided only from the core database's
// rows, never from the cluster's list. A candidate is named by a 'dropped' row
// and by no live row. A database the core database never recorded is never a
// candidate: it may belong to another system.
//
// The result is a plan; cmd/gameorphans prints it and acts only when told to.
func (s *OrphanSweeper) Find(ctx context.Context) ([]Orphan, error) {
	recorded, err := s.store.RecordedDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the recorded databases: %w", err)
	}

	// Two passes: a name is a candidate only once every row naming it is seen.
	live := make(map[string]bool, len(recorded))
	for _, row := range recorded {
		if row.Live() {
			live[row.Database] = true
		}
	}

	candidates := make(map[string]DatabaseRecord)
	for _, row := range recorded {
		if row.Live() || live[row.Database] {
			continue
		}
		candidates[row.Database] = row
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)

	// A name missing from the answer is not on the cluster: the healthy case.
	sizes, err := s.cluster.DatabaseSizes(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("measure the recorded databases: %w", err)
	}

	var orphans []Orphan
	for _, name := range names {
		size, stillThere := sizes[name]
		if !stillThere {
			continue
		}
		row := candidates[name]
		orphans = append(orphans, Orphan{
			Database: name, ContestID: row.ContestID, Template: row.Template, SizeBytes: size,
		})
	}
	return orphans, nil
}

// Remove drops the given orphans one at a time. It takes the plan rather
// than calling Find again, so what the operator approved is what runs.
//
// A busy database is left alone and is not a failure. One failure does not
// stop the rest; every failure is joined into the returned error.
func (s *OrphanSweeper) Remove(ctx context.Context, orphans []Orphan) (OrphanSweepResult, error) {
	var result OrphanSweepResult
	var failures []error

	for _, orphan := range orphans {
		dropped, err := s.cluster.DropIdle(ctx, orphan.Database)
		if err != nil {
			result.Failed++
			failures = append(failures, fmt.Errorf("drop %s: %w", orphan.Database, err))
			continue
		}
		if !dropped {
			result.Busy++
			continue
		}
		result.Removed++
		result.FreedBytes += orphan.SizeBytes

		if err := s.recordRemoval(ctx, orphan); err != nil {
			// The database is gone, so this is not counted as Failed.
			failures = append(failures, fmt.Errorf("record %s removed: %w", orphan.Database, err))
		}
	}
	return result, errors.Join(failures...)
}

// recordRemoval writes the trail entry for one removed database, under the
// reclaim sweep's action codes, since the fact recorded is the same.
func (s *OrphanSweeper) recordRemoval(ctx context.Context, orphan Orphan) error {
	if s.audit == nil {
		return nil
	}
	action := audit.ActionGameInstanceReclaim
	if orphan.Template {
		action = audit.ActionGameTemplateReclaim
	}
	return s.audit.Record(ctx, audit.Entry{
		Action:   action,
		Entity:   "contest",
		EntityID: orphan.ContestID.String(),
		Payload:  map[string]any{"database": orphan.Database},
	})
}

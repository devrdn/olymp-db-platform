package provisioning

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// DatabaseRecord is one line of the core database's own account of the game
// cluster: a database it knows about, and what it believes has become of it.
//
// Instances and templates in one shape, told apart by Template, because the
// question an orphan sweep asks — "does anything still consider this database
// live?" — is the same question for both, and answering it per table would
// mean a name could be live in one answer and gone in the other.
type DatabaseRecord struct {
	Database  string
	Status    string
	ContestID uuid.UUID
	Template  bool
}

// Live reports whether this row still claims a database on the cluster.
//
// Everything except the terminal 'dropped' does: a template that is still
// 'building' or has 'failed' may well have a half-built database behind it,
// and 'pending' is a row whose database has not been made yet — none of them
// is a promise that the disk is free.
func (r DatabaseRecord) Live() bool { return r.Status != InstanceStatusDropped }

// Orphan is a database the core database has already given up on that is
// nevertheless still on the game cluster.
type Orphan struct {
	Database  string
	ContestID uuid.UUID
	Template  bool
	// SizeBytes is what it holds. The whole reason to remove it, and the
	// number an operator weighs before saying yes.
	SizeBytes int64
}

// OrphanStore is the core database's account of which databases exist, as the
// orphan sweep needs it: the whole installation, in one read.
type OrphanStore interface {
	RecordedDatabases(ctx context.Context) ([]DatabaseRecord, error)
}

// OrphanCluster is the game cluster as the orphan sweep is allowed to see it.
//
// Narrow on purpose, and narrower than Cluster above in exactly one way that
// matters: there is no Drop. Cluster.Drop removes a database WITH (FORCE),
// severing whoever is connected, and its two callers know that a connection
// left over is a forgotten one. This sweep knows no such thing — it is
// removing databases whose contest ended months ago, on an operator's say-so,
// with no idea what a live connection to one would mean. Leaving the forcing
// drop out of the interface is what makes "this job cannot cut anybody off" a
// property of the types rather than a promise in a comment.
type OrphanCluster interface {
	// DatabaseSizes reports the size of each name that exists, and simply
	// omits the ones that do not — which is what makes it the existence check
	// this sweep needs as well as the report it prints.
	DatabaseSizes(ctx context.Context, names []string) (map[string]int64, error)
	// DropIdle removes a database only if nobody is connected to it, and says
	// whether it did.
	DropIdle(ctx context.Context, name string) (dropped bool, err error)
}

// OrphanSweeper finds and removes databases that the core database records as
// 'dropped' but that are still on the game cluster.
//
// Nothing in the product will ever remove these: Reclaimable and
// ReclaimableTemplates both exclude a row that already says 'dropped'
// (internal/postgres/gameinstances.go), which is right — the sweep must not
// pay for a DROP DATABASE on every contest it has already tidied — and it
// means a row marked dropped over a database that survived is disk nobody
// will ever come back for. That happens when the mark and the drop come
// apart: a reclaim pass against a cluster that only pretended to drop
// (internal/provisioning's own tests did exactly this against the development
// installation until withRollback), or a future defect of the same shape.
//
// It is an operator's job, run by hand from cmd/gameorphans, and deliberately
// not something the API offers: deciding that a database on disk is safe to
// destroy needs someone who can look at the cluster, and an endpoint for it
// would be a way to lose data by clicking.
type OrphanSweeper struct {
	store   OrphanStore
	cluster OrphanCluster
	// audit is optional in the same sense Service's is: without it the sweep
	// still removes databases and simply records nothing, which is only ever
	// right in a test.
	audit *audit.Recorder
}

// NewOrphanSweeper assembles the sweep.
func NewOrphanSweeper(store OrphanStore, cluster OrphanCluster) *OrphanSweeper {
	return &OrphanSweeper{store: store, cluster: cluster}
}

// WithAudit lets the sweep record what it removed.
//
// No unit of work beside it, unlike Service.WithAudit: there is no
// core-database write to be atomic with. The row already says 'dropped' —
// that is what made the database an orphan — so all this repair changes is
// what is on the other cluster, and the entry is the only trace it leaves in
// the core database at all.
func (s *OrphanSweeper) WithAudit(rec *audit.Recorder) *OrphanSweeper {
	s.audit = rec
	return s
}

// OrphanSweepResult is one removal pass's outcome.
type OrphanSweepResult struct {
	// Removed counts databases actually gone from the cluster; Busy counts
	// the ones something was connected to, left exactly as they were; Failed
	// counts attempts that errored outright.
	Removed, Busy, Failed int
	// FreedBytes is how much disk the removed databases held, as measured
	// when the plan was drawn up.
	FreedBytes int64
}

// Find is the whole decision, and it is made in one direction only: from the
// rows the core database holds, never from the list of databases on the
// cluster.
//
// A candidate has to be named by a row that says 'dropped', and must not be
// named by any row that still says anything else — the second condition is
// what keeps a live database out even if two rows somehow disagree about one
// name. A database the core database has never heard of is not a candidate at
// all and is not even asked about: it may be another system's, a leftover of
// somebody's manual work, or the cluster's own maintenance database, and
// nothing here can tell which. This job removes what the installation itself
// has already declared gone, and nothing else.
//
// The result is a plan, not an action. cmd/gameorphans prints it and stops
// unless it was told to go ahead.
func (s *OrphanSweeper) Find(ctx context.Context) ([]Orphan, error) {
	recorded, err := s.store.RecordedDatabases(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the recorded databases: %w", err)
	}

	// Two passes over the rows, because the second condition is about the
	// whole set: a name is only a candidate once every row naming it has been
	// seen, so the live ones cannot be collected as we go.
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

	// The existence check and the size report in one round trip: a name
	// missing from the answer is a database that is not there, which is the
	// healthy case and by far the common one.
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

// Remove drops the orphans it is given, one at a time, and reports what
// happened to each class.
//
// It takes the plan rather than making one, so that what an operator approved
// is exactly what runs: a second Find between the printed plan and the
// removal would be a chance for the two to differ.
//
// A busy database is left alone and is not a failure — the sweep has no
// forcing drop and wants none (OrphanCluster's own doc). One failure does not
// stop the rest: an operator running this has come to reclaim disk, and
// stopping at the first awkward database would leave most of it behind. Every
// failure is joined into the returned error so that nothing is quietly
// skipped.
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
			// The database is gone either way — reported rather than counted
			// as a failed removal, which would say the disk is still held.
			failures = append(failures, fmt.Errorf("record %s removed: %w", orphan.Database, err))
		}
	}
	return result, errors.Join(failures...)
}

// recordRemoval writes the trail entry for one removed database, under the
// same two action codes the reclaim sweep uses.
//
// The same codes rather than a new pair, because the fact an organizer is
// looking for is identical — this contest's database is gone, and here is its
// name — and a separate vocabulary for "gone late, by hand" would be one more
// thing anybody searching the trail has to know to ask for.
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

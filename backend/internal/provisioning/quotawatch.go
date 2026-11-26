package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// QuotaCandidate is one participant's database whose owner can make it grow:
// claimed and ready, in a running read_write contest whose template is built.
type QuotaCandidate struct {
	Database     string
	Registration uuid.UUID
	// Contest carries what Quota needs: the template, its version and the
	// policy's ratio.
	Contest Contest
	// UpdatedAt is the row as read, so a transition is written only to the
	// database that was measured, not to a copy a reset made since.
	UpdatedAt time.Time
	// OverQuotaSince is nil while the row is not flagged.
	OverQuotaSince *time.Time
}

// QuotaTransition is one database that crossed its quota, in either
// direction, on this tick.
type QuotaTransition struct {
	Database     string
	ContestID    uuid.UUID
	Registration uuid.UUID
	SizeBytes    int64
	QuotaBytes   int64
}

// ClusterWarningPercent is how full of GAME_CLUSTER_MAX_BYTES the game
// cluster may be before the monitor warns: the pool stops growing only at the
// budget itself, and the disk behind it is shared by every contest.
const ClusterWarningPercent = 80

// ClusterUsage is the game cluster's total size against its budget.
type ClusterUsage struct {
	Bytes int64
	// Budget is GAME_CLUSTER_MAX_BYTES; zero means none is set.
	Budget int64
	// Known is false when the cluster could not be measured.
	Known bool
}

// NearBudget reports whether the cluster holds ClusterWarningPercent of its
// budget or more. Without a budget or a measurement it claims nothing.
func (u ClusterUsage) NearBudget() bool {
	if !u.Known || !hasClusterBudget(u.Budget) {
		return false
	}
	// Multiplied first so a small budget keeps its threshold; the product
	// overflows only past about 100 PiB, no budget a cluster is given.
	return u.Bytes >= u.Budget*ClusterWarningPercent/100
}

// QuotaSweep is one tick of the quota monitor.
type QuotaSweep struct {
	// OverQuota counts the measured databases at or past their quota,
	// already flagged or not.
	OverQuota int
	// Complete is false when some candidate could not be measured, so
	// OverQuota is a lower bound. A database dropped meanwhile does not count.
	Complete bool
	// Exceeded and Restored are this tick's transitions, over and back within.
	Exceeded, Restored []QuotaTransition
	Cluster            ClusterUsage
}

// QuotaBatchLimit bounds the databases one DatabaseSizes round trip measures;
// each is a stat of every file of that database.
const QuotaBatchLimit = 200

// WatchQuotas measures every database a participant can still grow, flags
// one at or past its quota and lifts the flag once it is back within,
// recording each transition on the contest's trail. It reports and never
// enforces: the Query Runner refuses a growing write on its own check of the
// same quota, which a TRUNCATE still passes, so the flag never blocks the way
// back. A database whose flag already says what was measured is not written.
//
// Unless GAME_CLUSTER_MAX_BYTES is 0 (no budget) it also measures the whole
// cluster against it, apart: a failure of one part does not stop the other.
func (s *Service) WatchQuotas(ctx context.Context) (QuotaSweep, error) {
	sweep := QuotaSweep{Complete: true}
	var failures []error
	quotas := quotasOfTick{}

	for after := ""; ; {
		batch, err := s.repo.QuotaCandidates(ctx, after, QuotaBatchLimit)
		if err != nil {
			sweep.Complete = false
			failures = append(failures, fmt.Errorf("list the databases to measure: %w", err))
			break
		}
		if err := s.watchBatch(ctx, batch, quotas, &sweep); err != nil {
			sweep.Complete = false
			failures = append(failures, err)
		}
		if len(batch) < QuotaBatchLimit {
			break
		}
		after = batch[len(batch)-1].Database
	}

	// With GAME_CLUSTER_MAX_BYTES=0 nothing compares the total, and measuring
	// it stats every file of every database; past the tick's deadline the scan
	// would fail anyway.
	sweep.Cluster.Budget = s.maxClusterBytes
	switch {
	case !hasClusterBudget(sweep.Cluster.Budget):
	case ctx.Err() != nil:
		failures = append(failures, fmt.Errorf("measure the game cluster: %w", ctx.Err()))
	default:
		if used, err := s.clusterBytes(ctx); err != nil {
			failures = append(failures, fmt.Errorf("measure the game cluster: %w", err))
		} else {
			sweep.Cluster.Bytes, sweep.Cluster.Known = used, true
		}
	}
	return sweep, errors.Join(failures...)
}

// quotasOfTick holds each contest's quota, or why it could not be worked
// out, for one tick. Service.Quota caches only a measured template, so a
// template that is gone would otherwise cost a round trip and a failure for
// every copy of its contest.
type quotasOfTick map[quotaKey]quotaOutcome

type quotaKey struct {
	contest uuid.UUID
	version int
}

type quotaOutcome struct {
	bytes int64
	err   error
}

// of resolves contest's quota once per tick. first is true only the first
// time a failure is returned, so the caller records it once.
func (q quotasOfTick) of(ctx context.Context, s *Service, contest Contest) (bytes int64, first bool, err error) {
	key := quotaKey{contest.ID, contest.Version}
	if known, ok := q[key]; ok {
		return known.bytes, false, known.err
	}
	bytes, err = s.Quota(ctx, contest)
	q[key] = quotaOutcome{bytes, err}
	return bytes, true, err
}

// watchBatch measures one batch in one round trip and writes its transitions.
// A failing database is reported and the rest of the batch still handled.
func (s *Service) watchBatch(ctx context.Context, batch []QuotaCandidate, quotas quotasOfTick, sweep *QuotaSweep) error {
	if len(batch) == 0 {
		return nil
	}
	names := make([]string, len(batch))
	for i, c := range batch {
		names[i] = c.Database
	}
	sizes, err := s.cluster.DatabaseSizes(ctx, names)
	if err != nil {
		return fmt.Errorf("measure %d databases: %w", len(names), err)
	}

	var failures []error
	for _, c := range batch {
		size, exists := sizes[c.Database]
		if !exists {
			continue
		}
		quota, first, err := quotas.of(ctx, s, c.Contest)
		if err != nil {
			if first {
				failures = append(failures, fmt.Errorf("the quota of contest %s: %w", c.Contest.ID, err))
			}
			continue
		}
		moved := QuotaTransition{
			Database: c.Database, ContestID: c.Contest.ID, Registration: c.Registration,
			SizeBytes: size, QuotaBytes: quota,
		}
		over := sqlpolicy.AtQuota(size, quota)
		if over {
			sweep.OverQuota++
		}
		switch {
		case over && c.OverQuotaSince == nil:
			if err := s.recordQuota(ctx, c, moved, s.repo.MarkOverQuota, audit.ActionGameInstanceOverQuota, &sweep.Exceeded); err != nil {
				failures = append(failures, err)
			}
		case !over && c.OverQuotaSince != nil:
			if err := s.recordQuota(ctx, c, moved, s.repo.ClearOverQuota, audit.ActionGameInstanceWithinQuota, &sweep.Restored); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// recordQuota writes one transition and its audit entry in one transaction.
// A write that changed nothing (another replica's sweep, or a reset since the
// read) records nothing.
func (s *Service) recordQuota(
	ctx context.Context, c QuotaCandidate, moved QuotaTransition,
	write func(context.Context, string, time.Time) (bool, error), action string, into *[]QuotaTransition,
) error {
	var changed bool
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if changed, err = write(ctx, c.Database, c.UpdatedAt); err != nil || !changed {
			return err
		}
		return s.audit.Record(ctx, audit.Entry{
			Action:   action,
			Entity:   "contest",
			EntityID: c.Contest.ID.String(),
			Payload: map[string]any{
				"database":        c.Database,
				"registration_id": c.Registration.String(),
				"size_bytes":      moved.SizeBytes,
				"quota_bytes":     moved.QuotaBytes,
			},
		})
	})
	if err != nil {
		return fmt.Errorf("record %s for %s: %w", action, c.Database, err)
	}
	if changed {
		*into = append(*into, moved)
	}
	return nil
}

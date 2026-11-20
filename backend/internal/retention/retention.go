// Package retention deletes what the platform has kept past its period:
// the query journal of a contest archived a year ago, audit entries older
// than eighteen months, and mail outbox rows finished a month ago. The
// periods bound how long personal data (addresses, queries, recipients)
// outlives its purpose, including in backups taken afterwards.
//
// It does not decide periods per installation, touch the other journal
// tables, write to the audit trail, or schedule itself: a caller runs Prune.
package retention

import (
	"context"
	"errors"
)

const (
	// QueryLogMonths is how long a contest's query journal outlives the
	// contest's archiving.
	QueryLogMonths = 12
	// AuditLogMonths is how long an audit entry is kept after it is written.
	// The database refuses a shorter period; see migration "audit_log_retention".
	AuditLogMonths = 18
	// MailOutboxDays is how long a sent or abandoned message is kept; a
	// message still pending is never deleted.
	MailOutboxDays = 30
	// BatchSize is the rows one DELETE statement removes.
	BatchSize = 5000
	// MaxBatches is the statements Prune runs per table, so one call is
	// bounded however large the backlog.
	MaxBatches = 20
)

// Table names a table retention prunes. The values are metric label values.
type Table string

const (
	TableQueryLog   Table = "query_log"
	TableAuditLog   Table = "audit_log"
	TableMailOutbox Table = "mail_outbox"
)

var tables = []Table{TableQueryLog, TableAuditLog, TableMailOutbox}

// TableNames lists the pruned tables in the order Prune visits them.
func TableNames() []string {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = string(t)
	}
	return names
}

// Store deletes expired rows, each method one batch of at most limit rows,
// and returns how many it deleted. A batch run twice deletes nothing the
// second time.
type Store interface {
	DeleteQueryLog(ctx context.Context, archivedMonths, limit int) (int, error)
	DeleteAuditLog(ctx context.Context, olderThanMonths, limit int) (int, error)
	DeleteMailOutbox(ctx context.Context, finishedDays, limit int) (int, error)
}

// Outcome is what Prune did to one table. Deleted counts the rows removed
// even when Err is set.
type Outcome struct {
	Table   Table
	Deleted int
	Err     error
}

// Service prunes expired rows.
type Service struct{ store Store }

// New returns a service over store.
func New(store Store) *Service { return &Service{store: store} }

// Prune deletes expired rows table by table, batch by batch, until a batch
// comes back short or MaxBatches is reached. A failing table does not stop
// the others. It returns one Outcome per table in TableNames order and every
// error joined; when ctx ends the remaining tables are left alone and carry
// ctx's error.
func (s *Service) Prune(ctx context.Context) ([]Outcome, error) {
	outcomes := make([]Outcome, 0, len(tables))
	var errs []error
	for _, table := range tables {
		out := s.prune(ctx, table)
		outcomes = append(outcomes, out)
		if out.Err != nil {
			errs = append(errs, out.Err)
		}
	}
	return outcomes, errors.Join(dedupeContext(ctx, errs)...)
}

func (s *Service) prune(ctx context.Context, table Table) Outcome {
	out := Outcome{Table: table}
	for range MaxBatches {
		if err := ctx.Err(); err != nil {
			out.Err = err
			return out
		}
		n, err := s.batch(ctx, table)
		out.Deleted += n
		if err != nil {
			out.Err = err
			return out
		}
		if n < BatchSize {
			return out
		}
	}
	return out
}

func (s *Service) batch(ctx context.Context, table Table) (int, error) {
	switch table {
	case TableQueryLog:
		return s.store.DeleteQueryLog(ctx, QueryLogMonths, BatchSize)
	case TableAuditLog:
		return s.store.DeleteAuditLog(ctx, AuditLogMonths, BatchSize)
	default:
		return s.store.DeleteMailOutbox(ctx, MailOutboxDays, BatchSize)
	}
}

// dedupeContext keeps ctx's own error once, however many tables stopped on it.
func dedupeContext(ctx context.Context, errs []error) []error {
	ctxErr := ctx.Err()
	if ctxErr == nil {
		return errs
	}
	kept := errs[:0:0]
	seen := false
	for _, err := range errs {
		if err == ctxErr {
			if seen {
				continue
			}
			seen = true
		}
		kept = append(kept, err)
	}
	return kept
}

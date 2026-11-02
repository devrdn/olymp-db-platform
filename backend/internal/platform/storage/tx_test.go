package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
)

type fakeUnitOfWork struct {
	calls     int
	committed bool
	rolled    bool
}

func (f *fakeUnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	f.calls++
	if err := fn(ctx); err != nil {
		f.rolled = true
		return err
	}
	f.committed = true
	return nil
}

var _ UnitOfWork = (*fakeUnitOfWork)(nil)

func TestBusinessLogicCanRunAgainstAFakeUnitOfWork(t *testing.T) {
	uow := &fakeUnitOfWork{}

	err := uow.Do(context.Background(), func(ctx context.Context) error { return nil })

	if err != nil {
		t.Fatalf("Do() = %v, want nil", err)
	}
	if !uow.committed {
		t.Error("unit of work did not commit after a successful function")
	}
}

func TestFailureInsideTheUnitOfWorkIsPropagated(t *testing.T) {
	uow := &fakeUnitOfWork{}
	wantErr := errors.New("answer rejected")

	err := uow.Do(context.Background(), func(ctx context.Context) error { return wantErr })

	if !errors.Is(err, wantErr) {
		t.Errorf("Do() = %v, want it to wrap %v", err, wantErr)
	}
	if uow.committed {
		t.Error("unit of work committed despite a failure")
	}
}

func TestQuerierFromContextReturnsTheAmbientTransaction(t *testing.T) {
	tx := &fakeQuerier{name: "tx"}
	ctx := withQuerier(context.Background(), tx)

	got := QuerierFrom(ctx, &fakeQuerier{name: "pool"})

	if got != Querier(tx) {
		t.Error("QuerierFrom returned the pool while a transaction was active")
	}
}

func TestQuerierFromContextFallsBackToTheGivenQuerier(t *testing.T) {
	pool := &fakeQuerier{name: "pool"}

	got := QuerierFrom(context.Background(), pool)

	if got != Querier(pool) {
		t.Error("QuerierFrom did not fall back to the supplied querier")
	}
}

func TestInTxIsFalseWithoutATransaction(t *testing.T) {
	if InTx(context.Background()) {
		t.Error("InTx() = true on a bare context, want false")
	}
}

func TestInTxIsTrueInsideAUnitOfWork(t *testing.T) {
	ctx := withQuerier(context.Background(), &fakeQuerier{name: "tx"})

	if !InTx(ctx) {
		t.Error("InTx() = false inside a transaction, want true")
	}
}

// snapshotPool opens the core test database, or skips without one.
func snapshotPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := storagetest.OpenCore(ctx, nil)
	if err != nil {
		t.Fatalf("cannot use the test database: %v", err)
	}
	if pool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	t.Cleanup(pool.Close)
	return pool
}

// probeTable commits a table of its own, so another connection can write to
// it while a snapshot reads it, and drops it afterwards.
func probeTable(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	name := pgx.Identifier{"snapshot_probe_" + uuid.NewString()[:8]}.Sanitize()
	if _, err := pool.Exec(t.Context(), `CREATE TABLE `+name+` (n int NOT NULL)`); err != nil {
		t.Fatalf("create the probe table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS `+name)
	})
	return name
}

func countRows(ctx context.Context, q Querier, table string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n)
	return n, err
}

func TestReadSnapshotSeesOneMomentAcrossStatements(t *testing.T) {
	pool := snapshotPool(t)
	table := probeTable(t, pool)
	if _, err := pool.Exec(t.Context(), `INSERT INTO `+table+` VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	var before, after int
	err := NewUnitOfWork(pool).ReadSnapshot(t.Context(), func(ctx context.Context) error {
		var err error
		if before, err = countRows(ctx, QuerierFrom(ctx, pool), table); err != nil {
			return err
		}
		// The pool is another connection, so this commits on its own.
		if _, err := pool.Exec(ctx, `INSERT INTO `+table+` VALUES (2)`); err != nil {
			return err
		}
		after, err = countRows(ctx, QuerierFrom(ctx, pool), table)
		return err
	})

	if err != nil {
		t.Fatalf("ReadSnapshot() = %v", err)
	}
	if before != 1 || after != 1 {
		t.Errorf("counts inside the snapshot = %d, %d; want 1, 1 (a row committed between them is not seen)", before, after)
	}
	if now, err := countRows(t.Context(), pool, table); err != nil || now != 2 {
		t.Errorf("count after the snapshot = %d, %v; want 2", now, err)
	}
}

func TestReadSnapshotRefusesAWrite(t *testing.T) {
	pool := snapshotPool(t)
	table := probeTable(t, pool)

	err := NewUnitOfWork(pool).ReadSnapshot(t.Context(), func(ctx context.Context) error {
		_, err := QuerierFrom(ctx, pool).Exec(ctx, `INSERT INTO `+table+` VALUES (1)`)
		return err
	})

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("ReadSnapshot() = %v, want read_only_sql_transaction (25006)", err)
	}
	if n, err := countRows(t.Context(), pool, table); err != nil || n != 0 {
		t.Errorf("rows after a refused write = %d, %v; want 0", n, err)
	}
}

func TestReadSnapshotJoinsAnOpenTransaction(t *testing.T) {
	pool := snapshotPool(t)
	table := probeTable(t, pool)
	uow := NewUnitOfWork(pool)
	errRollback := errors.New("roll back")

	var seen int
	err := uow.Do(t.Context(), func(ctx context.Context) error {
		if _, err := QuerierFrom(ctx, pool).Exec(ctx, `INSERT INTO `+table+` VALUES (1)`); err != nil {
			return err
		}
		if err := uow.ReadSnapshot(ctx, func(ctx context.Context) error {
			var err error
			seen, err = countRows(ctx, QuerierFrom(ctx, pool), table)
			return err
		}); err != nil {
			return err
		}
		return errRollback
	})

	if !errors.Is(err, errRollback) {
		t.Fatalf("Do() = %v, want the test's rollback", err)
	}
	if seen != 1 {
		t.Errorf("rows seen inside the outer transaction = %d, want its uncommitted 1", seen)
	}
}

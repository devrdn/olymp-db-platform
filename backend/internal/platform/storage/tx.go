package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the set of database operations a repository performs. Both a
// connection pool and an open transaction satisfy it, which is what lets one
// repository method run standalone or inside a caller's transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// UnitOfWork runs a function inside a single database transaction.
//
// The architecture requires several writes to be atomic together — an accepted
// answer updates the score and appends an audit entry, and either all of it
// lands or none of it does. Handlers express that by wrapping their work in
// Do; they never touch a transaction object.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// txKey carries the ambient transaction. Unexported so nothing outside this
// package can inject one.
type txKey struct{}

func withQuerier(ctx context.Context, q Querier) context.Context {
	return context.WithValue(ctx, txKey{}, q)
}

// QuerierFrom returns the transaction active on ctx, or fallback when there is
// none. Repositories call it on every operation:
//
//	q := storage.QuerierFrom(ctx, r.pool)
//
// so the same code participates in an outer transaction when one exists.
func QuerierFrom(ctx context.Context, fallback Querier) Querier {
	if q, ok := ctx.Value(txKey{}).(Querier); ok {
		return q
	}
	return fallback
}

// PgxUnitOfWork implements UnitOfWork on a pgx pool.
type PgxUnitOfWork struct {
	pool *pgxpool.Pool
}

// NewUnitOfWork returns a transaction runner for the given pool.
func NewUnitOfWork(pool *pgxpool.Pool) *PgxUnitOfWork {
	return &PgxUnitOfWork{pool: pool}
}

// Do runs fn in a transaction, committing on success and rolling back on
// failure or panic.
//
// A nested call joins the outer transaction rather than opening a second one:
// PostgreSQL has no independent nested transactions, and silently opening one
// would break the atomicity the caller asked for.
func (u *PgxUnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	if _, nested := ctx.Value(txKey{}).(Querier); nested {
		return fn(ctx)
	}

	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		// Rollback after a successful commit is a no-op, so this is safe as an
		// unconditional guard against an early return or a panic.
		_ = tx.Rollback(ctx)
	}()

	if err := fn(withQuerier(ctx, tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

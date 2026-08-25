package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeQuerier is a stand-in used to assert which querier a repository would
// reach for. It is never executed against.
type fakeQuerier struct {
	name string
}

func (f *fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (f *fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func (f *fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

package queryproxy_test

import (
	"context"

	"github.com/devrdn/db-contest/backend/internal/audit"
)

// quiet is the audit sink and unit of work of a test that does not read the
// trail.
type quiet struct{}

var quietTrail = audit.New(quiet{})

func (quiet) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
func (quiet) Append(context.Context, audit.Entry) error                    { return nil }
func (quiet) AppendMany(context.Context, []audit.Entry) error              { return nil }

// trail is the audit sink and the unit of work in one, so a test can tell an
// entry written inside the start's transaction from one written after it.
type trail struct {
	entries []audit.Entry
	loose   []audit.Entry
}

type inTxKey struct{}

func (tr *trail) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(context.WithValue(ctx, inTxKey{}, true))
}

func (tr *trail) Append(ctx context.Context, e audit.Entry) error {
	tr.entries = append(tr.entries, e)
	if ctx.Value(inTxKey{}) == nil {
		tr.loose = append(tr.loose, e)
	}
	return nil
}

func (tr *trail) AppendMany(ctx context.Context, entries []audit.Entry) error {
	for _, e := range entries {
		if err := tr.Append(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

package settings_test

import (
	"context"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

// fixture assembles the service over in-memory storage, so the rules are
// exercised without a database.
type fixture struct {
	service *settings.Service
	repo    *repo
	sink    *sink
}

func newFixture() *fixture {
	r := &repo{values: settings.Values{}}
	s := &sink{}

	return &fixture{
		service: settings.NewService(r, audit.New(s), unitOfWork{}),
		repo:    r,
		sink:    s,
	}
}

type repo struct {
	values settings.Values
	// Err, when set, is returned by every method.
	Err error
}

func (r *repo) All(context.Context) (settings.Values, error) {
	if r.Err != nil {
		return nil, r.Err
	}
	out := settings.Values{}
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *repo) Save(_ context.Context, _ uuid.UUID, values settings.Values) error {
	if r.Err != nil {
		return r.Err
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

type sink struct{ entries []audit.Entry }

func (s *sink) Append(_ context.Context, e audit.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

// unitOfWork runs the function directly. It cannot roll back a map, and no
// test claims it does: what the fixture exercises is the rules, while the
// atomicity of the writes belongs to the real transaction runner.
type unitOfWork struct{}

func (unitOfWork) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

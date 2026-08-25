package storage

import (
	"context"
	"errors"
	"testing"
)

// fakeUnitOfWork records how it was used, standing in for a real transaction
// in tests of business logic.
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

// Compile-time proof that a test double can replace the real implementation:
// this is the point of the interface.
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
	// Repositories call this instead of holding a pool, so the same repository
	// method works standalone and inside somebody else's transaction. This is
	// what lets an audit entry be written in the same transaction as the action
	// it records.
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
	// Some statements are only correct inside a transaction — deferring a
	// constraint outside one is silently ignored — so a repository has to be
	// able to refuse rather than half-work.
	ctx := withQuerier(context.Background(), &fakeQuerier{name: "tx"})

	if !InTx(ctx) {
		t.Error("InTx() = false inside a transaction, want true")
	}
}

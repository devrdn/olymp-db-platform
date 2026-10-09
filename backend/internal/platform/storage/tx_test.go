package storage

import (
	"context"
	"errors"
	"testing"
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

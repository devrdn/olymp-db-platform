package participation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/google/uuid"
)

func TestTheReadBudgetRefusesPastItsRatePerAccount(t *testing.T) {
	reads := participation.NewReads(2)
	spent := uuid.New()
	for read := range 2 {
		if err := reads.Charge(t.Context(), spent); err != nil {
			t.Fatalf("read %d = %v", read+1, err)
		}
	}
	if err := reads.Charge(t.Context(), spent); !errors.Is(err, participation.ErrTooManyReads) {
		t.Fatalf("the third read = %v, want ErrTooManyReads", err)
	}
	// One account's reads are its own.
	if err := reads.Charge(t.Context(), uuid.New()); err != nil {
		t.Fatalf("another account's first read = %v", err)
	}
}

func TestTheReadBudgetDefaultsToAHundredAndTwentyAMinute(t *testing.T) {
	reads := participation.NewReads(participation.ReadsPerMinute)
	account := uuid.New()
	for read := range 120 {
		if err := reads.Charge(t.Context(), account); err != nil {
			t.Fatalf("read %d = %v", read+1, err)
		}
	}
	if err := reads.Charge(t.Context(), account); !errors.Is(err, participation.ErrTooManyReads) {
		t.Fatalf("read 121 = %v, want ErrTooManyReads", err)
	}
}

func TestAReadBudgetOfZeroHasNoLimit(t *testing.T) {
	reads := participation.NewReads(0)
	account := uuid.New()
	for read := range 1000 {
		if err := reads.Charge(t.Context(), account); err != nil {
			t.Fatalf("read %d = %v", read+1, err)
		}
	}
}

func TestANegativeReadBudgetPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewReads(-1) did not panic")
		}
	}()
	participation.NewReads(-1)
}

func TestABudgetFuncIsChargedWithTheAccount(t *testing.T) {
	var charged uuid.UUID
	budget := participation.BudgetFunc(func(_ context.Context, account uuid.UUID) error {
		charged = account
		return nil
	})
	account := uuid.New()
	if err := budget.Charge(t.Context(), account); err != nil || charged != account {
		t.Fatalf("Charge() = %v, charged %s; want %s charged", err, charged, account)
	}
}

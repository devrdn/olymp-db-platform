package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePinger struct {
	err     error
	calls   int
	lastCtx context.Context
}

func (f *fakePinger) Ping(ctx context.Context) error {
	f.calls++
	f.lastCtx = ctx
	return f.err
}

func TestCheckerReportsSuccessWhenPingSucceeds(t *testing.T) {
	p := &fakePinger{}
	checker := NewChecker("core-db", p)

	if err := checker.Check(context.Background()); err != nil {
		t.Errorf("Check() = %v, want nil", err)
	}
	if p.calls != 1 {
		t.Errorf("ping called %d times, want 1", p.calls)
	}
}

func TestCheckerPropagatesPingFailure(t *testing.T) {
	wantErr := errors.New("connection refused")
	checker := NewChecker("core-db", &fakePinger{err: wantErr})

	err := checker.Check(context.Background())

	if !errors.Is(err, wantErr) {
		t.Errorf("Check() = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCheckerReportsItsName(t *testing.T) {
	checker := NewChecker("redis", &fakePinger{})

	if got := checker.Name(); got != "redis" {
		t.Errorf("Name() = %q, want redis", got)
	}
}

func TestCheckerPassesCallerContextToPing(t *testing.T) {
	p := &fakePinger{}
	checker := NewChecker("core-db", p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_ = checker.Check(ctx)

	if p.lastCtx == nil {
		t.Fatal("ping received no context")
	}
	if _, ok := p.lastCtx.Deadline(); !ok {
		t.Error("ping context lost the caller deadline, so a probe could hang")
	}
}

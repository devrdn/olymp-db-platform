package retention_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/retention"
)

var errAway = errors.New("database is away")

type answer struct {
	n   int
	err error
}

type call struct{ period, limit int }

// fakeStore answers each table's calls from a script, repeating the last
// answer, and records every call's arguments.
type fakeStore struct {
	script map[retention.Table][]answer
	calls  map[retention.Table][]call
	onCall func(retention.Table)
}

func newFake(script map[retention.Table][]answer) *fakeStore {
	return &fakeStore{script: script, calls: map[retention.Table][]call{}}
}

func (f *fakeStore) answer(t retention.Table, period, limit int) (int, error) {
	i := len(f.calls[t])
	f.calls[t] = append(f.calls[t], call{period, limit})
	if f.onCall != nil {
		f.onCall(t)
	}
	s := f.script[t]
	if len(s) == 0 {
		return 0, nil
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i].n, s[i].err
}

func (f *fakeStore) DeleteQueryLog(_ context.Context, months, limit int) (int, error) {
	return f.answer(retention.TableQueryLog, months, limit)
}

func (f *fakeStore) DeleteAuditLog(_ context.Context, months, limit int) (int, error) {
	return f.answer(retention.TableAuditLog, months, limit)
}

func (f *fakeStore) DeleteMailOutbox(_ context.Context, days, limit int) (int, error) {
	return f.answer(retention.TableMailOutbox, days, limit)
}

func TestPruneDeletesEachTableUntilABatchComesBackShort(t *testing.T) {
	f := newFake(map[retention.Table][]answer{
		retention.TableQueryLog:   {{5000, nil}, {5000, nil}, {12, nil}},
		retention.TableAuditLog:   {{0, nil}},
		retention.TableMailOutbox: {{3, nil}},
	})

	got, err := retention.New(f).Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune() = %v", err)
	}

	want := []retention.Outcome{
		{Table: retention.TableQueryLog, Deleted: 10012},
		{Table: retention.TableAuditLog},
		{Table: retention.TableMailOutbox, Deleted: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("outcomes = %+v, want %+v", got, want)
	}
	if n := len(f.calls[retention.TableQueryLog]); n != 3 {
		t.Errorf("query_log calls = %d, want 3", n)
	}
	if n := len(f.calls[retention.TableAuditLog]); n != 1 {
		t.Errorf("audit_log calls = %d, want 1", n)
	}
	if n := len(f.calls[retention.TableMailOutbox]); n != 1 {
		t.Errorf("mail_outbox calls = %d, want 1", n)
	}
	for table, want := range map[retention.Table]call{
		retention.TableQueryLog:   {12, 5000},
		retention.TableAuditLog:   {18, 5000},
		retention.TableMailOutbox: {30, 5000},
	} {
		for _, c := range f.calls[table] {
			if c != want {
				t.Errorf("%s called with %+v, want %+v", table, c, want)
			}
		}
	}
}

func TestPruneStopsATableAtMaxBatches(t *testing.T) {
	f := newFake(map[retention.Table][]answer{retention.TableQueryLog: {{5000, nil}}})

	got, err := retention.New(f).Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune() = %v", err)
	}
	if n := len(f.calls[retention.TableQueryLog]); n != 20 {
		t.Errorf("query_log calls = %d, want 20", n)
	}
	if got[0].Deleted != 100000 {
		t.Errorf("query_log deleted = %d, want 100000", got[0].Deleted)
	}
}

func TestPruneCarriesOnPastAFailingTable(t *testing.T) {
	f := newFake(map[retention.Table][]answer{
		retention.TableAuditLog:   {{0, errAway}},
		retention.TableMailOutbox: {{3, nil}},
	})

	got, err := retention.New(f).Prune(context.Background())
	if !errors.Is(err, errAway) {
		t.Fatalf("Prune() error = %v, want it to wrap errAway", err)
	}
	if !errors.Is(got[1].Err, errAway) {
		t.Errorf("audit_log outcome error = %v, want errAway", got[1].Err)
	}
	if len(f.calls[retention.TableMailOutbox]) == 0 || got[2].Deleted != 3 {
		t.Errorf("mail_outbox was not pruned after audit_log failed: %+v", got[2])
	}
}

func TestPruneKeepsWhatAFailingTableDeletedBeforeFailing(t *testing.T) {
	f := newFake(map[retention.Table][]answer{
		retention.TableQueryLog: {{5000, nil}, {0, errAway}},
	})

	got, _ := retention.New(f).Prune(context.Background())

	want := retention.Outcome{Table: retention.TableQueryLog, Deleted: 5000, Err: errAway}
	if got[0] != want {
		t.Errorf("outcome = %+v, want %+v", got[0], want)
	}
}

func TestPruneStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFake(map[retention.Table][]answer{retention.TableQueryLog: {{5000, nil}}})
	f.onCall = func(retention.Table) { cancel() }

	outcomes, err := retention.New(f).Prune(ctx)

	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(outcomes))
	}
	if outcomes[0].Deleted != 5000 || outcomes[0].Err != context.Canceled {
		t.Errorf("query_log outcome = %+v, want 5000 deleted and context.Canceled", outcomes[0])
	}
	for _, o := range outcomes[1:] {
		if o.Err != context.Canceled || o.Deleted != 0 {
			t.Errorf("%s outcome = %+v, want not reached with context.Canceled", o.Table, o)
		}
	}
	if n := countIs(err, context.Canceled); n != 1 {
		t.Errorf("error %v holds context.Canceled %d times, want once", err, n)
	}
	if n := len(f.calls[retention.TableQueryLog]); n != 1 {
		t.Errorf("query_log calls = %d, want 1", n)
	}
	if len(f.calls[retention.TableAuditLog])+len(f.calls[retention.TableMailOutbox]) != 0 {
		t.Errorf("other tables were called after the context ended")
	}
}

func TestPruneReportsContextCancelledInsideAStoreCallOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wrapped := fmt.Errorf("delete: %w", context.Canceled)
	f := newFake(map[retention.Table][]answer{retention.TableQueryLog: {{0, wrapped}}})
	f.onCall = func(retention.Table) { cancel() }

	outcomes, err := retention.New(f).Prune(ctx)

	if !errors.Is(outcomes[0].Err, context.Canceled) || outcomes[0].Err == context.Canceled {
		t.Errorf("query_log outcome error = %v, want the store's wrapped error", outcomes[0].Err)
	}
	if n := countIs(err, context.Canceled); n != 1 {
		t.Errorf("error %v holds context.Canceled %d times, want once", err, n)
	}
}

// countIs counts the joined errors that are target.
func countIs(err, target error) int {
	if err == nil {
		return 0
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		n := 0
		for _, e := range j.Unwrap() {
			if errors.Is(e, target) {
				n++
			}
		}
		return n
	}
	if errors.Is(err, target) {
		return 1
	}
	return 0
}

func TestTableNamesListsTheThreeInOrder(t *testing.T) {
	want := []string{"query_log", "audit_log", "mail_outbox"}
	if got := retention.TableNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("TableNames() = %v, want %v", got, want)
	}
}

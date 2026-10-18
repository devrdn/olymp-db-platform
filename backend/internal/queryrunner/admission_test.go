package queryrunner_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/google/uuid"
)

// slowRunner allows pg_sleep, so a test can hold a slot.
func slowRunner(t *testing.T, limits queryrunner.Limits) (*queryrunner.Runner, string) {
	t.Helper()
	return setupWith(t, limits, checker.NewChecker("pg_sleep"))
}

// A participant with ten tabs must not hold ten slots.
func TestOneQueryAtATimePerParticipant(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Deadline = 3 * time.Second
	runner, database := slowRunner(t, limits)

	started := make(chan struct{})
	var first error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		close(started)
		_, first = runner.Run(t.Context(), request(database, `SELECT pg_sleep(1)`))
	}()

	<-started
	time.Sleep(300 * time.Millisecond) // let the first one take its slot

	_, err := runner.Run(t.Context(), request(database, `SELECT 1`))
	if !errors.Is(err, queryrunner.ErrAlreadyRunning) {
		t.Fatalf("second query: error = %v, want ErrAlreadyRunning", err)
	}

	wg.Wait()
	if first != nil {
		t.Fatalf("the first query failed: %v", first)
	}
}

func TestBeyondTheQueueTheAnswerIsImmediateRatherThanAWait(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Concurrent = 1
	limits.QueueDepth = 0
	limits.Deadline = 3 * time.Second
	runner, database := slowRunner(t, limits)

	holding := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(holding)
		_, _ = runner.Run(t.Context(), other("holder", database, `SELECT pg_sleep(1)`))
	}()

	<-holding
	time.Sleep(300 * time.Millisecond)

	started := time.Now()
	_, err := runner.Run(t.Context(), other("newcomer", database, `SELECT 1`))
	waited := time.Since(started)

	if !errors.Is(err, queryrunner.ErrBusy) {
		t.Fatalf("error = %v, want ErrBusy", err)
	}
	// The holder sleeps a second; well short of that proves no wait. A
	// tighter bound fails under the race detector.
	if waited > 700*time.Millisecond {
		t.Fatalf("waited %s before saying it was busy, so it queued after all", waited)
	}

	wg.Wait()
}

func TestWithRoomInTheQueueANewcomerWaitsAndSucceeds(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Concurrent = 1
	limits.QueueDepth = 4
	limits.Deadline = 3 * time.Second
	runner, database := slowRunner(t, limits)

	holding := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(holding)
		_, _ = runner.Run(t.Context(), other("holder", database, `SELECT pg_sleep(1)`))
	}()

	<-holding
	time.Sleep(200 * time.Millisecond)

	result, err := runner.Run(t.Context(), other("newcomer", database, `SELECT 1 AS a`))
	if err != nil {
		t.Fatalf("a queued query failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %d", len(result.Rows))
	}

	wg.Wait()
}

// A queued query must not wait as long as the caller is willing to.
func TestAQueuedQueryStopsWaitingAtTheQueueWait(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Concurrent = 1
	limits.QueueDepth = 4
	limits.QueueWait = 200 * time.Millisecond
	limits.Deadline = 3 * time.Second
	runner, database := slowRunner(t, limits)

	holding := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(holding)
		_, _ = runner.Run(t.Context(), other("holder", database, `SELECT pg_sleep(2)`))
	}()

	<-holding
	time.Sleep(300 * time.Millisecond)

	started := time.Now()
	_, err := runner.Run(t.Context(), other("newcomer", database, `SELECT 1`))
	waited := time.Since(started)

	if !errors.Is(err, queryrunner.ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	// The holder sleeps two seconds; returning well before proves the wait
	// was cut, not served.
	if waited > time.Second {
		t.Fatalf("waited %s for a slot, past the queue wait of %s", waited, limits.QueueWait)
	}

	wg.Wait()
}

func TestASlotIsReturnedWhateverHappened(t *testing.T) {
	limits := queryrunner.DefaultLimits()
	limits.Concurrent = 1
	limits.QueueDepth = 0
	limits.Deadline = 400 * time.Millisecond
	runner, database := slowRunner(t, limits)

	for name, sql := range map[string]string{
		"a refusal":        `DROP TABLE evidence`,
		"a database error": `SELECT * FROM no_such_table`,
		"a timeout":        `SELECT pg_sleep(30)`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runner.Run(t.Context(), request(database, sql)); err == nil {
				t.Fatalf("%s: expected a failure", sql)
			}
			// With Concurrent=1 and no queue, this fails if the slot leaked.
			if _, err := runner.Run(t.Context(), request(database, `SELECT 1`)); err != nil {
				t.Fatalf("the slot was not returned after %s: %v", name, err)
			}
		})
	}
}

func other(participant, database, sql string) queryrunner.Request {
	r := request(database, sql)
	r.Registration = uuid.NewSHA1(uuid.Nil, []byte(participant))
	return r
}

// One holder runs, one newcomer waits out the queue and a third finds it
// full: the gauges see the first two, and each ending says where it stopped.
func TestAdmissionIsVisibleWhileItHappensAndEachRefusalSaysWhere(t *testing.T) {
	limits := unlimited()
	limits.Concurrent = 1
	limits.QueueDepth = 1
	limits.QueueWait = 500 * time.Millisecond
	limits.Deadline = 3 * time.Second
	runner, database, observed := observedRunner(t, limits, checker.NewChecker("pg_sleep"))

	if running, waiting := runner.Admission(); running != 0 || waiting != 0 {
		t.Fatalf("an idle runner reports %d running and %d waiting", running, waiting)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = runner.Run(t.Context(), other("holder", database, `SELECT pg_sleep(1.5)`))
	}()
	until(t, func() bool { running, _ := runner.Admission(); return running == 1 })

	var queued error
	go func() {
		defer wg.Done()
		_, queued = runner.Run(t.Context(), other("queued", database, `SELECT 1`))
	}()
	until(t, func() bool { _, waiting := runner.Admission(); return waiting == 1 })

	if _, err := runner.Run(t.Context(), other("refused", database, `SELECT 1`)); !errors.Is(err, queryrunner.ErrBusy) {
		t.Fatalf("with the queue full: error = %v, want ErrBusy", err)
	}

	wg.Wait()
	if !errors.Is(queued, queryrunner.ErrTimeout) {
		t.Fatalf("the queued query: error = %v, want ErrTimeout", queued)
	}
	if running, waiting := runner.Admission(); running != 0 || waiting != 0 {
		t.Fatalf("after everything ended: %d running, %d waiting", running, waiting)
	}

	endings, executed, _, waits := observed.report()
	for _, want := range []string{"rejected/busy", "timeout/queue", "ok/"} {
		if !strings.Contains(endings, want) {
			t.Errorf("endings = %q, missing %s", endings, want)
		}
	}
	// The busy refusal never waited and only the holder ran.
	if waits != 2 || executed != "ok" {
		t.Errorf("waits = %d, executed = %q; want 2 and ok", waits, executed)
	}
}

// until polls cond for up to three seconds.
func until(t *testing.T, cond func() bool) {
	t.Helper()

	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the condition never held")
}

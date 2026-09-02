package queryrunner_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// slowRunner is a runner that can be made to hold a slot for as long as a test
// needs, by allowing the one function the standard list leaves out.
func slowRunner(t *testing.T, limits queryrunner.Limits) (*queryrunner.Runner, string) {
	t.Helper()
	return setupWith(t, limits, sqlpolicy.NewChecker("pg_sleep"))
}

// One query at a time per participant. Not a fairness rule but a resource one:
// a participant who can open ten tabs would otherwise hold ten execution slots
// while everyone else queues behind them.
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

// Past the semaphore and past the queue, the answer is immediate. A request
// that hangs waiting is worse than one that is turned away: the participant
// learns nothing and the slot is still spoken for.
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
	// The holder sleeps for a second, so anything well short of that proves
	// the answer did not wait for the slot — which is the claim. The earlier
	// bound of 200ms was measuring the machine rather than the code and failed
	// under the race detector.
	if waited > 700*time.Millisecond {
		t.Fatalf("waited %s before saying it was busy, so it queued after all", waited)
	}

	wg.Wait()
}

// With a queue, a newcomer waits rather than being turned away — that is what
// the queue is for, and the difference between "busy" and "briefly slower".
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

// A slot has to come back however the query ended, or the instance runs out of
// room after N failures and never recovers.
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
			// The next query proves the slot came back; with Concurrent=1 and
			// no queue it would be refused outright otherwise.
			if _, err := runner.Run(t.Context(), request(database, `SELECT 1`)); err != nil {
				t.Fatalf("the slot was not returned after %s: %v", name, err)
			}
		})
	}
}

// other is a different participant, named by a stable id so a failure names
// which one.
func other(participant, database, sql string) queryrunner.Request {
	r := request(database, sql)
	r.Registration = uuid.NewSHA1(uuid.Nil, []byte(participant))
	return r
}

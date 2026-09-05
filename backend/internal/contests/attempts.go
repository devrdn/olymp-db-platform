package contests

import (
	"context"

	"github.com/google/uuid"
)

// AttemptStats is what a registration has already done on one question: how
// many attempts it has spent, and whether one of them was correct.
type AttemptStats struct {
	Attempts int
	Correct  bool
}

// AttemptStore answers what a participant has already tried, for the
// participant-facing question list (docs/ARCHITECTURE.md §6.1: "how many
// attempts remain, and whether the question is closed").
//
// It is deliberately the read half only. Recording a submission is Task 3's
// own repository, once the answer path exists; introducing this narrow reader
// first does not preempt attempt numbering or grading, which both need a
// transaction this package does not open here.
type AttemptStore interface {
	// ForRegistration returns, for one registration, its attempt stats keyed
	// by question. A question with no submissions is simply absent from the
	// map — the caller reads a missing entry as "never attempted", not as an
	// error.
	ForRegistration(ctx context.Context, registrationID uuid.UUID) (map[uuid.UUID]AttemptStats, error)
}

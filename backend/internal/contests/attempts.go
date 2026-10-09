package contests

import (
	"context"

	"github.com/google/uuid"
)

// AttemptStats is what a registration has already done on one question.
type AttemptStats struct {
	Attempts int
	Correct  bool
	// PointsAwarded sums points_awarded over the registration's submissions on
	// the question. Only a correct one is non-zero and it closes the question,
	// so this is 0 or the winning attempt's award.
	PointsAwarded int
}

// AttemptStore reads what a participant has already tried, for the
// participant-facing question list (docs/ARCHITECTURE.md §6.1). Writing is
// SubmissionRepository's job.
type AttemptStore interface {
	// ForRegistration returns attempt stats keyed by question. A question
	// never attempted is absent from the map.
	ForRegistration(ctx context.Context, registrationID uuid.UUID) (map[uuid.UUID]AttemptStats, error)
}

package contests

import (
	"context"

	"github.com/google/uuid"
)

// SequentialGate answers whether a registration has closed (answered
// correctly or run out of attempts on) every question ordered before a given
// one (§6.1.1). Hidden questions count: the order is questions.ord.
//
// A plain read ahead of the write is safe here, unlike the deadline and
// attempt checks Insert folds in: while answers are taken, content cannot be
// edited (Contest.ContentEditable), so the state only grows more open.
type SequentialGate interface {
	// Open reports whether every question of contestID ordered strictly
	// before ord is closed for registrationID. The first question is always
	// open.
	Open(ctx context.Context, contestID, registrationID uuid.UUID, ord int) (bool, error)
	// Frontier returns the lowest-ordered question not yet closed for
	// registrationID, or uuid.Nil once all are closed, so a list needs one
	// statement rather than Open per question.
	Frontier(ctx context.Context, contestID, registrationID uuid.UUID) (uuid.UUID, error)
}

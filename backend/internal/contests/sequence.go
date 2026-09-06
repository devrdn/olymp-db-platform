package contests

import (
	"context"

	"github.com/google/uuid"
)

// SequentialGate answers the one question a contest with
// progression = sequential (§6.1.1) needs before Submit may accept an
// answer: has this registration closed every question ordered before this
// one — answered correctly, or spent every attempt on it? Hidden questions
// (is_visible = false) count exactly as visible ones do, since the ordering
// is questions.ord, the same one display uses, not "the ones this
// participant can see".
//
// Declared here, narrow, rather than reusing QuestionRepository or
// AttemptStore: the answer is a single yes/no about a state that only ever
// grows monotonically more open (a question does not un-close once it closes),
// so it is safe to compute with a plain read ahead of the write, unlike the
// deadline and attempt-count checks Insert folds into itself precisely
// because those are not monotonic in the same way. Submit consults this only
// when the contest is actually sequential — a contest that never uses it
// pays nothing for its existence.
type SequentialGate interface {
	// Open reports whether every question of contestID ordered strictly
	// before ord is closed for registrationID. ord is the target question's
	// own Question.Ord; a question with nothing before it (the first in
	// display order) is trivially open.
	Open(ctx context.Context, contestID, registrationID uuid.UUID, ord int) (bool, error)
}

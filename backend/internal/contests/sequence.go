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
// AttemptStore: the answer is a single yes/no about a state that only grows
// more open as the participant works — a question answered correctly stays
// closed. The one way back is an organizer raising a question's attempt cap,
// which reopens a question closed only by its spent attempts; that makes the
// gate stricter, never looser, and an answer that races it was one the
// participant was entitled to a moment earlier. So it is safe to compute
// with a plain read ahead of the write, unlike the
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
	// Frontier reports which question of contestID is currently open for
	// registrationID: the one lowest in display order that is not yet
	// closed. It is uuid.Nil once every question is closed. Consulted by
	// Reader.Questions (finding 3) to tell a participant which of several
	// unclosed questions may actually be answered right now, without asking
	// Open once per question in the list — the same fact, in one statement
	// instead of one per candidate.
	Frontier(ctx context.Context, contestID, registrationID uuid.UUID) (uuid.UUID, error)
}

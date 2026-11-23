package participation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryrunner"
	"github.com/google/uuid"
)

// ErrTooManyReads is a participant loading the play screen faster than the
// read budget allows. It is not queryrunner.ErrTooManyQueries: the two budgets
// are separate, and a participant told "too many queries" after reloading a
// page would wait for nothing.
var ErrTooManyReads = errors.New("too many page reads")

// Budget is one resource's allowance, charged to the asking account before
// admission looks anything up. Each resource keeps its own: the play screen's
// reads (Reads), the workspace's writes, the browser's signal batches, the
// questions to the organisers and the console's queries.
//
// Its refusal is returned as it is, so it must be a sentinel the API answers.
type Budget interface {
	Charge(ctx context.Context, account uuid.UUID) error
}

// BudgetFunc adapts a resource's own admit method to a Budget.
type BudgetFunc func(ctx context.Context, account uuid.UUID) error

func (f BudgetFunc) Charge(ctx context.Context, account uuid.UUID) error { return f(ctx, account) }

// RegistrationBudget is a Budget with a second key space: once admission has
// found the registration, it is charged again to that registration, before the
// gate. Its keys are bounded by the rosters (CLAUDE.md rule 5), and the contest
// is in hand for a limit the contest sets.
type RegistrationBudget interface {
	Budget
	ChargeRegistration(ctx context.Context, participant contests.Participant, contest contests.Contest) error
}

// ReadsPerMinute is the play screen's default read budget. A reload costs
// about six reads, so this allows a reload every few seconds for a minute.
const ReadsPerMinute = 120

// Reads is the budget every participant-facing read spends, per account: the
// story, the questions, the schema, the query log, the workspace, the
// clarifications feed and each events channel opened. It is apart from the
// console's query rate, so reloading the page never refuses a query.
//
// The window slides and is kept in this process, as the console's rate is.
type Reads struct {
	window    *queryrunner.RateLimiter
	perMinute int
}

// NewReads returns the read budget at perMinute (PLAY_READ_PER_MINUTE). Zero
// means no limit; a negative value panics, since config.Load never produces
// one.
func NewReads(perMinute int) *Reads {
	if perMinute < 0 {
		panic(fmt.Sprintf("participation: negative read budget %d", perMinute))
	}
	return &Reads{window: queryrunner.NewRateLimiter(0, time.Minute), perMinute: perMinute}
}

func (r *Reads) Charge(_ context.Context, account uuid.UUID) error {
	if err := r.window.Admit(account.String(), r.perMinute); err != nil {
		return ErrTooManyReads
	}
	return nil
}

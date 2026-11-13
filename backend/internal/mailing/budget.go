package mailing

import (
	"context"
	"log/slog"
	"time"
)

// BudgetLimiter is the slice of auth.Limiter a Budget needs.
type BudgetLimiter interface {
	// Spend counts one attempt, refusals included, and reports whether it
	// is within limit and whether it is the window's first refusal.
	Spend(ctx context.Context, subject string, limit int, window time.Duration) (allowed, firstRefused bool, err error)
}

// Limits is how many messages a budget admits in a minute and in a day.
type Limits struct {
	PerMinute, PerDay int
}

// Budget is the installation's allowance of one kind of mail sent to an
// address anyone may type. It protects the relay, which sends
// SMTP_MAX_PER_MINUTE messages for every kind together, and the sending
// mailbox's daily recipient limit. Each kind has a budget of its own, so a
// flood of one cannot shut another; each window is one fixed limiter key.
type Budget struct {
	name    string
	limits  Limits
	limiter BudgetLimiter
	log     *slog.Logger
}

// NewBudget names the budget in its limiter keys and its log lines; two
// budgets of one name share their counters.
func NewBudget(name string, limits Limits, limiter BudgetLimiter, log *slog.Logger) *Budget {
	return &Budget{name: name, limits: limits, limiter: limiter, log: log}
}

// Spend counts one message against the minute's window, then the day's, and
// reports whether both admit it. A refusal counts too, so a flood does not
// outlast the window. The first refusal of each window is logged at warn,
// and only that one, so the operator learns the budget ran out without a
// line per refused message. A limiter that cannot count is (false, err).
func (b *Budget) Spend(ctx context.Context) (bool, error) {
	for _, window := range []struct {
		name   string
		limit  int
		length time.Duration
	}{
		{"minute", b.limits.PerMinute, time.Minute},
		{"day", b.limits.PerDay, 24 * time.Hour},
	} {
		allowed, firstRefused, err := b.limiter.Spend(ctx, "mail_budget:"+b.name+":"+window.name, window.limit, window.length)
		if err != nil {
			return false, err
		}
		if firstRefused {
			b.log.WarnContext(ctx, "the installation's mail budget is spent; such mail is refused until the window passes",
				"budget", b.name, "window", window.length, "limit", window.limit)
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}

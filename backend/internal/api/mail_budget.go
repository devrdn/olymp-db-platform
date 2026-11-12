package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// The installation's budget for the mail self-service queues to an address
// anyone may type: sign-up codes, existing-account notices and reset links.
// The relay sends SMTP_MAX_PER_MINUTE messages for the whole installation (25
// by default), so the minute's share leaves room for staff mail such as
// sign-in details, and the day's ceiling keeps the sending mailbox under a
// provider's daily recipient limit. The per-address and per-email budgets
// protect one inbox; this one protects the relay.
const (
	SelfServiceMailsPerMinute = 15
	SelfServiceMailsPerDay    = 2000
)

// The budget's limiter subjects: one fixed key per window, the most bounded
// key there is.
const (
	SelfServiceMailSubjectPerMinute = "self_service_mail:minute"
	SelfServiceMailSubjectPerDay    = "self_service_mail:day"
)

// MailBudget spends the installation's self-service mail budget. Every
// endpoint that queues such mail spends it last, after its own budgets, so a
// caller they refused cannot use it up for everybody (CLAUDE.md rule 5), and
// before the expensive step.
type MailBudget struct {
	limiter           *auth.Limiter
	perMinute, perDay int
}

// NewMailBudget takes the limits so a test of another budget can stay clear
// of this one; the composition root passes SelfServiceMailsPerMinute and
// SelfServiceMailsPerDay.
func NewMailBudget(limiter *auth.Limiter, perMinute, perDay int) *MailBudget {
	return &MailBudget{limiter: limiter, perMinute: perMinute, perDay: perDay}
}

// Spend counts one message against each window, the minute's first, and
// reports whether both admit it; a refusal counts too (CLAUDE.md rule 13). A
// limiter that cannot count is (false, err).
func (b *MailBudget) Spend(ctx context.Context) (bool, error) {
	for _, window := range []struct {
		subject string
		limit   int
		length  time.Duration
	}{
		{SelfServiceMailSubjectPerMinute, b.perMinute, time.Minute},
		{SelfServiceMailSubjectPerDay, b.perDay, 24 * time.Hour},
	} {
		allowed, err := b.limiter.Allow(ctx, window.subject, window.limit, window.length)
		if err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}

// admitMail spends the budget and answers a refusal: too_many_attempts, which
// depends on no account.
func admitMail(w http.ResponseWriter, r *http.Request, budget *MailBudget, log *slog.Logger) bool {
	allowed, err := budget.Spend(r.Context())
	if err != nil {
		log.ErrorContext(r.Context(), "could not check the installation's mail budget", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		httpx.Error(w, r, http.StatusTooManyRequests, codeTooManyAttempts,
			"This site is sending too many messages just now; wait a few minutes before trying again")
		return false
	}
	return true
}

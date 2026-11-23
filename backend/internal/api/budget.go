package api

import (
	"log/slog"
	"net/http"

	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/httpx"
)

// budgets is what a handler charges its request budgets through: the shared
// counter, and the log a counter that cannot be kept is reported to.
type budgets struct {
	httpx.Budgets
	log *slog.Logger
}

func newBudgets(counter httpx.Counter, log *slog.Logger) budgets {
	return budgets{Budgets: httpx.NewBudgets(counter), log: log}
}

// spend charges the request to each budget and answers a refusal; it reports
// whether the handler may go on.
func (b budgets) spend(w http.ResponseWriter, r *http.Request, charges ...httpx.Charge) bool {
	if err := b.Charge(r.Context(), charges...); err != nil {
		b.refuse(w, r, err)
		return false
	}
	return true
}

// refuse answers what Charge returned, from answerBudgetError. An error it has
// no answer for is ours, and still answered: the handler has already stopped.
func (b budgets) refuse(w http.ResponseWriter, r *http.Request, err error) {
	if answerBudgetError(w, r, b.log, err) {
		return
	}
	b.log.ErrorContext(r.Context(), "a request budget refused with an error it has no answer for", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

// perAccount is middleware that charges every request to the signed-in
// account's budget before the route runs. It goes after authentication.
func (b budgets) perAccount(budget httpx.KeyBudget) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if b.spend(w, r, ofAccount(r, budget)) {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// ofAccount is budget charged to the signed-in account, one key per account
// (CLAUDE.md rule 5).
func ofAccount(r *http.Request, budget httpx.KeyBudget) httpx.Charge {
	identity, _ := auth.IdentityFrom(r.Context())
	return budget.Of(identity.UserID.String())
}

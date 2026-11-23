package api

import (
	"net/http"

	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/go-chi/chi/v5"
)

// Participation is what every participant route is admitted through: the
// admitter, and the read budget the reads among them share across the play
// screen, the clarifications feed and the events channel.
type Participation struct {
	Admitter *participation.Admitter
	Reads    participation.Budget
}

// admit admits the request to the contest in its URL, spending budget before
// anything else, the identifier's parse included (CLAUDE.md rule 13), and
// answers a refusal with fail. Every participant route is admitted here, or by
// the same module inside queryproxy for the console and the reset, so none can
// skip the budget, the gate or the monitor visit.
func (p Participation) admit(w http.ResponseWriter, r *http.Request, mw *auth.Middleware, budget participation.Budget,
	fail func(http.ResponseWriter, *http.Request, error), opts ...participation.Option) (*participation.Admission, bool) {
	identity, _ := auth.IdentityFrom(r.Context())
	admission, err := p.Admitter.Admit(r.Context(), budget, participation.Request{
		Contest: chi.URLParam(r, contestIDParam), Account: identity.UserID,
		Address: clientAddress(r), Session: sessionTag(mw, r), UserAgent: r.UserAgent(),
	}, opts...)
	if err != nil {
		fail(w, r, err)
		return nil, false
	}
	return admission, true
}

// start records the admitted participant's start once the content is ready
// and before it is sent, and answers a refusal with fail.
func start(w http.ResponseWriter, r *http.Request, admission *participation.Admission, fail func(http.ResponseWriter, *http.Request, error)) bool {
	if err := admission.Start(r.Context()); err != nil {
		fail(w, r, err)
		return false
	}
	return true
}

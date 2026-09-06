package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// GET /contests/{id}/events — a Server-Sent Events channel for a participant
// of a running contest (docs/ARCHITECTURE.md §8).
//
// It carries exactly two things over the channel's lifetime: a "sync" event,
// every defaultResyncInterval, with server_now and this caller's own
// deadline (contests.Deadline — never with the grace queryproxy adds before
// refusing a late answer; see queryproxy.Service's own doc for why a
// deadline shown to a participant must not carry it); and a "contest_started"
// or "contest_finished" event when the contest's status is the reason the
// channel opened or closed. Nothing about another participant ever crosses
// it — every event is built from this caller's own contests.Participant and
// contests.Contest, the same two values Access resolves for the read
// endpoints (participant_handler.go), and nothing else is ever added to the
// payload.
//
// Access is decided exactly once, by the same façade the participant's read
// endpoints already use (ParticipantAccess — Access and AdmitRead), so this
// is not a fourth set of "may this student be here". What is new here, and
// exists only here, is what an HTTP endpoint that holds the connection open
// needs beyond a single admission check:
//
//   - A connection limit. AdmitRead bounds how often a caller may ask to open
//     one, keyed by account and shared with the rate every other read and
//     every query already spends from — a caller who opens and abandons
//     connections in a loop pays for every attempt, exactly as a caller who
//     hammers Run with queries that are all refused still pays for each one
//     (queryproxy.Service.Run's own doc, finding 3). It does not bound how
//     many may be open at once: a burst of `AdmitRead`-cleared attempts
//     could all succeed within the same window and never be asked to close
//     any of them. connLimiter is that second, independent bound — a plain
//     count per registration, checked after Access resolves who is asking,
//     so the number that matters (how many sockets one participant holds)
//     is capped regardless of how quickly they were opened.
//   - Not touching AdmitRead's budget again after the connection opens. A
//     tick's own resync calls Access directly, the same lookups Run pays for
//     on every query, but never AdmitRead: this is a periodic push this
//     server chose to make, not a caller-initiated request, and charging it
//     against the shared per-minute budget would let an idle, correctly
//     open connection quietly starve the very account it belongs to of the
//     query rate the architecture actually promises it (CLAUDE.md rule 5's
//     reasoning applied to a budget rather than a cache key: spend the
//     bounded, caller-controlled cost first, never a cost the server itself
//     schedules).
//   - Holding nothing while idle. Between ticks this goroutine holds a
//     ticker and a TCP socket the standard library already owns — no pooled
//     database connection, no held row lock. Access's two lookups acquire a
//     connection from the pool and return it before the next tick even
//     begins, the same way any other short request would. A room of two
//     hundred participants therefore costs, between ticks, two hundred idle
//     goroutines and sockets and nothing the game database's connection
//     pool would ever notice; during a tick it costs the same two lookups
//     Access always costs, spread over whatever fraction of
//     defaultResyncInterval two hundred participants' tickers happen to
//     land in.
//   - Ending on disconnect, on shutdown, and on nothing else. A write that
//     fails (the socket is gone) or r.Context().Done() (the standard
//     library cancels it when the client disconnects) both return from this
//     handler immediately, which is what releases the connection-limit slot
//     and stops the ticker — no goroutine here outlives its request. On a
//     process shutdown the same is true one instant sooner: shutdown is
//     closed by internal/app.App.Run at the same moment it starts draining
//     the servers, so every open connection's next select sees it and
//     returns before http.Server.Shutdown would otherwise wait out its own
//     timeout for a stream nothing was ever going to end on its own
//     (internal/platform/server's own doc names exactly this endpoint as
//     the reason WriteTimeout is absent).
type EventsHandler struct {
	access ParticipantAccess
	mw     *auth.Middleware
	log    *slog.Logger

	limiter *connLimiter
	// resync is how often an open connection re-asks Access for the
	// participant's current state and pushes it down the wire — a field,
	// not the constant below, so a test can shrink it instead of waiting on
	// the real clock (the same reason queryproxy.Service.WithClock exists).
	resync time.Duration
	// now is the clock server_now is read from — never the deadline itself,
	// which is contests.Deadline alone (see the type's own doc above); only
	// the value a participant's own browser clock is offset against.
	now func() time.Time
	// shutdown is closed when the process starts shutting down
	// (internal/app.App wires it to the same context main.go cancels on
	// SIGINT/SIGTERM). Without it, every open connection would keep this
	// handler's goroutine running until its own client disconnected, which
	// on a contest with participants still connected is "until the process
	// is killed" — exactly the leak this type's own doc promises not to be.
	shutdown <-chan struct{}
}

// defaultResyncInterval is the middle of the range
// docs/ARCHITECTURE.md §8 gives the frontend for its own resynchronisation
// (30 to 60 seconds): far enough apart that two hundred participants cost
// only a few of these round trips a second between them, close enough that a
// browser clock nobody trusts for more than half a minute never drifts
// further than that from the server's own.
const defaultResyncInterval = 30 * time.Second

// defaultMaxConnections is how many of this endpoint one participant may
// hold open at once.
//
// Not one: a browser tab reconnecting after a network blip briefly holds the
// old EventSource and the new one at once, and working from two tabs — the
// story in one, the console in another — is not abuse. Not unbounded
// either: the question this endpoint exists to answer under load is exactly
// "how many may one participant hold, and what happens to the rest"
// (this task's own check) — a script opening connections in a loop must hit
// a wall well before a two-hundred-seat room's worth of them could ever be
// one account's doing.
const defaultMaxConnections = 4

// eventSync, eventContestStarted and eventContestFinished are the three
// event names this channel ever writes (see EventsHandler's own doc).
const (
	eventSync            = "sync"
	eventContestStarted  = "contest_started"
	eventContestFinished = "contest_finished"
)

// NewEventsHandler assembles the events endpoint. shutdown is closed to end
// every open connection promptly when the process is stopping — pass
// ctx.Done() of the same context internal/app.App.Run waits on, exactly as
// its own field doc describes.
func NewEventsHandler(access ParticipantAccess, mw *auth.Middleware, log *slog.Logger, shutdown <-chan struct{}) *EventsHandler {
	return &EventsHandler{
		access:   access,
		mw:       mw,
		log:      log,
		limiter:  newConnLimiter(defaultMaxConnections),
		resync:   defaultResyncInterval,
		now:      func() time.Time { return time.Now().UTC() },
		shutdown: shutdown,
	}
}

// WithResyncInterval overrides the interval New defaults to thirty seconds.
// A deployment never calls this; tests use it to observe more than one tick
// without waiting thirty real seconds to do it.
func (h *EventsHandler) WithResyncInterval(d time.Duration) *EventsHandler {
	if d > 0 {
		h.resync = d
	}
	return h
}

// WithMaxConnections overrides how many connections one participant may hold
// at once. A deployment never calls this; tests use a small number so the
// limit can be reached with a handful of goroutines instead of a thousand.
func (h *EventsHandler) WithMaxConnections(n int) *EventsHandler {
	if n > 0 {
		h.limiter = newConnLimiter(n)
	}
	return h
}

// WithClock overrides the clock server_now is read from. A deployment never
// calls this and gets time.Now().UTC(); tests use it to make the value
// assertable instead of merely "close to when the test ran".
func (h *EventsHandler) WithClock(now func() time.Time) *EventsHandler {
	if now != nil {
		h.now = now
	}
	return h
}

// ActiveConnections reports how many of this endpoint's connections
// participantID currently holds open. Exported so a test can prove the limit
// above is enforced by counting rather than by racing real sockets and hoping
// the timing lines up (this task's own instruction: "a connection limit ...
// [is] testable by counting").
func (h *EventsHandler) ActiveConnections(participantID uuid.UUID) int {
	return h.limiter.count(participantID)
}

// Mount registers the route.
func (h *EventsHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate)
		r.Get("/contests/{"+contestIDParam+"}/events", h.events)
	})
}

func (h *EventsHandler) events(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	// The same pre-lookup charge every other participant-facing read pays
	// (see ParticipantAccess.AdmitRead's own doc): bounded by account, ahead
	// of any lookup, so a caller opening and abandoning connections in a
	// loop is charged for every attempt.
	if err := h.access.AdmitRead(identity.UserID); err != nil {
		h.fail(w, r, err)
		return
	}

	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, auth.CodeInvalidContestID, "Contest identifier is not valid")
		return
	}

	addr := clientAddress(r)
	participant, contest, err := h.access.Access(r.Context(), contestID, identity.UserID, addr)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	if !h.limiter.acquire(participant.ID) {
		httpx.Error(w, r, http.StatusTooManyRequests, codeTooManyConnections,
			"This participant already holds as many live event channels as this installation allows")
		return
	}
	defer h.limiter.release(participant.ID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Some reverse proxies buffer a response until it ends or fills an
	// internal buffer, which would turn every event on this channel into one
	// that arrives in a batch minutes later, or on the way out when the
	// connection finally closes. Harmless to send when the proxy in front
	// does not look at it.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)

	if err := h.sendSync(w, contest, participant); err != nil {
		return
	}
	// contest.Status is always StatusRunning here: Access never succeeds
	// otherwise (queryproxy.Service.Admitted's own doc). Sent once, on
	// connect, so a frontend that opens this channel speculatively while
	// waiting for a contest to start learns "it has" the moment the
	// connection itself succeeds, without a fourth status check of its own.
	if err := writeEvent(w, eventContestStarted, statusPayload{Status: contest.Status}); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	ticker := time.NewTicker(h.resync)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.shutdown:
			return
		case <-ticker.C:
			participant, contest, err = h.access.Access(r.Context(), contestID, identity.UserID, addr)
			if err != nil {
				// ErrFinished and ErrContestNotRunning both mean the
				// contest's window is over for this participant — the
				// status moved to finished, or their own deadline passed
				// while the scheduler has not caught up yet (§8: the status
				// and this channel affect only what the interface shows,
				// never the closing guarantee itself). A caller turned away
				// by ErrNotAParticipant (disqualified mid-contest) or
				// ErrAddressNotAllowed learns nothing more specific than
				// the channel closing — the same "not this caller's
				// business" rule the read endpoints already apply to a
				// refusal that is not about the contest's own clock.
				if errors.Is(err, queryproxy.ErrFinished) || errors.Is(err, queryproxy.ErrContestNotRunning) {
					if writeEvent(w, eventContestFinished, statusPayload{Status: contests.StatusFinished}) == nil {
						_ = rc.Flush()
					}
				}
				return
			}
			if err := h.sendSync(w, contest, participant); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// statusPayload is the whole body of a contest_started or contest_finished
// event: the contest's status and nothing else (§8's own words: "события
// несут статус олимпиады. И больше ничего").
type statusPayload struct {
	Status string `json:"status"`
}

// syncPayload is the whole body of a sync event: server_now, for the
// frontend to compute its own clock offset against, and this participant's
// own deadline — never anyone else's, and never with queryproxy's grace
// added (see EventsHandler's own doc and queryproxy.Service's own comment on
// why a deadline shown to a participant must not carry it).
type syncPayload struct {
	ServerNow string `json:"server_now"`
	// Deadline is absent, not null, when contests.Deadline has none yet — an
	// individual-timing participant who has not started (Deadline's own
	// doc). A client sees no deadline field at all rather than one it has to
	// know means "not started" instead of "no limit".
	Deadline string `json:"deadline,omitempty"`
}

// sendSync writes one sync event for contest and participant's current
// state, computing the deadline the one way this codebase ever computes one.
func (h *EventsHandler) sendSync(w http.ResponseWriter, contest contests.Contest, participant contests.Participant) error {
	payload := syncPayload{ServerNow: h.now().Format(time.RFC3339)}
	if deadline, ok := contests.Deadline(contest, participant); ok {
		payload.Deadline = deadline.Format(time.RFC3339)
	}
	return writeEvent(w, eventSync, payload)
}

// writeEvent writes one Server-Sent Event. The caller flushes: several
// events are sometimes written back to back (the initial sync and
// contest_started), and flushing once for the pair is one syscall instead of
// two for a client that is about to read both anyway.
func writeEvent(w http.ResponseWriter, event string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", event, err)
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	return err
}

// connLimiter bounds how many of one thing a single identifier may hold at
// once. Used here keyed by registration id (see EventsHandler's own doc for
// why this exists beside AdmitRead's own, different bound).
type connLimiter struct {
	mu   sync.Mutex
	max  int
	open map[uuid.UUID]int
}

func newConnLimiter(max int) *connLimiter {
	return &connLimiter{max: max, open: map[uuid.UUID]int{}}
}

// acquire reports whether id may hold one more, and counts it if so.
func (l *connLimiter) acquire(id uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[id] >= l.max {
		return false
	}
	l.open[id]++
	return true
}

// release gives back a slot acquire counted. Every acquire that returned true
// is matched by exactly one release, from a defer right beside it, so the
// count an idle connection holds is exactly the connections actually open —
// never one leaked by a handler that returned early.
func (l *connLimiter) release(id uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.open[id]--
	if l.open[id] <= 0 {
		delete(l.open, id)
	}
}

func (l *connLimiter) count(id uuid.UUID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open[id]
}

// fail maps a refusal from ParticipantAccess.AdmitRead or .Access to a
// response.
//
// CLAUDE.md rule 1: every one of these is a declared sentinel with a mapping
// here and a handler test asserting the 4xx it produces.
func (h *EventsHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, queryrunner.ErrTooManyQueries):
		httpx.Error(w, r, http.StatusTooManyRequests, codeQueryTooOften,
			"This caller is asking faster than this installation allows")
	case errors.Is(err, queryproxy.ErrNotAParticipant):
		// The same answer whether the caller never registered, was
		// disqualified, or the contest named in the URL belongs to somebody
		// else entirely (participant_handler.go's own fail carries the same
		// reasoning for the same sentinel).
		httpx.Error(w, r, http.StatusForbidden, codeNotAParticipant, "The caller is not taking part in this contest")
	case errors.Is(err, queryproxy.ErrContestNotRunning):
		httpx.Error(w, r, http.StatusConflict, codeContestNotRunning, "The contest is not running")
	case errors.Is(err, queryproxy.ErrFinished):
		httpx.Error(w, r, http.StatusConflict, codeContestFinished, "The participant has already finished")
	case errors.Is(err, queryproxy.ErrAddressNotAllowed):
		httpx.Error(w, r, http.StatusForbidden, codeAddressNotAllowed,
			"This contest is only available from the university network")
	case errors.Is(err, queryproxy.ErrUnavailable):
		h.log.ErrorContext(r.Context(), "could not resolve participant access", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	default:
		h.log.ErrorContext(r.Context(), "could not open the events channel", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// EventsAccess is the slice of queryproxy.Service this handler needs.
// AccessForEvents differs from Access in one way: it also admits a published
// contest that has not started, so an early participant learns of the start
// without polling. Every read and answer still goes through Access.
type EventsAccess interface {
	AdmitRead(userID uuid.UUID) error
	// AccessForEvents also returns the participant's Standing, refused or not,
	// so a refused resync can tell whether the contest is over for them.
	AccessForEvents(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, contests.Standing, error)
}

// EventsHandler serves GET /contests/{id}/events, a Server-Sent Events channel
// for a participant of a running contest, or a published one waiting to start
// (docs/ARCHITECTURE.md §8).
//
// It sends a "sync" event every resync interval with server_now and this
// caller's deadline (contests.Deadline, without the gate's grace: that is an
// allowance for a request already in flight, not time to show),
// "contest_started" when the contest starts, and "contest_finished" when the
// channel closes because the contest is over for this participant. Every event
// is built from this caller's own Participant and Contest; nothing about
// another participant crosses it.
//
// Beyond admission, a held-open connection needs:
//
//   - A connection limit. AdmitRead bounds how often a caller may open one
//     (charged like any read, CLAUDE.md rule 13), not how many are open at
//     once; connLimiter caps that per registration.
//   - No AdmitRead charge per tick. A resync is a push the server chose, and
//     charging it would let an idle connection starve its own account's query
//     rate.
//   - Paced reconnects. The stream's retry field is set to the resync interval
//     instead of EventSource's three-second default, which would spend the
//     account's read budget on flaky wifi.
//   - Tolerance of transient failures. queryproxy.ErrUnavailable on a tick is
//     retried next tick; only a real refusal closes the channel.
//   - Nothing held while idle. Between ticks a connection holds a ticker and a
//     socket, no pooled database connection or lock.
//   - An end on disconnect, on shutdown, or on a stalled write (writeTimeout).
type EventsHandler struct {
	access EventsAccess
	mw     *auth.Middleware
	log    *slog.Logger

	limiter *connLimiter
	// resync is how often an open connection re-asks for the participant's
	// state; a field so a test can shrink it.
	resync time.Duration
	// writeTimeout bounds one write; a field so a test can shrink it.
	writeTimeout time.Duration
	// now is the clock server_now is read from, never the deadline's.
	now func() time.Time
	// shutdown is closed when the process starts shutting down; without it each
	// open stream would run until its client left.
	shutdown <-chan struct{}
}

// defaultResyncInterval sits in the 30 to 60 seconds docs/ARCHITECTURE.md §8
// gives: two hundred participants cost a few lookups a second, and a browser
// clock never drifts more than half a minute from the server's.
const defaultResyncInterval = 30 * time.Second

// defaultMaxConnections is how many channels one participant may hold open at
// once. More than one, because a reconnecting tab briefly holds two and working
// from two tabs is normal; small, so a script opening connections in a loop
// hits a wall quickly.
//
// The count is per process, not in the shared cache: with N replicas the real
// cap is defaultMaxConnections * N.
const defaultMaxConnections = 4

// writeTimeout bounds how long one write may block before the client is treated
// as gone. A peer that stops reading holds a zero TCP window that never returns
// an error, so without it the goroutine and its connection slot are held for
// the life of the process. Every event is a few dozen bytes, so any client
// still reading finishes well inside ten seconds.
const writeTimeout = 10 * time.Second

const (
	eventSync            = "sync"
	eventContestStarted  = "contest_started"
	eventContestFinished = "contest_finished"
)

// NewEventsHandler assembles the events endpoint. shutdown is closed to end
// every open connection when the process stops: pass the Done channel of the
// context internal/app.App.Run waits on.
func NewEventsHandler(access EventsAccess, mw *auth.Middleware, log *slog.Logger, shutdown <-chan struct{}) *EventsHandler {
	return &EventsHandler{
		access:       access,
		mw:           mw,
		log:          log,
		limiter:      newConnLimiter(defaultMaxConnections),
		resync:       defaultResyncInterval,
		writeTimeout: writeTimeout,
		now:          func() time.Time { return time.Now().UTC() },
		shutdown:     shutdown,
	}
}

// WithResyncInterval overrides the thirty-second resync interval, for tests.
func (h *EventsHandler) WithResyncInterval(d time.Duration) *EventsHandler {
	if d > 0 {
		h.resync = d
	}
	return h
}

// WithWriteTimeout overrides the ten-second write timeout, for tests.
func (h *EventsHandler) WithWriteTimeout(d time.Duration) *EventsHandler {
	if d > 0 {
		h.writeTimeout = d
	}
	return h
}

// WithMaxConnections overrides the per-participant connection cap, for tests.
func (h *EventsHandler) WithMaxConnections(n int) *EventsHandler {
	if n > 0 {
		h.limiter = newConnLimiter(n)
	}
	return h
}

// WithClock overrides the clock server_now is read from, for tests.
func (h *EventsHandler) WithClock(now func() time.Time) *EventsHandler {
	if now != nil {
		h.now = now
	}
	return h
}

// ActiveConnections reports how many connections participantID holds open, so a
// test can check the limit by counting instead of racing sockets.
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
	// Charged like every participant read, ahead of any lookup, so opening and
	// abandoning connections in a loop pays for each attempt.
	if err := h.access.AdmitRead(identity.UserID); err != nil {
		h.fail(w, r, err)
		return
	}

	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	addr := clientAddress(r)
	participant, contest, _, err := h.access.AccessForEvents(r.Context(), contestID, identity.UserID, addr)
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

	// Past every check that could answer an ordinary 4xx: from here the request
	// is a stream, and its duration belongs in the stream histogram
	// (metrics.MarkStreaming).
	metrics.MarkStreaming(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Stops reverse proxies that buffer responses from delivering events in
	// batches; harmless where the proxy ignores it.
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	h.setWriteDeadline(rc)
	w.WriteHeader(http.StatusOK)

	// Sent once, before the first event, to pace reconnects.
	if err := writeRetry(w, h.resync); err != nil {
		return
	}
	if err := h.sendSync(w, contest, participant); err != nil {
		return
	}
	// Only for a running contest: a published one has not started, and the loop
	// below announces the start when a tick observes it.
	if contest.Status == contests.StatusRunning {
		if err := writeEvent(w, eventContestStarted, statusPayload{Status: contest.Status}); err != nil {
			return
		}
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
			// The stream outlives the session check made when it opened, so
			// each tick checks again. An invalid session or account ends the
			// stream; a store that is briefly unreadable is retried next tick.
			alive, err := h.mw.SessionStillValid(r)
			if err != nil {
				h.log.WarnContext(r.Context(), "events resync could not read the session; retrying next tick", "error", err)
				continue
			}
			if !alive {
				return
			}
			newParticipant, newContest, standing, err := h.access.AccessForEvents(r.Context(), contestID, identity.UserID, addr)
			if err != nil {
				// A store that is briefly away is not a refusal of this
				// participant; retry next tick.
				if errors.Is(err, queryproxy.ErrUnavailable) {
					h.log.WarnContext(r.Context(), "events resync could not reach storage; retrying next tick", "error", err)
					continue
				}
				// Over means the registration or the contest finished, or the
				// participant's own time ran out before the scheduler caught
				// up. Anything not over (a contest back in draft, an address no
				// longer allowed) closes silently, since it may yet let them
				// back in. A disqualified participant is over but still closes
				// silently: the channel tells them nothing a stranger would not
				// be told.
				if standing.Over() && !errors.Is(err, contests.ErrNotAParticipant) {
					h.setWriteDeadline(rc)
					if writeEvent(w, eventContestFinished, statusPayload{Status: contests.StatusFinished}) == nil {
						_ = rc.Flush()
					}
				}
				return
			}
			// Assigned only on success: after a transient failure the last good
			// state must stay, or the next tick would compare against a zero
			// value and announce a fresh start.
			wasRunning := contest.Status == contests.StatusRunning
			participant, contest = newParticipant, newContest
			// The write deadline is armed only after the lookup, so a slow
			// database round trip on our side never counts against the client.
			h.setWriteDeadline(rc)
			// The published to running transition, announced when a tick
			// observes it.
			if contest.Status == contests.StatusRunning && !wasRunning {
				if err := writeEvent(w, eventContestStarted, statusPayload{Status: contest.Status}); err != nil {
					return
				}
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

// setWriteDeadline extends the write deadline before the next write. Its error
// is ignored: some writers (a proxy's, a test recorder) do not support
// deadlines, and that is no reason to refuse a write.
func (h *EventsHandler) setWriteDeadline(rc *http.ResponseController) {
	_ = rc.SetWriteDeadline(time.Now().Add(h.writeTimeout))
}

// writeRetry writes the stream's retry field: how long a client should wait
// before reconnecting.
func writeRetry(w http.ResponseWriter, d time.Duration) error {
	_, err := fmt.Fprintf(w, "retry: %d\n\n", d.Milliseconds())
	return err
}

// statusPayload is the whole body of a contest_started or contest_finished
// event: the status and nothing else (§8).
type statusPayload struct {
	Status string `json:"status"`
}

// syncPayload is the whole body of a sync event: server_now for the client's
// clock offset, and this participant's own deadline without the gate's grace.
type syncPayload struct {
	ServerNow string `json:"server_now"`
	// Deadline is omitted while contests.Deadline has none, as for an
	// individual-timing participant who has not started.
	Deadline string `json:"deadline,omitempty"`
}

// sendSync writes one sync event for the current state.
func (h *EventsHandler) sendSync(w http.ResponseWriter, contest contests.Contest, participant contests.Participant) error {
	payload := syncPayload{ServerNow: h.now().Format(time.RFC3339)}
	if deadline, ok := contests.Deadline(contest, participant); ok {
		payload.Deadline = deadline.Format(time.RFC3339)
	}
	return writeEvent(w, eventSync, payload)
}

// writeEvent writes one Server-Sent Event. The caller flushes, so events
// written back to back cost one flush.
func writeEvent(w http.ResponseWriter, event string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", event, err)
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	return err
}

// connLimiter bounds how many connections one registration holds at once.
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

// release gives back a slot acquire counted. Each successful acquire is paired
// with a deferred release, so no early return leaks a slot.
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

// fail maps a refusal from AdmitRead or AccessForEvents to a response, from the
// same tables the console and the play screen use (CLAUDE.md rule 1).
func (h *EventsHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if queryproxyErrors.answer(w, r, h.log, err) || queryrunnerErrors.answer(w, r, h.log, err) {
		return
	}
	h.log.ErrorContext(r.Context(), "could not open the events channel", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

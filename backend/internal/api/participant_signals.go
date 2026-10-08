package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The signals a participant's own browser reports from the play screen —
// leaving the page and pasting text (design §2.2) — sent in batches to
// POST /contests/{id}/play/signals.
//
// Admitted like the rest of /play (queryproxy.Service.Access), but spending a
// budget of its own (monitor.Signals.AdmitBatch) rather than the read budget
// the SQL console shares: a browser reporting that its participant switched
// windows must not take a query away from them. The budget is spent first,
// before the body is read or anything is looked up (CLAUDE.md rule 13).
//
// What a browser sends is a claim, and one bad signal must not cost the good
// ones beside it: an event this handler cannot read, of a kind a browser may
// not report, or outside its bounds is dropped (monitor.CleanBatch), and the
// batch still answers 204. The batch itself is refused only when it is too
// large — more than monitor.MaxBatchEvents events, or a body over
// maxSignalBodyBytes, which is refused as the bytes arrive, before anything
// is decoded (rule 12).

// SignalRecorder is the slice of monitor.Signals these endpoints need.
type SignalRecorder interface {
	AdmitBatch(ctx context.Context, account uuid.UUID) error
	Record(ctx context.Context, events []monitor.Event) (kept int, err error)
}

// maxSignalBodyBytes bounds a batch's body. Fifty events of the largest kind —
// a paste carrying 500 characters, each at most six bytes once JSON-escaped —
// come to about 160 KiB; anything much beyond that is not a batch the play
// screen sends.
const maxSignalBodyBytes = 256 << 10

// signalsKeptHeader reports how many of a batch's events were stored, so a
// client (and a test) can see what was dropped without a body on a 204.
const signalsKeptHeader = "X-Signals-Kept"

// WithSignals serves the browser-signal endpoint from signals. Without it the
// route is not mounted at all.
func (h *ParticipantHandler) WithSignals(signals SignalRecorder) *ParticipantHandler {
	h.signals = signals
	return h
}

// mountSignals registers the signal route on an authenticated router.
func (h *ParticipantHandler) mountSignals(r chi.Router) {
	if h.signals == nil {
		return
	}
	r.Post("/contests/{"+contestIDParam+"}/play/signals", h.postSignals)
}

// signalsRequest is the body of POST .../play/signals. Each event is kept
// raw until the batch's size has been checked, so no event is decoded in a
// batch that is refused anyway, and so one unreadable event is dropped
// rather than failing the whole body.
type signalsRequest struct {
	Events *[]json.RawMessage `json:"events"`
}

// signalEvent is one event as a browser sends it: the kind, the time the
// browser claims, and the fields of that kind's payload, flat.
type signalEvent struct {
	Kind     monitor.Kind `json:"kind"`
	ClientAt string       `json:"client_at"`
	AwayMs   int64        `json:"away_ms"`
	Target   string       `json:"target"`
	Chars    int          `json:"chars"`
	Text     string       `json:"text"`
}

// toEvent reads one raw event into a monitor event of the admitted
// registration, or reports it unreadable. Only the kinds a browser may send
// are read at all; anything else is dropped here, before CleanBatch would
// drop it again.
func toEvent(raw json.RawMessage, contest contests.Contest, participant contests.Participant) (monitor.Event, bool) {
	var in signalEvent
	if err := json.Unmarshal(raw, &in); err != nil {
		return monitor.Event{}, false
	}
	event := monitor.Event{Contest: contest.ID, Registration: participant.ID}
	switch in.Kind {
	case monitor.KindPageLeft:
		event.Payload = monitor.PageLeft{AwayMs: in.AwayMs}
	case monitor.KindPaste:
		event.Payload = monitor.Paste{Target: monitor.PasteTarget(in.Target), Chars: in.Chars, Text: in.Text}
	default:
		return monitor.Event{}, false
	}
	// A claimed time that is not a time is ignored, not a reason to lose the
	// event: it was only ever a claim.
	if claimed, err := time.Parse(time.RFC3339Nano, in.ClientAt); err == nil {
		event.ClientAt = &claimed
	}
	return event, true
}

func (h *ParticipantHandler) postSignals(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.signals.AdmitBatch(r.Context(), identity.UserID); err != nil {
		h.failSignals(w, r, err)
		return
	}

	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	// The body is read and its size checked before Access: both are cheap,
	// and a batch refused for its shape costs no lookup.
	var req signalsRequest
	if err := httpx.DecodeJSONWithin(w, r, &req, maxSignalBodyBytes); err != nil {
		if errors.Is(err, httpx.ErrBodyTooLarge) {
			h.failSignals(w, r, monitor.ErrBatchTooLarge)
			return
		}
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	if req.Events == nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "events is required")
		return
	}
	raw := *req.Events
	if len(raw) > monitor.MaxBatchEvents {
		h.failSignals(w, r, monitor.ErrBatchTooLarge)
		return
	}

	participant, contest, err := h.access.Access(r.Context(), contestID, identity.UserID, clientAddress(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.observe(r, participant, contest)

	events := make([]monitor.Event, 0, len(raw))
	for _, one := range raw {
		if event, ok := toEvent(one, contest, participant); ok {
			events = append(events, event)
		}
	}
	kept, err := h.signals.Record(r.Context(), events)
	if err != nil {
		h.failSignals(w, r, err)
		return
	}
	w.Header().Set(signalsKeptHeader, strconv.Itoa(kept))
	w.WriteHeader(http.StatusNoContent)
}

// failSignals maps a signal refusal to a response (CLAUDE.md rule 1).
// Anything else is ours, and an internal error.
func (h *ParticipantHandler) failSignals(w http.ResponseWriter, r *http.Request, err error) {
	if monitorErrors.answer(w, r, h.log, err) {
		return
	}
	h.log.ErrorContext(r.Context(), "the participant's browser signals could not be stored", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

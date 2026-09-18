package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// The signals a participant's browser reports (design §2.2): absences from
// the page and pastes, sent in batches. What arrives is whatever the browser
// chose to send, so the rules here are about keeping it bounded and keeping
// one bad signal from costing the good ones beside it.

const (
	// BatchesPerMinute is how many batches one account may send a minute,
	// refused ones included (CLAUDE.md rule 13). A browser flushes every ten
	// seconds and on every hide of the page; twelve leaves room for a
	// participant flicking between windows without leaving room for a flood.
	BatchesPerMinute = 12
	// MaxClaimSkew is how far a browser's claimed time may be from the
	// server's before it is ignored. The event is kept either way; a claim a
	// day off is a wrong clock, and showing it beside the server's time would
	// only mislead.
	MaxClaimSkew = 24 * time.Hour
)

// signalWindow is the batch throttle's fixed window.
const signalWindow = time.Minute

// ErrSignalsTooOften: the account has sent BatchesPerMinute batches this
// minute already.
var ErrSignalsTooOften = fmt.Errorf("at most %d signal batches a minute", BatchesPerMinute)

// SignalRetryAfter is how long a caller refused with ErrSignalsTooOften
// should wait at most: the whole window, since its start is not known here.
func SignalRetryAfter() time.Duration { return signalWindow }

// CleanBatch keeps what a browser batch may store and drops the rest.
//
// Every per-event refusal is a drop, not a refusal of the batch: an absence
// shorter than MinAway (which is not recorded, by design), a paste into an
// unknown target, an event without a payload, and any kind the server
// records for itself — a browser does not get to report an address change.
// The kept events come out normalised (Event.Normalize), in the order given,
// with a claimed time more than MaxClaimSkew away from now removed.
//
// The one refusal is the batch's size, checked before any event is looked
// at: ErrBatchTooLarge.
func CleanBatch(events []Event, now time.Time) (kept []Event, dropped int, err error) {
	if err := CheckBatch(events); err != nil {
		return nil, 0, err
	}
	kept = make([]Event, 0, len(events))
	for _, event := range events {
		if !event.Kind().FromBrowser() {
			dropped++
			continue
		}
		clean, err := event.Normalize()
		if err != nil {
			dropped++
			continue
		}
		if clean.ClientAt != nil && clean.ClientAt.Sub(now).Abs() > MaxClaimSkew {
			clean.ClientAt = nil
		}
		kept = append(kept, clean)
	}
	return kept, dropped, nil
}

// Limiter is the slice of auth.Limiter the batch throttle needs.
type Limiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// Signals admits and records the batches a participant's browser sends.
type Signals struct {
	limiter Limiter
	events  EventWriter
	now     func() time.Time
}

// NewSignals returns the browser-signal service.
func NewSignals(limiter Limiter, events EventWriter) *Signals {
	return &Signals{limiter: limiter, events: events, now: time.Now}
}

// WithClock replaces the clock claimed times are compared with; for tests.
func (s *Signals) WithClock(now func() time.Time) *Signals {
	s.now = now
	return s
}

// AdmitBatch spends one batch of the account's budget, and refuses with
// ErrSignalsTooOften once this minute's is spent. The caller asks it before
// anything else — before the participant and the contest are looked up and
// before the body is read — so every attempt counts, a refused one included
// (CLAUDE.md rule 13).
//
// Keyed by the account, which is known before those lookups and is one key
// per account assigned at sign-in, never named by the request (rule 5). Its
// own budget: signals must not spend the reads the SQL console shares, nor
// the workspace's autosave writes.
func (s *Signals) AdmitBatch(ctx context.Context, account uuid.UUID) error {
	allowed, err := s.limiter.Allow(ctx, "signals:user:"+account.String(), BatchesPerMinute, signalWindow)
	if err != nil {
		// A counter that cannot be kept refuses: taking batches unthrottled
		// is what it exists to prevent. Not ErrSignalsTooOften — nobody
		// asked too often.
		return fmt.Errorf("check the signal batch rate: %w", err)
	}
	if !allowed {
		return ErrSignalsTooOften
	}
	return nil
}

// Record cleans a batch (CleanBatch) and stores what is kept in one insert,
// returning how many events were kept. A batch with nothing to keep writes
// nothing.
func (s *Signals) Record(ctx context.Context, events []Event) (int, error) {
	kept, _, err := CleanBatch(events, s.now())
	if err != nil {
		return 0, err
	}
	if len(kept) == 0 {
		return 0, nil
	}
	if err := s.events.InsertEvents(ctx, kept); err != nil {
		return 0, fmt.Errorf("store %d browser signals: %w", len(kept), err)
	}
	return len(kept), nil
}

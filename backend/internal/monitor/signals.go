package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	// BatchesPerMinute counts refused batches too (CLAUDE.md rule 13). A
	// browser flushes every ten seconds and on every page hide.
	BatchesPerMinute = 12
	// MaxClaimSkew is how far a claimed time may be from the server's before
	// the claim is dropped; the event is kept.
	MaxClaimSkew = 24 * time.Hour
)

const signalWindow = time.Minute

var ErrSignalsTooOften = fmt.Errorf("at most %d signal batches a minute", BatchesPerMinute)

// SignalRetryAfter is the longest a caller refused with ErrSignalsTooOften
// should wait: the whole window, since its start is not known here.
func SignalRetryAfter() time.Duration { return signalWindow }

// CleanBatch keeps what a browser batch may store and drops the rest. A bad
// event is dropped, not the batch; so is any server-only kind, since a
// browser may not report an address change. Kept events are normalised, in
// order, with claimed times past MaxClaimSkew removed.
//
// A paste identical to the one kept just before it is folded into its Count,
// and pastes past MaxBatchPastes are dropped. The only refusal is the batch's
// size (ErrBatchTooLarge), checked first.
func CleanBatch(events []Event, now time.Time) (kept []Event, dropped int, err error) {
	if err := CheckBatch(events); err != nil {
		return nil, 0, err
	}
	kept = make([]Event, 0, len(events))
	pastes := 0
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
		if paste, ok := clean.Payload.(Paste); ok {
			if foldPaste(kept, paste) {
				continue
			}
			if pastes == MaxBatchPastes {
				dropped++
				continue
			}
			pastes++
		}
		kept = append(kept, clean)
	}
	return kept, dropped, nil
}

// foldPaste counts paste into the last kept event when it is the same paste.
// Count is zero for a single paste, so the first fold makes it two.
func foldPaste(kept []Event, paste Paste) bool {
	if len(kept) == 0 {
		return false
	}
	last, ok := kept[len(kept)-1].Payload.(Paste)
	if !ok || last.Target != paste.Target || last.Text != paste.Text || last.Chars != paste.Chars {
		return false
	}
	last.Count = max(last.Count, 1) + 1
	kept[len(kept)-1].Payload = last
	return true
}

// Limiter is the slice of auth.Limiter the batch throttle needs.
type Limiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

type Signals struct {
	limiter Limiter
	events  EventWriter
	now     func() time.Time
}

func NewSignals(limiter Limiter, events EventWriter) *Signals {
	return &Signals{limiter: limiter, events: events, now: time.Now}
}

// WithClock replaces the clock, for tests.
func (s *Signals) WithClock(now func() time.Time) *Signals {
	s.now = now
	return s
}

// AdmitBatch spends one batch of the account's budget, refusing with
// ErrSignalsTooOften once this minute's is spent. The caller asks it before
// any lookup or body read, so every attempt counts (CLAUDE.md rule 13). It is
// keyed by the account, one bounded key per account and never a value from the
// request (CLAUDE.md rule 5), and is a budget of its own, apart from console
// reads and autosave writes.
func (s *Signals) AdmitBatch(ctx context.Context, account uuid.UUID) error {
	allowed, err := s.limiter.Allow(ctx, "signals:user:"+account.String(), BatchesPerMinute, signalWindow)
	if err != nil {
		// A counter that cannot be kept refuses, but not with
		// ErrSignalsTooOften.
		return fmt.Errorf("check the signal batch rate: %w", err)
	}
	if !allowed {
		return ErrSignalsTooOften
	}
	return nil
}

// Record cleans a batch and stores what is kept in one insert, returning how
// many events were kept.
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

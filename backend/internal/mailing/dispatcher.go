package mailing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/platform/mail"
)

// The retry policy for a message the relay defers. It is tried again after a
// wait that doubles from a minute up to an hour; the MaxAttempts-th deferral,
// or one whose retry would come MaxAge after the message was queued, abandons
// it. A failure of the relay itself charges no message (see relayFailed).
const (
	MaxAttempts = 8

	firstRetry = time.Minute
	lastRetry  = time.Hour
)

// RefusalsBeforeBreaking is how many refusals of the message in a row, with
// nothing delivered between them, are taken for the relay refusing everyone
// (Exchange's 550 5.1.8 for a mailbox it has restricted): a roster seldom
// holds three bad addresses in a row, and each refusal costs a message.
const RefusalsBeforeBreaking = 3

// MaxAge bounds how long a message, and the one-time password its body may
// hold, is kept unsent: an older one is abandoned and never sent, whatever
// the relay is doing.
const MaxAge = 24 * time.Hour

// The dispatcher's pause after a failure of the relay: from thirty seconds,
// doubling to fifteen minutes, back to thirty after the relay next answers
// for a message.
const (
	firstPause = 30 * time.Second
	lastPause  = 15 * time.Minute
)

// ClaimLease is how long a claimed message stays out of every other
// dispatcher's reach. It outlasts the transport's dial and send timeouts by
// minutes, so a slow send is never taken again by another replica; a process
// that dies mid-send leaves the message due again once it ends.
const ClaimLease = 5 * time.Minute

// MaxLastErrorBytes bounds the reason stored with a failed attempt.
const MaxLastErrorBytes = 500

// recordTimeout bounds writing an attempt's outcome, which runs even after
// the run was cancelled: a delivered message not marked sent would go out
// again.
const recordTimeout = 10 * time.Second

// Outcomes of an attempt, as the metrics label them.
const (
	OutcomeSent      = "sent"
	OutcomeRetry     = "retry"
	OutcomeAbandoned = "abandoned"
)

// Outcomes is every outcome label value.
func Outcomes() []string { return []string{OutcomeSent, OutcomeRetry, OutcomeAbandoned} }

// Queued is a stored message as a claim returns it.
type Queued struct {
	ID int64
	Message
	// Attempts counts the claim that returned it.
	Attempts  int
	CreatedAt time.Time
}

// Store is the dispatcher's view of the outbox table.
type Store interface {
	Expirer
	// Claim takes the oldest due message, counts an attempt and leases it for
	// lease, unless perMinute messages were claimed in the last minute by any
	// replica; ok is false when it took nothing.
	Claim(ctx context.Context, perMinute int, lease time.Duration) (q Queued, ok bool, err error)
	// MarkSent and Abandon finish a message and erase its body.
	MarkSent(ctx context.Context, id int64) error
	Abandon(ctx context.Context, id int64, lastError string) error
	// Retry makes the message due again after the given wait.
	Retry(ctx context.Context, id int64, after time.Duration, lastError string) error
	// Release is Retry with the claim's attempt refunded, for an attempt that
	// said nothing about the message.
	Release(ctx context.Context, id int64, after time.Duration, lastError string) error
	CountDue(ctx context.Context) (int, error)
}

// Transport hands one message to the relay; its errors are classified by
// mail.IsTemporary and mail.IsRelayFailure.
type Transport interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Counters receives each attempt's outcome, the relay's failures and the
// number of due messages.
type Counters interface {
	Count(kind, outcome string)
	RelayFailed()
	SetRelayFailing(failing bool)
	SetDue(n int)
}

// DispatcherConfig assembles a Dispatcher. Now defaults to time.Now.
type DispatcherConfig struct {
	Store     Store
	Transport Transport
	// PerMinute is SMTP_MAX_PER_MINUTE, at least 1.
	PerMinute int
	Counters  Counters
	Logger    *slog.Logger
	Now       func() time.Time
}

// Dispatcher delivers what the outbox holds.
type Dispatcher struct {
	cfg DispatcherConfig

	// mu keeps runs apart: the pause below is their shared state.
	mu sync.Mutex
	// pausedUntil and pauses are the relay's backoff, kept per process: each
	// replica learns of a failing relay on its own attempt.
	pausedUntil time.Time
	pauses      int
	// refusals counts the messages refused since the last delivery; it is
	// not reset by the breaker, so a relay still refusing after the pause
	// trips it again at once rather than costing more messages.
	refusals int
}

// NewDispatcher needs every field of cfg but Now; it panics when PerMinute is
// below 1.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.PerMinute < 1 {
		panic("mailing: the dispatcher needs a rate of at least one message a minute")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Dispatcher{cfg: cfg}
}

// Drain first abandons the messages past MaxAge, then, unless a failure of the
// relay paused it, sends due messages one at a time until none is due, the
// rate is spent, the relay fails or defers, or ctx ends. It returns only
// storage failures; the relay's are recorded on the message.
func (d *Dispatcher) Drain(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := expire(ctx, d.cfg.Store, d.cfg.Counters, d.cfg.Logger); err != nil {
		return err
	}
	for ctx.Err() == nil && !d.cfg.Now().Before(d.pausedUntil) {
		q, ok, err := d.cfg.Store.Claim(ctx, d.cfg.PerMinute, ClaimLease)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		next, err := d.attempt(ctx, q)
		if err != nil {
			return err
		}
		if !next {
			break
		}
	}

	if ctx.Err() != nil {
		return nil
	}
	due, err := d.cfg.Store.CountDue(ctx)
	if err != nil {
		return err
	}
	d.cfg.Counters.SetDue(due)
	return nil
}

// attempt sends one claimed message and records how it ended. next is false
// when the run should stop: the relay failed, or ctx ended.
func (d *Dispatcher) attempt(ctx context.Context, q Queued) (next bool, err error) {
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	if age := d.cfg.Now().Sub(q.CreatedAt); age >= MaxAge {
		return true, d.abandon(record, q, "abandoned unsent: queued more than "+MaxAge.String()+" ago")
	}
	if q.Attempts > MaxAttempts {
		return true, d.abandon(record, q, "abandoned unsent: the message was claimed again after its last attempt")
	}

	sendErr := d.cfg.Transport.Send(ctx, q.To, q.Subject, q.Body)
	switch {
	case sendErr == nil:
		if err := d.cfg.Store.MarkSent(record, q.ID); err != nil {
			return false, err
		}
		d.refusals = 0
		d.relayAnswered()
		d.cfg.Counters.Count(string(q.Kind), OutcomeSent)
		d.cfg.Logger.DebugContext(ctx, "sent a message", "message", q.ID, "kind", q.Kind, "attempt", q.Attempts)
		return ctx.Err() == nil, nil

	case ctx.Err() != nil:
		// Shutdown cut the send; neither the relay nor the message failed.
		return false, d.cfg.Store.Release(record, q.ID, 0, reason(sendErr, q.Body))

	case mail.IsRelayFailure(sendErr):
		return false, d.relayFailed(ctx, record, q, sendErr)
	}

	why := reason(sendErr, q.Body)
	if errors.Is(sendErr, mail.ErrInvalidMessage) {
		// Refused before any connection: the relay said nothing.
		return true, d.abandon(record, q, why)
	}

	if mail.IsTemporary(sendErr) {
		// A deferral may be the relay's for every recipient, so the run
		// stops rather than charge each due message in turn.
		d.relayAnswered()
		after := backoff(q.Attempts)
		if q.Attempts >= MaxAttempts || d.cfg.Now().Add(after).Sub(q.CreatedAt) >= MaxAge {
			return false, d.abandon(record, q, why)
		}
		if err := d.cfg.Store.Retry(record, q.ID, after, why); err != nil {
			return false, err
		}
		d.cfg.Counters.Count(string(q.Kind), OutcomeRetry)
		d.cfg.Logger.InfoContext(ctx, "the relay deferred a message; it will be tried again",
			"message", q.ID, "kind", q.Kind, "attempt", q.Attempts, "retry_in", after.String(), "error", why)
		return false, nil
	}

	d.refusals++
	if d.refusals >= RefusalsBeforeBreaking {
		return false, d.relayFailed(ctx, record, q,
			fmt.Errorf("taken for the relay's after %d refusals in a row with nothing sent: %w", d.refusals, sendErr))
	}
	d.relayAnswered()
	return true, d.abandon(record, q, why)
}

// relayFailed puts the message back uncharged and pauses the dispatcher. The
// message waits out the pause too, so a message the transport misjudged as
// the relay's fault does not stand first in line at every probe.
func (d *Dispatcher) relayFailed(ctx, record context.Context, q Queued, sendErr error) error {
	pause := min(firstPause<<min(d.pauses, 10), lastPause)
	d.pauses++
	d.pausedUntil = d.cfg.Now().Add(pause)

	why := reason(sendErr, q.Body)
	if err := d.cfg.Store.Release(record, q.ID, pause, why); err != nil {
		return err
	}
	d.cfg.Counters.RelayFailed()
	d.cfg.Counters.SetRelayFailing(true)

	level := slog.LevelWarn
	if !mail.IsTemporary(sendErr) {
		// A refused account or certificate does not mend by itself.
		level = slog.LevelError
	}
	d.cfg.Logger.Log(ctx, level, "the relay failed; no message is sent until it is tried again",
		"message", q.ID, "retry_in", pause.String(), "error", why)
	return nil
}

// relayAnswered ends the pause's backoff: the relay answered for a message.
func (d *Dispatcher) relayAnswered() {
	d.pauses = 0
	d.cfg.Counters.SetRelayFailing(false)
}

func (d *Dispatcher) abandon(ctx context.Context, q Queued, why string) error {
	if err := d.cfg.Store.Abandon(ctx, q.ID, why); err != nil {
		return err
	}
	d.cfg.Counters.Count(string(q.Kind), OutcomeAbandoned)
	d.cfg.Logger.WarnContext(ctx, "a message was abandoned and will not be sent",
		"message", q.ID, "kind", q.Kind, "attempt", q.Attempts, "error", why)
	return nil
}

// backoff is the wait after the given failed attempt (1-based).
func backoff(attempt int) time.Duration {
	after := firstRetry
	for i := 1; i < attempt && after < lastRetry; i++ {
		after *= 2
	}
	return min(after, lastRetry)
}

// secretLength is the shortest word of a body that reason redacts: a one-time
// password is at least this long (users.MinPasswordLength).
const secretLength = 12

// reason is what is stored and logged about a failed attempt: the error's
// text, printable and bounded. A relay that quotes the message back would
// put a password in it, so every long word of the body is redacted from it;
// the reply's code stays.
func reason(err error, body string) string {
	text := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, err.Error())

	for _, word := range strings.Fields(body) {
		if len(word) >= secretLength {
			text = strings.ReplaceAll(text, word, "[redacted]")
		}
	}

	if len(text) > MaxLastErrorBytes {
		cut := MaxLastErrorBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}

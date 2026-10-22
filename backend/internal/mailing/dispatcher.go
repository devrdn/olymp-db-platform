package mailing

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/platform/mail"
)

// The retry policy. A temporary failure is tried again after a wait that
// doubles from a minute up to an hour; the MaxAttempts-th failure, or one
// MaxAge after the message was queued, abandons it. Eight attempts span about
// two hours, so a relay that never answers (a mistyped SMTP_HOST is a lookup
// failure, which reads as temporary) ends in abandoned messages and an alert.
const (
	MaxAttempts = 8
	MaxAge      = 24 * time.Hour

	firstRetry = time.Minute
	lastRetry  = time.Hour
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
	// Claim takes the oldest due message and leases it for lease, unless
	// perMinute messages were claimed in the last minute by any replica; ok
	// is false when it took nothing.
	Claim(ctx context.Context, perMinute int, lease time.Duration) (q Queued, ok bool, err error)
	// MarkSent and Abandon finish a message and erase its body.
	MarkSent(ctx context.Context, id int64) error
	Retry(ctx context.Context, id int64, after time.Duration, lastError string) error
	Abandon(ctx context.Context, id int64, lastError string) error
	CountDue(ctx context.Context) (int, error)
}

// Transport hands one message to the relay; its errors are classified by
// mail.IsTemporary.
type Transport interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Counters receives each attempt's outcome and the number of due messages.
type Counters interface {
	Count(kind, outcome string)
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
}

func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.PerMinute < 1 {
		panic("mailing: the dispatcher needs a rate of at least one message a minute")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Dispatcher{cfg: cfg}
}

// Drain sends due messages one at a time until none is due, the rate is
// spent, the relay fails temporarily, or ctx ends. It returns only storage
// failures; the relay's are recorded on the message.
func (d *Dispatcher) Drain(ctx context.Context) error {
	for ctx.Err() == nil {
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
// when the run should stop: the relay is failing for now, or ctx ended.
func (d *Dispatcher) attempt(ctx context.Context, q Queued) (next bool, err error) {
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	if q.Attempts > MaxAttempts {
		return true, d.abandon(record, q, "abandoned unsent: the message was claimed again after its last attempt")
	}

	sendErr := d.cfg.Transport.Send(ctx, q.To, q.Subject, q.Body)
	switch {
	case sendErr == nil:
		if err := d.cfg.Store.MarkSent(record, q.ID); err != nil {
			return false, err
		}
		d.cfg.Counters.Count(string(q.Kind), OutcomeSent)
		d.cfg.Logger.DebugContext(ctx, "sent a message", "message", q.ID, "kind", q.Kind, "attempt", q.Attempts)
		return ctx.Err() == nil, nil

	case ctx.Err() != nil:
		// Shutdown cut the send; the relay did not refuse it.
		return false, d.cfg.Store.Retry(record, q.ID, 0, reason(sendErr, q.Body))

	case mail.IsTemporary(sendErr) && q.Attempts < MaxAttempts && d.cfg.Now().Sub(q.CreatedAt) < MaxAge:
		after := backoff(q.Attempts)
		why := reason(sendErr, q.Body)
		if err := d.cfg.Store.Retry(record, q.ID, after, why); err != nil {
			return false, err
		}
		d.cfg.Counters.Count(string(q.Kind), OutcomeRetry)
		d.cfg.Logger.InfoContext(ctx, "the relay refused a message for now; it will be tried again",
			"message", q.ID, "kind", q.Kind, "attempt", q.Attempts, "retry_in", after.String(), "error", why)
		return false, nil

	default:
		return true, d.abandon(record, q, reason(sendErr, q.Body))
	}
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

// secretLength is the shortest word of a body treated as a possible secret:
// a one-time password is longer (users.MinPasswordLength is 12).
const secretLength = 12

// reason is what is stored and logged about a failed attempt: the error's
// text, printable and bounded. A relay that quotes the message back would
// put a password in it, so a text containing any long word of the body is
// replaced by its class alone.
func reason(err error, body string) string {
	text := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, err.Error())

	for _, word := range strings.Fields(body) {
		if len(word) >= secretLength && strings.Contains(text, word) {
			text = errorClass(err) + " (the reply quoted the message and is not kept)"
			break
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

func errorClass(err error) string {
	switch {
	case errors.Is(err, mail.ErrTemporary):
		return "temporary failure"
	case errors.Is(err, mail.ErrInvalidMessage):
		return "invalid message"
	default:
		return "permanent failure"
	}
}

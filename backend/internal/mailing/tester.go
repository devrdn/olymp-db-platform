package mailing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/mail"
	"github.com/google/uuid"
)

// The test message's refusals, besides ErrDisabled while mail is off. A
// failure the relay replied to wraps the transport's error, so mail.ReplyOf
// reads the reply back from it.
var (
	ErrNoAddress    = errors.New("the account has no email address to send the test message to")
	ErrTestTooOften = errors.New("too many test messages; wait a minute")
	// ErrRelayFailed: the relay could not be reached or secured, or refused
	// the sending account. Mended in the deployment's SMTP settings.
	ErrRelayFailed = errors.New("the relay could not be reached or refused the sending account")
	// ErrRefused: the relay took the account and refused this message, most
	// often for its recipient.
	ErrRefused = errors.New("the relay refused the message")
)

// TestsPerMinute is one administrator's budget of test messages. Each opens
// a session with the relay, and Microsoft 365 lets the mailbox send 30
// messages a minute, of which the outbox takes 25 by default.
const TestsPerMinute = 3

const testWindow = time.Minute

// TestTransport is the relay the test message goes through; *mail.Sender.
type TestTransport interface {
	Enabled() bool
	From() string
	Send(ctx context.Context, to, subject, body string) error
}

// Limiter is the slice of auth.Limiter the test budget needs.
type Limiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// TesterConfig is what a Tester needs; every field is required.
type TesterConfig struct {
	Transport TestTransport
	Texts     *Texts
	Limiter   Limiter
	Audit     *audit.Recorder
	Logger    *slog.Logger
}

// Tester sends the test message an administrator asks for, at once rather
// than through the outbox, so they see how the relay answers.
type Tester struct {
	cfg TesterConfig
}

func NewTester(cfg TesterConfig) *Tester {
	return &Tester{cfg: cfg}
}

// Enabled reports whether mail is on.
func (t *Tester) Enabled() bool { return t.cfg.Transport.Enabled() }

// From is the address mail is sent as, or empty while mail is off.
func (t *Tester) From() string { return t.cfg.Transport.From() }

// TestRequest is who asks for the test message and what it says. To is the
// caller's own address, so nobody can mail a stranger through this.
type TestRequest struct {
	Actor        uuid.UUID
	To           string
	Language     Language
	Installation string
}

// Send writes the test message in the request's language and hands it to
// the relay. It refuses with ErrTestTooOften past the caller's budget,
// ErrDisabled while mail is off, ErrNoAddress without a usable address,
// ErrRelayFailed or ErrRefused when the relay does. Every attempt within the
// budget is recorded with its outcome.
func (t *Tester) Send(ctx context.Context, req TestRequest) error {
	allowed, err := t.cfg.Limiter.Allow(ctx, "mailtest:user:"+req.Actor.String(), TestsPerMinute, testWindow)
	if err != nil {
		return fmt.Errorf("check the test message rate: %w", err)
	}
	if !allowed {
		return ErrTestTooOften
	}

	err = t.send(ctx, req)
	t.record(ctx, req.Actor, err)
	return err
}

func (t *Tester) send(ctx context.Context, req TestRequest) error {
	if !t.cfg.Transport.Enabled() {
		return ErrDisabled
	}
	// Mail on means PUBLIC_URL is set (config refuses otherwise), and the
	// language and name come from validated settings, so a failure here is a
	// wiring fault.
	text, err := t.cfg.Texts.Test(req.Language, TestData{Installation: req.Installation})
	if err != nil {
		return fmt.Errorf("write the test message: %w", err)
	}

	err = t.cfg.Transport.Send(ctx, req.To, text.Subject, text.Body)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mail.ErrDisabled):
		return ErrDisabled
	case errors.Is(err, mail.ErrInvalidMessage):
		// The subject and body are bounded well inside the relay's limits,
		// so it is the address, empty or not one bare address, that the
		// transport refused before connecting.
		return fmt.Errorf("%w: %w", ErrNoAddress, err)
	case mail.IsRelayFailure(err):
		return fmt.Errorf("%w: %w", ErrRelayFailed, err)
	default:
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
}

// outcomes name each answer in the trail.
var outcomes = []struct {
	err  error
	name string
}{
	{ErrDisabled, "mail_off"},
	{ErrNoAddress, "no_address"},
	{ErrRelayFailed, "relay_failed"},
	{ErrRefused, "refused"},
}

// record writes the attempt to the trail. The message has left (or not) by
// now, so a trail that cannot be written is logged rather than failing a
// request the administrator would only repeat.
func (t *Tester) record(ctx context.Context, actor uuid.UUID, sendErr error) {
	payload := map[string]any{"outcome": "sent"}
	if sendErr != nil {
		payload["outcome"] = "failed"
		for _, o := range outcomes {
			if errors.Is(sendErr, o.err) {
				payload["outcome"] = o.name
				break
			}
		}
	}
	if reply, ok := mail.ReplyOf(sendErr); ok {
		// The code alone: the text may quote the address.
		payload["reply"] = replyCode(reply)
	}

	err := t.cfg.Audit.Record(ctx, audit.Entry{
		ActorID: &actor,
		Action:  audit.ActionSettingsMailTest,
		Entity:  "settings",
		Payload: payload,
	})
	if err != nil {
		t.cfg.Logger.ErrorContext(ctx, "could not write audit entry", "action", audit.ActionSettingsMailTest, "error", err)
	}
}

func replyCode(r mail.Reply) string {
	if r.Enhanced == "" {
		return strconv.Itoa(r.Code)
	}
	return strconv.Itoa(r.Code) + " " + r.Enhanced
}

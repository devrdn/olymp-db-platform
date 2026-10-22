package mailing_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/mail"
	"github.com/devrdn/db-contest/backend/internal/platform/mail/mailtest"
)

var now = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

func dispatcher(store *fakeStore, transport mailing.Transport, counters *fakeCounters) *mailing.Dispatcher {
	return dispatcherAt(store, transport, counters, func() time.Time { return now })
}

func dispatcherAt(store *fakeStore, transport mailing.Transport, counters *fakeCounters, clock func() time.Time) *mailing.Dispatcher {
	return mailing.NewDispatcher(mailing.DispatcherConfig{
		Store:     store,
		Transport: transport,
		PerMinute: 25,
		Counters:  counters,
		Logger:    quiet(),
		Now:       clock,
	})
}

// The message's own failures, as the transport classes them.
func deferred() error { return fmt.Errorf("%w: 452 4.2.2 mailbox full", mail.ErrTemporary) }
func refused() error  { return fmt.Errorf("%w: 550 5.1.1 no such mailbox", mail.ErrPermanent) }

// The relay's failures, whatever the message.
func badPassword() error {
	return fmt.Errorf("%w: %w: 535 5.7.139 Authentication unsuccessful", mail.ErrPermanent, mail.ErrRelay)
}
func unreachable() error {
	return fmt.Errorf("%w: %w: dial tcp: lookup smtp.offfice365.com: no such host", mail.ErrTemporary, mail.ErrRelay)
}

func TestADueMessageIsSentAndRecorded(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0)}, due: 3}
	transport := &fakeTransport{}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}

	if len(transport.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(transport.sent))
	}
	m := aMessage()
	if transport.sent[0] != (sentMessage{m.To, m.Subject, m.Body}) {
		t.Errorf("sent %+v, want the stored message", transport.sent[0])
	}
	if fmt.Sprint(store.sent) != "[1 2]" {
		t.Errorf("marked sent %v, want [1 2]", store.sent)
	}
	if counters.counted["sign_in_details/sent"] != 2 {
		t.Errorf("counted %v, want two sent", counters.counted)
	}
	if counters.due != 3 {
		t.Errorf("due gauge = %d, want the store's count, 3", counters.due)
	}
}

// The rate is the store's to enforce, across replicas; the dispatcher passes
// it on with every claim, and a lease that outlasts any one send.
func TestEveryClaimCarriesTheRateAndALeaseLongerThanASend(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0)}}

	if err := dispatcher(store, &fakeTransport{}, &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	for i, perMinute := range store.claimedPerMinute {
		if perMinute != 25 {
			t.Errorf("claim %d asked for %d a minute, want SMTP_MAX_PER_MINUTE (25)", i, perMinute)
		}
		if lease := store.claimedLease[i]; lease <= mail.DefaultDialTimeout+mail.DefaultSendTimeout {
			t.Errorf("claim %d leased the row for %s; a send can take %s, and another replica would send it again",
				i, lease, mail.DefaultDialTimeout+mail.DefaultSendTimeout)
		}
	}
	if len(store.claimedPerMinute) != 2 {
		t.Errorf("claimed %d times, want one per message and one that found nothing", len(store.claimedPerMinute))
	}
}

// A deferral may be the relay's for every recipient (a busy relay answers
// each RCPT with a 4xx), so the run stops rather than charge the whole queue.
func TestAMessageDeferredByTheRelayIsRetriedLaterAndEndsTheRun(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0)}}
	transport := &fakeTransport{replies: []error{deferred()}}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}

	if len(store.retried) != 1 || store.retried[0].id != 1 || store.retried[0].after != time.Minute {
		t.Fatalf("retried %+v, want message 1 after a minute", store.retried)
	}
	if !strings.Contains(store.retried[0].lastError, "452 4.2.2") {
		t.Errorf("last error %q does not carry the relay's reply", store.retried[0].lastError)
	}
	if len(transport.sent) != 1 || len(store.pending) != 1 || store.pending[0].Attempts != 0 {
		t.Errorf("tried %d messages, %v left; want the run to stop after the deferral", len(transport.sent), store.pending)
	}
	if counters.counted["sign_in_details/retry"] != 1 {
		t.Errorf("counted %v, want one retry", counters.counted)
	}
}

func TestTheWaitBeforeARetryDoublesUpToAnHour(t *testing.T) {
	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour,
	}
	if len(want) != mailing.MaxAttempts-1 {
		t.Fatalf("the table covers %d retries, MaxAttempts allows %d", len(want), mailing.MaxAttempts-1)
	}
	for i, after := range want {
		store := &fakeStore{pending: []mailing.Queued{queued(1, now, i)}}
		transport := &fakeTransport{replies: []error{deferred()}}
		if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
			t.Fatalf("Drain() = %v", err)
		}
		if len(store.retried) != 1 || store.retried[0].after != after {
			t.Errorf("after failed attempt %d: retried %+v, want after %s", i+1, store.retried, after)
		}
	}
}

func TestAPermanentFailureIsAbandonedAndTheRunGoesOn(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0)}}
	transport := &fakeTransport{replies: []error{refused()}}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}

	if len(store.abandoned) != 1 || store.abandoned[0].id != 1 ||
		!strings.Contains(store.abandoned[0].lastError, "550 5.1.1") {
		t.Fatalf("abandoned %+v, want message 1 with the relay's reply", store.abandoned)
	}
	if fmt.Sprint(store.sent) != "[2]" {
		t.Errorf("sent %v, want the next message sent in the same run", store.sent)
	}
	if counters.counted["sign_in_details/abandoned"] != 1 {
		t.Errorf("counted %v, want one abandoned", counters.counted)
	}
}

func TestAMessageTheRelayCannotTakeIsAbandoned(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0)}}
	transport := &fakeTransport{replies: []error{fmt.Errorf("%w: body too long", mail.ErrInvalidMessage)}}

	if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 1 {
		t.Fatalf("abandoned %+v, want the message", store.abandoned)
	}
}

func TestTheLastTemporaryFailureAbandonsTheMessage(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, mailing.MaxAttempts-1)}}
	transport := &fakeTransport{replies: []error{deferred()}}

	if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 1 || len(store.retried) != 0 {
		t.Fatalf("attempt %d failed: abandoned %+v, retried %+v; want it abandoned",
			mailing.MaxAttempts, store.abandoned, store.retried)
	}
}

func TestATemporaryFailureNearTheMaximumAgeAbandonsTheMessage(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now.Add(-mailing.MaxAge+time.Second), 1)}}
	transport := &fakeTransport{replies: []error{deferred()}}

	if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 1 || len(store.retried) != 0 {
		t.Fatalf("a message whose retry would land past %s: abandoned %+v, retried %+v; want it abandoned",
			mailing.MaxAge, store.abandoned, store.retried)
	}
}

// A body may hold a one-time password, and MaxAge bounds how long it is kept:
// a message that old is never sent, however the relay is doing.
func TestAMessagePastTheMaximumAgeIsAbandonedAtClaimWithoutBeingSent(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now.Add(-mailing.MaxAge), 0), queued(2, now, 0)}}
	transport := &fakeTransport{}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 1 || store.abandoned[0].id != 1 || !strings.Contains(store.abandoned[0].lastError, "24h") {
		t.Fatalf("abandoned %+v, want message 1 with its age as the reason", store.abandoned)
	}
	if len(transport.sent) != 1 || fmt.Sprint(store.sent) != "[2]" {
		t.Errorf("sent %v, want only the fresh message", store.sent)
	}
	if counters.counted["sign_in_details/abandoned"] != 1 {
		t.Errorf("counted %v, want one abandoned", counters.counted)
	}
}

// Every run first gives up the messages past the maximum age that nobody is
// sending, paused or not, so a body never outlives it by more than a tick.
func TestEveryRunExpiresMessagesPastTheMaximumAge(t *testing.T) {
	store := &fakeStore{expired: map[mailing.Kind]int{mailing.KindSignInDetails: 3}}
	counters := &fakeCounters{}
	d := dispatcher(store, &fakeTransport{replies: []error{badPassword()}}, counters)

	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.expiredAsked) != 1 || store.expiredAsked[0].maxAge != mailing.MaxAge ||
		store.expiredAsked[0].lease != mailing.ClaimLease || store.expiredAsked[0].reason == "" {
		t.Fatalf("Expire was asked %+v, want MaxAge, ClaimLease and a reason", store.expiredAsked)
	}
	if counters.counted["sign_in_details/abandoned"] != 3 {
		t.Errorf("counted %v, want the three expired messages as abandoned", counters.counted)
	}

	// The relay fails, and the run after that is paused: it still expires.
	store.pending = []mailing.Queued{queued(1, now, 0)}
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.expiredAsked) != 3 || len(store.claimedPerMinute) != 2 {
		t.Errorf("three runs, the last paused: Expire ran %d times and Claim %d, want 3 and 2",
			len(store.expiredAsked), len(store.claimedPerMinute))
	}
}

// A process that died mid-send leaves the row to come back when its lease
// ends, an attempt already counted; a crash loop must not retry forever.
func TestAMessageClaimedPastItsAttemptsIsAbandonedUnsent(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, mailing.MaxAttempts)}}
	transport := &fakeTransport{}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(transport.sent) != 0 {
		t.Errorf("sent a message on attempt %d", mailing.MaxAttempts+1)
	}
	if len(store.abandoned) != 1 || store.abandoned[0].lastError == "" {
		t.Fatalf("abandoned %+v, want the message with a reason", store.abandoned)
	}
	if counters.counted["sign_in_details/abandoned"] != 1 {
		t.Errorf("counted %v, want one abandoned", counters.counted)
	}
}

func TestTheRecordedErrorIsBoundedPrintableAndNeverCarriesTheBody(t *testing.T) {
	m := aMessage()
	for name, tc := range map[string]struct {
		reply  error
		keep   string
		reject string
	}{
		"echoes the password": {
			reply:  fmt.Errorf("%w: 550 5.7.1 rejected content Zx8-q2LmT0pa4RwK", mail.ErrPermanent),
			keep:   "550 5.7.1 rejected content [redacted]",
			reject: "Zx8-q2LmT0pa4RwK",
		},
		"echoes the whole body": {
			reply:  fmt.Errorf("%w: 550 5.6.0 %s", mail.ErrPermanent, m.Body),
			keep:   "550 5.6.0",
			reject: "Zx8-q2LmT0pa4RwK",
		},
		"control characters": {
			reply:  fmt.Errorf("%w: 550 line\r\nBcc: x@y\x1b[31m", mail.ErrPermanent),
			keep:   "550 line",
			reject: "\n",
		},
		"too long": {
			reply:  fmt.Errorf("%w: 550 %s", mail.ErrPermanent, strings.Repeat("я", mailing.MaxLastErrorBytes)),
			keep:   "550",
			reject: strings.Repeat("я", mailing.MaxLastErrorBytes/2),
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0)}}
			transport := &fakeTransport{replies: []error{tc.reply}}
			if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
				t.Fatalf("Drain() = %v", err)
			}
			if len(store.abandoned) != 1 {
				t.Fatalf("abandoned %+v", store.abandoned)
			}
			assertRecordedError(t, store.abandoned[0].lastError, tc.keep, tc.reject)
		})
	}
}

// Microsoft 365 quotes the recipient in a 5.1.x refusal, and a sign-in body
// names the address too: the address is redacted, the code the runbook asks
// for is kept.
func TestARefusalQuotingTheRecipientKeepsItsCode(t *testing.T) {
	q := queued(1, now, 0)
	q.To = "ana.popescu@usm.md"
	q.Body = "Login: ana.popescu\nEmail: ana.popescu@usm.md\nParola: Zx8-q2LmT0pa4RwK\n"
	store := &fakeStore{pending: []mailing.Queued{q}}
	transport := &fakeTransport{replies: []error{fmt.Errorf(
		"%w: 550 5.1.10 RESOLVER.ADR.RecipientNotFound; Recipient ana.popescu@usm.md not found by SMTP address lookup",
		mail.ErrPermanent)}}

	if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 1 {
		t.Fatalf("abandoned %+v", store.abandoned)
	}
	assertRecordedError(t, store.abandoned[0].lastError,
		"550 5.1.10 RESOLVER.ADR.RecipientNotFound; Recipient [redacted] not found", "ana.popescu@usm.md")
}

func assertRecordedError(t *testing.T, got, keep, reject string) {
	t.Helper()
	if !strings.Contains(got, keep) || strings.Contains(got, reject) {
		t.Errorf("last error %q, want it to keep %q and drop %q", got, keep, reject)
	}
	if len(got) > mailing.MaxLastErrorBytes || !utf8.ValidString(got) {
		t.Errorf("last error is %d bytes (valid UTF-8: %v), want at most %d",
			len(got), utf8.ValidString(got), mailing.MaxLastErrorBytes)
	}
	for _, r := range got {
		if r < 0x20 || r == 0x7f {
			t.Errorf("last error %q carries the control character %U", got, r)
		}
	}
}

// A message the relay accepted is recorded as sent even when shutdown cancels
// the run during the send; otherwise it would go out again.
func TestADeliveryIsRecordedEvenWhenTheRunIsCancelledDuringTheSend(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0)}}
	transport := &fakeTransport{during: cancel}

	_ = dispatcher(store, transport, &fakeCounters{}).Drain(ctx)

	if fmt.Sprint(store.sent) != "[1]" {
		t.Fatalf("marked sent %v, want [1] and nothing claimed after the cancellation", store.sent)
	}
	for i, err := range store.recordCtxErr {
		if err != nil {
			t.Errorf("record %d ran on a cancelled context (%v) and would not reach the database", i, err)
		}
	}
}

// Shutdown interrupts a send: that is nobody's failure, so the message is due
// again at once, its attempt refunded, and nothing is counted.
func TestASendCutByShutdownIsDueAgainAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0)}}
	transport := &fakeTransport{
		during:  cancel,
		replies: []error{fmt.Errorf("%w: context canceled", mail.ErrTemporary)},
	}
	counters := &fakeCounters{}

	_ = dispatcher(store, transport, counters).Drain(ctx)

	if len(store.released) != 1 || store.released[0].after != 0 || len(store.retried) != 0 {
		t.Fatalf("released %+v, retried %+v; want the message released, due at once", store.released, store.retried)
	}
	if store.pending[0].Attempts != 0 {
		t.Errorf("the message has %d attempts after a send cut by shutdown, want 0", store.pending[0].Attempts)
	}
	if len(counters.counted) != 0 || counters.relayFailures != 0 {
		t.Errorf("counted %v and %d relay failures for a send cut by shutdown", counters.counted, counters.relayFailures)
	}
	if store.recordCtxErr[0] != nil {
		t.Errorf("the release ran on a cancelled context: %v", store.recordCtxErr[0])
	}
}

func TestAStoreFailureEndsTheRunAndIsReturned(t *testing.T) {
	failure := errors.New("connection refused")
	store := &fakeStore{claimErr: failure}

	if err := dispatcher(store, &fakeTransport{}, &fakeCounters{}).Drain(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("Drain() = %v, want the store's failure", err)
	}
}

func TestNothingLogsTheSubjectOrTheBody(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0), queued(3, now, 0), queued(4, now, 0)}}
	transport := &fakeTransport{replies: []error{nil, refused(), badPassword(), deferred()}}
	clock := now
	d := mailing.NewDispatcher(mailing.DispatcherConfig{
		Store: store, Transport: transport, PerMinute: 25, Counters: &fakeCounters{},
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    func() time.Time { return clock },
	})

	// Sent, refused, relay failure; then, after the pause, a deferral.
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	clock = clock.Add(time.Hour)
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if logs.Len() == 0 {
		t.Fatal("nothing was logged; the test would pass vacuously")
	}
	m := aMessage()
	for _, secret := range []string{m.Subject, "Zx8-q2LmT0pa4RwK", "Parola"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log carries %q:\n%s", secret, logs.String())
		}
	}
}

// A wrong relay password, or SMTP AUTH turned off for the mailbox, refuses
// every message alike: none is charged an attempt or given up, and the
// dispatcher stops asking until its pause ends.
func TestARelayFailureChargesNoMessageAndPausesTheDispatcher(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0), queued(3, now, 0)}}
	transport := &fakeTransport{replies: []error{badPassword(), badPassword()}}
	counters := &fakeCounters{}
	clock := now
	d := dispatcherAt(store, transport, counters, func() time.Time { return clock })

	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(transport.sent) != 1 {
		t.Fatalf("tried %d messages against a relay refusing the account, want 1", len(transport.sent))
	}
	if len(store.abandoned) != 0 || len(store.retried) != 0 || len(store.sent) != 0 {
		t.Fatalf("abandoned %+v, retried %+v, sent %v; want every message left pending",
			store.abandoned, store.retried, store.sent)
	}
	if len(store.released) != 1 || store.released[0].after != 30*time.Second ||
		!strings.Contains(store.released[0].lastError, "535 5.7.139") {
		t.Fatalf("released %+v, want message 1 released for 30s with the relay's reply", store.released)
	}
	if len(store.pending) != 3 {
		t.Fatalf("%d messages pending, want all 3", len(store.pending))
	}
	for _, q := range store.pending {
		if q.Attempts != 0 {
			t.Errorf("message %d has %d attempts charged for the relay's failure", q.ID, q.Attempts)
		}
	}
	if counters.relayFailures != 1 || !counters.relayFailing || len(counters.counted) != 0 {
		t.Errorf("relay failures %d, failing %v, outcomes %v; want one relay failure and no message outcome",
			counters.relayFailures, counters.relayFailing, counters.counted)
	}

	// Paused: no claim until the pause ends.
	claims := len(store.claimedPerMinute)
	clock = now.Add(29 * time.Second)
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.claimedPerMinute) != claims {
		t.Fatal("a paused dispatcher claimed a message")
	}

	// The pause ends; the relay still refuses, and the next pause is longer.
	clock = now.Add(30 * time.Second)
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.released) != 2 || store.released[1].after != time.Minute {
		t.Fatalf("released %+v, want a second release for a minute", store.released)
	}

	// Fixed: everything goes, and the relay is no longer failing.
	clock = clock.Add(time.Minute)
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.sent) != 3 || counters.relayFailing {
		t.Fatalf("sent %v, relay failing %v; want all three sent and the relay recovered", store.sent, counters.relayFailing)
	}

	// The pause starts again from 30 seconds.
	store.pending = []mailing.Queued{queued(4, clock, 0)}
	transport.replies = []error{badPassword()}
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if last := store.released[len(store.released)-1]; last.after != 30*time.Second {
		t.Errorf("after a success the next pause is %s, want 30s", last.after)
	}
}

func TestThePauseDoublesUpToFifteenMinutes(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0)}}
	transport := &fakeTransport{}
	clock := now
	d := dispatcherAt(store, transport, &fakeCounters{}, func() time.Time { return clock })

	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 15 * time.Minute, 15 * time.Minute,
	}
	for i, pause := range want {
		transport.replies = []error{unreachable()}
		if err := d.Drain(t.Context()); err != nil {
			t.Fatalf("Drain() = %v", err)
		}
		if len(store.released) != i+1 || store.released[i].after != pause {
			t.Fatalf("relay failure %d: released %+v, want a pause of %s", i+1, store.released, pause)
		}
		clock = clock.Add(pause)
	}
	if store.pending[0].Attempts != 0 || len(store.abandoned) != 0 {
		t.Errorf("an unreachable relay charged %d attempts and abandoned %+v", store.pending[0].Attempts, store.abandoned)
	}
}

// Through the real transport and an SMTP relay that refuses the password.
func TestAWrongRelayPasswordLeavesEveryMessagePending(t *testing.T) {
	relay := mailtest.NewServer(t, mailtest.Options{
		StartTLS: true, AuthMechs: "PLAIN LOGIN", User: "noreply@usm.md", Password: "the-right-one",
	})
	transport := relaySender(t, relay, "a-wrong-one")
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0), queued(3, now, 0)}}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}

	if len(store.abandoned) != 0 || len(store.retried) != 0 || len(store.released) != 1 {
		t.Fatalf("abandoned %+v, retried %+v, released %+v; want one message released and none given up",
			store.abandoned, store.retried, store.released)
	}
	if !strings.Contains(store.released[0].lastError, "535") {
		t.Errorf("last error %q does not carry the 535", store.released[0].lastError)
	}
	if len(store.pending) != 3 {
		t.Fatalf("%d messages pending, want all 3", len(store.pending))
	}
	for _, q := range store.pending {
		if q.Attempts != 0 {
			t.Errorf("message %d was charged %d attempts", q.ID, q.Attempts)
		}
	}
	if !counters.relayFailing || len(relay.Messages()) != 0 {
		t.Errorf("relay failing %v, delivered %d; want the dispatcher paused and nothing delivered",
			counters.relayFailing, len(relay.Messages()))
	}
}

// Through the real transport: a refused recipient is that message's problem.
func TestARefusedRecipientAbandonsOnlyItsMessage(t *testing.T) {
	relay := mailtest.NewServer(t, mailtest.Options{
		StartTLS: true, AuthMechs: "PLAIN", User: "noreply@usm.md", Password: "the-right-one",
		RefuseRecipients: map[string]string{"ion@usm.md": "550 5.1.1 no such user"},
	})
	transport := relaySender(t, relay, "the-right-one")
	refusedOne := queued(2, now, 0)
	refusedOne.To = "ion@usm.md"
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), refusedOne, queued(3, now, 0)}}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}

	if len(store.abandoned) != 1 || store.abandoned[0].id != 2 || !strings.Contains(store.abandoned[0].lastError, "5.1.1 no such user") {
		t.Fatalf("abandoned %+v, want message 2 with the relay's 550", store.abandoned)
	}
	if fmt.Sprint(store.sent) != "[1 3]" || len(relay.Messages()) != 2 {
		t.Errorf("sent %v, relay received %d; want messages 1 and 3 delivered", store.sent, len(relay.Messages()))
	}
	if counters.relayFailing || counters.relayFailures != 0 {
		t.Error("a refused recipient was taken for a failing relay")
	}
}

func relaySender(t *testing.T, relay *mailtest.Server, password string) *mail.Sender {
	t.Helper()
	sender, err := mail.New(mail.Config{
		Host: relay.Host(), Port: relay.Port(), Username: "noreply@usm.md", Password: password,
		From: "noreply@usm.md", TLS: mail.TLSStartTLS, RootCAs: relay.Roots,
		DialTimeout: 2 * time.Second, SendTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("mail.New() = %v", err)
	}
	return sender
}

// Exchange refuses every recipient of a mailbox it has restricted for spam
// (550 5.1.8) with a reply that reads as the message's. Refusals in a row
// with nothing sent between are taken for the relay's after the breaker's
// count: the rest of the queue is kept, not given up.
func TestRefusalsInARowAreTakenForTheRelaysAndKeepTheRestOfTheQueue(t *testing.T) {
	relay := mailtest.NewServer(t, mailtest.Options{
		StartTLS: true, AuthMechs: "PLAIN", User: "noreply@usm.md", Password: "the-right-one",
		Replies: map[string]string{"RCPT": "550 5.1.8 Access denied, bad outbound sender"},
	})
	store := &fakeStore{}
	for id := int64(1); id <= 6; id++ {
		store.pending = append(store.pending, queued(id, now, 0))
	}
	counters := &fakeCounters{}
	clock := now
	d := dispatcherAt(store, relaySender(t, relay, "the-right-one"), counters, func() time.Time { return clock })

	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != mailing.RefusalsBeforeBreaking-1 {
		t.Fatalf("abandoned %d messages, want at most %d before the breaker", len(store.abandoned), mailing.RefusalsBeforeBreaking-1)
	}
	if len(store.released) != 1 || !strings.Contains(store.released[0].lastError, "5.1.8") {
		t.Fatalf("released %+v, want the message that tripped the breaker, with the reply", store.released)
	}
	if !counters.relayFailing || counters.relayFailures != 1 {
		t.Errorf("relay failing %v, failures %d; want the breaker counted as a relay failure",
			counters.relayFailing, counters.relayFailures)
	}
	for _, q := range store.pending {
		if q.Attempts != 0 {
			t.Errorf("message %d kept %d attempts", q.ID, q.Attempts)
		}
	}
	if len(store.pending) != 6-(mailing.RefusalsBeforeBreaking-1) {
		t.Errorf("%d messages pending, want every one not abandoned", len(store.pending))
	}

	// Still refused after the pause: nothing more is given up, and the pause grows.
	clock = clock.Add(time.Hour)
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != mailing.RefusalsBeforeBreaking-1 || len(store.released) != 2 ||
		store.released[1].after <= store.released[0].after {
		t.Errorf("after the pause: abandoned %d, released %+v; want no new abandon and a longer pause",
			len(store.abandoned), store.released)
	}
}

// Refused recipients among accepted ones are each the message's own: a
// delivery between them resets the count.
func TestRefusalsBetweenDeliveriesAreEachAbandoned(t *testing.T) {
	store := &fakeStore{}
	var replies []error
	for id := int64(1); id <= 8; id++ {
		store.pending = append(store.pending, queued(id, now, 0))
		if id%2 == 0 {
			replies = append(replies, nil)
		} else {
			replies = append(replies, refused())
		}
	}
	counters := &fakeCounters{}

	if err := dispatcher(store, &fakeTransport{replies: replies}, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 4 || len(store.sent) != 4 || len(store.released) != 0 || counters.relayFailing {
		t.Errorf("abandoned %d, sent %d, released %d, relay failing %v; want four of each and no breaker",
			len(store.abandoned), len(store.sent), len(store.released), counters.relayFailing)
	}
}

// Through the real transport: a relay answering every RCPT with a 4xx
// charges one message per run, not the whole queue.
func TestARelayDeferringEveryRecipientChargesOneMessageARun(t *testing.T) {
	relay := mailtest.NewServer(t, mailtest.Options{
		StartTLS: true, AuthMechs: "PLAIN", User: "noreply@usm.md", Password: "the-right-one",
		Replies: map[string]string{"RCPT": "451 4.7.500 Server busy. Please try again later"},
	})
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0), queued(3, now, 0)}}

	if err := dispatcher(store, relaySender(t, relay, "the-right-one"), &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.retried) != 1 || len(store.pending) != 2 {
		t.Errorf("retried %+v with %d left untouched; want one message charged", store.retried, len(store.pending))
	}
}

// A message the transport refuses before connecting says nothing about the
// relay, so it neither clears a failing relay nor resets the pause.
func TestAMessageRefusedBeforeConnectingLeavesTheRelayState(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0)}}
	transport := &fakeTransport{replies: []error{badPassword()}}
	counters := &fakeCounters{}
	clock := now
	d := dispatcherAt(store, transport, counters, func() time.Time { return clock })

	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	clock = clock.Add(30 * time.Second)
	transport.replies = []error{fmt.Errorf("%w: the body is longer than 65536 bytes", mail.ErrInvalidMessage), badPassword()}
	store.pending = append(store.pending, queued(2, now, 0))
	if err := d.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}

	if len(store.abandoned) != 1 {
		t.Fatalf("abandoned %+v, want the invalid message", store.abandoned)
	}
	if !counters.relayFailing {
		t.Error("an invalid message cleared the failing relay")
	}
	if last := store.released[len(store.released)-1]; last.after != time.Minute {
		t.Errorf("the next relay failure paused for %s, want 1m: an invalid message reset the backoff", last.after)
	}
}

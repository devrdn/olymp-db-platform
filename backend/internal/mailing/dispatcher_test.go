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
)

var now = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

func dispatcher(store *fakeStore, transport *fakeTransport, counters *fakeCounters) *mailing.Dispatcher {
	return mailing.NewDispatcher(mailing.DispatcherConfig{
		Store:     store,
		Transport: transport,
		PerMinute: 25,
		Counters:  counters,
		Logger:    quiet(),
		Now:       func() time.Time { return now },
	})
}

func throttled() error { return fmt.Errorf("%w: 421 4.7.0 too many messages", mail.ErrTemporary) }
func refused() error   { return fmt.Errorf("%w: 550 5.1.1 no such mailbox", mail.ErrPermanent) }

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

// A throttling relay refuses the next message too: the run stops at the first
// temporary failure instead of spending every due message's attempts.
func TestATemporaryFailureIsRetriedLaterAndEndsTheRun(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0)}}
	transport := &fakeTransport{replies: []error{throttled()}}
	counters := &fakeCounters{}

	if err := dispatcher(store, transport, counters).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}

	if len(store.retried) != 1 || store.retried[0].id != 1 || store.retried[0].after != time.Minute {
		t.Fatalf("retried %+v, want message 1 after a minute", store.retried)
	}
	if !strings.Contains(store.retried[0].lastError, "421 4.7.0") {
		t.Errorf("last error %q does not carry the relay's reply", store.retried[0].lastError)
	}
	if len(transport.sent) != 1 {
		t.Errorf("tried %d messages, want the run to stop after the throttled one", len(transport.sent))
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
		transport := &fakeTransport{replies: []error{throttled()}}
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
	transport := &fakeTransport{replies: []error{throttled()}}

	if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 1 || len(store.retried) != 0 {
		t.Fatalf("attempt %d failed: abandoned %+v, retried %+v; want it abandoned",
			mailing.MaxAttempts, store.abandoned, store.retried)
	}
}

func TestATemporaryFailurePastTheMaximumAgeAbandonsTheMessage(t *testing.T) {
	store := &fakeStore{pending: []mailing.Queued{queued(1, now.Add(-mailing.MaxAge), 1)}}
	transport := &fakeTransport{replies: []error{throttled()}}

	if err := dispatcher(store, transport, &fakeCounters{}).Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if len(store.abandoned) != 1 || len(store.retried) != 0 {
		t.Fatalf("a message %s old failed: abandoned %+v, retried %+v; want it abandoned",
			mailing.MaxAge, store.abandoned, store.retried)
	}
}

// A mistyped SMTP_HOST fails every attempt as temporary (the name does not
// resolve). Re-offering the message until it stops proves the cap is finite.
func TestARelayThatAlwaysFailsTemporarilyIsGivenUpOn(t *testing.T) {
	store := &fakeStore{}
	dnsFailure := fmt.Errorf("%w: dial tcp: lookup smtp.offfice365.com: no such host", mail.ErrTemporary)
	transport := &fakeTransport{}
	d := dispatcher(store, transport, &fakeCounters{})

	message := queued(1, now, 0)
	for runs := 1; ; runs++ {
		if runs > 100 {
			t.Fatal("still retrying after 100 runs")
		}
		store.pending = []mailing.Queued{message}
		transport.replies = []error{dnsFailure}
		if err := d.Drain(t.Context()); err != nil {
			t.Fatalf("Drain() = %v", err)
		}
		if len(store.abandoned) == 1 {
			if runs != mailing.MaxAttempts {
				t.Errorf("abandoned after %d attempts, want %d", runs, mailing.MaxAttempts)
			}
			return
		}
		message.Attempts++
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
		reject string
	}{
		"echoes the password": {
			reply:  fmt.Errorf("%w: 550 rejected content Zx8-q2LmT0pa4RwK", mail.ErrPermanent),
			reject: "Zx8-q2LmT0pa4RwK",
		},
		"echoes the whole body": {
			reply:  fmt.Errorf("%w: 550 %s", mail.ErrPermanent, m.Body),
			reject: "Parola:",
		},
		"control characters": {
			reply:  fmt.Errorf("%w: 550 line\r\nBcc: x@y\x1b[31m", mail.ErrPermanent),
			reject: "\n",
		},
		"too long": {
			reply:  fmt.Errorf("%w: 550 %s", mail.ErrPermanent, strings.Repeat("я", mailing.MaxLastErrorBytes)),
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
			got := store.abandoned[0].lastError
			if got == "" || strings.Contains(got, tc.reject) {
				t.Errorf("last error %q, want it non-empty and without %q", got, tc.reject)
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
		})
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

// Shutdown interrupts a send: that is not the relay's failure, so the message
// is due again at once and counts no retry.
func TestASendCutByShutdownIsDueAgainAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0)}}
	transport := &fakeTransport{
		during:  cancel,
		replies: []error{fmt.Errorf("%w: context canceled", mail.ErrTemporary)},
	}
	counters := &fakeCounters{}

	_ = dispatcher(store, transport, counters).Drain(ctx)

	if len(store.retried) != 1 || store.retried[0].after != 0 {
		t.Fatalf("retried %+v, want the message due again at once", store.retried)
	}
	if len(counters.counted) != 0 {
		t.Errorf("counted %v for a send cut by shutdown", counters.counted)
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
	store := &fakeStore{pending: []mailing.Queued{queued(1, now, 0), queued(2, now, 0), queued(3, now, 0)}}
	transport := &fakeTransport{replies: []error{nil, refused(), throttled()}}
	d := mailing.NewDispatcher(mailing.DispatcherConfig{
		Store: store, Transport: transport, PerMinute: 25, Counters: &fakeCounters{},
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    func() time.Time { return now },
	})

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

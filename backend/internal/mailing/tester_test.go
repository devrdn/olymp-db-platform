package mailing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"mime"
	"net"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/mail"
	"github.com/devrdn/db-contest/backend/internal/platform/mail/mailtest"
	"github.com/google/uuid"
)

var administrator = uuid.MustParse("6f1c2b0e-8d4a-4e8b-9a51-1f2e3d4c5b6a")

// fakeLimiter counts attempts per subject, as auth.Limiter does in its cache.
type fakeLimiter struct {
	counts   map[string]int
	limits   []int
	windows  []time.Duration
	subjects []string
	err      error
}

func (l *fakeLimiter) Allow(_ context.Context, subject string, limit int, window time.Duration) (bool, error) {
	l.subjects = append(l.subjects, subject)
	l.limits = append(l.limits, limit)
	l.windows = append(l.windows, window)
	if l.err != nil {
		return false, l.err
	}
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.counts[subject]++
	return l.counts[subject] <= limit, nil
}

type auditSink struct {
	entries []audit.Entry
	err     error
}

func (s *auditSink) Append(_ context.Context, e audit.Entry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, e)
	return nil
}

func (s *auditSink) AppendMany(_ context.Context, entries []audit.Entry) error {
	s.entries = append(s.entries, entries...)
	return nil
}

type testerFixture struct {
	tester  *mailing.Tester
	limiter *fakeLimiter
	trail   *auditSink
	log     *bytes.Buffer
}

func newTester(t *testing.T, transport mailing.TestTransport) *testerFixture {
	t.Helper()
	f := &testerFixture{limiter: &fakeLimiter{}, trail: &auditSink{}, log: &bytes.Buffer{}}
	f.tester = mailing.NewTester(mailing.TesterConfig{
		Transport: transport,
		Texts:     mailing.NewTexts("https://olympdb.usm.md"),
		Limiter:   f.limiter,
		Audit:     audit.New(f.trail),
		Logger:    slog.New(slog.NewTextHandler(f.log, nil)),
	})
	return f
}

func testRequest(to string) mailing.TestRequest {
	return mailing.TestRequest{
		Actor: administrator, To: to, Language: mailing.Russian, Installation: "Olimpiada USM",
	}
}

// relay is a fake relay that takes the password relaySender is given.
func relay(t *testing.T, opts mailtest.Options) *mailtest.Server {
	t.Helper()
	opts.StartTLS, opts.AuthMechs, opts.User, opts.Password = true, "PLAIN", "noreply@usm.md", "the-right-one"
	return mailtest.NewServer(t, opts)
}

func disabledSender(t *testing.T) *mail.Sender {
	t.Helper()
	sender, err := mail.New(mail.Config{})
	if err != nil {
		t.Fatalf("mail.New() = %v", err)
	}
	return sender
}

// outcomeOf is the one entry the trail holds, failing the test otherwise.
func (f *testerFixture) outcomeOf(t *testing.T) map[string]any {
	t.Helper()
	if len(f.trail.entries) != 1 {
		t.Fatalf("the trail holds %d entries, want 1: %+v", len(f.trail.entries), f.trail.entries)
	}
	e := f.trail.entries[0]
	if e.Action != audit.ActionSettingsMailTest || e.Entity != "settings" || e.ActorID == nil || *e.ActorID != administrator {
		t.Errorf("entry = %+v, want %s on settings by the administrator", e, audit.ActionSettingsMailTest)
	}
	return e.Payload
}

func TestTheTestMessageGoesToTheCallerInTheInstallationsLanguage(t *testing.T) {
	server := relay(t, mailtest.Options{})
	f := newTester(t, relaySender(t, server, "the-right-one"))

	if err := f.tester.Send(t.Context(), testRequest("ana@usm.md")); err != nil {
		t.Fatalf("Send() = %v", err)
	}

	got := server.Messages()
	if len(got) != 1 || len(got[0].To) != 1 || got[0].To[0] != "ana@usm.md" {
		t.Fatalf("the relay received %+v, want one message to ana@usm.md", got)
	}
	want, err := mailing.NewTexts("https://olympdb.usm.md").Test(mailing.Russian, mailing.TestData{Installation: "Olimpiada USM"})
	if err != nil {
		t.Fatal(err)
	}
	if subject := decodedSubject(t, got[0].Data); subject != want.Subject {
		t.Errorf("subject = %q, want the Russian test message's %q", subject, want.Subject)
	}
	if payload := f.outcomeOf(t); payload["outcome"] != "sent" {
		t.Errorf("payload = %v, want outcome sent", payload)
	}
}

func TestWithMailOffNothingIsSent(t *testing.T) {
	f := newTester(t, disabledSender(t))

	err := f.tester.Send(t.Context(), testRequest("ana@usm.md"))

	if !errors.Is(err, mailing.ErrDisabled) {
		t.Fatalf("Send() = %v, want ErrDisabled", err)
	}
	if payload := f.outcomeOf(t); payload["outcome"] != "mail_off" {
		t.Errorf("payload = %v, want outcome mail_off", payload)
	}
}

func TestACallerWithoutAnAddressIsToldSo(t *testing.T) {
	for name, to := range map[string]string{
		"no address": "",
		// Older rows may hold what the accounts screen now refuses.
		"not one bare address": "Ana <ana@usm.md>",
	} {
		t.Run(name, func(t *testing.T) {
			server := relay(t, mailtest.Options{})
			f := newTester(t, relaySender(t, server, "the-right-one"))

			err := f.tester.Send(t.Context(), testRequest(to))

			if !errors.Is(err, mailing.ErrNoAddress) {
				t.Fatalf("Send() = %v, want ErrNoAddress", err)
			}
			if len(server.Messages()) != 0 {
				t.Error("a message was sent")
			}
			if payload := f.outcomeOf(t); payload["outcome"] != "no_address" {
				t.Errorf("payload = %v, want outcome no_address", payload)
			}
		})
	}
}

// A failure of the relay (unreachable, TLS, the account refused) is told
// apart from the relay refusing this message, since the first is mended in
// the deployment's settings and the second at the recipient's mailbox.
func TestARelayFailureIsToldApartFromARefusalAndCarriesTheReply(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		opts           mailtest.Options
		want           error
		outcome, reply string
	}{
		{"wrong password", "a-wrong-one", mailtest.Options{}, mailing.ErrRelayFailed, "relay_failed", "535 5.7.8"},
		{"sender refused", "the-right-one",
			mailtest.Options{Replies: map[string]string{"MAIL": "550 5.7.60 SMTP; Client does not have permissions to send as this sender"}},
			mailing.ErrRelayFailed, "relay_failed", "550 5.7.60"},
		{"recipient refused", "the-right-one",
			mailtest.Options{RefuseRecipients: map[string]string{"ana@usm.md": "550 5.1.1 RESOLVER.ADR.RecipNotFound; not found"}},
			mailing.ErrRefused, "refused", "550 5.1.1"},
		{"message refused", "the-right-one",
			mailtest.Options{Replies: map[string]string{"DATA": "554 5.6.0 Invalid message content"}},
			mailing.ErrRefused, "refused", "554 5.6.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := relay(t, tc.opts)
			f := newTester(t, relaySender(t, server, tc.password))

			err := f.tester.Send(t.Context(), testRequest("ana@usm.md"))

			if !errors.Is(err, tc.want) {
				t.Fatalf("Send() = %v, want %v", err, tc.want)
			}
			if reply, ok := mail.ReplyOf(err); !ok || !strings.HasPrefix(reply.String(), tc.reply+" ") {
				t.Errorf("ReplyOf(Send()) = %q, %v; want the relay's %s", reply, ok, tc.reply)
			}
			payload := f.outcomeOf(t)
			if payload["outcome"] != tc.outcome || payload["reply"] != tc.reply {
				t.Errorf("payload = %v, want outcome %s and reply %s", payload, tc.outcome, tc.reply)
			}
		})
	}
}

func TestAnUnreachableRelayIsARelayFailureWithNoReply(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	sender, err := mail.New(mail.Config{
		Host: "127.0.0.1", Port: port, From: "noreply@usm.md", TLS: mail.TLSStartTLS,
		DialTimeout: time.Second, SendTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newTester(t, sender)

	err = f.tester.Send(t.Context(), testRequest("ana@usm.md"))

	if !errors.Is(err, mailing.ErrRelayFailed) {
		t.Fatalf("Send() = %v, want ErrRelayFailed", err)
	}
	payload := f.outcomeOf(t)
	if _, carried := payload["reply"]; carried || payload["outcome"] != "relay_failed" {
		t.Errorf("payload = %v, want outcome relay_failed and no reply", payload)
	}
}

// Every attempt opens a session with the relay, so the budget is spent before
// anything else, and a refusal spends it as a delivery does.
func TestEveryAttemptSpendsTheCallersBudget(t *testing.T) {
	server := relay(t, mailtest.Options{})
	f := newTester(t, relaySender(t, server, "the-right-one"))

	for range mailing.TestsPerMinute {
		if err := f.tester.Send(t.Context(), testRequest("")); !errors.Is(err, mailing.ErrNoAddress) {
			t.Fatalf("Send() = %v, want ErrNoAddress", err)
		}
	}
	err := f.tester.Send(t.Context(), testRequest("ana@usm.md"))

	if !errors.Is(err, mailing.ErrTestTooOften) {
		t.Fatalf("Send() past the budget = %v, want ErrTestTooOften", err)
	}
	if len(server.Messages()) != 0 {
		t.Error("a refused attempt reached the relay")
	}
	if len(f.trail.entries) != mailing.TestsPerMinute {
		t.Errorf("the trail holds %d entries, want one per attempt within the budget", len(f.trail.entries))
	}
	for i, subject := range f.limiter.subjects {
		if !strings.Contains(subject, administrator.String()) || f.limiter.windows[i] != time.Minute {
			t.Errorf("attempt %d spent %q over %v, want the caller's account over a minute", i, subject, f.limiter.windows[i])
		}
	}
}

func TestTheBudgetIsSpentEvenWithMailOff(t *testing.T) {
	f := newTester(t, disabledSender(t))

	_ = f.tester.Send(t.Context(), testRequest("ana@usm.md"))

	if len(f.limiter.subjects) != 1 {
		t.Errorf("the limiter was asked %d times, want once", len(f.limiter.subjects))
	}
}

func TestACounterThatCannotBeKeptRefusesTheSend(t *testing.T) {
	server := relay(t, mailtest.Options{})
	f := newTester(t, relaySender(t, server, "the-right-one"))
	f.limiter.err = errors.New("cache down")

	err := f.tester.Send(t.Context(), testRequest("ana@usm.md"))

	if err == nil || errors.Is(err, mailing.ErrTestTooOften) {
		t.Fatalf("Send() = %v, want a failure that is not the caller's", err)
	}
	if len(server.Messages()) != 0 {
		t.Error("a message was sent without a counter")
	}
}

// The message has left by the time the trail is written; failing the request
// then would only make the administrator send it again.
func TestATrailThatCannotBeWrittenDoesNotFailASentMessage(t *testing.T) {
	server := relay(t, mailtest.Options{})
	f := newTester(t, relaySender(t, server, "the-right-one"))
	f.trail.err = errors.New("database down")

	if err := f.tester.Send(t.Context(), testRequest("ana@usm.md")); err != nil {
		t.Fatalf("Send() = %v, want the delivery reported", err)
	}
	if len(server.Messages()) != 1 {
		t.Error("the message was not sent")
	}
	if !strings.Contains(f.log.String(), "database down") {
		t.Errorf("the failed trail write was not logged: %s", f.log.String())
	}
}

func TestTheTesterSaysWhetherMailIsOnAndAsWhom(t *testing.T) {
	server := relay(t, mailtest.Options{})
	on := newTester(t, relaySender(t, server, "the-right-one")).tester
	off := newTester(t, disabledSender(t)).tester

	if !on.Enabled() || on.From() != "noreply@usm.md" {
		t.Errorf("on: Enabled() = %v, From() = %q", on.Enabled(), on.From())
	}
	if off.Enabled() || off.From() != "" {
		t.Errorf("off: Enabled() = %v, From() = %q", off.Enabled(), off.From())
	}
}

func decodedSubject(t *testing.T, data string) string {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("the relay received no message: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("decode the subject: %v", err)
	}
	return subject
}

package mail_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/mail"
)

const (
	testUser     = "olympiad@usm.md"
	testPassword = "relay-secret-4f1c"
)

// relayConfig is the configuration of a sender pointed at the fake server.
func relayConfig(s *fakeSMTP, mode mail.TLSMode) mail.Config {
	return mail.Config{
		Host:        s.Host(),
		Port:        s.Port(),
		Username:    testUser,
		Password:    testPassword,
		From:        "Olimpiada USM <noreply@usm.md>",
		TLS:         mode,
		RootCAs:     s.Roots,
		DialTimeout: 2 * time.Second,
		SendTimeout: 5 * time.Second,
	}
}

func newSender(t *testing.T, cfg mail.Config) *mail.Sender {
	t.Helper()
	sender, err := mail.New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return sender
}

func send(t *testing.T, sender *mail.Sender) error {
	t.Helper()
	return sender.Send(context.Background(), "student@example.md", "Datele de conectare", "Bună ziua!\n")
}

func TestAMessageArrivesWithItsHeadersAndBody(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN LOGIN", User: testUser, Password: testPassword})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	subject := "Данные для входа — олимпиада"
	body := "Здравствуйте!\nВаш логин: ion.popescu\nAdresa: https://olympdb.usm.md\n"
	if err := sender.Send(context.Background(), "student@example.md", subject, body); err != nil {
		t.Fatalf("Send() = %v", err)
	}

	got := server.Messages()
	if len(got) != 1 {
		t.Fatalf("the server received %d messages, want 1", len(got))
	}
	if !got[0].Secure {
		t.Error("the message travelled before STARTTLS")
	}
	if got[0].From != "noreply@usm.md" || len(got[0].To) != 1 || got[0].To[0] != "student@example.md" {
		t.Errorf("envelope = %s -> %v, want noreply@usm.md -> [student@example.md]", got[0].From, got[0].To)
	}
	if auths := server.Auths(); len(auths) != 1 || auths[0] != "PLAIN "+testUser {
		t.Errorf("authentications = %v, want one PLAIN as %s", auths, testUser)
	}

	msg := parseMessage(t, got[0].Data)
	from, err := msg.Header.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Name != "Olimpiada USM" || from[0].Address != "noreply@usm.md" {
		t.Errorf("From = %v (%v), want Olimpiada USM <noreply@usm.md>", from, err)
	}
	if to := msg.Header.Get("To"); !strings.Contains(to, "student@example.md") {
		t.Errorf("To = %q", to)
	}
	if decoded := decodeHeader(t, msg.Header.Get("Subject")); decoded != subject {
		t.Errorf("Subject = %q, want %q", decoded, subject)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/plain" || !strings.EqualFold(params["charset"], "utf-8") {
		t.Errorf("Content-Type = %q, want text/plain in UTF-8", msg.Header.Get("Content-Type"))
	}
	if id := msg.Header.Get("Message-Id"); !strings.HasSuffix(id, "@usm.md>") {
		t.Errorf("Message-ID = %q, want one in the sending domain", id)
	}
	if msg.Header.Get("Date") == "" {
		t.Error("the message has no Date")
	}
	if text := readBody(t, msg); text != body {
		t.Errorf("body = %q, want %q", text, body)
	}
}

func TestImplicitTLSDeliversWithLoginAuthentication(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{ImplicitTLS: true, AuthMechs: "LOGIN", User: testUser, Password: testPassword})
	sender := newSender(t, relayConfig(server, mail.TLSImplicit))

	if err := send(t, sender); err != nil {
		t.Fatalf("Send() = %v", err)
	}

	if got := server.Messages(); len(got) != 1 || !got[0].Secure {
		t.Fatalf("messages = %+v, want one over TLS", got)
	}
	if auths := server.Auths(); len(auths) != 1 || auths[0] != "LOGIN "+testUser {
		t.Errorf("authentications = %v, want one LOGIN as %s", auths, testUser)
	}
}

func TestAPlainRelayDeliversWithoutTLSOrCredentials(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{})
	cfg := relayConfig(server, mail.TLSNone)
	cfg.Username, cfg.Password = "", ""
	sender := newSender(t, cfg)

	if err := send(t, sender); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	if got := server.Messages(); len(got) != 1 {
		t.Fatalf("the server received %d messages, want 1", len(got))
	}
}

func TestStartTLSModeNeverSendsOverAPlainConnection(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{AuthMechs: "PLAIN LOGIN", User: testUser, Password: testPassword})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	err := send(t, sender)

	assertPermanent(t, err)
	if len(server.Auths()) != 0 || len(server.Messages()) != 0 {
		t.Error("credentials or a message crossed a connection STARTTLS never protected")
	}
}

func TestAnUntrustedCertificateIsPermanent(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword})
	cfg := relayConfig(server, mail.TLSStartTLS)
	cfg.RootCAs = nil
	sender := newSender(t, cfg)

	assertPermanent(t, send(t, sender))
	if len(server.Auths()) != 0 {
		t.Error("credentials were sent to a server whose certificate was not trusted")
	}
}

func TestTLSOlderThanOnePointTwoIsRefused(t *testing.T) {
	for _, tc := range []struct {
		mode mail.TLSMode
		opts fakeOptions
	}{
		{mail.TLSStartTLS, fakeOptions{StartTLS: true}},
		{mail.TLSImplicit, fakeOptions{ImplicitTLS: true}},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			tc.opts.AuthMechs, tc.opts.User, tc.opts.Password = "PLAIN", testUser, testPassword
			tc.opts.MaxTLSVersion = tls.VersionTLS11
			server := newFakeSMTP(t, tc.opts)
			sender := newSender(t, relayConfig(server, tc.mode))

			assertPermanent(t, send(t, sender))
			if len(server.Auths()) != 0 || len(server.Messages()) != 0 {
				t.Error("credentials or a message crossed TLS 1.1")
			}
		})
	}
}

func TestImplicitTLSAgainstAPlainPortIsPermanent(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true})
	sender := newSender(t, relayConfig(server, mail.TLSImplicit))

	assertPermanent(t, send(t, sender))
}

func TestAnAuthenticationFailureIsPermanentAndDoesNotRepeatThePassword(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN LOGIN", User: testUser, Password: "another"})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	err := send(t, sender)

	assertPermanent(t, err)
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("the error repeats the password: %q", err)
	}
	if len(server.Messages()) != 0 {
		t.Error("a message was delivered without authentication")
	}
}

func TestARelayWithoutAuthenticationRefusesCredentialsPermanently(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	assertPermanent(t, send(t, sender))
}

func TestARefusalReplyIsPermanentAndADeferralTemporary(t *testing.T) {
	for _, tc := range []struct {
		stage, reply string
		temporary    bool
	}{
		{"GREETING", "554 5.3.2 no service", false},
		{"GREETING", "421 4.3.2 try later", true},
		{"STARTTLS", "454 4.7.0 TLS not available", true},
		{"AUTH", "454 4.7.0 temporary authentication failure", true},
		{"MAIL", "550 5.7.1 sender refused", false},
		{"MAIL", "451 4.3.0 local error", true},
		{"RCPT", "550 5.1.1 no such user", false},
		{"RCPT", "452 4.2.2 mailbox full", true},
		{"DATA", "554 5.6.0 no", false},
		{"DOT", "552 5.3.4 message too big", false},
		{"DOT", "451 4.7.1 greylisted", true},
	} {
		t.Run(tc.stage+" "+tc.reply[:3], func(t *testing.T) {
			server := newFakeSMTP(t, fakeOptions{
				StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword,
				Replies: map[string]string{tc.stage: tc.reply},
			})
			sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

			err := send(t, sender)

			if tc.temporary {
				assertTemporary(t, err)
			} else {
				assertPermanent(t, err)
			}
			if len(server.Messages()) != 0 {
				t.Error("the server recorded a message it refused")
			}
		})
	}
}

func TestADroppedConnectionIsTemporary(t *testing.T) {
	for _, stage := range []string{"GREETING", "EHLO", "AUTH", "MAIL", "RCPT", "DATA", "DOT"} {
		t.Run(stage, func(t *testing.T) {
			server := newFakeSMTP(t, fakeOptions{
				StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword, DropAt: stage,
			})
			sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

			assertTemporary(t, send(t, sender))
		})
	}
}

func TestAnUnreachableRelayIsTemporary(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	sender := newSender(t, mail.Config{
		Host: "127.0.0.1", Port: port, From: "noreply@usm.md", TLS: mail.TLSStartTLS,
		DialTimeout: time.Second, SendTimeout: 2 * time.Second,
	})

	assertTemporary(t, send(t, sender))
}

func TestASilentRelayIsCutOffAtTheSendTimeout(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword, StallAt: "DOT"})
	cfg := relayConfig(server, mail.TLSStartTLS)
	cfg.DialTimeout = 5 * time.Second
	cfg.SendTimeout = 300 * time.Millisecond
	sender := newSender(t, cfg)

	started := time.Now()
	err := send(t, sender)

	assertTemporary(t, err)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("Send() returned after %s, want it cut off near the 300ms send timeout", elapsed)
	}
}

func TestACancelledContextStopsTheSend(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword, StallAt: "MAIL"})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := sender.Send(ctx, "student@example.md", "s", "b")

	assertTemporary(t, err)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("Send() returned after %s, want it to stop with its context", elapsed)
	}
}

// A failure after the relay accepted the message must not report the send as
// failed: a caller would retry and deliver it twice.
func TestAFailureAfterAcceptanceIsNotAFailedSend(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{
		StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword,
		Replies: map[string]string{"QUIT": "554 5.0.0 goodbye anyway"},
	})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	if err := send(t, sender); err != nil {
		t.Fatalf("Send() = %v after the relay accepted the message", err)
	}
	if len(server.Messages()) != 1 {
		t.Fatal("the relay did not receive the message")
	}
}

func TestANewlineInTheSubjectCannotAddAHeader(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	subject := "Hello\r\nBcc: victim@example.md\r\nX-Injected: yes"
	if err := sender.Send(context.Background(), "student@example.md", subject, "body"); err != nil {
		t.Fatalf("Send() = %v", err)
	}

	got := server.Messages()
	if len(got) != 1 || len(got[0].To) != 1 {
		t.Fatalf("messages = %+v, want one to one recipient", got)
	}
	assertNoInjectedHeader(t, got[0].Data)
}

func TestADisplayNameWithANewlineCannotAddAHeader(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword})

	// A raw newline does not parse as an address at all.
	cfg := relayConfig(server, mail.TLSStartTLS)
	cfg.From = "\"Olimpiada\r\nBcc: victim@example.md\" <noreply@usm.md>"
	if _, err := mail.New(cfg); err == nil {
		t.Error("New() accepted a From with a raw newline in its display name")
	}

	// An encoded word decodes to one, and is refused all the same.
	cfg.From = "=?utf-8?q?Olimpiada=0D=0ABcc=3A_victim=40example=2Emd?= <noreply@usm.md>"
	if _, err := mail.New(cfg); err == nil {
		t.Error("New() accepted a From whose encoded display name carries a newline")
	}
}

func TestOnlyABareValidRecipientIsAccepted(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	for _, to := range []string{
		"",
		"not an address",
		"a@example.md, b@example.md",
		"Ion Popescu <ion@example.md>",
		"ion@example.md\r\nBcc: victim@example.md",
		strings.Repeat("a", mail.MaxAddressBytes) + "@example.md",
	} {
		err := sender.Send(context.Background(), to, "subject", "body")
		if !errors.Is(err, mail.ErrInvalidMessage) {
			t.Errorf("Send(to=%q) = %v, want ErrInvalidMessage", to, err)
		}
		if mail.IsTemporary(err) {
			t.Errorf("Send(to=%q) reported a refused address as temporary", to)
		}
	}
	if len(server.Messages()) != 0 {
		t.Error("a message to an invalid recipient reached the relay")
	}
}

func TestTheSubjectAndBodyAreBounded(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))
	ctx := context.Background()

	if err := sender.Send(ctx, "s@example.md", strings.Repeat("s", mail.MaxSubjectBytes+1), "b"); !errors.Is(err, mail.ErrInvalidMessage) {
		t.Errorf("an oversized subject = %v, want ErrInvalidMessage", err)
	}
	if err := sender.Send(ctx, "s@example.md", "s", strings.Repeat("b", mail.MaxBodyBytes+1)); !errors.Is(err, mail.ErrInvalidMessage) {
		t.Errorf("an oversized body = %v, want ErrInvalidMessage", err)
	}
	if err := sender.Send(ctx, "s@example.md", strings.Repeat("s", mail.MaxSubjectBytes), strings.Repeat("b", mail.MaxBodyBytes)); err != nil {
		t.Errorf("a subject and body at their bounds = %v, want delivery", err)
	}
}

func TestWithoutAHostMailIsOff(t *testing.T) {
	sender, err := mail.New(mail.Config{})
	if err != nil {
		t.Fatalf("New() without a host = %v, want a disabled sender", err)
	}

	if sender.Enabled() {
		t.Error("Enabled() = true without a host")
	}
	if sender.From() != "" {
		t.Errorf("From() = %q without a host, want empty", sender.From())
	}
	err = sender.Send(context.Background(), "student@example.md", "s", "b")
	if !errors.Is(err, mail.ErrDisabled) || mail.IsTemporary(err) {
		t.Errorf("Send() = %v, want ErrDisabled, not temporary", err)
	}
}

func TestTheSenderReportsItsAddressAndNoSecret(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{})
	sender := newSender(t, relayConfig(server, mail.TLSStartTLS))

	if !sender.Enabled() {
		t.Error("Enabled() = false with a host")
	}
	if sender.From() != "noreply@usm.md" {
		t.Errorf("From() = %q, want the bare address noreply@usm.md", sender.From())
	}
}

func TestNewRefusesAnInconsistentConfiguration(t *testing.T) {
	valid := mail.Config{Host: "smtp.usm.md", Port: 587, From: "noreply@usm.md", TLS: mail.TLSStartTLS}
	if _, err := mail.New(valid); err != nil {
		t.Fatalf("New(valid) = %v", err)
	}

	for name, change := range map[string]func(*mail.Config){
		"no From":                   func(c *mail.Config) { c.From = "" },
		"a From that is no address": func(c *mail.Config) { c.From = "noreply" },
		"two From addresses":        func(c *mail.Config) { c.From = "a@usm.md, b@usm.md" },
		"an unknown TLS mode":       func(c *mail.Config) { c.TLS = "ssl" },
		"port zero":                 func(c *mail.Config) { c.Port = 0 },
		"a port past 65535":         func(c *mail.Config) { c.Port = 65536 },
		"a user without password":   func(c *mail.Config) { c.Username = "u" },
		"a password without user":   func(c *mail.Config) { c.Password = "p" },
		"credentials without TLS": func(c *mail.Config) {
			c.TLS, c.Username, c.Password = mail.TLSNone, "u", "p"
		},
		"a negative timeout": func(c *mail.Config) { c.SendTimeout = -time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			change(&cfg)
			if _, err := mail.New(cfg); err == nil {
				t.Errorf("New() accepted %s", name)
			}
		})
	}
}

func assertTemporary(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Send() succeeded, want a temporary error")
	}
	if !mail.IsTemporary(err) || !errors.Is(err, mail.ErrTemporary) || errors.Is(err, mail.ErrPermanent) {
		t.Errorf("Send() = %v, want temporary and only temporary", err)
	}
}

func assertPermanent(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Send() succeeded, want a permanent error")
	}
	if mail.IsTemporary(err) || !errors.Is(err, mail.ErrPermanent) {
		t.Errorf("Send() = %v, want permanent", err)
	}
}

func parseMessage(t *testing.T, data string) *netmail.Message {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("the received message does not parse: %v\n%s", err, data)
	}
	return msg
}

func decodeHeader(t *testing.T, value string) string {
	t.Helper()
	decoded, err := new(mime.WordDecoder).DecodeHeader(value)
	if err != nil {
		t.Fatalf("decode %q: %v", value, err)
	}
	return decoded
}

func readBody(t *testing.T, msg *netmail.Message) string {
	t.Helper()
	var reader io.Reader = msg.Body
	if strings.EqualFold(msg.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
		reader = quotedprintable.NewReader(msg.Body)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

// assertNoInjectedHeader checks that the header block holds only the headers
// a message is built with, each on a line of its own. The fake server hands
// the message text over with LF line endings, as textproto decodes it.
func assertNoInjectedHeader(t *testing.T, data string) {
	t.Helper()
	head, _, _ := strings.Cut(data, "\n\n")
	for _, line := range strings.Split(head, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name, _, _ := strings.Cut(line, ":")
		switch strings.ToLower(name) {
		case "date", "message-id", "mime-version", "from", "to", "subject", "content-type",
			"content-transfer-encoding", "user-agent", "x-mailer":
		default:
			t.Errorf("the message carries an unexpected header line %q", line)
		}
	}
}

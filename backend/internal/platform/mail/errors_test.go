package mail_test

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/platform/mail"
	"github.com/devrdn/db-contest/backend/internal/platform/mail/mailtest"
)

func TestOnlyATemporaryFailureIsWorthARetry(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("something else"), false},
		{fmt.Errorf("send: %w", mail.ErrTemporary), true},
		{fmt.Errorf("send: %w", mail.ErrPermanent), false},
		{mail.ErrInvalidMessage, false},
		{mail.ErrDisabled, false},
	} {
		if got := mail.IsTemporary(tc.err); got != tc.want {
			t.Errorf("IsTemporary(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestOnlyAFailureMarkedAsTheRelaysIsARelayFailure(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("something else"), false},
		{fmt.Errorf("%w: %w: 535 5.7.3", mail.ErrPermanent, mail.ErrRelay), true},
		{fmt.Errorf("%w: %w: dial", mail.ErrTemporary, mail.ErrRelay), true},
		{fmt.Errorf("%w: 550 5.1.1", mail.ErrPermanent), false},
		{mail.ErrInvalidMessage, false},
		{mail.ErrDisabled, false},
	} {
		if got := mail.IsRelayFailure(tc.err); got != tc.want {
			t.Errorf("IsRelayFailure(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestOnlyAFailureMarkedAsTheRecipientsIsARecipientRefusal(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("%w: %w: 550 5.1.1", mail.ErrPermanent, mail.ErrRecipient), true},
		{fmt.Errorf("%w: 550 5.1.8", mail.ErrPermanent), false},
		{mail.ErrInvalidMessage, false},
	} {
		if got := mail.IsRecipientRefusal(tc.err); got != tc.want {
			t.Errorf("IsRecipientRefusal(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// The relay's own answer is what an administrator needs to mend a refused
// send, so it is read back from the failure: the code, the enhanced code and
// the text, without what go-mail adds around them (the message ID, the
// recipient).
func TestTheRelaysReplyIsReadBackFromAFailedSend(t *testing.T) {
	for _, tc := range []struct {
		stage, reply string
		want         mail.Reply
	}{
		{"AUTH", "535 5.7.139 Authentication unsuccessful, the user credentials were incorrect.",
			mail.Reply{Code: 535, Enhanced: "5.7.139", Text: "Authentication unsuccessful, the user credentials were incorrect."}},
		{"GREETING", "421 4.3.2 Service not available",
			mail.Reply{Code: 421, Enhanced: "4.3.2", Text: "Service not available"}},
		{"MAIL", "530 5.7.57 Client not authenticated to send mail",
			mail.Reply{Code: 530, Enhanced: "5.7.57", Text: "Client not authenticated to send mail"}},
		{"RCPT", "550 5.1.1 RESOLVER.ADR.RecipNotFound; not found",
			mail.Reply{Code: 550, Enhanced: "5.1.1", Text: "RESOLVER.ADR.RecipNotFound; not found"}},
		{"RCPT", "550 no such user",
			mail.Reply{Code: 550, Text: "no such user"}},
		{"DATA", `554 5.6.0 "quoted", and a backslash \ kept`,
			mail.Reply{Code: 554, Enhanced: "5.6.0", Text: `"quoted", and a backslash \ kept`}},
		{"DOT", "554 5.2.252 SendAsDenied; noreply@usm.md not allowed to send as other@usm.md",
			mail.Reply{Code: 554, Enhanced: "5.2.252", Text: "SendAsDenied; noreply@usm.md not allowed to send as other@usm.md"}},
	} {
		t.Run(tc.stage+" "+tc.reply[:9], func(t *testing.T) {
			server := mailtest.NewServer(t, mailtest.Options{
				StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword,
				Replies: map[string]string{tc.stage: tc.reply},
			})

			reply, ok := mail.ReplyOf(send(t, newSender(t, relayConfig(server, mail.TLSStartTLS))))

			if !ok || reply != tc.want {
				t.Errorf("ReplyOf() = %+v, %v; want %+v, true", reply, ok, tc.want)
			}
		})
	}
}

func TestAReplyReadsAsItsCodeEnhancedCodeAndText(t *testing.T) {
	for _, tc := range []struct {
		reply mail.Reply
		want  string
	}{
		{mail.Reply{Code: 550, Enhanced: "5.1.1", Text: "no such user"}, "550 5.1.1 no such user"},
		{mail.Reply{Code: 550, Text: "no such user"}, "550 no such user"},
		{mail.Reply{Code: 421}, "421"},
	} {
		if got := tc.reply.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.reply, got, tc.want)
		}
	}
}

// A relay's text is shown on a screen and kept in the audit trail, so it is
// one bounded line of valid text whatever the relay sent.
func TestAReplysTextIsOneBoundedLine(t *testing.T) {
	long := strings.Repeat("я", mail.MaxReplyTextBytes)
	server := mailtest.NewServer(t, mailtest.Options{
		StartTLS: true, AuthMechs: "PLAIN", User: testUser, Password: testPassword,
		Replies: map[string]string{"RCPT": "550-5.1.1 first line\r\n550 5.1.1 " + long},
	})

	reply, ok := mail.ReplyOf(send(t, newSender(t, relayConfig(server, mail.TLSStartTLS))))

	if !ok || reply.Code != 550 || reply.Enhanced != "5.1.1" {
		t.Fatalf("ReplyOf() = %+v, %v; want 550 5.1.1", reply, ok)
	}
	if !strings.HasPrefix(reply.Text, "first line ") {
		t.Errorf("text = %q, want the lines joined by a space", reply.Text)
	}
	if len(reply.Text) > mail.MaxReplyTextBytes || !utf8.ValidString(reply.Text) {
		t.Errorf("text is %d bytes (at most %d) or cut inside a character: %q", len(reply.Text), mail.MaxReplyTextBytes, reply.Text)
	}
	if strings.ContainsFunc(reply.Text, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		t.Errorf("text carries a control character: %q", reply.Text)
	}
}

func TestAFailureWithNoReplyHasNone(t *testing.T) {
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

	for _, err := range []error{nil, errors.New("something else"), send(t, sender)} {
		if reply, ok := mail.ReplyOf(err); ok {
			t.Errorf("ReplyOf(%v) = %+v, want none", err, reply)
		}
	}
}

package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	gomail "github.com/wneessen/go-mail"
)

// Every error Send returns wraps exactly one of these.
var (
	// ErrDisabled: no relay is configured (SMTP_HOST is empty).
	ErrDisabled = errors.New("mail: sending is disabled")
	// ErrInvalidMessage: the recipient, subject or body was refused before
	// anything was sent. A retry with the same values fails the same way.
	ErrInvalidMessage = errors.New("mail: invalid message")
	// ErrTemporary: the relay deferred the message (a 4xx reply) or could not
	// be reached or finished with (a network error, a timeout). A later retry
	// may succeed.
	ErrTemporary = errors.New("mail: temporary failure")
	// ErrPermanent: the relay refused (a 5xx reply, failed authentication, a
	// refused address) or the connection cannot be made safely (TLS). A retry
	// fails the same way until the configuration or the message changes.
	ErrPermanent = errors.New("mail: permanent failure")

	// ErrRelay marks, beside ErrTemporary or ErrPermanent, a failure of the
	// relay rather than of the message: any other message would fail the same
	// way now.
	ErrRelay = errors.New("mail: relay failure")

	// ErrRecipient marks, beside ErrPermanent, a refusal that names the
	// recipient: another message, to another address, would not meet it.
	ErrRecipient = errors.New("mail: recipient refused")
)

// IsTemporary reports whether a retry of the send may succeed.
func IsTemporary(err error) bool {
	return errors.Is(err, ErrTemporary)
}

// IsRelayFailure reports whether the failure is the relay's: it could not be
// reached or secured, refused the account or the sender, deferred the message
// after accepting its recipient, or dropped the connection. Such a failure
// says nothing about the message, so a caller holding a queue stops sending
// rather than give up on the message.
func IsRelayFailure(err error) bool {
	return errors.Is(err, ErrRelay)
}

// IsRecipientRefusal reports whether the relay refused the message for its
// recipient's sake (an unknown, moved, disabled or full mailbox). Every other
// refusal of the message may be the relay refusing everyone the same way.
func IsRecipientRefusal(err error) bool {
	return errors.Is(err, ErrRecipient)
}

// classify wraps an SMTP failure in ErrTemporary or ErrPermanent, and in
// ErrRelay when it is the relay's. ctx is the send's own context: once it is
// done the connection was cut on purpose.
func classify(ctx context.Context, err error) error {
	class := ErrPermanent
	if temporary(ctx, err) {
		class = ErrTemporary
	}
	if relayFailure(err) {
		return fmt.Errorf("%w: %w: %w", class, ErrRelay, err)
	}
	if class == ErrPermanent && recipientRefusal(err) {
		return fmt.Errorf("%w: %w: %w", class, ErrRecipient, err)
	}
	return fmt.Errorf("%w: %w", class, err)
}

// relayFailure tells the relay's failures from the message's by the stage the
// session reached. Dialling, TLS, the greeting, EHLO, AUTH and MAIL FROM come
// before go-mail knows the recipient, so their failures are the relay's. At
// RCPT only a reply naming the recipient is the message's. After RCPT a
// deferral is the relay throttling or busy, and Exchange Online reports a
// refusal of the sending mailbox (Send As denied, the submission quota) as
// STOREDRV.Submission or 5.7.60 at DATA; any other refusal there is the
// message's.
func relayFailure(err error) bool {
	var sendErr *gomail.SendError
	if !errors.As(err, &sendErr) {
		return true
	}
	code := sendErr.ErrorCode()
	switch sendErr.Reason {
	case gomail.ErrGetRcpts, gomail.ErrNoUnencoded:
		return false
	case gomail.ErrSMTPRcptTo:
		return code == 0 || code == 421
	case gomail.ErrSMTPData, gomail.ErrSMTPDataClose, gomail.ErrWriteContent:
		text := sendErr.Error()
		return code < 500 || strings.Contains(text, "STOREDRV.Submission") || enhancedCode(text) == "7.60"
	default:
		return true
	}
}

// recipientStatus are the RFC 3463 subjects and details that name the
// recipient's address or mailbox: X.1.1-3, X.1.6, X.1.10, X.2.1 and X.2.2.
var recipientStatus = map[string]bool{
	"1.1": true, "1.2": true, "1.3": true, "1.6": true, "1.10": true, "2.1": true, "2.2": true,
}

// enhancedStatus finds the RFC 3463 code at the start of a reply's text, as
// go-mail quotes it: 550 "5.1.1 no such user".
var enhancedStatus = regexp.MustCompile(`\b[245]\.(\d{1,3}\.\d{1,3})(?:[\s"]|$)`)

// recipientRefusal reports whether a refusal names the recipient: by its
// enhanced code, or, without one, by coming in answer to RCPT.
func recipientRefusal(err error) bool {
	var sendErr *gomail.SendError
	if !errors.As(err, &sendErr) {
		return false
	}
	if code := enhancedCode(sendErr.Error()); code != "" {
		return recipientStatus[code]
	}
	return sendErr.Reason == gomail.ErrSMTPRcptTo
}

// enhancedCode is the subject and detail of the reply's RFC 3463 code ("1.1"
// for 5.1.1), or "" when the reply carries none.
func enhancedCode(reply string) string {
	if m := enhancedStatus.FindStringSubmatch(reply); m != nil {
		return m[1]
	}
	return ""
}

func temporary(ctx context.Context, err error) bool {
	// go-mail's SendError does not unwrap; it carries the reply code instead,
	// and zero when the failure was not a reply.
	var sendErr *gomail.SendError
	if errors.As(err, &sendErr) {
		switch code := sendErr.ErrorCode(); {
		case code >= 400 && code < 500:
			return true
		case code >= 500:
			return false
		}
		switch sendErr.Reason {
		case gomail.ErrGetSender, gomail.ErrGetRcpts, gomail.ErrNoUnencoded:
			return false
		}
		// Once the session has begun, a failure without a reply is the
		// connection's.
		return true
	}

	var reply *textproto.Error
	if errors.As(err, &reply) {
		return reply.Code >= 400 && reply.Code < 500
	}

	// A TLS refusal is a disagreement about versions or certificates, not a
	// passing fault, though crypto/tls reports the peer's alert as a
	// *net.OpError whose Op is "remote error".
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "remote error" {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	// Anything else that is not the network's, a certificate this side does
	// not trust among them, is permanent.
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed)
}

// MaxReplyTextBytes bounds the text ReplyOf returns. A reply is shown to an
// administrator and kept in the audit trail, and Exchange's diagnostics run
// long after the sentence that says what is wrong.
const MaxReplyTextBytes = 200

// Reply is the relay's answer that failed a send.
type Reply struct {
	Code int
	// Enhanced is the RFC 3463 code, such as 5.7.139; empty when the relay
	// gave none.
	Enhanced string
	// Text is the rest of the reply, on one line and at most
	// MaxReplyTextBytes.
	Text string
}

func (r Reply) String() string {
	parts := []string{strconv.Itoa(r.Code)}
	for _, part := range []string{r.Enhanced, r.Text} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// leadingStatus is an RFC 3463 code at the start of a reply's text.
var leadingStatus = regexp.MustCompile(`^[245]\.\d{1,3}\.\d{1,3}(?:\s+|$)`)

// ReplyOf returns the relay's reply a failed Send carries, and false when
// the failure was not a reply (an unreachable relay, a TLS refusal, a
// dropped connection). The text comes only from the relay, never from the
// message.
func ReplyOf(err error) (Reply, bool) {
	var code int
	var text string
	var reply *textproto.Error
	var sendErr *gomail.SendError
	switch {
	case errors.As(err, &reply):
		code, text = reply.Code, reply.Msg
	case errors.As(err, &sendErr) && sendErr.ErrorCode() != 0:
		code = sendErr.ErrorCode()
		text = replyText(sendErr.Error(), code)
	default:
		return Reply{}, false
	}

	text = strings.TrimSpace(text)
	enhanced := strings.TrimSpace(leadingStatus.FindString(text))
	text = oneLine(text[len(leadingStatus.FindString(text)):])
	return Reply{Code: code, Enhanced: enhanced, Text: text}, true
}

// replyText finds the reply in a SendError's text, which is the only place
// go-mail keeps it: the code and the text as net/textproto quotes it, among
// the stage before and the recipients and message ID after.
func replyText(s string, code int) string {
	prefix := strconv.Itoa(code) + ` "`
	at := strings.Index(s, prefix)
	if at < 0 {
		return ""
	}
	// The quoted string starts at the prefix's own quotation mark.
	quoted, err := strconv.QuotedPrefix(s[at+len(prefix)-1:])
	if err != nil {
		return ""
	}
	text, err := strconv.Unquote(quoted)
	if err != nil {
		return ""
	}
	return text
}

// oneLine makes a reply's text printable, on one line and bounded.
func oneLine(text string) string {
	text = strings.Join(strings.FieldsFunc(strings.ToValidUTF8(text, ""), func(r rune) bool {
		return r < 0x20 || r == 0x7f || r == ' '
	}), " ")
	if len(text) <= MaxReplyTextBytes {
		return text
	}
	cut := MaxReplyTextBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

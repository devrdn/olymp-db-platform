package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"regexp"
	"strings"

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
// STOREDRV.Submission at the end of DATA; any other refusal there is the
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
		return code < 500 || strings.Contains(sendErr.Error(), "STOREDRV.Submission")
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
	if m := enhancedStatus.FindStringSubmatch(sendErr.Error()); m != nil {
		return recipientStatus[m[1]]
	}
	return sendErr.Reason == gomail.ErrSMTPRcptTo
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

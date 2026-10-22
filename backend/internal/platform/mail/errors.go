package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"

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
)

// IsTemporary reports whether a retry of the send may succeed.
func IsTemporary(err error) bool {
	return errors.Is(err, ErrTemporary)
}

// classify wraps an SMTP failure in ErrTemporary or ErrPermanent. ctx is the
// send's own context: once it is done the connection was cut on purpose.
func classify(ctx context.Context, err error) error {
	if temporary(ctx, err) {
		return fmt.Errorf("%w: %w", ErrTemporary, err)
	}
	return fmt.Errorf("%w: %w", ErrPermanent, err)
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

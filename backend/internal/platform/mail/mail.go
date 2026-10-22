// Package mail sends one plain-text message through an SMTP relay: Sender.Send
// opens a connection, delivers, and closes it. The protocol, TLS and header
// encoding are github.com/wneessen/go-mail's; this package decides the
// configuration it accepts and what a failure means to the caller.
//
// Every error Send returns wraps exactly one of ErrDisabled,
// ErrInvalidMessage, ErrTemporary or ErrPermanent, and IsTemporary reports
// whether a retry may help: a 4xx reply, a network error or a timeout yes; a
// 5xx reply, failed authentication, a refused address, an untrusted
// certificate or a refused TLS handshake no. A temporary or permanent failure
// also wraps ErrRelay when it is the relay's and not the message's
// (IsRelayFailure): everything before RCPT, a lost connection, a deferral
// after the recipient was accepted. A permanent refusal naming the recipient
// (an unknown or full mailbox, by its RFC 3463 code) also wraps ErrRecipient
// (IsRecipientRefusal). ReplyOf reads back the relay's reply a failure
// carries, bounded, for a person to read. TLS is 1.2 or later. A Sender
// built without a host is disabled rather than missing, so a deployment
// without a relay still starts.
//
// It does not queue, retry, render texts or decide who is written to; it
// sends what it is given, once. It imports no domain package.
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	netmail "net/mail"
	"slices"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// TLSMode is how the connection to the relay is encrypted.
type TLSMode string

const (
	// TLSStartTLS upgrades a plain connection and refuses a relay that does
	// not offer it; the usual mode on port 587.
	TLSStartTLS TLSMode = "starttls"
	// TLSImplicit speaks TLS from the first byte, as on port 465.
	TLSImplicit TLSMode = "tls"
	// TLSNone sends in the clear, and so never with credentials. For a
	// development relay only.
	TLSNone TLSMode = "none"
)

// Modes lists the accepted TLS modes.
var Modes = []TLSMode{TLSStartTLS, TLSImplicit, TLSNone}

// Bounds on what one message may carry. An address is bounded by the SMTP
// path limit (RFC 5321); the others are far above any message this service
// writes.
const (
	MaxAddressBytes = 254
	MaxSubjectBytes = 512
	MaxBodyBytes    = 64 << 10
)

// Defaults for Config's zero timeouts.
const (
	DefaultDialTimeout = 10 * time.Second
	DefaultSendTimeout = 30 * time.Second
)

// Config is a relay and the address messages are sent from.
type Config struct {
	// Host empty disables sending.
	Host string
	Port int
	// Username and Password are both set or both empty; empty sends no AUTH.
	Username string
	Password string
	// From is an address, optionally with a display name:
	// "Olimpiada USM <noreply@usm.md>".
	From string
	TLS  TLSMode
	// RootCAs verifies the relay's certificate; nil trusts the system roots.
	RootCAs *x509.CertPool
	// DialTimeout bounds connecting and then each reply; SendTimeout bounds
	// the whole exchange, connection included. Zero takes the default.
	DialTimeout time.Duration
	SendTimeout time.Duration
}

// Sender delivers messages through one relay. It is safe for concurrent use;
// every Send has a connection of its own.
type Sender struct {
	cfg    Config
	from   *netmail.Address
	domain string
}

// New validates cfg. Without a host it returns a disabled Sender.
func New(cfg Config) (*Sender, error) {
	if cfg.Host == "" {
		return &Sender{}, nil
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("mail: port %d is outside 1-65535", cfg.Port)
	}
	if !slices.Contains(Modes, cfg.TLS) {
		return nil, fmt.Errorf("mail: unknown TLS mode %q, want one of %v", cfg.TLS, Modes)
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errors.New("mail: a username and a password go together")
	}
	if cfg.Username != "" && cfg.TLS == TLSNone {
		return nil, errors.New("mail: credentials are never sent without TLS")
	}
	if cfg.DialTimeout < 0 || cfg.SendTimeout < 0 {
		return nil, errors.New("mail: a timeout cannot be negative")
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.SendTimeout == 0 {
		cfg.SendTimeout = DefaultSendTimeout
	}
	from, err := ParseFrom(cfg.From)
	if err != nil {
		return nil, err
	}
	_, domain, _ := strings.Cut(from.Address, "@")
	return &Sender{cfg: cfg, from: from, domain: domain}, nil
}

// ParseFrom reads a sending address, with an optional display name. A name
// that decodes to a control character is refused: it would end the header.
func ParseFrom(raw string) (*netmail.Address, error) {
	from, err := netmail.ParseAddress(raw)
	if err != nil || !strings.Contains(from.Address, "@") {
		return nil, fmt.Errorf("mail: the sending address %q is not one address", raw)
	}
	if strings.ContainsFunc(from.Name, isControl) {
		return nil, errors.New("mail: the sending address's display name carries a control character")
	}
	return from, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// Enabled reports whether a relay is configured.
func (s *Sender) Enabled() bool { return s.from != nil }

// From is the bare sending address, empty when disabled.
func (s *Sender) From() string {
	if s.from == nil {
		return ""
	}
	return s.from.Address
}

// Send delivers one plain-text message to one bare address. A nil error means
// the relay accepted it; a failure after that (closing the session) is not
// reported, since a retry would deliver it twice.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	msg, err := s.message(to, subject, body)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.SendTimeout)
	defer cancel()
	client, err := gomail.NewClient(s.cfg.Host, s.options(ctx)...)
	if err != nil {
		return fmt.Errorf("%w: %w: %w", ErrPermanent, ErrRelay, err)
	}
	err = client.DialAndSendWithContext(ctx, msg)
	if err == nil || msg.IsDelivered() {
		return nil
	}
	return classify(ctx, err)
}

func (s *Sender) message(to, subject, body string) (*gomail.Msg, error) {
	if len(to) > MaxAddressBytes {
		return nil, fmt.Errorf("%w: the recipient is longer than %d bytes", ErrInvalidMessage, MaxAddressBytes)
	}
	rcpt, err := netmail.ParseAddress(to)
	if err != nil || rcpt.Name != "" || !strings.Contains(rcpt.Address, "@") {
		return nil, fmt.Errorf("%w: the recipient %q is not one bare address", ErrInvalidMessage, to)
	}
	if len(subject) > MaxSubjectBytes {
		return nil, fmt.Errorf("%w: the subject is longer than %d bytes", ErrInvalidMessage, MaxSubjectBytes)
	}
	if len(body) > MaxBodyBytes {
		return nil, fmt.Errorf("%w: the body is longer than %d bytes", ErrInvalidMessage, MaxBodyBytes)
	}

	msg := gomail.NewMsg()
	if err := msg.From(s.from.String()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	if err := msg.To(rcpt.Address); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	// go-mail encodes a header value with any control character as an encoded
	// word, so a newline in the subject stays inside it.
	msg.Subject(subject)
	msg.SetMessageIDWithValue(messageID(s.domain))
	msg.SetBodyString(gomail.TypeTextPlain, body)
	return msg, nil
}

// messageID names the message in the sending domain rather than after the
// container's host name.
func messageID(domain string) string {
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	return hex.EncodeToString(random) + "@" + domain
}

func (s *Sender) options(ctx context.Context) []gomail.Option {
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, RootCAs: s.cfg.RootCAs, MinVersion: tls.VersionTLS12}
	opts := []gomail.Option{
		gomail.WithPort(s.cfg.Port),
		gomail.WithTimeout(s.cfg.DialTimeout),
		gomail.WithTLSConfig(tlsConfig),
		gomail.WithDialContextFunc(s.dialer(ctx, tlsConfig)),
		// One message per connection: no NOOP before it, no RSET after it.
		gomail.WithoutNoop(),
		gomail.WithoutRset(),
	}
	switch s.cfg.TLS {
	case TLSStartTLS:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	default:
		// Implicit TLS is the dialer's; none is none.
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	}
	if s.cfg.Username != "" {
		opts = append(opts,
			gomail.WithOpportunisticSMTPAuth(gomail.SMTPAuthPlain, gomail.SMTPAuthLogin),
			gomail.WithUsername(s.cfg.Username),
			gomail.WithPassword(s.cfg.Password))
	}
	return opts
}

// dialer connects, completes implicit TLS itself, and closes the connection
// when the send's context ends. go-mail bounds each reply but not the
// exchange, and never looks at the context once connected.
func (s *Sender) dialer(sendCtx context.Context, tlsConfig *tls.Config) gomail.DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if s.cfg.TLS == TLSImplicit {
			tlsConn := tls.Client(conn, tlsConfig)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			conn = tlsConn
		}
		context.AfterFunc(sendCtx, func() { _ = conn.Close() })
		return conn, nil
	}
}

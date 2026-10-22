package mail_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is an in-process SMTP server speaking enough of the protocol for
// the transport: EHLO, STARTTLS, AUTH PLAIN and LOGIN, MAIL, RCPT, DATA,
// RSET, NOOP and QUIT. Nothing it receives leaves the process.
type fakeSMTP struct {
	t       *testing.T
	opts    fakeOptions
	ln      net.Listener
	tlsConf *tls.Config
	// Roots trusts the server's certificate.
	Roots *x509.CertPool
	done  chan struct{}

	mu       sync.Mutex
	messages []received
	auths    []string
}

// fakeOptions shapes the server. The zero value is a plain server that
// offers neither TLS nor AUTH.
type fakeOptions struct {
	// ImplicitTLS speaks TLS from the first byte, as on port 465.
	ImplicitTLS bool
	// StartTLS advertises STARTTLS on a plain connection.
	StartTLS bool
	// AuthMechs is advertised as AUTH once the connection is encrypted, or on
	// a server that offers no TLS at all. Empty advertises no AUTH.
	AuthMechs string
	User      string
	Password  string
	// Replies overrides the answer to a stage: GREETING, EHLO, STARTTLS,
	// AUTH, MAIL, RCPT, DATA, DOT (the end of the message text) or QUIT.
	Replies map[string]string
	// DropAt closes the connection at that stage instead of answering.
	DropAt string
	// StallAt stops answering at that stage until the test ends.
	StallAt string
	// MaxTLSVersion caps the TLS the server speaks; zero leaves Go's maximum.
	MaxTLSVersion uint16
}

type received struct {
	From   string
	To     []string
	Data   string
	Secure bool
}

func newFakeSMTP(t *testing.T, opts fakeOptions) *fakeSMTP {
	t.Helper()
	cert, roots := testCertificate(t)
	s := &fakeSMTP{
		t:    t,
		opts: opts,
		tlsConf: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS10, // #nosec G402 -- lets a test offer what the client must refuse.
			MaxVersion:   opts.MaxTLSVersion,
		},
		Roots: roots,
		done:  make(chan struct{}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if opts.ImplicitTLS {
		ln = tls.NewListener(ln, s.tlsConf)
	}
	s.ln = ln

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		close(s.done)
		_ = ln.Close()
		wg.Wait()
	})
	return s
}

func (s *fakeSMTP) Host() string { return "127.0.0.1" }

func (s *fakeSMTP) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeSMTP) Messages() []received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]received(nil), s.messages...)
}

// Auths lists the users that authenticated successfully, with the mechanism.
func (s *fakeSMTP) Auths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auths...)
}

// misbehave applies a stage's fault, reporting whether the stage is answered
// (replied) and whether the connection is over (stop).
func (s *fakeSMTP) misbehave(stage string, tp *textproto.Conn) (replied, stop bool) {
	switch stage {
	case s.opts.DropAt:
		return false, true
	case s.opts.StallAt:
		<-s.done
		return false, true
	}
	if reply, ok := s.opts.Replies[stage]; ok {
		_ = tp.PrintfLine("%s", reply)
		code, _ := strconv.Atoi(reply[:3])
		return true, stage == "GREETING" || stage == "QUIT" || code == 421
	}
	return false, false
}

func (s *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	secure := s.opts.ImplicitTLS
	tp := textproto.NewConn(conn)

	if replied, stop := s.misbehave("GREETING", tp); stop || replied {
		return
	}
	_ = tp.PrintfLine("220 fake.test ESMTP")

	var msg received
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		stage := verb
		if verb == "HELO" {
			stage = "EHLO"
		}
		if replied, stop := s.misbehave(stage, tp); stop {
			return
		} else if replied {
			continue
		}

		switch verb {
		case "EHLO", "HELO":
			lines := []string{"fake.test greets you", "8BITMIME", "ENHANCEDSTATUSCODES"}
			if s.opts.StartTLS && !secure {
				lines = append(lines, "STARTTLS")
			}
			if s.opts.AuthMechs != "" && (secure || !s.opts.StartTLS) {
				lines = append(lines, "AUTH "+s.opts.AuthMechs)
			}
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				_ = tp.PrintfLine("250%s%s", sep, l)
			}
		case "STARTTLS":
			if !s.opts.StartTLS || secure {
				_ = tp.PrintfLine("502 5.5.1 not offered")
				continue
			}
			_ = tp.PrintfLine("220 2.0.0 go ahead")
			tlsConn := tls.Server(conn, s.tlsConf)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn, tp, secure = tlsConn, textproto.NewConn(tlsConn), true
			msg = received{}
		case "AUTH":
			if !s.authenticate(tp, arg) {
				return
			}
		case "MAIL":
			msg = received{From: addressIn(arg, "FROM:"), Secure: secure}
			_ = tp.PrintfLine("250 2.1.0 ok")
		case "RCPT":
			msg.To = append(msg.To, addressIn(arg, "TO:"))
			_ = tp.PrintfLine("250 2.1.5 ok")
		case "DATA":
			_ = tp.PrintfLine("354 go ahead")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			if replied, stop := s.misbehave("DOT", tp); stop {
				return
			} else if replied {
				continue
			}
			msg.Data = string(data)
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.mu.Unlock()
			msg = received{}
			_ = tp.PrintfLine("250 2.0.0 queued")
		case "RSET":
			msg = received{}
			_ = tp.PrintfLine("250 2.0.0 ok")
		case "NOOP":
			_ = tp.PrintfLine("250 2.0.0 ok")
		case "QUIT":
			_ = tp.PrintfLine("221 2.0.0 bye")
			return
		default:
			_ = tp.PrintfLine("502 5.5.2 unknown command")
		}
	}
}

// authenticate runs one AUTH exchange and reports whether the connection
// continues.
func (s *fakeSMTP) authenticate(tp *textproto.Conn, arg string) bool {
	mech, initial, _ := strings.Cut(arg, " ")
	var user, pass string
	switch strings.ToUpper(mech) {
	case "PLAIN":
		if initial == "" {
			_ = tp.PrintfLine("334 ")
			line, err := tp.ReadLine()
			if err != nil {
				return false
			}
			initial = line
		}
		raw, err := base64.StdEncoding.DecodeString(initial)
		if err != nil {
			_ = tp.PrintfLine("501 5.5.2 bad encoding")
			return true
		}
		parts := strings.Split(string(raw), "\x00")
		if len(parts) != 3 {
			_ = tp.PrintfLine("501 5.5.2 bad PLAIN")
			return true
		}
		user, pass = parts[1], parts[2]
	case "LOGIN":
		var ok bool
		if user, ok = loginStep(tp, "VXNlcm5hbWU6"); !ok {
			return false
		}
		if pass, ok = loginStep(tp, "UGFzc3dvcmQ6"); !ok {
			return false
		}
	default:
		_ = tp.PrintfLine("504 5.5.4 unrecognised mechanism")
		return true
	}
	if user != s.opts.User || pass != s.opts.Password {
		_ = tp.PrintfLine("535 5.7.8 authentication credentials invalid")
		return true
	}
	s.mu.Lock()
	s.auths = append(s.auths, strings.ToUpper(mech)+" "+user)
	s.mu.Unlock()
	_ = tp.PrintfLine("235 2.7.0 authenticated")
	return true
}

func loginStep(tp *textproto.Conn, challenge string) (string, bool) {
	_ = tp.PrintfLine("334 %s", challenge)
	line, err := tp.ReadLine()
	if err != nil {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	return string(raw), err == nil
}

// addressIn reads the path out of "FROM:<a@b> BODY=8BITMIME".
func addressIn(arg, prefix string) string {
	arg = strings.TrimSpace(arg)
	if len(arg) < len(prefix) || !strings.EqualFold(arg[:len(prefix)], prefix) {
		return ""
	}
	path, _, _ := strings.Cut(strings.TrimSpace(arg[len(prefix):]), " ")
	return strings.Trim(path, "<>")
}

// testCertificate makes a self-signed certificate for 127.0.0.1 and a pool
// that trusts it.
func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake smtp"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}, roots
}

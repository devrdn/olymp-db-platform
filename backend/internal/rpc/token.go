package rpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// The Query Runner runs whatever database, policy and disk quota a request
// names: the separation between participants is the Core API choosing those
// from game_instances. So the runner must answer only the Core API, and the
// proof it asks for is a shared secret (QUERY_RUNNER_TOKEN) on every call —
// the private network the two share is where the service is placed, not a
// reason to believe whoever reaches it.

// authorizationHeader and bearerScheme spell the token on the wire. Lower case
// because gRPC metadata keys are.
const (
	authorizationHeader = "authorization"
	bearerScheme        = "Bearer "
)

// errUnauthenticated is what a caller without the token is told. It names
// nothing about the token, the expected one or the one presented.
var errUnauthenticated = status.Error(codes.Unauthenticated, "the query runner requires its token")

// tokenGate checks the token before any handler work.
//
// An empty token turns the check off. Only a development configuration can
// produce one: config refuses to start either binary without a token outside
// development, and Serve logs a warning whenever it runs without one.
type tokenGate struct {
	// digest is SHA-256 of the full expected header value. Comparing digests
	// keeps the comparison constant-time in the length of the presented value
	// too, which subtle.ConstantTimeCompare on the raw strings does not.
	digest [sha256.Size]byte
	open   bool

	// refused counts every refused call since start. A refusal is either
	// somebody on the network without the token or a Core API holding the
	// wrong one, and an operator must hear about both; lastLogged (Unix
	// nanoseconds) keeps a flood of refusals from becoming a flood of lines.
	log         *slog.Logger
	logInterval time.Duration
	refused     atomic.Int64
	lastLogged  atomic.Int64
}

// refusalLogInterval is the least time between two refusal lines. A variable
// only so a test can shorten it.
var refusalLogInterval = 10 * time.Second

func newTokenGate(token string, log *slog.Logger) *tokenGate {
	gate := &tokenGate{log: log, logInterval: refusalLogInterval}
	if token == "" {
		gate.open = true
		return gate
	}
	gate.digest = sha256.Sum256([]byte(bearerScheme + token))
	return gate
}

// admit reports whether the incoming call carries exactly one authorization
// value equal to the expected one, and counts it when it does not.
func (g *tokenGate) admit(ctx context.Context) error {
	if g.open {
		return nil
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return g.refuse(ctx)
	}
	values := md.Get(authorizationHeader)
	if len(values) != 1 {
		return g.refuse(ctx)
	}
	presented := sha256.Sum256([]byte(values[0]))
	if subtle.ConstantTimeCompare(presented[:], g.digest[:]) != 1 {
		return g.refuse(ctx)
	}
	return nil
}

// refuse counts a refusal and logs it, at most once per interval, with the
// running total and the peer's address. Never with anything the caller
// presented: a wrong token may be a right one with a typo.
func (g *tokenGate) refuse(ctx context.Context) error {
	total := g.refused.Add(1)

	now := time.Now().UnixNano()
	last := g.lastLogged.Load()
	due := last == 0 || now-last >= int64(g.logInterval)
	if due && g.lastLogged.CompareAndSwap(last, now) {
		address := "unknown"
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			address = p.Addr.String()
		}
		g.log.Warn("query runner refused a call without a valid token",
			"peer", address, "refused_total", total)
	}
	return errUnauthenticated
}

// unary guards every unary call, the health check included.
func (g *tokenGate) unary(
	ctx context.Context,
	req any,
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	if err := g.admit(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// stream guards every streaming call. The only stream served today is the
// health service's Watch; guarding the chain rather than the method is what
// keeps a stream added later from arriving unauthenticated.
func (g *tokenGate) stream(
	srv any,
	ss grpc.ServerStream,
	_ *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	if err := g.admit(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

// bearerToken attaches the token to every call a client makes.
type bearerToken string

var _ credentials.PerRPCCredentials = bearerToken("")

func (t bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{authorizationHeader: bearerScheme + string(t)}, nil
}

// RequireTransportSecurity is false because the link has no TLS (see Dial):
// the token proves the caller, the private network carries it.
func (bearerToken) RequireTransportSecurity() bool { return false }

// withToken is the dial option that sends the token, or none when there is no
// token to send.
func withToken(token string) []grpc.DialOption {
	if token == "" {
		return nil
	}
	return []grpc.DialOption{grpc.WithPerRPCCredentials(bearerToken(token))}
}

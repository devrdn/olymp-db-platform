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
// names, so it must answer only the Core API. Every call carries a shared
// secret (QUERY_RUNNER_TOKEN); the private network alone is not trusted.

// authorizationHeader is lower case because gRPC metadata keys are.
const (
	authorizationHeader = "authorization"
	bearerScheme        = "Bearer "
)

// errUnauthenticated says nothing about the expected or presented token.
var errUnauthenticated = status.Error(codes.Unauthenticated, "the query runner requires its token")

// tokenGate checks the token before any handler work. An empty token turns
// the check off; config allows that only in development.
type tokenGate struct {
	// digest is SHA-256 of the expected header value. Comparing digests keeps
	// the comparison constant-time in the presented value's length too.
	digest [sha256.Size]byte
	open   bool

	// refused counts refusals since start; lastLogged (Unix nanoseconds)
	// rate-limits the warning so a flood of refusals is not a flood of lines.
	log         *slog.Logger
	logInterval time.Duration
	refused     atomic.Int64
	lastLogged  atomic.Int64
}

// refusalLogInterval is the least time between two refusal lines. A variable
// so a test can shorten it.
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

// admit accepts exactly one authorization value equal to the expected one.
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

// refuse counts a refusal and logs it at most once per interval. It never logs
// what the caller presented: a wrong token may be the right one with a typo.
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

// stream guards the stream chain, so a stream added later is guarded too.
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

type bearerToken string

var _ credentials.PerRPCCredentials = bearerToken("")

func (t bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{authorizationHeader: bearerScheme + string(t)}, nil
}

// RequireTransportSecurity is false: the link has no TLS (see Dial).
func (bearerToken) RequireTransportSecurity() bool { return false }

func withToken(token string) []grpc.DialOption {
	if token == "" {
		return nil
	}
	return []grpc.DialOption{grpc.WithPerRPCCredentials(bearerToken(token))}
}

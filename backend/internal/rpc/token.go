package rpc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
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
}

func newTokenGate(token string) tokenGate {
	if token == "" {
		return tokenGate{open: true}
	}
	return tokenGate{digest: sha256.Sum256([]byte(bearerScheme + token))}
}

// admit reports whether the incoming call carries exactly one authorization
// value equal to the expected one.
func (g tokenGate) admit(ctx context.Context) error {
	if g.open {
		return nil
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return errUnauthenticated
	}
	values := md.Get(authorizationHeader)
	if len(values) != 1 {
		return errUnauthenticated
	}
	presented := sha256.Sum256([]byte(values[0]))
	if subtle.ConstantTimeCompare(presented[:], g.digest[:]) != 1 {
		return errUnauthenticated
	}
	return nil
}

// unary guards every unary call, the health check included.
func (g tokenGate) unary(
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
func (g tokenGate) stream(
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

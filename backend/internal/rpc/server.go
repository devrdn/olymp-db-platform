package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ServiceName is what the health service reports on and a probe asks about.
var ServiceName = pb.QueryRunner_ServiceDesc.ServiceName

// Probe reports whether a Query Runner at this address is serving. The
// container health check runs it, since the image has no shell. It presents
// the token: the health service has no exemption from the check.
func Probe(ctx context.Context, address, token string) error {
	options := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, withToken(token)...)
	conn, err := grpc.NewClient(address, options...)
	if err != nil {
		return fmt.Errorf("dial %s: %w", address, err)
	}
	defer func() { _ = conn.Close() }()

	response, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: ServiceName})
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	if response.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("health check: status is %s", response.GetStatus())
	}
	return nil
}

// ProbeAddress turns a listen address into one that can be dialled: ":9100"
// means every interface when listening and nothing when connecting.
func ProbeAddress(listen string) string {
	if listen == "" {
		listen = "127.0.0.1:9100"
	}
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
}

// MaxPayloadBytes is the largest message either side will carry. gRPC's 4 MiB
// default is below the runner's result budget, which would turn a valid answer
// into ResourceExhausted. It is larger than the budget because a message also
// carries column names and framing; the command that wires the service refuses
// a budget that does not fit.
const MaxPayloadBytes = 16 << 20

// Server serves the Query Runner contract over gRPC.
type Server struct {
	pb.UnimplementedQueryRunnerServer

	runner queryrunner.Executor
	limits queryrunner.Limits
	log    *slog.Logger
}

// NewServer adapts an executor to the service. It takes the limits because
// only this layer knows the rendered size; the runner's count over Go values
// is a floor, not a bound.
func NewServer(runner queryrunner.Executor, limits queryrunner.Limits, log *slog.Logger) *Server {
	return &Server{runner: runner, limits: limits, log: log}
}

// Register attaches the service to a gRPC server.
func (s *Server) Register(server *grpc.Server) { pb.RegisterQueryRunnerServer(server, s) }

// Run executes one participant's query. Refusals, a busy instance and
// timeouts are answers in the response; a gRPC error means a malformed request
// or that the service could not answer.
func (s *Server) Run(ctx context.Context, req *pb.RunRequest) (*pb.RunResponse, error) {
	registration, err := uuid.Parse(req.GetRegistration())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "registration is not a uuid")
	}
	// Platform-generated names only, but checked here anyway: it goes into a
	// connection string.
	if !sqlpolicy.PlainIdentifier(req.GetDatabase()) {
		return nil, status.Errorf(codes.InvalidArgument, "database is not a plain identifier")
	}
	if req.GetDiskQuotaBytes() < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "disk_quota_bytes cannot be negative")
	}

	result, runErr := s.runner.Run(ctx, queryrunner.Request{
		Registration:   registration,
		Database:       req.GetDatabase(),
		SQL:            req.GetSql(),
		Policy:         policyFrom(req.GetPolicy()),
		DiskQuotaBytes: req.GetDiskQuotaBytes(),
	})
	if runErr != nil {
		return &pb.RunResponse{
			Outcome: &pb.RunResponse_Failure{Failure: failureFor(runErr)},
		}, nil
	}

	answer := &pb.Result{
		Columns:     result.Columns,
		ColumnTypes: result.ColumnTypes,
		Truncated:   ptr(result.Truncated),
		// Microseconds: milliseconds would send a fast query as zero.
		DurationMicros: ptr(result.Duration.Microseconds()),
		RowsAffected:   ptr(result.RowsAffected),
		Rows:           make([]*pb.Row, 0, len(result.Rows)),
	}
	var spent int
	for _, values := range result.Rows {
		row := cellsFor(values)
		// Measured after rendering, checked before appending: the budget is a
		// ceiling the last row may not cross.
		if spent += weigh(row); spent > s.limits.MaxBytes {
			answer.Truncated = ptr(true)
			break
		}
		answer.Rows = append(answer.Rows, row)
	}
	return &pb.RunResponse{Outcome: &pb.RunResponse_Result{Result: answer}}, nil
}

// Serve runs the service and the gRPC health service on lis until the context
// is cancelled. Every call must carry QUERY_RUNNER_TOKEN, checked first in both
// interceptor chains. An empty token serves without authentication, which
// configuration allows only in development, and logs a warning.
func Serve(ctx context.Context, lis net.Listener, server *Server, token string, shutdown time.Duration, log *slog.Logger) error {
	gate := newTokenGate(token, log)
	if gate.open {
		log.Warn("QUERY_RUNNER_TOKEN is not set: the query runner answers any caller that reaches it. " +
			"Acceptable only on a development machine listening on loopback.")
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(MaxPayloadBytes),
		grpc.MaxSendMsgSize(MaxPayloadBytes),
		grpc.ChainUnaryInterceptor(gate.unary, correlate),
		grpc.ChainStreamInterceptor(gate.stream),
	)
	server.Register(grpcServer)

	healthy := health.NewServer()
	healthy.SetServingStatus(ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthy)

	go func() {
		<-ctx.Done()

		// Graceful first, so in-flight queries finish, but GracefulStop has
		// no limit: the shutdown timeout bounds it.
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()

		select {
		case <-stopped:
		case <-time.After(shutdown):
			log.Warn("shutting down without waiting further", "after", shutdown)
			grpcServer.Stop()
		}
	}()

	log.Info("query runner listening", "address", lis.Addr().String())
	if err := grpcServer.Serve(lis); err != nil {
		return err
	}
	return nil
}

// correlate carries the caller's request identifier into this process's
// context so the two services' logs can be joined. It only labels; nothing
// trusts it.
func correlate(
	ctx context.Context,
	req any,
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if ids := md.Get(requestIDHeader); len(ids) > 0 && ids[0] != "" {
			ctx = logging.WithRequestID(ctx, ids[0])
		}
	}
	// Test seam: the service logs nothing per query to assert on.
	if carried != nil {
		carried(ctx)
	}
	return handler(ctx, req)
}

// carried is set only by tests.
var carried func(context.Context)

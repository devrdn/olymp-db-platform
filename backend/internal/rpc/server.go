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

// ServiceName is what the health service reports on, and what a probe asks
// about. The generated constant, so it cannot drift from the contract.
var ServiceName = pb.QueryRunner_ServiceDesc.ServiceName

// Probe reports whether a Query Runner at this address is serving.
//
// Used by the container health check, which runs this binary with a flag
// rather than a shell: the runtime image has neither a shell nor grpc_health_probe.
//
// It presents the token like any other caller. The health service sits behind
// the same check as the Query Runner itself rather than on an exemption list,
// and the health check runs inside the runner's own container, which holds the
// token already.
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

// ProbeAddress turns a listen address into one that can be dialled.
//
// A listen address is often just a port (":9100"), which means "every
// interface" when listening and nothing at all when connecting.
func ProbeAddress(listen string) string {
	if listen == "" {
		listen = "127.0.0.1:9100"
	}
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
}

// MaxPayloadBytes is the largest message either side will carry.
//
// Set here rather than left at gRPC's own default, which is 4 MiB on the
// receiving side and was smaller than the runner's own 5 MiB result budget: a
// large but perfectly valid answer came back as ResourceExhausted, which the
// client reported as the service being unable to answer and the journal
// recorded as an error. A big answer looked like an outage.
//
// Generous, and deliberately not equal to the result budget: the budget bounds
// the cells, while a message also carries column names and framing. The
// command that wires the service refuses a configured budget that would not
// fit inside this, so the two cannot be set into conflict again.
const MaxPayloadBytes = 16 << 20

// Server serves the Query Runner contract over gRPC.
type Server struct {
	pb.UnimplementedQueryRunnerServer

	runner queryrunner.Executor
	limits queryrunner.Limits
	log    *slog.Logger
}

// NewServer adapts an executor to the service.
//
// It takes the limits as well as the runner because the byte budget can only
// be applied honestly here: the runner counts the Go values it read, and what
// crosses the wire is those values rendered as text — a number counted as
// eight bytes can render as twenty characters, so the estimate is a floor and
// not a bound. This is the layer that knows the real size.
func NewServer(runner queryrunner.Executor, limits queryrunner.Limits, log *slog.Logger) *Server {
	return &Server{runner: runner, limits: limits, log: log}
}

// Register attaches the service to a gRPC server.
func (s *Server) Register(server *grpc.Server) { pb.RegisterQueryRunnerServer(server, s) }

// Run executes one participant's query.
//
// Almost nothing returns a gRPC error. A refused query, a busy instance and a
// query that ran too long are answers, and answers belong in the response —
// what an error status means here is that this service could not answer at
// all, which is what lets a caller tell "your query was refused" from "the
// query service is down".
func (s *Server) Run(ctx context.Context, req *pb.RunRequest) (*pb.RunResponse, error) {
	registration, err := uuid.Parse(req.GetRegistration())
	if err != nil {
		// A malformed identifier is the caller's mistake, not an outcome of
		// running anything, so it is an error status rather than a Failure.
		return nil, status.Errorf(codes.InvalidArgument, "registration is not a uuid")
	}
	// A database name is the caller's, taken from game_instances, and every
	// name that table holds is one the platform generated. Checked anyway, at
	// the door: it goes into a connection string, and the runner would refuse
	// it too, but "invalid argument" here names the caller's mistake where a
	// failure inside the response would name the database's.
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
		// Microseconds, which is the contract's unit: the console rounds to
		// milliseconds and a sub-millisecond query rounded here would cross as
		// a zero.
		DurationMicros: ptr(result.Duration.Microseconds()),
		RowsAffected:   ptr(result.RowsAffected),
		Rows:           make([]*pb.Row, 0, len(result.Rows)),
	}
	var spent int
	for _, values := range result.Rows {
		row := cellsFor(values)
		// Measured after rendering, because rendering is where the size
		// becomes real. Checked before appending, so the budget is a ceiling
		// rather than a threshold the last row may cross.
		if spent += weigh(row); spent > s.limits.MaxBytes {
			answer.Truncated = ptr(true)
			break
		}
		answer.Rows = append(answer.Rows, row)
	}
	return &pb.RunResponse{Outcome: &pb.RunResponse_Result{Result: answer}}, nil
}

// Serve runs the service on lis until the context is cancelled.
//
// The standard gRPC health service is registered alongside it, because the
// runtime image is distroless: it carries no shell and no probe, so the
// container's health check is the binary dialling itself (see Probe).
//
// Every call, on every service, must carry token (QUERY_RUNNER_TOKEN); the
// check runs first in both interceptor chains, before any handler work. An
// empty token serves without authentication, which configuration allows only
// in development, and says so in the log.
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

		// Graceful first: a query in flight is a participant waiting, and a
		// few seconds of shutdown is cheaper than an answer thrown away. But
		// GracefulStop waits without limit, so a call that never returns would
		// hold the deploy open indefinitely — which is what SHUTDOWN_TIMEOUT
		// is for, and what it was not doing.
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
// context, so that every line this service logs about a call joins the line
// the Core API logged about the same one.
//
// A separate service whose logs cannot be joined to the requests that caused
// them is a separate service nobody can debug. The identifier is the caller's
// and is not trusted for anything: it decides nothing, it only labels.
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
	// A seam for the test that proves the identifier arrived: the service
	// deliberately logs nothing per query, so there is no line to look for.
	if carried != nil {
		carried(ctx)
	}
	return handler(ctx, req)
}

// carried is set only by tests.
var carried func(context.Context)

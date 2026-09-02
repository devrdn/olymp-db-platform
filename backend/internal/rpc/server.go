package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// ServiceName is what the health service reports on, and what a probe asks
// about. The generated constant, so it cannot drift from the contract.
var ServiceName = pb.QueryRunner_ServiceDesc.ServiceName

// Probe reports whether a Query Runner at this address is serving.
//
// Used by the container health check, which runs this binary with a flag
// rather than a shell: the runtime image has neither a shell nor grpc_health_probe.
func Probe(ctx context.Context, address string) error {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
		listen = ":9100"
	}
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
}

// Server serves the Query Runner contract over gRPC.
type Server struct {
	pb.UnimplementedQueryRunnerServer

	runner queryrunner.Executor
	log    *slog.Logger
}

// NewServer adapts an executor to the service.
func NewServer(runner queryrunner.Executor, log *slog.Logger) *Server {
	return &Server{runner: runner, log: log}
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
	if req.GetDatabase() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "database is required")
	}

	result, runErr := s.runner.Run(ctx, queryrunner.Request{
		Registration: registration,
		Database:     req.GetDatabase(),
		SQL:          req.GetSql(),
		Policy:       policyFrom(req.GetPolicy()),
	})
	if runErr != nil {
		return &pb.RunResponse{
			Outcome: &pb.RunResponse_Failure{Failure: failureFor(runErr)},
		}, nil
	}

	answer := &pb.Result{
		Columns:   result.Columns,
		Truncated: ptr(result.Truncated),
		Rows:      make([]*pb.Row, 0, len(result.Rows)),
	}
	for _, values := range result.Rows {
		answer.Rows = append(answer.Rows, cellsFor(values))
	}
	return &pb.RunResponse{Outcome: &pb.RunResponse_Result{Result: answer}}, nil
}

// Serve runs the service on lis until the context is cancelled.
//
// The standard gRPC health service is registered alongside it, because the
// runtime image is distroless: it carries no shell and no probe, so the
// container's health check is the binary dialling itself (see Probe).
func Serve(ctx context.Context, lis net.Listener, server *Server, log *slog.Logger) error {
	grpcServer := grpc.NewServer()
	server.Register(grpcServer)

	healthy := health.NewServer()
	healthy.SetServingStatus(ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthy)

	go func() {
		<-ctx.Done()
		// Graceful: a query in flight is a participant waiting, and five
		// seconds of shutdown is cheaper than an answer thrown away.
		grpcServer.GracefulStop()
	}()

	log.Info("query runner listening", "address", lis.Addr().String())
	if err := grpcServer.Serve(lis); err != nil {
		return err
	}
	return nil
}

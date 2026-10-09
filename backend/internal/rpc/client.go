package rpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// ErrUnreachable is the Query Runner failing to answer at all, as opposed to
// answering that the query was refused. That distinction is why a Failure
// travels in the response rather than as a gRPC status.
var ErrUnreachable = errors.New("the query service could not answer")

// requestIDHeader carries the correlation identifier. Lower case because gRPC
// metadata keys are.
const requestIDHeader = "x-request-id"

// Client calls the Query Runner service. It satisfies queryrunner.Executor, so
// a remote or in-process runner is a choice made in the composition root.
type Client struct {
	conn    *grpc.ClientConn
	service pb.QueryRunnerClient
}

var _ queryrunner.Executor = (*Client)(nil)

// Dial connects to the Query Runner.
//
// No TLS: the link never leaves the private compose network and the runner's
// port is not published. If the two are ever split across hosts, this must
// change. Every call still carries QUERY_RUNNER_TOKEN, because the runner obeys
// whatever database and policy a request names. An empty token sends none,
// which only a development runner accepts.
func Dial(address, token string) (*Client, error) {
	options := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// gRPC's default 4 MiB receive limit is below the result budget.
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxPayloadBytes),
			grpc.MaxCallSendMsgSize(MaxPayloadBytes),
		),
	}, withToken(token)...)
	conn, err := grpc.NewClient(address, options...)
	if err != nil {
		return nil, fmt.Errorf("connecting to the query runner: %w", err)
	}
	return &Client{conn: conn, service: pb.NewQueryRunnerClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Run asks the service to execute one query. A gRPC error is returned wrapped
// in ErrUnreachable; a Failure in the response becomes the same Go error a local
// runner would have produced.
func (c *Client) Run(ctx context.Context, req queryrunner.Request) (*queryrunner.Result, error) {
	// Metadata, not a contract field: it only joins the two processes' logs.
	if id := logging.RequestIDFrom(ctx); id != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, requestIDHeader, id)
	}

	response, err := c.service.Run(ctx, &pb.RunRequest{
		Registration:   ptr(req.Registration.String()),
		Database:       ptr(req.Database),
		Sql:            ptr(req.SQL),
		Policy:         policyProto(req.Policy),
		DiskQuotaBytes: ptr(req.DiskQuotaBytes),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}

	if failure := response.GetFailure(); failure != nil {
		return nil, errorFor(failure)
	}

	answer := response.GetResult()
	if answer == nil {
		// A runner this build does not understand. An empty result would
		// look like a query with no rows.
		return nil, fmt.Errorf("the query service answered with neither a result nor a failure")
	}
	result := &queryrunner.Result{
		Columns:     answer.GetColumns(),
		ColumnTypes: answer.GetColumnTypes(),
		Truncated:   answer.GetTruncated(),
		// The contract carries microseconds. A runner that sends none leaves
		// zero, which the console shows as "not measured".
		Duration:     time.Duration(answer.GetDurationMicros()) * time.Microsecond,
		RowsAffected: answer.GetRowsAffected(),
		Rows:         make([][]any, 0, len(answer.GetRows())),
	}
	// All rows share one backing array; each row is capped at its own length,
	// so appending to one can never write into the next.
	width := 0
	for _, row := range answer.GetRows() {
		width += len(row.GetCells())
	}
	values := make([]any, 0, width)
	for _, row := range answer.GetRows() {
		// Rendered text, nil for NULL: typed values stay with the runner,
		// which had the connection's type map.
		start := len(values)
		for _, cell := range row.GetCells() {
			if cell.GetIsNull() {
				values = append(values, nil)
				continue
			}
			values = append(values, cell.GetText())
		}
		result.Rows = append(result.Rows, values[start:len(values):len(values)])
	}
	return result, nil
}

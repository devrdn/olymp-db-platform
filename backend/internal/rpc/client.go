package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ErrUnreachable is the Query Runner failing to answer at all, as opposed to
// answering that the query was refused.
//
// The distinction is the whole reason a Failure travels in the response rather
// than as a gRPC status: without it, a service that is down and a query that
// was refused reach the participant as the same sentence.
var ErrUnreachable = errors.New("the query service could not answer")

// Client calls the Query Runner service.
//
// It satisfies queryrunner.Executor, which is the whole point: the Core API
// journals and calls the same shape whether the runner is across a socket or
// in this process, so which one it is becomes a line in the composition root.
type Client struct {
	conn    *grpc.ClientConn
	service pb.QueryRunnerClient
}

var _ queryrunner.Executor = (*Client)(nil)

// Dial connects to the Query Runner.
//
// Without transport credentials, deliberately: this call never leaves the
// private compose network, whose only published port belongs to the proxy, and
// the runner's own port is not published at all. TLS here would be encrypting
// a link between two containers against an attacker who, to be on it, would
// already be inside the network — while adding certificates to rotate. If the
// two are ever split across hosts, that reasoning stops holding and this is
// the line that has to change.
func Dial(address string) (*Client, error) {
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// gRPC's own default receive limit is 4 MiB, which is smaller than the
		// result budget the runner enforces. Left alone, a large but valid
		// answer arrives as ResourceExhausted.
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxPayloadBytes),
			grpc.MaxCallSendMsgSize(MaxPayloadBytes),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to the query runner: %w", err)
	}
	return &Client{conn: conn, service: pb.NewQueryRunnerClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Run asks the service to execute one query.
//
// A gRPC error is the service failing to answer and is returned as it is; a
// Failure in the response is the answer, and becomes the same Go error a local
// runner would have produced.
func (c *Client) Run(ctx context.Context, req queryrunner.Request) (*queryrunner.Result, error) {
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
		// Neither an answer nor a failure: a runner this build does not
		// understand. Saying nothing was returned beats an empty result that
		// looks like a query with no rows.
		return nil, fmt.Errorf("the query service answered with neither a result nor a failure")
	}
	result := &queryrunner.Result{
		Columns:      answer.GetColumns(),
		Truncated:    answer.GetTruncated(),
		RowsAffected: answer.GetRowsAffected(),
		Rows:         make([][]any, 0, len(answer.GetRows())),
	}
	for _, row := range answer.GetRows() {
		// Rendered text, and nil where the column was NULL. The console shows
		// what the database printed; the typed value stayed in the process
		// that read it, which is the one that had the connection's type map.
		values := make([]any, 0, len(row.GetCells()))
		for _, cell := range row.GetCells() {
			if cell.GetIsNull() {
				values = append(values, nil)
				continue
			}
			values = append(values, cell.GetText())
		}
		result.Rows = append(result.Rows, values)
	}
	return result, nil
}

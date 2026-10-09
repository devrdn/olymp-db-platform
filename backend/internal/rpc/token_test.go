package rpc

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// These tests use a fake executor: what is under test is the token check, and
// a test that needs a game cluster would skip where it matters.

const (
	theToken   = "a-query-runner-token-of-at-least-32-bytes"
	wrongToken = "not-the-query-runner-token-but-just-as-long"
)

// countingExecutor records whether a request got past the token check.
type countingExecutor struct{ calls atomic.Int32 }

func (e *countingExecutor) Run(context.Context, queryrunner.Request) (*queryrunner.Result, error) {
	e.calls.Add(1)
	return &queryrunner.Result{Columns: []string{"one"}, Rows: [][]any{{int64(1)}}}, nil
}

func behindTheDoor(t *testing.T, token string) (string, *countingExecutor, *syncBuffer) {
	t.Helper()

	executor := &countingExecutor{}
	logged := &syncBuffer{}
	log := logging.New("info", logged)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve(ctx, lis, NewServer(executor, queryrunner.DefaultLimits(), log), token, 5*time.Second, log)
	}()
	t.Cleanup(func() {
		stop()
		<-done
	})
	return lis.Addr().String(), executor, logged
}

func dialWith(t *testing.T, address, token string) *Client {
	t.Helper()
	client, err := Dial(address, token)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func aQuery() queryrunner.Request {
	return queryrunner.Request{
		Registration: uuid.New(),
		Database:     "game_inst_1",
		SQL:          "SELECT 1",
		Policy:       sqlpolicy.ReadOnly(),
	}
}

// assertRefused checks the call was refused, nothing ran, and no token leaked
// into the error.
func assertRefused(t *testing.T, err error, executor *countingExecutor) {
	t.Helper()
	if err == nil {
		t.Fatal("the call was answered without the right token")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("status = %s (%v), want Unauthenticated", got, err)
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("a refused call is %v; the Core API should see it as the service not answering", err)
	}
	if executor != nil && executor.calls.Load() != 0 {
		t.Fatalf("the executor ran %d times behind a refused call", executor.calls.Load())
	}
	for _, secret := range []string{theToken, wrongToken} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the error carries a token: %q", err.Error())
		}
	}
}

func TestARunWithoutTheTokenIsRefusedBeforeAnythingRuns(t *testing.T) {
	address, executor, _ := behindTheDoor(t, theToken)

	_, err := dialWith(t, address, "").Run(t.Context(), aQuery())

	assertRefused(t, err, executor)
}

func TestARunWithTheWrongTokenIsRefusedBeforeAnythingRuns(t *testing.T) {
	address, executor, logged := behindTheDoor(t, theToken)

	_, err := dialWith(t, address, wrongToken).Run(t.Context(), aQuery())

	assertRefused(t, err, executor)
	if strings.Contains(logged.String(), theToken) || strings.Contains(logged.String(), wrongToken) {
		t.Fatalf("the server logged a token: %q", logged.String())
	}
}

func TestAPrefixOfTheTokenIsRefused(t *testing.T) {
	address, executor, _ := behindTheDoor(t, theToken)

	_, err := dialWith(t, address, theToken[:len(theToken)-1]).Run(t.Context(), aQuery())

	assertRefused(t, err, executor)
}

func TestAMalformedAuthorizationIsRefused(t *testing.T) {
	address, executor, _ := behindTheDoor(t, theToken)

	for name, values := range map[string][]string{
		"bare token":   {theToken},
		"other scheme": {"Basic " + theToken},
		"two values":   {"Bearer " + wrongToken, "Bearer " + theToken},
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatalf("dialling: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			pairs := make([]string, 0, 2*len(values))
			for _, v := range values {
				pairs = append(pairs, authorizationHeader, v)
			}
			ctx := metadata.AppendToOutgoingContext(t.Context(), pairs...)
			_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: ServiceName})
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("status = %s (%v), want Unauthenticated", status.Code(err), err)
			}
		})
	}
	if executor.calls.Load() != 0 {
		t.Fatal("the executor ran behind a refused call")
	}
}

func TestARunWithTheTokenIsAnswered(t *testing.T) {
	address, executor, _ := behindTheDoor(t, theToken)

	result, err := dialWith(t, address, theToken).Run(t.Context(), aQuery())
	if err != nil {
		t.Fatalf("running with the right token: %v", err)
	}
	if executor.calls.Load() != 1 || len(result.Rows) != 1 {
		t.Fatalf("calls = %d, rows = %d", executor.calls.Load(), len(result.Rows))
	}
}

// No exemption list for health: it would be one more thing that can drift.
func TestTheHealthCheckCarriesTheTokenAndIsRefusedWithoutIt(t *testing.T) {
	address, _, _ := behindTheDoor(t, theToken)

	if err := Probe(t.Context(), address, theToken); err != nil {
		t.Fatalf("the probe with the token failed: %v", err)
	}

	err := Probe(t.Context(), address, "")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a probe without the token = %v, want Unauthenticated", err)
	}
	err = Probe(t.Context(), address, wrongToken)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("a probe with the wrong token = %v, want Unauthenticated", err)
	}
	if strings.Contains(err.Error(), wrongToken) || strings.Contains(err.Error(), theToken) {
		t.Fatalf("the probe's error carries a token: %q", err.Error())
	}
}

// Streams pass through a separate interceptor chain from unary calls.
func TestAStreamWithoutTheTokenIsRefused(t *testing.T) {
	address, _, _ := behindTheDoor(t, theToken)

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	stream, err := healthpb.NewHealthClient(conn).Watch(t.Context(), &healthpb.HealthCheckRequest{Service: ServiceName})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("an unauthenticated stream = %v, want Unauthenticated", err)
	}
}

// A deployment that lost its token must not pass for a working one in the logs.
func TestWithoutATokenTheServerAnswersAndSaysSoLoudly(t *testing.T) {
	address, executor, logged := behindTheDoor(t, "")

	if _, err := dialWith(t, address, "").Run(t.Context(), aQuery()); err != nil {
		t.Fatalf("an unauthenticated development server refused a call: %v", err)
	}
	if executor.calls.Load() != 1 {
		t.Fatalf("calls = %d", executor.calls.Load())
	}
	if out := logged.String(); !strings.Contains(out, "WARN") || !strings.Contains(out, "QUERY_RUNNER_TOKEN") {
		t.Fatalf("no warning naming QUERY_RUNNER_TOKEN was logged: %q", out)
	}
}

func TestWithATokenTheServerDoesNotWarn(t *testing.T) {
	_, _, logged := behindTheDoor(t, theToken)

	// Wait for Serve's startup log line.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logged.String(), "listening") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(logged.String(), "WARN") {
		t.Fatalf("a server with a token warned: %q", logged.String())
	}
}

// syncBuffer is a log destination tests can read while the server writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestARefusedCallIsCountedAndLoggedWithoutTheToken(t *testing.T) {
	address, _, logged := behindTheDoor(t, theToken)
	client := dialWith(t, address, wrongToken)

	for range 3 {
		if _, err := client.Run(t.Context(), aQuery()); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("status = %v, want Unauthenticated", err)
		}
	}

	out := logged.String()
	if strings.Count(out, "refused a call without a valid token") != 1 {
		t.Fatalf("want exactly one refusal line within the interval, got: %q", out)
	}
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"refused_total":1`) {
		t.Fatalf("the refusal line is not a WARN carrying the count: %q", out)
	}
	if strings.Contains(out, theToken) || strings.Contains(out, wrongToken) {
		t.Fatalf("the log carries a token: %q", out)
	}
}

func TestTheRefusalCountKeepsRunningBetweenLogLines(t *testing.T) {
	previous := refusalLogInterval
	refusalLogInterval = 0
	t.Cleanup(func() { refusalLogInterval = previous })

	address, _, logged := behindTheDoor(t, theToken)
	client := dialWith(t, address, "")
	for range 2 {
		_, _ = client.Run(t.Context(), aQuery())
	}

	if out := logged.String(); !strings.Contains(out, `"refused_total":2`) {
		t.Fatalf("the second refusal did not report a running total of 2: %q", out)
	}
}

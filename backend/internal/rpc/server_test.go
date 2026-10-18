package rpc

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
	"github.com/google/uuid"
)

// answering is an Executor that returns a fixed result.
type answering struct{ result *queryrunner.Result }

func (a answering) Run(context.Context, queryrunner.Request) (*queryrunner.Result, error) {
	return a.result, nil
}

// cuts records the limits a server reported.
type cuts []string

func (c *cuts) CutOff(limit string) { *c = append(*c, limit) }

func runThrough(t *testing.T, result *queryrunner.Result, maxBytes int) (*pb.Result, cuts) {
	t.Helper()

	limits := queryrunner.DefaultLimits()
	limits.MaxBytes = maxBytes
	var reported cuts
	server := NewServer(answering{result}, limits, slog.New(slog.NewTextHandler(io.Discard, nil))).WithCutOffs(&reported)

	response, err := server.Run(t.Context(), &pb.RunRequest{
		Registration: ptr(uuid.NewString()),
		Database:     ptr("game"),
		Sql:          ptr("SELECT payload FROM evidence"),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return response.GetResult(), reported
}

// bytea renders as \x and two hex digits a byte, so an answer the runner's
// estimate let through can still be cut by its rendering, and that cut counts.
func TestARenderedAnswerCutToTheBudgetIsReported(t *testing.T) {
	rows := make([][]any, 10)
	for i := range rows {
		rows[i] = []any{make([]byte, 100)}
	}

	answer, reported := runThrough(t, &queryrunner.Result{Columns: []string{"payload"}, Rows: rows}, 1500)

	if !answer.GetTruncated() || len(answer.GetRows()) >= len(rows) {
		t.Fatalf("truncated = %v with %d rows; the rendered budget cut nothing", answer.GetTruncated(), len(answer.GetRows()))
	}
	if len(reported) != 1 || reported[0] != queryrunner.CutOffRendered {
		t.Fatalf("reported cut-offs = %v, want [%s]", reported, queryrunner.CutOffRendered)
	}
}

func TestAnAnswerInsideTheRenderedBudgetReportsNoCut(t *testing.T) {
	answer, reported := runThrough(t, &queryrunner.Result{Columns: []string{"a"}, Rows: [][]any{{"one"}, {"two"}}}, 1500)

	if answer.GetTruncated() || len(answer.GetRows()) != 2 {
		t.Fatalf("truncated = %v with %d rows, want a whole answer", answer.GetTruncated(), len(answer.GetRows()))
	}
	if len(reported) != 0 {
		t.Fatalf("a whole answer reported cut-offs %v", reported)
	}
}

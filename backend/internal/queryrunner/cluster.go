package queryrunner

import (
	"context"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// Cluster opens one connection to one participant's database.
//
// Short-lived by design (section 4.3): a connection is opened for an execution
// and closed after it, so nothing is shared between two queries — no temporary
// table, no session setting, no open transaction. In a local network the cost
// is milliseconds, which at thirty queries a minute is not worth trading for
// state that would have to be reasoned about.
//
// It is also what makes the deadline enforceable. Closing the connection is
// what ends a query the server is still running, and a pooled connection is
// one you have to hand back rather than close.
type Cluster struct{ base *url.URL }

// NewCluster reads the base connection string, whose credentials are the
// participant role's and whose database name is replaced per request.
func NewCluster(dsn string) (*Cluster, error) {
	base, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("the game cluster DSN is not a URL: %w", err)
	}
	if base.User == nil {
		return nil, fmt.Errorf("the game cluster DSN carries no credentials")
	}
	return &Cluster{base: base}, nil
}

// connect opens a connection to one database.
//
// The name comes from the caller, which took it from game_instances — never
// from the participant. That is the first line of section 5: the query is the
// participant's, the address is not.
func (c *Cluster) connect(ctx context.Context, database string) (*pgx.Conn, error) {
	target := *c.base
	target.Path = "/" + database

	conn, err := pgx.Connect(ctx, target.String())
	if err != nil {
		return nil, fmt.Errorf("connecting to the game database: %w", err)
	}
	return conn, nil
}

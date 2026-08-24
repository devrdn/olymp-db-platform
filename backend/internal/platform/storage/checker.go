// Package storage wires the Core API to PostgreSQL and Redis and exposes them
// as readiness probes.
package storage

import (
	"context"
	"fmt"
)

// Pinger is the minimal contract shared by the database pool and the cache
// client. Depending on this instead of the concrete types keeps the readiness
// probe testable without a live server.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Checker adapts a Pinger to the health.Checker interface.
type Checker struct {
	name   string
	pinger Pinger
}

// NewChecker returns a readiness probe for the given dependency.
func NewChecker(name string, pinger Pinger) Checker {
	return Checker{name: name, pinger: pinger}
}

// Name identifies the dependency in the readiness response.
func (c Checker) Name() string { return c.name }

// Check pings the dependency using the caller's context, so the readiness
// timeout applies to the underlying network call.
func (c Checker) Check(ctx context.Context) error {
	if err := c.pinger.Ping(ctx); err != nil {
		return fmt.Errorf("ping %s: %w", c.name, err)
	}
	return nil
}

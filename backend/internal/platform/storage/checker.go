// Package storage opens the core PostgreSQL pool, runs transactions through
// UnitOfWork, and adapts the database and cache to readiness probes.
// It holds no queries: repositories live in internal/postgres.
package storage

import (
	"context"
	"fmt"
)

// Pinger is satisfied by both the database pool and the cache client.
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

// Check pings the dependency with the caller's context, so the readiness
// timeout bounds the network call.
func (c Checker) Check(ctx context.Context) error {
	if err := c.pinger.Ping(ctx); err != nil {
		return fmt.Errorf("ping %s: %w", c.name, err)
	}
	return nil
}

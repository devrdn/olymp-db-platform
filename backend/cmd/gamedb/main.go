// Command gamedb prepares the game cluster: the two participant roles, their
// session defaults, and the databases they may not reach.
//
// A one-shot job, run before the Query Runner starts, the way `migrate` runs
// before the API. It is a program rather than an init script in the PostgreSQL
// image for one reason: an init script runs once, when the data directory is
// created, and is silently skipped ever after — so a restriction added in a
// later release would never reach a cluster that already exists, and nothing
// would say so. This is idempotent and runs on every deploy.
//
// It ships in the Query Runner's image because it reaches the same sensitive
// catalog list the validator uses, which links PostgreSQL's parser and so
// needs cgo. One list, two layers, as section 4.1 requires.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// The provisioning role, not the participant's: this creates roles and
	// revokes privileges, neither of which game_reader can do.
	dsn, err := required("GAME_DB_ADMIN_DSN")
	if err != nil {
		return err
	}
	roles := gamedb.Roles{}
	if roles.ReaderPassword, err = required("GAME_READER_PASSWORD"); err != nil {
		return err
	}
	if roles.WriterPassword, err = required("GAME_WRITER_PASSWORD"); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open the game cluster: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("reach the game cluster: %w", err)
	}
	if err := gamedb.PrepareCluster(ctx, pool, roles); err != nil {
		return err
	}

	fmt.Println("game cluster prepared: roles, session defaults and connect privileges are as declared")
	return nil
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

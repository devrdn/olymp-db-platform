// Command gamedb prepares the game cluster: the two participant roles, the
// role an organiser's game script runs as, their session defaults, and the
// databases they may not reach.
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
	"net/url"
	"os"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/jackc/pgx/v5"
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
	// The third role: the one an organiser's game script runs as. Created
	// here with the other two because this is the only place any of them is
	// defined, so a cluster that already exists picks it up on the next
	// deploy rather than needing a hand-written CREATE ROLE.
	if roles.AuthorPassword, err = required("GAME_AUTHOR_PASSWORD"); err != nil {
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
	if err := hardenTheDefault(ctx, dsn); err != nil {
		return err
	}

	fmt.Println("game cluster prepared: roles, session defaults, connect privileges " +
		"and the catalogue revocations every new database inherits")
	return nil
}

// hardenTheDefault applies the catalogue revocations to template1.
//
// Every database created without an explicit TEMPLATE comes from template1 and
// copies its catalogue, ACLs included. Hardening it once means an instance
// nobody remembered to harden is hardened anyway — which matters because the
// alternative fails silently: an unhardened instance looks exactly like the
// others, and only a participant would ever find out.
//
// The connection is closed before this returns, because CREATE DATABASE
// refuses while anything is connected to its source. That is also why this is
// a deploy-time job and not something the running system does.
func hardenTheDefault(ctx context.Context, adminDSN string) error {
	target, err := url.Parse(adminDSN)
	if err != nil {
		return fmt.Errorf("GAME_DB_ADMIN_DSN is not a URL: %w", err)
	}
	target.Path = "/template1"

	conn, err := pgx.Connect(ctx, target.String())
	if err != nil {
		return fmt.Errorf("connect to template1: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	if err := gamedb.HardenDatabase(ctx, conn); err != nil {
		return fmt.Errorf("harden template1: %w", err)
	}
	return nil
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

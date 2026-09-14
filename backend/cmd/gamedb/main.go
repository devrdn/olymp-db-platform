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
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
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
	// Same rule the API and the Query Runner hold their own credentials to:
	// a deployment copied from deploy/.env.example and never edited must not
	// come up on the password everybody who has read that file knows.
	env := os.Getenv("ENV")
	if env == "" {
		env = "development"
	}
	if err := refusePlaceholderCredentials(env, dsn, roles); err != nil {
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
	if err := verifyMemory(ctx, pool); err != nil {
		return err
	}
	if err := hardenTheDefault(ctx, dsn); err != nil {
		return err
	}

	fmt.Println("game cluster prepared: roles, session defaults, connect privileges " +
		"and the catalogue revocations every new database inherits")
	return nil
}

// verifyMemory proves, at deploy time, that the game cluster is the cluster the
// Query Runner's memory arithmetic was checked against. Three checks, in order:
//
//   - the per-process cap (GAME_DB_PROCESS_MEMORY_BYTES) is the limit the
//     backends run under and is enforced;
//   - the settings the arithmetic restates as constants — game_author's
//     CONNECTION LIMIT, max_parallel_workers, autovacuum_max_workers — are what
//     the cluster actually runs with, so a pin changed on one side cannot drift;
//   - the container's memory limit, read from its own cgroup, is
//     GAME_DB_MEMORY_BYTES.
//
// Each fails silently in production if left unchecked — the cluster looks fine
// and only OOMs under load — so the deploy refuses to finish instead.
//
// The deploy always sets both variables. A run with neither set is a
// development run against a cluster not created by the compose file (make
// game-roles), and the checks are skipped with a notice saying so; set either
// and the other takes the same default the compose file and the Query Runner
// use.
func verifyMemory(ctx context.Context, pool *pgxpool.Pool) error {
	capRaw := os.Getenv("GAME_DB_PROCESS_MEMORY_BYTES")
	limitRaw := os.Getenv("GAME_DB_MEMORY_BYTES")
	if capRaw == "" && limitRaw == "" {
		fmt.Println("NOTICE: GAME_DB_PROCESS_MEMORY_BYTES and GAME_DB_MEMORY_BYTES are unset: " +
			"skipping the memory checks the deploy runs (cap, cluster settings, container limit)")
		return nil
	}
	capBytes, err := bytesEnv("GAME_DB_PROCESS_MEMORY_BYTES", config.DefaultProcessMemoryBytes)
	if err != nil {
		return err
	}
	limitBytes, err := bytesEnv("GAME_DB_MEMORY_BYTES", config.DefaultGameDBMemoryBytes)
	if err != nil {
		return err
	}
	allowUnreadable, err := strconv.ParseBool(orDefault(os.Getenv("GAME_DB_ALLOW_UNREADABLE_MEMORY_LIMIT"), "false"))
	if err != nil {
		return fmt.Errorf("GAME_DB_ALLOW_UNREADABLE_MEMORY_LIMIT: %w", err)
	}

	if err := gamedb.VerifyProcessMemoryCap(ctx, pool, capBytes); err != nil {
		return fmt.Errorf("the game cluster's per-process memory cap could not be confirmed: %w", err)
	}
	fmt.Printf("per-process memory cap confirmed: %d bytes, enforced\n", capBytes)

	if err := gamedb.VerifyMemorySettings(ctx, pool, gamedb.MemorySettings{
		AuthorConnectionLimit: config.MaxBuildSessions,
		MaxParallelWorkers:    config.MaxParallelWorkers,
		AutovacuumWorkers:     config.AutovacuumWorkers,
	}); err != nil {
		return err
	}
	fmt.Println("cluster settings match the memory arithmetic: build sessions, parallel workers, autovacuum workers")

	err = gamedb.VerifyContainerMemoryLimit(ctx, pool, gamedb.CgroupMemoryLimitFile, limitBytes)
	switch {
	case err == nil:
		fmt.Printf("container memory limit confirmed: %d bytes\n", limitBytes)
	case errors.Is(err, gamedb.ErrContainerLimitUnreadable) && allowUnreadable:
		fmt.Printf("WARNING: %v; continuing because GAME_DB_ALLOW_UNREADABLE_MEMORY_LIMIT is set — "+
			"the container is assumed, not confirmed, to have a %d-byte limit\n", err, limitBytes)
	case errors.Is(err, gamedb.ErrContainerLimitUnreadable):
		return fmt.Errorf("%w; on a host whose containers cannot report their limit (cgroup v1), "+
			"set GAME_DB_ALLOW_UNREADABLE_MEMORY_LIMIT=true to deploy without confirming it", err)
	default:
		return err
	}
	return nil
}

// bytesEnv reads a positive byte count, or the default when the variable is
// unset or empty.
func bytesEnv(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("%s: %q is not a positive whole number of bytes", key, raw)
	}
	return v, nil
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
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

// refusePlaceholderCredentials refuses, outside development, an admin DSN or
// role password that still carries deploy/.env.example's placeholder. This
// job runs before the Query Runner and the Core API ever connect to this
// cluster, on credentials neither of them validates on this path (the
// runner and the API only check the DSNs and passwords they themselves read),
// so a deployment left on the example's values would otherwise prepare the
// cluster successfully and only fail once something tries to use it.
func refusePlaceholderCredentials(env, adminDSN string, roles gamedb.Roles) error {
	for _, cred := range []struct {
		name, value string
	}{
		{"GAME_DB_ADMIN_DSN", adminDSN},
		{"GAME_READER_PASSWORD", roles.ReaderPassword},
		{"GAME_WRITER_PASSWORD", roles.WriterPassword},
		{"GAME_AUTHOR_PASSWORD", roles.AuthorPassword},
	} {
		if err := config.RefusePlaceholder(env, cred.name, cred.value); err != nil {
			return err
		}
	}
	return nil
}

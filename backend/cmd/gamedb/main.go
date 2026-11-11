// Command gamedb prepares the game cluster: the participant roles, the game
// script role, their session defaults, the databases they may not reach, and
// the catalogs and functions revoked inside every database.
//
// It runs idempotently on every deploy, before the Query Runner. An init
// script would run only when the data directory is created, so later
// restrictions would never reach an existing cluster. It ships in the Query
// Runner's image to share the validator's sensitive catalog and function
// lists (cgo).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// The provisioning role: this creates roles and revokes privileges.
	dsn, err := required("GAME_DB_ADMIN_DSN")
	if err != nil {
		return err
	}
	roles, err := rolesFromEnv()
	if err != nil {
		return err
	}
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
	if err := hardenEveryDatabase(ctx, pool, dsn); err != nil {
		return err
	}

	fmt.Println("game cluster prepared: roles, session defaults, connect privileges " +
		"and the catalogue and function revocations in every database")
	return nil
}

// rolesFromEnv reads what the three roles are given.
func rolesFromEnv() (gamedb.Roles, error) {
	var (
		roles gamedb.Roles
		err   error
	)
	if roles.ReaderPassword, err = required("GAME_READER_PASSWORD"); err != nil {
		return gamedb.Roles{}, err
	}
	if roles.WriterPassword, err = required("GAME_WRITER_PASSWORD"); err != nil {
		return gamedb.Roles{}, err
	}
	// The game script role, defined here with the other two so an existing
	// cluster picks it up on the next deploy.
	if roles.AuthorPassword, err = required("GAME_AUTHOR_PASSWORD"); err != nil {
		return gamedb.Roles{}, err
	}
	// The Core API's own reading, so the deploy and every build state the
	// same limit.
	if roles.AuthorTempFileBytes, err = config.GameTemplateMaxBytes(); err != nil {
		return gamedb.Roles{}, err
	}
	return roles, nil
}

// verifyMemory proves at deploy time that the game cluster matches the Query
// Runner's memory arithmetic: the per-process cap is enforced; game_author's
// CONNECTION LIMIT, max_parallel_workers and autovacuum_max_workers match the
// constants; and the container's cgroup limit is GAME_DB_MEMORY_BYTES. A
// mismatch would only show as an OOM under load.
//
// With neither variable set (a development cluster, make game-roles) the
// checks are skipped with a notice; with one set, the other takes the shared
// default.
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

// hardenEveryDatabase applies the catalogue and function revocations to every
// database the cluster holds. The privileges live in each database and are
// copied with it, so a restriction added since a template or an instance was
// built reaches it only here. template1 is among them: every database without
// an explicit TEMPLATE copies it, so a forgotten instance is hardened anyway.
func hardenEveryDatabase(ctx context.Context, pool *pgxpool.Pool, adminDSN string) error {
	// No author credential: this provisioner builds nothing.
	provisioner, err := gamedb.NewProvisioner(pool, adminDSN, "")
	if err != nil {
		return fmt.Errorf("GAME_DB_ADMIN_DSN: %w", err)
	}
	databases, err := provisioner.Databases(ctx)
	if err != nil {
		return err
	}
	return provisioner.Harden(ctx, databases...)
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

// refusePlaceholderCredentials refuses, outside development, an admin DSN or
// role password still carrying deploy/.env.example's placeholder; nothing
// else validates these credentials.
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

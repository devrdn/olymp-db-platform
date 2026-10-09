package gamedb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// outOfMemory is PostgreSQL's SQLSTATE for a failed allocation. A backend that
// hits its RLIMIT_DATA cap reports this rather than being killed.
const outOfMemory = "53200"

// The range of caps VerifyProcessMemoryCap can prove. Below the minimum, the
// quarter-cap probe fits in a backend's baseline and proves nothing. Above the
// maximum, the probes run into PostgreSQL's 1 GiB value limit or the
// container's memory before the cap.
//
// verifyChunkBytes is one value of the over-cap probe; separate values keep
// each under the 1 GiB limit and within int4.
const (
	MinVerifiableCapBytes = 256 << 20
	MaxVerifiableCapBytes = 1536 << 20
	verifyChunkBytes      = 64 << 20
)

// maxDataSize captures the soft "Max data size" from /proc/*/limits, in bytes.
var maxDataSize = regexp.MustCompile(`(?m)^Max data size\s+(\d+)`)

// VerifyProcessMemoryCap checks at deploy time that the game cluster's
// per-process memory cap is in force and equals capBytes. A missing or wrong
// cap is silent until the cluster OOMs under load, and the sandbox relies on a
// runaway query failing in its own backend.
//
// It checks the limit the backend reports in /proc/self/limits, then that it
// is enforced: a quarter-cap allocation must succeed and an over-cap one must
// fail with out_of_memory. Reading /proc needs a superuser. Caps outside
// MinVerifiableCapBytes..MaxVerifiableCapBytes are refused as unsupported.
func VerifyProcessMemoryCap(ctx context.Context, conn Conn, capBytes int64) error {
	if capBytes < MinVerifiableCapBytes || capBytes > MaxVerifiableCapBytes {
		return fmt.Errorf("unsupported cap: %d bytes is outside the %d–%d bytes (256 MiB–1.5 GiB) this check can verify",
			capBytes, int64(MinVerifiableCapBytes), int64(MaxVerifiableCapBytes))
	}

	var limits string
	if err := conn.QueryRow(ctx, `SELECT pg_read_file('/proc/self/limits')`).Scan(&limits); err != nil {
		return fmt.Errorf("reading the backend's own resource limits: %w", err)
	}
	match := maxDataSize.FindStringSubmatch(limits)
	if match == nil {
		return errors.New("the backend's /proc limits carry no 'Max data size' line; the per-process cap cannot be confirmed")
	}
	got, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parsing the backend's Max data size %q: %w", match[1], err)
	}
	if got != capBytes {
		return fmt.Errorf("the game cluster's per-process memory cap is %d bytes, not the configured %d "+
			"(ulimits.data on pg-game and GAME_DB_PROCESS_MEMORY_BYTES must agree)", got, capBytes)
	}

	// Must succeed, or the cap refuses ordinary work. At most 384 MiB, an int4.
	var scanned int64
	small := int32(capBytes / 4)
	if err := conn.QueryRow(ctx, `SELECT length(repeat('x', $1))`, small).Scan(&scanned); err != nil {
		return fmt.Errorf("the game cluster refused a %d-byte allocation, well under the %d-byte cap: %w", small, capBytes, err)
	}

	// Must fail with out_of_memory specifically; any other error proves
	// nothing about the cap.
	chunks := int32(capBytes/verifyChunkBytes + 2)
	err = conn.QueryRow(ctx,
		`SELECT cardinality(array_agg(repeat('x', $1))) FROM generate_series(1, $2)`,
		int32(verifyChunkBytes), chunks).Scan(&scanned)
	if err == nil {
		return fmt.Errorf("the game cluster held about %d bytes in one backend, over the %d-byte per-process cap: "+
			"the cap (ulimits.data on pg-game) is not in force, and a runaway query would crash the cluster instead of failing alone",
			int64(chunks)*verifyChunkBytes, capBytes)
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != outOfMemory {
		return fmt.Errorf("an allocation over the %d-byte cap failed with %v, not the expected out-of-memory error; "+
			"the cap cannot be confirmed", capBytes, err)
	}
	return nil
}

// CgroupMemoryLimitFile is where a cgroup v2 container reads its own memory
// limit: a byte count, or "max" for none.
const CgroupMemoryLimitFile = "/sys/fs/cgroup/memory.max"

// ErrContainerLimitUnreadable means the game cluster could not report its
// container memory limit (a cgroup v1 host, or no cgroup namespace). The deploy
// fails on it unless GAME_DB_ALLOW_UNREADABLE_MEMORY_LIMIT opts out.
var ErrContainerLimitUnreadable = errors.New("the game cluster's container memory limit cannot be read")

// VerifyContainerMemoryLimit checks at deploy time that the game cluster's
// container runs with limitBytes (GAME_DB_MEMORY_BYTES), the limit the Query
// Runner's memory arithmetic assumes. A container created before a change, or
// by hand, keeps its old limit silently. The backend reads its own cgroup file,
// so the number is the kernel's. limitFile is CgroupMemoryLimitFile in
// production and a parameter for tests.
func VerifyContainerMemoryLimit(ctx context.Context, conn Conn, limitFile string, limitBytes int64) error {
	var raw string
	if err := conn.QueryRow(ctx, `SELECT pg_read_file($1)`, limitFile).Scan(&raw); err != nil {
		return fmt.Errorf("%w (%s): %v", ErrContainerLimitUnreadable, limitFile, err)
	}
	value := strings.TrimSpace(raw)
	if value == "max" {
		return fmt.Errorf("the game cluster's container has no memory limit, not the configured %d bytes "+
			"(GAME_DB_MEMORY_BYTES): the per-process cap no longer protects the cluster from the OOM killer", limitBytes)
	}
	got, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: %s holds %q, not a byte count", ErrContainerLimitUnreadable, limitFile, value)
	}
	if got != limitBytes {
		return fmt.Errorf("the game cluster's container memory limit is %d bytes, not the configured %d "+
			"(GAME_DB_MEMORY_BYTES): recreate pg-game so its limit is interpolated from the same value", got, limitBytes)
	}
	return nil
}

// MemorySettings are the cluster settings the Query Runner's memory arithmetic
// restates as constants in config. The platform layer cannot import this
// package, so the caller passes its numbers in.
type MemorySettings struct {
	AuthorConnectionLimit int // game_author's CONNECTION LIMIT
	MaxParallelWorkers    int // max_parallel_workers
	AutovacuumWorkers     int // autovacuum_max_workers
}

// VerifyMemorySettings reads each setting back from the cluster and reports
// every one that differs from want.
func VerifyMemorySettings(ctx context.Context, conn Conn, want MemorySettings) error {
	var drift []string

	var authorLimit int
	if err := conn.QueryRow(ctx,
		`SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, RoleAuthor).Scan(&authorLimit); err != nil {
		return fmt.Errorf("reading %s's connection limit: %w", RoleAuthor, err)
	}
	if authorLimit != want.AuthorConnectionLimit {
		drift = append(drift, fmt.Sprintf("%s CONNECTION LIMIT is %d, the arithmetic counts %d build sessions",
			RoleAuthor, authorLimit, want.AuthorConnectionLimit))
	}

	for _, setting := range []struct {
		name string
		want int
	}{
		{"max_parallel_workers", want.MaxParallelWorkers},
		{"autovacuum_max_workers", want.AutovacuumWorkers},
	} {
		var raw string
		if err := conn.QueryRow(ctx, `SELECT current_setting($1)`, setting.name).Scan(&raw); err != nil {
			return fmt.Errorf("reading %s: %w", setting.name, err)
		}
		got, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%s is %q, not a number", setting.name, raw)
		}
		if got != setting.want {
			drift = append(drift, fmt.Sprintf("%s is %d, the arithmetic assumes %d", setting.name, got, setting.want))
		}
	}

	if len(drift) > 0 {
		return fmt.Errorf("the game cluster does not match the Query Runner's memory arithmetic: %s",
			strings.Join(drift, "; "))
	}
	return nil
}

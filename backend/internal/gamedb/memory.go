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

// The range of caps this check can prove, and the unit its over-cap probe is
// built from.
//
// Below MinVerifiableCapBytes the "a quarter of the cap must succeed" probe is
// small enough to fit in a backend's baseline and says nothing. Above
// MaxVerifiableCapBytes the probes stop being honest: the quarter-cap value
// approaches PostgreSQL's 1 GiB limit on a single value, and the over-cap
// allocation, which must fail with out_of_memory specifically, starts to meet
// that limit (a different error) or the container's memory before the cap. The
// deployment's arithmetic is sized for 256–512 MiB; 1.5 GiB leaves room above.
//
// verifyChunkBytes is one value the over-cap probe holds. The probe collects
// enough of them with array_agg to exceed the cap — separate values, so no
// single one approaches the 1 GiB limit and no length overflows an int4.
const (
	MinVerifiableCapBytes = 256 << 20
	MaxVerifiableCapBytes = 1536 << 20
	verifyChunkBytes      = 64 << 20
)

// maxDataSize matches the "Max data size" line of a Linux /proc/*/limits file
// and captures its soft (hard is the same for a ulimit) value in bytes.
var maxDataSize = regexp.MustCompile(`(?m)^Max data size\s+(\d+)`)

// VerifyProcessMemoryCap checks, at deploy time, that the game cluster's
// per-process memory cap is actually in force and is the value the deployment
// configured. The whole memory story rests on it (config.Runner sizes the
// container around it, and the sandbox's guarantee is that a runaway query
// fails in its own backend rather than crashing the cluster), and a missing or
// wrong cap fails silently — the cluster looks identical and only OOMs under
// load. So the deploy proves it once and refuses to come up otherwise.
//
// Two independent checks. First the exact limit: the backend reads its own
// /proc/self/limits and the reported "Max data size" must equal capBytes — this
// catches a ulimit dropped, or set to a different number than the arithmetic
// assumes. Then enforcement: an allocation a quarter of the cap must succeed
// and one over it must be refused with out_of_memory — this catches a limit
// that is reported but not enforced (a kernel or image that ignores it).
//
// Reading /proc requires superuser (pg_read_file); PrepareCluster's caller is
// the provisioning superuser, which is who runs this.
//
// Supported caps: MinVerifiableCapBytes (256 MiB) to MaxVerifiableCapBytes
// (1.5 GiB). Anything else is refused as unsupported before a byte is
// allocated, rather than proved by probes that no longer mean what they say.
func VerifyProcessMemoryCap(ctx context.Context, conn Conn, capBytes int64) error {
	if capBytes < MinVerifiableCapBytes || capBytes > MaxVerifiableCapBytes {
		return fmt.Errorf("unsupported cap: %d bytes is outside the %d–%d bytes (256 MiB–1.5 GiB) this check can verify",
			capBytes, int64(MinVerifiableCapBytes), int64(MaxVerifiableCapBytes))
	}

	// The exact limit the kernel is enforcing on this backend.
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

	// Well under the cap: this must succeed, or the cap is set so low it would
	// refuse ordinary work. Within the supported range a quarter of the cap is
	// at most 384 MiB, an int4 without a cast.
	var scanned int64
	small := int32(capBytes / 4)
	if err := conn.QueryRow(ctx, `SELECT length(repeat('x', $1))`, small).Scan(&scanned); err != nil {
		return fmt.Errorf("the game cluster refused a %d-byte allocation, well under the %d-byte cap: %w", small, capBytes, err)
	}

	// Over the cap: enough separate chunks held at once by array_agg to exceed
	// it. This must be refused, and refused with out_of_memory specifically —
	// any other error would mean it failed for some unrelated reason and proves
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

// ErrContainerLimitUnreadable means the game cluster could not report its own
// container memory limit — a cgroup v1 host, or a container without a cgroup
// namespace. The deploy fails on it unless the operator has explicitly opted
// out (GAME_DB_ALLOW_UNREADABLE_MEMORY_LIMIT), because the arithmetic the Query
// Runner checked is only true if the limit is the one it was checked against.
var ErrContainerLimitUnreadable = errors.New("the game cluster's container memory limit cannot be read")

// VerifyContainerMemoryLimit checks, at deploy time, that the game cluster's
// container runs with the memory limit the Query Runner's arithmetic was
// checked against (GAME_DB_MEMORY_BYTES). The compose file interpolates both
// from the same variable, but a container created before a change, or by
// hand, keeps its old limit silently — and a limit smaller than the arithmetic
// assumes is exactly how the OOM killer comes back. The backend reads its own
// cgroup file (superuser, pg_read_file), so the number is the kernel's, not
// the compose file's.
//
// limitFile is CgroupMemoryLimitFile in production; it is a parameter so the
// unreadable case can be exercised.
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
// restates as constants (config.MaxBuildSessions, config.MaxParallelWorkers,
// config.AutovacuumWorkers). The platform layer cannot import this package, so
// the caller passes its own numbers in and this compares them with the cluster.
type MemorySettings struct {
	// AuthorConnectionLimit is the build sessions the arithmetic counts; it
	// must be game_author's CONNECTION LIMIT.
	AuthorConnectionLimit int
	// MaxParallelWorkers must be max_parallel_workers on the cluster.
	MaxParallelWorkers int
	// AutovacuumWorkers must be autovacuum_max_workers on the cluster.
	AutovacuumWorkers int
}

// VerifyMemorySettings reads each setting the memory arithmetic depends on back
// from the cluster and reports every one that differs, so a pin changed on the
// pg-game command, or a role limit changed here, cannot drift from the numbers
// the runner sized the container with.
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

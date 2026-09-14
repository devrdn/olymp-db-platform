package gamedb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

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

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

// verifyChunkBytes is one allocation the over-cap probe stacks. It is far below
// the smallest supported cap and well within an int4 length, so the probe
// builds a value larger than any cap by summing enough of these rather than
// asking for one huge length (which would overflow repeat's integer argument
// for a large cap).
const verifyChunkBytes = 64 << 20

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
// Supported cap range: from a few times verifyChunkBytes up. The pilot uses
// 256 MiB and the arithmetic is sized for 256–512 MiB; a much larger cap still
// works because the over-cap value is built by stacking chunks, not by one
// oversized length.
func VerifyProcessMemoryCap(ctx context.Context, conn Conn, capBytes int64) error {
	if capBytes < 4*verifyChunkBytes {
		return fmt.Errorf("a per-process cap of %d bytes is below the %d-byte floor this check supports", capBytes, int64(4*verifyChunkBytes))
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
	// refuse ordinary work.
	var scanned int64
	small := capBytes / 4
	if err := conn.QueryRow(ctx, `SELECT length(repeat('x', $1::int))`, small).Scan(&scanned); err != nil {
		return fmt.Errorf("the game cluster refused a %d-byte allocation, well under the %d-byte cap: %w", small, capBytes, err)
	}

	// Over the cap: built by stacking chunks into one value, so no single
	// length argument overflows even for a large cap. This must be refused,
	// and refused with out_of_memory specifically — any other error would mean
	// it failed for some unrelated reason and proves nothing about the cap.
	chunks := capBytes/verifyChunkBytes + 2
	err = conn.QueryRow(ctx,
		`SELECT length(string_agg(repeat('x', $1::int), '')) FROM generate_series(1, $2)`,
		int64(verifyChunkBytes), chunks).Scan(&scanned)
	if err == nil {
		return fmt.Errorf("the game cluster built a value of about %d bytes, over the %d-byte per-process cap: "+
			"the cap (ulimits.data on pg-game) is not in force, and a runaway query would crash the cluster instead of failing alone",
			chunks*verifyChunkBytes, capBytes)
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != outOfMemory {
		return fmt.Errorf("an allocation over the %d-byte cap failed with %v, not the expected out-of-memory error; "+
			"the cap cannot be confirmed", capBytes, err)
	}
	return nil
}

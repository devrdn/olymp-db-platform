package gamedb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// outOfMemory is PostgreSQL's SQLSTATE for a failed allocation. A backend that
// hits its RLIMIT_DATA cap reports this rather than being killed.
const outOfMemory = "53200"

// VerifyProcessMemoryCap checks, at deploy time, that the game cluster's
// per-process memory cap is actually in force — that a backend which asks for
// more than the cap is refused with an "out of memory" ERROR, and one that
// asks for well under it succeeds.
//
// The whole memory story rests on this cap (config.Runner sizes the container
// around it, and the sandbox's guarantee is that a runaway query fails in its
// own backend rather than crashing the cluster). A cap that is missing —
// ulimits.data dropped from the compose file, a base image that ignores it —
// fails silently: the cluster looks identical and only OOMs under load, in
// production, during a contest. So the deploy proves it once, and refuses to
// come up if it cannot.
//
// capBytes is the cap the deployment set. The probe asks for a quarter of it
// (which must succeed) and for the cap plus a margin (which must be refused).
// A quarter leaves room for the backend's own baseline; the margin puts the
// large request unambiguously over the cap.
func VerifyProcessMemoryCap(ctx context.Context, conn Conn, capBytes int64) error {
	if capBytes <= 0 {
		return fmt.Errorf("a per-process memory cap of %d bytes is not a size to verify", capBytes)
	}

	const margin = 64 << 20
	small := capBytes / 4
	over := capBytes + margin

	// Well under the cap: this must succeed, or the cap is set so low it would
	// refuse ordinary work.
	var got int64
	if err := conn.QueryRow(ctx, `SELECT length(repeat('x', $1::int))`, small).Scan(&got); err != nil {
		return fmt.Errorf("the game cluster refused a %d-byte allocation, well under the %d-byte cap: %w", small, capBytes, err)
	}

	// Over the cap: this must be refused, and refused with out_of_memory
	// specifically — a different error would mean the query failed for some
	// other reason and proves nothing about the cap.
	err := conn.QueryRow(ctx, `SELECT length(repeat('x', $1::int))`, over).Scan(&got)
	if err == nil {
		return fmt.Errorf("the game cluster built a %d-byte value, over the %d-byte per-process cap: "+
			"the cap (ulimits.data on pg-game) is not in force, and a runaway query would crash the cluster instead of failing alone", over, capBytes)
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != outOfMemory {
		return fmt.Errorf("a %d-byte allocation over the %d-byte cap failed with %v, not the expected out-of-memory error; "+
			"the cap cannot be confirmed", over, capBytes, err)
	}
	return nil
}
